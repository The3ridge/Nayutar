package liveness

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
)

// ErrAlreadyRun is returned when Run is called more than once on an Aggregator or Node.
var ErrAlreadyRun = errors.New("liveness: already run")

const (
	defaultTickInterval = 100 * time.Millisecond
	defaultStoreTimeout = 2 * time.Second
	maxReloadBackoff    = 5 * time.Second
	maxWriteSlots       = 4
)

// Clock is the time source of a runner.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Transport delivers messages best effort. Send must not block; Recv is closed never.
type Transport interface {
	Send(m Message)
	Recv() <-chan Message
}

// AggregatorConfig configures an Aggregator runner.
type AggregatorConfig struct {
	Engine       AggregatorEngineConfig
	Store        store.Store
	Transport    Transport
	Clock        Clock         // nil → real clock
	TickInterval time.Duration // 0 → 100ms
	StoreTimeout time.Duration // 0 → 2s
	OnError      func(error)   // nil → ignore; called from the Run goroutine
}

// Aggregator drives an AggregatorEngine against a store and message transport.
type Aggregator struct {
	engine       *AggregatorEngine
	store        store.Store
	registry     *Registry
	transport    Transport
	clock        Clock
	tickInterval time.Duration
	storeTimeout time.Duration
	onError      func(error)
	started      atomic.Bool
}

// NewAggregator validates cfg and constructs an Aggregator runner.
func NewAggregator(cfg AggregatorConfig) (*Aggregator, error) {
	if cfg.Store == nil {
		return nil, errors.New("liveness: store must be non-nil")
	}
	if cfg.Transport == nil {
		return nil, errors.New("liveness: transport must be non-nil")
	}
	if cfg.TickInterval < 0 {
		return nil, errors.New("liveness: tick interval must be >= 0")
	}
	if cfg.StoreTimeout < 0 {
		return nil, errors.New("liveness: store timeout must be >= 0")
	}
	tickInterval := cfg.TickInterval
	if tickInterval == 0 {
		tickInterval = defaultTickInterval
	}
	storeTimeout := cfg.StoreTimeout
	if storeTimeout == 0 {
		storeTimeout = defaultStoreTimeout
	}
	clk := cfg.Clock
	if clk == nil {
		clk = realClock{}
	}
	engCfg := cfg.Engine
	if engCfg.Detector.RoundBase == 0 {
		// Offset round ids by start time so ids of two incarnations do not overlap.
		if ms := clk.Now().UnixMilli(); ms > 0 {
			engCfg.Detector.RoundBase = uint64(ms) << 10
		}
	}
	eng, err := NewAggregatorEngine(engCfg)
	if err != nil {
		return nil, err
	}
	return &Aggregator{
		engine:       eng,
		store:        cfg.Store,
		registry:     NewRegistry(cfg.Store),
		transport:    cfg.Transport,
		clock:        clk,
		tickInterval: tickInterval,
		storeTimeout: storeTimeout,
		onError:      cfg.OnError,
	}, nil
}

type loadResult struct {
	recs        []Record
	decodeErrs  []error
	watch       store.WatchStream
	watchCancel context.CancelFunc
	err         error
}

