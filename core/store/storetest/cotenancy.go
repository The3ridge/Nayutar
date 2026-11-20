package storetest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Kaikei-e/Nayutar/core/store"
)

// CotenancyLoad sizes one co-tenancy measurement.
type CotenancyLoad struct {
	Duration      time.Duration // length of each of the two phases
	StrongRate    int           // strongly consistent writes per second, paced
	HotWriters    int           // goroutines writing hot keys back to back in the loaded phase
	HotKeys       int           // distinct hot keys
	HotValueBytes int           // size of a hot value
}

// Latencies summarises a set of samples.
type Latencies struct {
	Count         int
	P50, P99, Max time.Duration
}

// CotenancyPhase is what one phase measured.
type CotenancyPhase struct {
	Txn       Latencies // call-to-return time of the strong writes
	Watch     Latencies // from the call of a strong write to the delivery of its event
	HotWrites int       // hot writes acknowledged during the phase
}

// CotenancyReport compares the strong workload alone with the same workload next to hot writes.
type CotenancyReport struct {
	Baseline CotenancyPhase
	Loaded   CotenancyPhase
}

// String renders the baseline and loaded phases as two summary lines.
func (r CotenancyReport) String() string {
	return r.Baseline.formatLine("baseline") + "\n" + r.Loaded.formatLine("loaded")
}

func (p CotenancyPhase) formatLine(name string) string {
	return fmt.Sprintf(
		"%s: hot_writes=%d txn(n=%d p50=%s p99=%s max=%s) watch(n=%d p50=%s p99=%s max=%s)",
		name, p.HotWrites,
		p.Txn.Count, p.Txn.P50, p.Txn.P99, p.Txn.Max,
		p.Watch.Count, p.Watch.P50, p.Watch.P99, p.Watch.Max,
	)
}

// MeasureCotenancy measures strong-write and watch latencies on s alone and
// alongside back-to-back hot-key writes. Keys under "strong/" and "hot/" must
// be unused and nothing else may write to s during the measurement.
func MeasureCotenancy(ctx context.Context, s store.Store, load CotenancyLoad) (CotenancyReport, error) {
	baseline, err := measurePhase(ctx, s, load, "baseline", false)
	if err != nil {
		return CotenancyReport{}, err
	}
	loaded, err := measurePhase(ctx, s, load, "loaded", true)
	if err != nil {
		return CotenancyReport{}, err
	}
	return CotenancyReport{Baseline: baseline, Loaded: loaded}, nil
}