// Run blocks until ctx is done and returns ctx.Err(); store errors are retried.
// Run is single-use: a second call returns ErrAlreadyRun immediately.
func (a *Aggregator) Run(ctx context.Context) error {
	if !a.started.CompareAndSwap(false, true) {
		return ErrAlreadyRun
	}
	start := a.clock.Now()
	mono := func(t time.Time) int64 {
		return max(0, t.Sub(start).Milliseconds())
	}
	reportErr := func(err error) {
		if a.onError != nil && err != nil {
			a.onError(err)
		}
	}

	records := make(map[string]Record)
	slots := make(chan struct{}, maxWriteSlots)
	errCh := make(chan error, maxWriteSlots)
	loadCh := make(chan loadResult, 1)
	var wg sync.WaitGroup
	var watchStream store.WatchStream
	var watchCancel context.CancelFunc
	var watchCh <-chan store.Event
	var reloadCh <-chan time.Time
	var backoff time.Duration
	var watchOpenedAt time.Time
	var watchDelivered bool
	var loading bool

	nextBackoff := func() time.Duration {
		if backoff == 0 {
			backoff = min(a.tickInterval, maxReloadBackoff)
		} else {
			backoff = min(backoff*2, maxReloadBackoff)
		}
		return backoff
	}

	shutdown := func() {
		if watchCancel != nil {
			watchCancel()
			watchCancel = nil
		}
		if watchCh != nil {
			for range watchCh {
			}
			watchCh = nil
		}
		watchStream = nil
		wg.Wait()
		select {
		case res := <-loadCh:
			if res.watchCancel != nil {
				res.watchCancel()
			}
			if res.watch != nil {
				for range res.watch.Events() {
				}
			}
		default:
		}
	}

	startLoad := func() {
		if loading {
			return
		}
		loading = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			loadCh <- a.loadAndWatch(ctx)
		}()
	}

	startLoad()

	recv := a.transport.Recv()
	tickCh := a.clock.After(a.tickInterval)

	for {
		select {
		case <-ctx.Done():
			shutdown()
			return ctx.Err()
		case m := <-recv:
			for _, out := range a.engine.Handle(mono(a.clock.Now()), m) {
				a.transport.Send(out)
			}
		case err := <-errCh:
			reportErr(err)
		case res := <-loadCh:
			loading = false
			for _, derr := range res.decodeErrs {
				reportErr(derr)
			}
			if res.err != nil {
				if ctx.Err() == nil {
					reportErr(res.err)
					reloadCh = a.clock.After(nextBackoff())
				}
				continue
			}
			watchOpenedAt = a.clock.Now()
			watchDelivered = false
			nowMono := mono(watchOpenedAt)
			seen := make(map[string]struct{}, len(res.recs))
			for _, rec := range res.recs {
				seen[rec.Node] = struct{}{}
				records[rec.Node] = rec
				a.engine.Observed(nowMono, rec)
			}
			for node := range records {
				if _, ok := seen[node]; !ok {
					delete(records, node)
					a.engine.Forgotten(nowMono, node)
				}
			}
			watchStream = res.watch
			watchCancel = res.watchCancel
			watchCh = res.watch.Events()
		case ev, ok := <-watchCh:
			if !ok {
				if watchCancel != nil {
					watchCancel()
					watchCancel = nil
				}
				watchCh = nil
				var werr error
				if watchStream != nil {
					werr = watchStream.Err()
					watchStream = nil
				}
				if ctx.Err() != nil {
					shutdown()
					return ctx.Err()
				}
				if werr != nil {
					reportErr(fmt.Errorf("watch nodes: %w", werr))
				}
				if watchDelivered || a.clock.Now().Sub(watchOpenedAt) >= max(backoff, a.tickInterval) {
					backoff = 0
				}
				reloadCh = a.clock.After(nextBackoff())
				continue
			}
			watchDelivered = true
			backoff = 0
			nowMono := mono(a.clock.Now())
			switch ev.Type {
			case store.EventPut:
				parsed, err := keyspace.Parse(ev.Key)
				if err != nil || parsed.Family != keyspace.FamilyNode {
					reportErr(fmt.Errorf("watch put: invalid registry key %q: %w", ev.Key, ErrCorruptRecord))
					continue
				}
				rec, err := DecodeRecord(parsed.Node, ev.Value, ev.Rev)
				if err != nil {
					reportErr(fmt.Errorf("watch put: decode record for node %s: %w", parsed.Node, err))
					continue
				}
				records[rec.Node] = rec
				a.engine.Observed(nowMono, rec)
			case store.EventDelete:
				parsed, err := keyspace.Parse(ev.Key)
				if err != nil || parsed.Family != keyspace.FamilyNode {
					reportErr(fmt.Errorf("watch delete: invalid registry key %q: %w", ev.Key, ErrCorruptRecord))
					continue
				}
				delete(records, parsed.Node)
				a.engine.Forgotten(nowMono, parsed.Node)
			}
		case <-reloadCh:
			reloadCh = nil
			startLoad()
		case <-tickCh:
			nowTime := a.clock.Now()
			msgs, props := a.engine.Tick(mono(nowTime))
			for _, out := range msgs {
				a.transport.Send(out)
			}
			wall := nowTime.UnixMilli()
			totalNodes := len(records)
			for _, p := range props {
				rec, ok := records[p.Node]
				if !ok || rec.ModRev != p.ModRev {
					continue
				}
				select {
				case slots <- struct{}{}:
				default:
					continue
				}
				wg.Add(1)
				go a.executeProposal(ctx, p, rec, wall, totalNodes, slots, &wg, errCh)
			}
			tickCh = a.clock.After(a.tickInterval)
		}
	}
}

func (a *Aggregator) loadAndWatch(ctx context.Context) loadResult {
	recs, rev, decodeErrs, err := a.registry.list(ctx, false, a.storeTimeout)
	if err != nil {
		return loadResult{
			decodeErrs: decodeErrs,
			err:        fmt.Errorf("list nodes: %w", err),
		}
	}
	if err := ctx.Err(); err != nil {
		return loadResult{decodeErrs: decodeErrs, err: err}
	}

	wctx, wcancel := context.WithCancel(ctx)
	type watchOpen struct {
		ws  store.WatchStream
		err error
	}
	openCh := make(chan watchOpen, 1)
	go func() {
		ws, err := a.store.Watch(wctx, keyspace.NodesPrefix, rev+1)
		openCh <- watchOpen{ws: ws, err: err}
	}()

	var opened watchOpen
	select {
	case opened = <-openCh:
	case <-a.clock.After(a.storeTimeout):
		wcancel()
		opened = <-openCh
		if opened.err == nil && opened.ws != nil {
			for range opened.ws.Events() {
			}
		}
		if ctx.Err() == nil && (opened.err == nil || errors.Is(opened.err, context.Canceled)) {
			opened.err = context.DeadlineExceeded
		}
	}
	if opened.err != nil {
		wcancel()
		return loadResult{
			decodeErrs: decodeErrs,
			err:        fmt.Errorf("watch nodes: %w", opened.err),
		}
	}
	return loadResult{
		recs:        recs,
		decodeErrs:  decodeErrs,
		watch:       opened.ws,
		watchCancel: wcancel,
	}
}

func (a *Aggregator) executeProposal(
	ctx context.Context,
	p Proposal,
	rec Record,
	wall int64,
	totalNodes int,
	slots chan struct{},
	wg *sync.WaitGroup,
	errCh chan<- error,
) {
	defer wg.Done()
	defer func() { <-slots }()

	wctx, cancel := context.WithTimeout(ctx, a.storeTimeout)
	defer cancel()

	var err error
	var op string
	switch p.Kind {
	case ProposeReady:
		op = "set ready"
		_, err = a.registry.SetReady(wctx, rec, wall)
	case ProposeNotReady:
		op = "set notready"
		_, err = a.registry.SetNotReady(wctx, rec, wall)
	case ProposeEvictable:
		op = "grant eviction"
		_, err = a.registry.GrantEviction(wctx, rec, wall, totalNodes)
	default:
		return
	}
	if err != nil && ctx.Err() == nil {
		select {
		case errCh <- fmt.Errorf("%s for node %s: %w", op, p.Node, err):
		case <-ctx.Done():
		}
	}
}