func measurePhase(ctx context.Context, s store.Store, load CotenancyLoad, phaseName string, withHot bool) (CotenancyPhase, error) {
	phaseCtx, cancelPhase := context.WithCancel(ctx)
	defer cancelPhase()

	ws, err := s.Watch(phaseCtx, "strong/", 0)
	if err != nil {
		return CotenancyPhase{}, fmt.Errorf("%s: watch: %w", phaseName, err)
	}
	// Sync ensures the fromRev=0 watch stream is established on the server before any write begins.
	syncEvents, _, err := Sync(phaseCtx, s, ws)
	if err != nil {
		return CotenancyPhase{}, fmt.Errorf("%s: sync watch: %w", phaseName, err)
	}
	if len(syncEvents) != 0 {
		return CotenancyPhase{}, fmt.Errorf("%s: watch saw %d pre-existing events", phaseName, len(syncEvents))
	}

	var errMu sync.Mutex
	var firstErr error
	fail := func(err error) {
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		errMu.Unlock()
		cancelPhase()
	}

	var callMu sync.Mutex
	callTimes := make(map[string]time.Time)
	ackedCh := make(chan []string, 1)

	var watcherWG sync.WaitGroup
	var watchLatencies []time.Duration
	watcherWG.Add(1)
	go func() {
		defer watcherWG.Done()
		delivered := make(map[string]time.Duration)
		var acked []string
		ackedReady := false
		pending := ackedCh

		allDelivered := func() bool {
			if !ackedReady {
				return false
			}
			for _, v := range acked {
				if _, ok := delivered[v]; !ok {
					return false
				}
			}
			return true
		}

		for !allDelivered() {
			select {
			case vals, ok := <-pending:
				if !ok {
					return
				}
				acked = vals
				ackedReady = true
				pending = nil
			case ev, open := <-ws.Events():
				if !open {
					if err := ws.Err(); err != nil {
						fail(fmt.Errorf("watch ended early: %w", err))
					} else {
						fail(errors.New("watch ended early"))
					}
					return
				}
				if ev.Type == store.EventProgress {
					continue
				}
				if ev.Type != store.EventPut {
					fail(fmt.Errorf("unexpected watch event type %d on key %q", ev.Type, ev.Key))
					return
				}
				now := time.Now()
				val := string(ev.Value)
				callMu.Lock()
				callAt, ok := callTimes[val]
				callMu.Unlock()
				if !ok {
					fail(fmt.Errorf("unexpected watch event value %q on key %q", val, ev.Key))
					return
				}
				if _, dup := delivered[val]; dup {
					fail(fmt.Errorf("duplicate watch event for value %q", val))
					return
				}
				delivered[val] = now.Sub(callAt)
			case <-phaseCtx.Done():
				if ackedReady {
					fail(fmt.Errorf("waiting for %d/%d strong watch events: %w", len(delivered), len(acked), phaseCtx.Err()))
				} else {
					fail(fmt.Errorf("watch: %w", phaseCtx.Err()))
				}
				return
			}
		}

		watchLatencies = make([]time.Duration, 0, len(acked))
		for _, v := range acked {
			watchLatencies = append(watchLatencies, delivered[v])
		}
	}()

	stopCh := make(chan struct{})
	var hotWrites atomic.Int64
	var writersWG sync.WaitGroup

	if withHot && load.HotWriters > 0 {
		hotKeys := make([]string, load.HotKeys)
		for i := range hotKeys {
			hotKeys[i] = fmt.Sprintf("hot/%d", i)
		}
		hotVal := bytes.Repeat([]byte("h"), load.HotValueBytes)
		for id := range load.HotWriters {
			writersWG.Add(1)
			go func() {
				defer writersWG.Done()
				for seq := 0; ; seq++ {
					select {
					case <-stopCh:
						return
					case <-phaseCtx.Done():
						return
					default:
					}
					key := hotKeys[(id+seq)%len(hotKeys)]
					ok, _, err := s.Txn(phaseCtx, nil, []store.Op{{Type: store.OpPut, Key: key, Value: hotVal}})
					if err != nil {
						fail(fmt.Errorf("hot txn: %w", err))
						return
					}
					if !ok {
						fail(errors.New("hot txn did not apply"))
						return
					}
					hotWrites.Add(1)
				}
			}()
		}
	}

	var txnLatencies []time.Duration
	var ackedVals []string

	const strongKeysCount = 4
	interval := time.Second / time.Duration(load.StrongRate)
	writersWG.Add(1)
	go func() {
		defer writersWG.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for seq := 1; ; seq++ {
			if seq > 1 {
				select {
				case <-stopCh:
					return
				case <-phaseCtx.Done():
					return
				case <-ticker.C:
				}
				select {
				case <-stopCh:
					return
				case <-phaseCtx.Done():
					return
				default:
				}
			} else if phaseCtx.Err() != nil {
				return
			}

			key := fmt.Sprintf("strong/%d", (seq-1)%strongKeysCount)
			val := fmt.Sprintf("%s-%d", phaseName, seq)

			// Record the call timestamp before Txn so a concurrent watch event always finds it.
			callAt := time.Now()
			callMu.Lock()
			callTimes[val] = callAt
			callMu.Unlock()

			ok, _, err := s.Txn(phaseCtx, nil, []store.Op{{Type: store.OpPut, Key: key, Value: []byte(val)}})
			txnDur := time.Since(callAt)
			if err != nil {
				fail(fmt.Errorf("strong txn %d: %w", seq, err))
				return
			}
			if !ok {
				fail(fmt.Errorf("strong txn %d did not apply", seq))
				return
			}
			txnLatencies = append(txnLatencies, txnDur)
			ackedVals = append(ackedVals, val)
		}
	}()

	timer := time.NewTimer(load.Duration)
	select {
	case <-timer.C:
	case <-phaseCtx.Done():
	}
	timer.Stop()
	close(stopCh)

	writersWG.Wait()

	errMu.Lock()
	phaseErr := firstErr
	errMu.Unlock()
	if phaseErr != nil {
		close(ackedCh)
		watcherWG.Wait()
		return CotenancyPhase{}, fmt.Errorf("%s: %w", phaseName, phaseErr)
	}

	ackedCh <- ackedVals
	watcherWG.Wait()

	errMu.Lock()
	phaseErr = firstErr
	errMu.Unlock()
	if phaseErr != nil {
		return CotenancyPhase{}, fmt.Errorf("%s: %w", phaseName, phaseErr)
	}

	return CotenancyPhase{
		Txn:       summarizeLatencies(txnLatencies),
		Watch:     summarizeLatencies(watchLatencies),
		HotWrites: int(hotWrites.Load()),
	}, nil
}

func summarizeLatencies(samples []time.Duration) Latencies {
	n := len(samples)
	if n == 0 {
		return Latencies{}
	}
	sorted := slices.Clone(samples)
	slices.Sort(sorted)
	return Latencies{
		Count: n,
		P50:   nearestRank(sorted, 50),
		P99:   nearestRank(sorted, 99),
		Max:   sorted[n-1],
	}
}

func nearestRank(sorted []time.Duration, pct int) time.Duration {
	idx := (pct*len(sorted) - 1) / 100
	return sorted[idx]
}
