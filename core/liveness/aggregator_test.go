package liveness_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Kaikei-e/Nayutar/core/liveness"
	"github.com/Kaikei-e/Nayutar/core/liveness/agent"
	"github.com/Kaikei-e/Nayutar/core/liveness/detector"
	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
	"github.com/Kaikei-e/Nayutar/core/store/memstore"
)

type memEndpoint struct {
	self liveness.Addr
	hub  *memHub
	ch   chan liveness.Message
	down atomic.Bool
}

type memHub struct {
	endpoints map[liveness.Addr]*memEndpoint
}

func newMemHub(aggs, nodes []string) *memHub {
	eps := make(map[liveness.Addr]*memEndpoint, len(aggs)+len(nodes))
	h := &memHub{endpoints: eps}
	for _, id := range aggs {
		addr := liveness.Addr{ID: id}
		eps[addr] = &memEndpoint{self: addr, hub: h, ch: make(chan liveness.Message, 256)}
	}
	for _, id := range nodes {
		addr := liveness.Addr{ID: id, Node: true}
		eps[addr] = &memEndpoint{self: addr, hub: h, ch: make(chan liveness.Message, 256)}
	}
	return h
}

func (e *memEndpoint) Send(m liveness.Message) {
	if e.down.Load() {
		return
	}
	dst, ok := e.hub.endpoints[m.To]
	if !ok || dst.down.Load() {
		return
	}
	select {
	case dst.ch <- m:
	default:
	}
}

func (e *memEndpoint) Recv() <-chan liveness.Message {
	return e.ch
}

type offsetClock struct {
	offset time.Duration
}

func (c offsetClock) Now() time.Time {
	return time.Now().Add(c.offset)
}

func (c offsetClock) After(d time.Duration) <-chan time.Time {
	return time.After(d)
}

type dropFirstWatchStore struct {
	store.Store
	dropped atomic.Bool
}

func (d *dropFirstWatchStore) Watch(ctx context.Context, prefix string, fromRev store.Rev) (store.WatchStream, error) {
	ws, err := d.Store.Watch(ctx, prefix, fromRev)
	if err != nil {
		return nil, err
	}
	if d.dropped.CompareAndSwap(false, true) {
		ch := make(chan store.Event)
		close(ch)
		return &closedWatchStream{inner: ws, ch: ch, ctx: ctx}, nil
	}
	return ws, nil
}

type closedWatchStream struct {
	inner store.WatchStream
	ch    chan store.Event
	ctx   context.Context
}

func (w *closedWatchStream) Events() <-chan store.Event { return w.ch }

func (w *closedWatchStream) RequestProgress(ctx context.Context) error {
	return store.ErrCompacted
}

func (w *closedWatchStream) Err() error { return store.ErrCompacted }

type conflictFirstSetReadyStore struct {
	store.Store
	node     string
	rack     string
	injected atomic.Bool
}

func (c *conflictFirstSetReadyStore) Txn(ctx context.Context, cmps []store.Cmp, ops []store.Op) (bool, store.Rev, error) {
	if c.injected.CompareAndSwap(false, true) {
		// Force a CAS conflict by rewriting the pending record with a newer ModRev.
		if _, _, err := c.Store.Txn(ctx, nil, []store.Op{{
			Type:  store.OpPut,
			Key:   "/sys/nodes/" + c.node,
			Value: []byte(`{"$lkal":"0.1","evictable":false,"rack":"` + c.rack + `","since":100,"state":{"$enum":"pending"}}`),
		}}); err != nil {
			return false, 0, err
		}
	}
	return c.Store.Txn(ctx, cmps, ops)
}

func TestAggregatorConfigValidation(t *testing.T) {
	aggs := []string{"agg-a"}
	hub := newMemHub(aggs, nil)
	st := memstore.New()
	engCfg := liveness.DefaultAggregatorEngineConfig("agg-a", aggs)

	if _, err := liveness.NewAggregator(liveness.AggregatorConfig{
		Engine:    engCfg,
		Transport: hub.endpoints[liveness.Addr{ID: "agg-a"}],
	}); err == nil {
		t.Fatal("NewAggregator(nil Store) = nil, want error")
	}
	if _, err := liveness.NewAggregator(liveness.AggregatorConfig{
		Engine: engCfg,
		Store:  st,
	}); err == nil {
		t.Fatal("NewAggregator(nil Transport) = nil, want error")
	}
	if _, err := liveness.NewAggregator(liveness.AggregatorConfig{
		Engine:       engCfg,
		Store:        st,
		Transport:    hub.endpoints[liveness.Addr{ID: "agg-a"}],
		TickInterval: -time.Millisecond,
	}); err == nil {
		t.Fatal("NewAggregator(negative TickInterval) = nil, want error")
	}
	if _, err := liveness.NewAggregator(liveness.AggregatorConfig{
		Engine:       engCfg,
		Store:        st,
		Transport:    hub.endpoints[liveness.Addr{ID: "agg-a"}],
		StoreTimeout: -time.Millisecond,
	}); err == nil {
		t.Fatal("NewAggregator(negative StoreTimeout) = nil, want error")
	}
	if _, err := liveness.NewAggregator(liveness.AggregatorConfig{
		Engine:    liveness.AggregatorEngineConfig{},
		Store:     st,
		Transport: hub.endpoints[liveness.Addr{ID: "agg-a"}],
	}); err == nil {
		t.Fatal("NewAggregator(invalid Engine) = nil, want error")
	}
}

func TestAggregatorAndNodeLifecycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		aggs := []string{"agg-a", "agg-b", "agg-c"}
		nodes := []string{"n1", "n2", "n3"}
		hub := newMemHub(aggs, nodes)
		st := memstore.New()
		reg := liveness.NewRegistry(st)

		ctx := context.Background()
		for _, id := range nodes {
			if _, err := reg.Register(ctx, id, "r1", time.Now().UnixMilli()); err != nil {
				t.Fatalf("Register(%s): %v", id, err)
			}
		}

		runCtx, cancelAll := context.WithCancel(ctx)
		var wg sync.WaitGroup
		errs := make(chan error, len(aggs)+len(nodes)+2)

		for _, id := range aggs {
			cfg := liveness.DefaultAggregatorEngineConfig(id, aggs)
			cfg.Detector.EvictionGrace = 2000 // short grace for test
			cfg.Detector.EvictionRetryInterval = 500
			agg, err := liveness.NewAggregator(liveness.AggregatorConfig{
				Engine:    cfg,
				Store:     st,
				Transport: hub.endpoints[liveness.Addr{ID: id}],
			})
			if err != nil {
				t.Fatalf("NewAggregator(%s): %v", id, err)
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs <- agg.Run(runCtx)
			}()
		}

		nodeCancels := make(map[string]context.CancelFunc, len(nodes))
		var nodeWG sync.WaitGroup
		startNode := func(id string) {
			nctx, ncancel := context.WithCancel(runCtx)
			nodeCancels[id] = ncancel
			n, err := liveness.NewNode(liveness.NodeConfig{
				Agent:       agent.DefaultConfig(id, nil),
				Aggregators: aggs,
				Transport:   hub.endpoints[liveness.Addr{ID: id, Node: true}],
			})
			if err != nil {
				t.Fatalf("NewNode(%s): %v", id, err)
			}
			nodeWG.Add(1)
			go func() {
				defer nodeWG.Done()
				errs <- n.Run(nctx)
			}()
		}
		for _, id := range nodes {
			startNode(id)
		}

		// ReadyHeartbeats requires 3 heartbeats at 1s intervals (0.1s, 1.1s, 2.1s).
		time.Sleep(3 * time.Second)
		synctest.Wait()
		for _, id := range nodes {
			rec, err := reg.Get(ctx, id)
			if err != nil {
				t.Fatalf("Get(%s): %v", id, err)
			}
			if rec.State != detector.StateReady || rec.Evictable {
				t.Fatalf("node %s = %+v, want Ready and Evictable=false", id, rec)
			}
		}

		nodeCancels["n1"]()
		hub.endpoints[liveness.Addr{ID: "n1", Node: true}].down.Store(true)

		// Cover phi suspicion, round timeout, and EvictionGrace (2s).
		time.Sleep(15 * time.Second)
		synctest.Wait()
		n1Rec, err := reg.Get(ctx, "n1")
		if err != nil {
			t.Fatal(err)
		}
		if n1Rec.State != detector.StateNotReady || !n1Rec.Evictable {
			t.Fatalf("stopped n1 = %+v, want StateNotReady and Evictable=true", n1Rec)
		}
		for _, id := range []string{"n2", "n3"} {
			rec, err := reg.Get(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if rec.State != detector.StateReady || rec.Evictable {
				t.Fatalf("running node %s = %+v, want Ready", id, rec)
			}
		}

		hub.endpoints[liveness.Addr{ID: "n1", Node: true}].down.Store(false)
		startNode("n1")
		time.Sleep(3 * time.Second)
		synctest.Wait()
		n1Rec, err = reg.Get(ctx, "n1")
		if err != nil {
			t.Fatal(err)
		}
		if n1Rec.State != detector.StateReady || n1Rec.Evictable {
			t.Fatalf("restarted n1 = %+v, want Ready and Evictable=false", n1Rec)
		}

		cancelAll()
		nodeWG.Wait()
		wg.Wait()
		close(errs)
		for err := range errs {
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("runner returned %v, want context.Canceled", err)
			}
		}
	})
}

func TestAggregatorWatchReloadAndCASRetry(t *testing.T) {
	t.Run("WatchStreamEndsReloads", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			aggs := []string{"agg-a"}
			nodes := []string{"n1", "n2"}
			hub := newMemHub(aggs, nodes)
			raw := memstore.New()
			wrapped := &dropFirstWatchStore{Store: raw}
			reg := liveness.NewRegistry(raw)

			ctx := context.Background()
			if _, err := reg.Register(ctx, "n1", "r1", time.Now().UnixMilli()); err != nil {
				t.Fatal(err)
			}
			if _, err := reg.Register(ctx, "n2", "r1", time.Now().UnixMilli()); err != nil {
				t.Fatal(err)
			}

			cfg := liveness.DefaultAggregatorEngineConfig("agg-a", aggs)
			cfg.Detector.Owners = 1
			agg, err := liveness.NewAggregator(liveness.AggregatorConfig{
				Engine:    cfg,
				Store:     wrapped,
				Transport: hub.endpoints[liveness.Addr{ID: "agg-a"}],
			})
			if err != nil {
				t.Fatal(err)
			}

			runCtx, cancel := context.WithCancel(ctx)
			var wg sync.WaitGroup
			aggErr := make(chan error, 1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				aggErr <- agg.Run(runCtx)
			}()

			// Delete n2 before reload runs so recovery relies on list rather than watch events.
			if err := reg.Delete(ctx, "n2"); err != nil {
				t.Fatal(err)
			}
			n1, err := liveness.NewNode(liveness.NodeConfig{
				Agent:       agent.DefaultConfig("n1", nil),
				Aggregators: aggs,
				Transport:   hub.endpoints[liveness.Addr{ID: "n1", Node: true}],
			})
			if err != nil {
				t.Fatal(err)
			}
			nodeErr := make(chan error, 1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				nodeErr <- n1.Run(runCtx)
			}()

			// Reload runs after 100ms, then n1 sends 3 heartbeats at 1s intervals.
			time.Sleep(3 * time.Second)
			synctest.Wait()

			rec, err := reg.Get(ctx, "n1")
			if err != nil {
				t.Fatal(err)
			}
			if rec.State != detector.StateReady {
				t.Fatalf("n1 after watch reload = %+v, want Ready", rec)
			}
			if _, err := reg.Get(ctx, "n2"); !errors.Is(err, liveness.ErrNotRegistered) {
				t.Fatalf("n2 after delete = %v, want ErrNotRegistered", err)
			}

			cancel()
			wg.Wait()
			if err := <-aggErr; !errors.Is(err, context.Canceled) {
				t.Fatalf("agg.Run = %v, want context.Canceled", err)
			}
			if err := <-nodeErr; !errors.Is(err, context.Canceled) {
				t.Fatalf("node.Run = %v, want context.Canceled", err)
			}
		})
	})

	t.Run("CASConflictDropsAndRetriesWithClockWall", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			aggs := []string{"agg-a"}
			nodes := []string{"n1"}
			hub := newMemHub(aggs, nodes)
			raw := memstore.New()
			reg := liveness.NewRegistry(raw)

			startWall := time.Unix(1700000000, 0).UTC()
			clk := offsetClock{offset: startWall.Sub(time.Now())}

			ctx := context.Background()
			if _, err := reg.Register(ctx, "n1", "r1", clk.Now().UnixMilli()); err != nil {
				t.Fatal(err)
			}

			conflictStore := &conflictFirstSetReadyStore{
				Store: raw,
				node:  "n1",
				rack:  "r1",
			}

			cfg := liveness.DefaultAggregatorEngineConfig("agg-a", aggs)
			cfg.Detector.Owners = 1
			agg, err := liveness.NewAggregator(liveness.AggregatorConfig{
				Engine:    cfg,
				Store:     conflictStore,
				Transport: hub.endpoints[liveness.Addr{ID: "agg-a"}],
				Clock:     clk,
			})
			if err != nil {
				t.Fatal(err)
			}

			runCtx, cancel := context.WithCancel(ctx)
			var wg sync.WaitGroup
			defer func() {
				cancel()
				wg.Wait()
			}()
			aggErr := make(chan error, 1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				aggErr <- agg.Run(runCtx)
			}()

			n1, err := liveness.NewNode(liveness.NodeConfig{
				Agent:       agent.DefaultConfig("n1", nil),
				Aggregators: aggs,
				Transport:   hub.endpoints[liveness.Addr{ID: "n1", Node: true}],
				Clock:       clk,
			})
			if err != nil {
				t.Fatal(err)
			}
			nodeErr := make(chan error, 1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				nodeErr <- n1.Run(runCtx)
			}()

			time.Sleep(5 * time.Second)
			synctest.Wait()

			if !conflictStore.injected.Load() {
				t.Fatal("expected first SetReady Txn to inject a CAS conflict")
			}
			rec, err := reg.Get(ctx, "n1")
			if err != nil {
				t.Fatal(err)
			}
			if rec.State != detector.StateReady {
				t.Fatalf("n1 after retry = %+v, want StateReady", rec)
			}
			if rec.Since < startWall.UnixMilli() || rec.Since > clk.Now().UnixMilli() {
				t.Fatalf("n1.Since = %d, want in [%d, %d]", rec.Since, startWall.UnixMilli(), clk.Now().UnixMilli())
			}

			cancel()
			wg.Wait()
			if err := <-aggErr; !errors.Is(err, context.Canceled) {
				t.Fatalf("agg.Run = %v, want context.Canceled", err)
			}
			if err := <-nodeErr; !errors.Is(err, context.Canceled) {
				t.Fatalf("node.Run = %v, want context.Canceled", err)
			}
		})
	})

	t.Run("CorruptRecordSkippedAndRecoveredByWatch", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			aggs := []string{"agg-a"}
			nodes := []string{"n1", "n2"}
			hub := newMemHub(aggs, nodes)
			st := memstore.New()
			reg := liveness.NewRegistry(st)

			ctx := context.Background()
			if _, err := reg.Register(ctx, "n1", "r1", time.Now().UnixMilli()); err != nil {
				t.Fatal(err)
			}
			n2Key, err := (keyspace.Key{Family: keyspace.FamilyNode, Node: "n2"}).Encode()
			if err != nil {
				t.Fatal(err)
			}
			if ok, _, err := st.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: n2Key, Value: []byte("not-valid-lkal")}}); err != nil || !ok {
				t.Fatalf("seed corrupt record = (%v, %v)", ok, err)
			}

			cfg := liveness.DefaultAggregatorEngineConfig("agg-a", aggs)
			cfg.Detector.Owners = 1
			agg, err := liveness.NewAggregator(liveness.AggregatorConfig{
				Engine:    cfg,
				Store:     st,
				Transport: hub.endpoints[liveness.Addr{ID: "agg-a"}],
			})
			if err != nil {
				t.Fatal(err)
			}

			runCtx, cancel := context.WithCancel(ctx)
			var wg sync.WaitGroup
			defer func() {
				cancel()
				wg.Wait()
			}()
			aggErr := make(chan error, 1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				aggErr <- agg.Run(runCtx)
			}()

			nodeErrs := make(chan error, 2)
			for _, id := range nodes {
				n, err := liveness.NewNode(liveness.NodeConfig{
					Agent:       agent.DefaultConfig(id, nil),
					Aggregators: aggs,
					Transport:   hub.endpoints[liveness.Addr{ID: id, Node: true}],
				})
				if err != nil {
					t.Fatal(err)
				}
				wg.Add(1)
				go func() {
					defer wg.Done()
					nodeErrs <- n.Run(runCtx)
				}()
			}

			time.Sleep(3 * time.Second)
			synctest.Wait()

			n1Rec, err := reg.Get(ctx, "n1")
			if err != nil {
				t.Fatal(err)
			}
			if n1Rec.State != detector.StateReady {
				t.Fatalf("n1 = %+v, want StateReady despite corrupt n2", n1Rec)
			}

			validPending := []byte(`{"$lkal":"0.1","evictable":false,"rack":"r1","since":1000,"state":{"$enum":"pending"}}`)
			if ok, _, err := st.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: n2Key, Value: validPending}}); err != nil || !ok {
				t.Fatalf("repair n2 = (%v, %v)", ok, err)
			}

			time.Sleep(3 * time.Second)
			synctest.Wait()

			n2Rec, err := reg.Get(ctx, "n2")
			if err != nil {
				t.Fatal(err)
			}
			if n2Rec.State != detector.StateReady {
				t.Fatalf("n2 after valid write = %+v, want StateReady", n2Rec)
			}

			cancel()
			wg.Wait()
			if err := <-aggErr; !errors.Is(err, context.Canceled) {
				t.Fatalf("agg.Run = %v, want context.Canceled", err)
			}
			close(nodeErrs)
			for err := range nodeErrs {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("node.Run = %v, want context.Canceled", err)
				}
			}
		})
	})
}

type faultyStore struct {
	store.Store
	failRanges atomic.Int64
	failAlways atomic.Bool

	mu          sync.Mutex
	watchCancel context.CancelFunc
}

func (f *faultyStore) Range(ctx context.Context, prefix string, atRev store.Rev, limit int, cont string) ([]store.KV, store.Rev, string, error) {
	if f.failAlways.Load() {
		return nil, 0, "", errors.New("injected range outage")
	}
	for {
		rem := f.failRanges.Load()
		if rem <= 0 {
			break
		}
		if f.failRanges.CompareAndSwap(rem, rem-1) {
			return nil, 0, "", errors.New("injected transient range error")
		}
	}
	return f.Store.Range(ctx, prefix, atRev, limit, cont)
}

func (f *faultyStore) Watch(ctx context.Context, prefix string, fromRev store.Rev) (store.WatchStream, error) {
	if f.failAlways.Load() {
		return nil, errors.New("injected watch outage")
	}
	wctx, cancel := context.WithCancel(ctx)
	ws, err := f.Store.Watch(wctx, prefix, fromRev)
	if err != nil {
		cancel()
		return nil, err
	}
	f.mu.Lock()
	f.watchCancel = cancel
	f.mu.Unlock()
	return ws, nil
}

func (f *faultyStore) breakWatch() {
	f.mu.Lock()
	cancel := f.watchCancel
	f.watchCancel = nil
	f.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func TestAggregatorStoreOutageRetries(t *testing.T) {
	t.Run("InitialRangeFailuresRecover", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			aggs := []string{"agg-a"}
			nodes := []string{"n1"}
			hub := newMemHub(aggs, nodes)
			raw := memstore.New()
			faulty := &faultyStore{Store: raw}
			faulty.failRanges.Store(5)
			reg := liveness.NewRegistry(raw)

			ctx := context.Background()
			if _, err := reg.Register(ctx, "n1", "r1", time.Now().UnixMilli()); err != nil {
				t.Fatal(err)
			}

			cfg := liveness.DefaultAggregatorEngineConfig("agg-a", aggs)
			cfg.Detector.Owners = 1
			agg, err := liveness.NewAggregator(liveness.AggregatorConfig{
				Engine:    cfg,
				Store:     faulty,
				Transport: hub.endpoints[liveness.Addr{ID: "agg-a"}],
			})
			if err != nil {
				t.Fatal(err)
			}

			runCtx, cancel := context.WithCancel(ctx)
			var wg sync.WaitGroup
			defer func() {
				cancel()
				wg.Wait()
			}()
			aggErr := make(chan error, 1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				aggErr <- agg.Run(runCtx)
			}()

			n1, err := liveness.NewNode(liveness.NodeConfig{
				Agent:       agent.DefaultConfig("n1", nil),
				Aggregators: aggs,
				Transport:   hub.endpoints[liveness.Addr{ID: "n1", Node: true}],
			})
			if err != nil {
				t.Fatal(err)
			}
			nodeErr := make(chan error, 1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				nodeErr <- n1.Run(runCtx)
			}()

			time.Sleep(350 * time.Millisecond)
			synctest.Wait()
			select {
			case err := <-aggErr:
				t.Fatalf("agg.Run exited early during initial outage: %v", err)
			default:
			}

			// Exponential backoff across 5 failures sums to 3.1s, plus 3s for 3 heartbeats.
			time.Sleep(7 * time.Second)
			synctest.Wait()
			rec, err := reg.Get(ctx, "n1")
			if err != nil {
				t.Fatal(err)
			}
			if rec.State != detector.StateReady {
				t.Fatalf("n1 after store recovery = %+v, want StateReady", rec)
			}

			cancel()
			wg.Wait()
			if err := <-aggErr; !errors.Is(err, context.Canceled) {
				t.Fatalf("agg.Run = %v, want context.Canceled", err)
			}
			if err := <-nodeErr; !errors.Is(err, context.Canceled) {
				t.Fatalf("node.Run = %v, want context.Canceled", err)
			}
		})
	})

	t.Run("ReloadRangeFailureRecoversAndPicksUpChange", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			aggs := []string{"agg-a"}
			nodes := []string{"n1", "n2"}
			hub := newMemHub(aggs, nodes)
			raw := memstore.New()
			faulty := &faultyStore{Store: raw}
			reg := liveness.NewRegistry(raw)

			ctx := context.Background()
			if _, err := reg.Register(ctx, "n1", "r1", time.Now().UnixMilli()); err != nil {
				t.Fatal(err)
			}

			cfg := liveness.DefaultAggregatorEngineConfig("agg-a", aggs)
			cfg.Detector.Owners = 1
			agg, err := liveness.NewAggregator(liveness.AggregatorConfig{
				Engine:    cfg,
				Store:     faulty,
				Transport: hub.endpoints[liveness.Addr{ID: "agg-a"}],
			})
			if err != nil {
				t.Fatal(err)
			}

			runCtx, cancel := context.WithCancel(ctx)
			var wg sync.WaitGroup
			defer func() {
				cancel()
				wg.Wait()
			}()
			aggErr := make(chan error, 1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				aggErr <- agg.Run(runCtx)
			}()

			nodeErrs := make(chan error, 2)
			for _, id := range nodes {
				n, err := liveness.NewNode(liveness.NodeConfig{
					Agent:       agent.DefaultConfig(id, nil),
					Aggregators: aggs,
					Transport:   hub.endpoints[liveness.Addr{ID: id, Node: true}],
				})
				if err != nil {
					t.Fatal(err)
				}
				wg.Add(1)
				go func() {
					defer wg.Done()
					nodeErrs <- n.Run(runCtx)
				}()
			}

			time.Sleep(3 * time.Second)
			synctest.Wait()
			n1Rec, err := reg.Get(ctx, "n1")
			if err != nil || n1Rec.State != detector.StateReady {
				t.Fatalf("n1 = (%+v, %v), want StateReady", n1Rec, err)
			}

			faulty.failAlways.Store(true)
			faulty.breakWatch()
			if _, err := reg.Register(ctx, "n2", "r1", time.Now().UnixMilli()); err != nil {
				t.Fatal(err)
			}

			time.Sleep(500 * time.Millisecond)
			synctest.Wait()
			select {
			case err := <-aggErr:
				t.Fatalf("agg.Run exited during reload outage: %v", err)
			default:
			}

			faulty.failAlways.Store(false)
			time.Sleep(4 * time.Second)
			synctest.Wait()
			n2Rec, err := reg.Get(ctx, "n2")
			if err != nil {
				t.Fatal(err)
			}
			if n2Rec.State != detector.StateReady {
				t.Fatalf("n2 after reload recovery = %+v, want StateReady", n2Rec)
			}

			cancel()
			wg.Wait()
			if err := <-aggErr; !errors.Is(err, context.Canceled) {
				t.Fatalf("agg.Run = %v, want context.Canceled", err)
			}
			close(nodeErrs)
			for err := range nodeErrs {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("node.Run = %v, want context.Canceled", err)
				}
			}
		})
	})

	t.Run("HandlesMessagesDuringOutageAndCancelsCleanly", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			aggs := []string{"agg-a", "agg-b"}
			nodes := []string{"n1"}
			hub := newMemHub(aggs, nodes)
			raw := memstore.New()
			faulty := &faultyStore{Store: raw}
			faulty.failAlways.Store(true)

			agg, err := liveness.NewAggregator(liveness.AggregatorConfig{
				Engine:    liveness.DefaultAggregatorEngineConfig("agg-a", aggs),
				Store:     faulty,
				Transport: hub.endpoints[liveness.Addr{ID: "agg-a"}],
			})
			if err != nil {
				t.Fatal(err)
			}

			runCtx, cancel := context.WithCancel(context.Background())
			var wg sync.WaitGroup
			defer func() {
				cancel()
				wg.Wait()
			}()
			aggErr := make(chan error, 1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				aggErr <- agg.Run(runCtx)
			}()

			time.Sleep(350 * time.Millisecond)
			synctest.Wait()

			aggBEp := hub.endpoints[liveness.Addr{ID: "agg-b"}]
			n1Ep := hub.endpoints[liveness.Addr{ID: "n1", Node: true}]
			aggBEp.Send(liveness.Message{
				Kind:  liveness.MsgVoteRequest,
				From:  liveness.Addr{ID: "agg-b"},
				To:    liveness.Addr{ID: "agg-a"},
				Node:  "n1",
				Round: 1,
			})
			aggBEp.Send(liveness.Message{
				Kind:  liveness.MsgProbeRequest,
				From:  liveness.Addr{ID: "agg-b"},
				To:    liveness.Addr{ID: "agg-a"},
				Node:  "n1",
				Round: 2,
			})
			synctest.Wait()

			var gotVote, gotPing bool
			for len(aggBEp.ch) > 0 {
				m := <-aggBEp.ch
				if m.Kind == liveness.MsgVote && m.Node == "n1" && m.Round == 1 {
					gotVote = true
				}
			}
			for len(n1Ep.ch) > 0 {
				m := <-n1Ep.ch
				if m.Kind == liveness.MsgPing && m.To == (liveness.Addr{ID: "n1", Node: true}) {
					gotPing = true
				}
			}
			if !gotVote || !gotPing {
				t.Fatalf("message handling during outage: gotVote=%v gotPing=%v", gotVote, gotPing)
			}

			cancel()
			wg.Wait()
			if err := <-aggErr; !errors.Is(err, context.Canceled) {
				t.Fatalf("agg.Run = %v, want context.Canceled", err)
			}
		})
	})
}

type blockingStore struct {
	store.Store

	rangeCalls atomic.Int64
	watchCalls atomic.Int64

	mu           sync.Mutex
	rangeGate    chan struct{}
	watchGate    chan struct{}
	rangeErr     error
	watchErr     error
	txnErr       error
	watchCancel  context.CancelFunc
	customStream func(ctx context.Context, inner store.WatchStream) store.WatchStream
	rangeTimes   []time.Time
}

func (b *blockingStore) setRangeGate(ch chan struct{}) {
	b.mu.Lock()
	b.rangeGate = ch
	b.mu.Unlock()
}

func (b *blockingStore) setWatchGate(ch chan struct{}) {
	b.mu.Lock()
	b.watchGate = ch
	b.mu.Unlock()
}

func (b *blockingStore) setErrors(rangeErr, watchErr, txnErr error) {
	b.mu.Lock()
	b.rangeErr = rangeErr
	b.watchErr = watchErr
	b.txnErr = txnErr
	b.mu.Unlock()
}

func (b *blockingStore) breakWatch() {
	b.mu.Lock()
	cancel := b.watchCancel
	b.watchCancel = nil
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (b *blockingStore) Range(ctx context.Context, prefix string, atRev store.Rev, limit int, cont string) ([]store.KV, store.Rev, string, error) {
	b.rangeCalls.Add(1)
	b.mu.Lock()
	b.rangeTimes = append(b.rangeTimes, time.Now())
	gate := b.rangeGate
	rerr := b.rangeErr
	b.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, 0, "", ctx.Err()
		}
	}
	if rerr != nil {
		return nil, 0, "", rerr
	}
	return b.Store.Range(ctx, prefix, atRev, limit, cont)
}

func (b *blockingStore) Watch(ctx context.Context, prefix string, fromRev store.Rev) (store.WatchStream, error) {
	b.watchCalls.Add(1)
	b.mu.Lock()
	gate := b.watchGate
	werr := b.watchErr
	wrap := b.customStream
	b.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if werr != nil {
		return nil, werr
	}
	wctx, cancel := context.WithCancel(ctx)
	ws, err := b.Store.Watch(wctx, prefix, fromRev)
	if err != nil {
		cancel()
		return nil, err
	}
	b.mu.Lock()
	b.watchCancel = cancel
	b.mu.Unlock()
	if wrap != nil {
		return wrap(wctx, ws), nil
	}
	return ws, nil
}

func (b *blockingStore) Txn(ctx context.Context, cmps []store.Cmp, ops []store.Op) (bool, store.Rev, error) {
	b.mu.Lock()
	terr := b.txnErr
	b.mu.Unlock()
	if terr != nil {
		return false, 0, terr
	}
	return b.Store.Txn(ctx, cmps, ops)
}

func TestAggregatorNonBlockingStore(t *testing.T) {
	runBlockingReloadCase := func(t *testing.T, blockWatch bool) {
		t.Helper()
		synctest.Test(t, func(t *testing.T) {
			aggs := []string{"agg-a", "agg-b"}
			nodes := []string{"n1", "n2"}
			hub := newMemHub(aggs, nodes)
			raw := memstore.New()
			bs := &blockingStore{Store: raw}
			reg := liveness.NewRegistry(raw)

			ctx := context.Background()
			rec1, err := reg.Register(ctx, "n1", "r1", time.Now().UnixMilli())
			if err != nil {
				t.Fatal(err)
			}
			if out, err := reg.SetReady(ctx, rec1, time.Now().UnixMilli()); err != nil || out != liveness.Committed {
				t.Fatalf("SetReady(n1): (%v, %v)", out, err)
			}

			cfg := liveness.DefaultAggregatorEngineConfig("agg-a", aggs)
			cfg.Detector.Owners = 2
			agg, err := liveness.NewAggregator(liveness.AggregatorConfig{
				Engine:       cfg,
				Store:        bs,
				Transport:    hub.endpoints[liveness.Addr{ID: "agg-a"}],
				StoreTimeout: 30 * time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}

			runCtx, cancel := context.WithCancel(ctx)
			var wg sync.WaitGroup
			defer func() {
				cancel()
				wg.Wait()
			}()
			aggErr := make(chan error, 1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				aggErr <- agg.Run(runCtx)
			}()

			synctest.Wait()

			gate := make(chan struct{})
			if blockWatch {
				bs.setWatchGate(gate)
			} else {
				bs.setRangeGate(gate)
			}
			bs.breakWatch()

			// Wait past the 100ms reload delay so loadAndWatch blocks in the store call,
			// and advance 10s so n1 triggers phi suspicion and agg-a emits its own VoteRequest.
			time.Sleep(10 * time.Second)
			synctest.Wait()

			aggBEp := hub.endpoints[liveness.Addr{ID: "agg-b"}]
			n1Ep := hub.endpoints[liveness.Addr{ID: "n1", Node: true}]

			var gotOwnVoteReq bool
			for len(aggBEp.ch) > 0 {
				m := <-aggBEp.ch
				if m.Kind == liveness.MsgVoteRequest && m.Node == "n1" {
					gotOwnVoteReq = true
				}
			}
			if !gotOwnVoteReq {
				t.Fatal("expected aggregator to emit its own MsgVoteRequest while store call was blocked")
			}

			aggBEp.Send(liveness.Message{
				Kind:  liveness.MsgVoteRequest,
				From:  liveness.Addr{ID: "agg-b"},
				To:    liveness.Addr{ID: "agg-a"},
				Node:  "n1",
				Round: 11,
			})
			aggBEp.Send(liveness.Message{
				Kind:  liveness.MsgProbeRequest,
				From:  liveness.Addr{ID: "agg-b"},
				To:    liveness.Addr{ID: "agg-a"},
				Node:  "n1",
				Round: 12,
			})
			synctest.Wait()

			var gotVote, gotPing bool
			for len(aggBEp.ch) > 0 {
				m := <-aggBEp.ch
				if m.Kind == liveness.MsgVote && m.Node == "n1" && m.Round == 11 {
					gotVote = true
				}
			}
			for len(n1Ep.ch) > 0 {
				m := <-n1Ep.ch
				if m.Kind == liveness.MsgPing && m.To == (liveness.Addr{ID: "n1", Node: true}) {
					gotPing = true
				}
			}
			if !gotVote || !gotPing {
				t.Fatalf("responses while store blocked: gotVote=%v gotPing=%v", gotVote, gotPing)
			}

			if _, err := reg.Register(ctx, "n2", "r1", time.Now().UnixMilli()); err != nil {
				t.Fatal(err)
			}
			if blockWatch {
				bs.setWatchGate(nil)
			} else {
				bs.setRangeGate(nil)
			}
			close(gate)

			n2Cfg := agent.DefaultConfig("n2", nil)
			n2Cfg.Owners = 2
			n2, err := liveness.NewNode(liveness.NodeConfig{
				Agent:       n2Cfg,
				Aggregators: aggs,
				Transport:   hub.endpoints[liveness.Addr{ID: "n2", Node: true}],
			})
			if err != nil {
				t.Fatal(err)
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = n2.Run(runCtx)
			}()

			time.Sleep(4 * time.Second)
			synctest.Wait()
			n2Rec, err := reg.Get(ctx, "n2")
			if err != nil {
				t.Fatal(err)
			}
			if n2Rec.State != detector.StateReady {
				t.Fatalf("n2 after release = %+v, want StateReady", n2Rec)
			}

			cancel()
			wg.Wait()
			if err := <-aggErr; !errors.Is(err, context.Canceled) {
				t.Fatalf("agg.Run = %v, want context.Canceled", err)
			}
		})
	}

	t.Run("RangeBlocksAndRecovers", func(t *testing.T) {
		runBlockingReloadCase(t, false)
	})
	t.Run("WatchBlocksAndRecovers", func(t *testing.T) {
		runBlockingReloadCase(t, true)
	})

	for _, blockWatch := range []bool{false, true} {
		name := "CancelWhileRangeBlocked"
		if blockWatch {
			name = "CancelWhileWatchBlocked"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				aggs := []string{"agg-a", "agg-b"}
				hub := newMemHub(aggs, []string{"n1"})
				bs := &blockingStore{Store: memstore.New()}
				gate := make(chan struct{})
				if blockWatch {
					bs.setWatchGate(gate)
				} else {
					bs.setRangeGate(gate)
				}

				agg, err := liveness.NewAggregator(liveness.AggregatorConfig{
					Engine:       liveness.DefaultAggregatorEngineConfig("agg-a", aggs),
					Store:        bs,
					Transport:    hub.endpoints[liveness.Addr{ID: "agg-a"}],
					StoreTimeout: 30 * time.Second,
				})
				if err != nil {
					t.Fatal(err)
				}

				runCtx, cancel := context.WithCancel(context.Background())
				aggErr := make(chan error, 1)
				go func() {
					aggErr <- agg.Run(runCtx)
				}()

				synctest.Wait()

				// While initial load is blocked, incoming requests are still answered.
				aggBEp := hub.endpoints[liveness.Addr{ID: "agg-b"}]
				aggBEp.Send(liveness.Message{
					Kind:  liveness.MsgVoteRequest,
					From:  liveness.Addr{ID: "agg-b"},
					To:    liveness.Addr{ID: "agg-a"},
					Node:  "n1",
					Round: 1,
				})
				synctest.Wait()
				if len(aggBEp.ch) == 0 {
					t.Fatal("expected MsgVote while initial store call was blocked")
				}

				cancelAt := time.Now()
				cancel()
				err = <-aggErr
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("agg.Run = %v, want context.Canceled", err)
				}
				if elapsed := time.Since(cancelAt); elapsed != 0 {
					t.Fatalf("Run took %v after cancel, want immediate return", elapsed)
				}
			})
		})
	}

	for _, blockWatch := range []bool{false, true} {
		name := "StoreTimeoutBoundsRange"
		if blockWatch {
			name = "StoreTimeoutBoundsWatch"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				aggs := []string{"agg-a"}
				hub := newMemHub(aggs, nil)
				bs := &blockingStore{Store: memstore.New()}
				gate := make(chan struct{})
				if blockWatch {
					bs.setWatchGate(gate)
				} else {
					bs.setRangeGate(gate)
				}

				var gotErrs []error
				agg, err := liveness.NewAggregator(liveness.AggregatorConfig{
					Engine:       liveness.DefaultAggregatorEngineConfig("agg-a", aggs),
					Store:        bs,
					Transport:    hub.endpoints[liveness.Addr{ID: "agg-a"}],
					TickInterval: 100 * time.Millisecond,
					StoreTimeout: 500 * time.Millisecond,
					OnError:      func(err error) { gotErrs = append(gotErrs, err) },
				})
				if err != nil {
					t.Fatal(err)
				}

				runCtx, cancel := context.WithCancel(context.Background())
				aggErr := make(chan error, 1)
				go func() {
					aggErr <- agg.Run(runCtx)
				}()

				// 500ms timeout + 100ms backoff + 50ms margin -> second attempt has started.
				time.Sleep(650 * time.Millisecond)
				synctest.Wait()

				if blockWatch {
					if bs.watchCalls.Load() != 2 {
						t.Fatalf("watchCalls = %d, want 2", bs.watchCalls.Load())
					}
				} else {
					if bs.rangeCalls.Load() != 2 {
						t.Fatalf("rangeCalls = %d, want 2", bs.rangeCalls.Load())
					}
				}

				cancel()
				if err := <-aggErr; !errors.Is(err, context.Canceled) {
					t.Fatalf("agg.Run = %v, want context.Canceled", err)
				}
				if len(gotErrs) == 0 || !errors.Is(gotErrs[0], context.DeadlineExceeded) {
					t.Fatalf("gotErrs = %v, want DeadlineExceeded", gotErrs)
				}
			})
		})
	}
}

func TestAggregatorReloadBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		aggs := []string{"agg-a"}
		hub := newMemHub(aggs, nil)
		bs := &blockingStore{Store: memstore.New()}
		bs.setErrors(nil, errors.New("watch down"), nil)

		agg, err := liveness.NewAggregator(liveness.AggregatorConfig{
			Engine:       liveness.DefaultAggregatorEngineConfig("agg-a", aggs),
			Store:        bs,
			Transport:    hub.endpoints[liveness.Addr{ID: "agg-a"}],
			TickInterval: 100 * time.Millisecond,
		})
		if err != nil {
			t.Fatal(err)
		}

		runCtx, cancel := context.WithCancel(context.Background())
		aggErr := make(chan error, 1)
		go func() {
			aggErr <- agg.Run(runCtx)
		}()

		// Fast-failing Watch triggers retries at 0, 0.1s, 0.3s, 0.7s, 1.5s, 3.1s, 6.3s, 11.3s, 16.3s, 21.3s, 26.3s.
		time.Sleep(250 * time.Millisecond)
		synctest.Wait()
		if got := bs.rangeCalls.Load(); got != 2 {
			t.Fatalf("rangeCalls at 250ms = %d, want 2", got)
		}

		time.Sleep(29_750 * time.Millisecond)
		synctest.Wait()
		if got := bs.rangeCalls.Load(); got != 11 {
			t.Fatalf("rangeCalls at 30s = %d, want 11 (capped at 5s)", got)
		}

		// Wait past the 5s backoff interval so the healthy watch resets backoff.
		bs.setErrors(nil, nil, nil)
		time.Sleep(7 * time.Second)
		synctest.Wait()
		before := bs.rangeCalls.Load()

		bs.setErrors(errors.New("range down"), nil, nil)
		bs.breakWatch()
		time.Sleep(150 * time.Millisecond)
		synctest.Wait()
		if got := bs.rangeCalls.Load(); got != before+1 {
			t.Fatalf("rangeCalls 150ms after reset = %d, want %d", got, before+1)
		}

		cancel()
		if err := <-aggErr; !errors.Is(err, context.Canceled) {
			t.Fatalf("agg.Run = %v, want context.Canceled", err)
		}
	})
}

func TestAggregatorWatchEndBackoff(t *testing.T) {
	t.Run("ImmediateWatchCloseGrowsAndCapsAt5s", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			aggs := []string{"agg-a"}
			hub := newMemHub(aggs, nil)
			bs := &blockingStore{Store: memstore.New()}
			bs.customStream = func(ctx context.Context, inner store.WatchStream) store.WatchStream {
				ch := make(chan store.Event)
				close(ch)
				return &closedWatchStream{inner: inner, ch: ch, ctx: ctx}
			}

			var errCount int
			agg, err := liveness.NewAggregator(liveness.AggregatorConfig{
				Engine:       liveness.DefaultAggregatorEngineConfig("agg-a", aggs),
				Store:        bs,
				Transport:    hub.endpoints[liveness.Addr{ID: "agg-a"}],
				TickInterval: 100 * time.Millisecond,
				OnError:      func(error) { errCount++ },
			})
			if err != nil {
				t.Fatal(err)
			}

			runCtx, cancel := context.WithCancel(context.Background())
			aggErr := make(chan error, 1)
			go func() {
				aggErr <- agg.Run(runCtx)
			}()

			time.Sleep(60 * time.Second)
			synctest.Wait()

			cancel()
			if err := <-aggErr; !errors.Is(err, context.Canceled) {
				t.Fatalf("agg.Run = %v, want context.Canceled", err)
			}

			bs.mu.Lock()
			times := append([]time.Time(nil), bs.rangeTimes...)
			bs.mu.Unlock()

			// Over 60s with 100ms initial backoff capped at 5s: 17 calls total.
			if len(times) >= 30 {
				t.Fatalf("range calls over 60s = %d, want bounded well below 60", len(times))
			}
			var maxGap time.Duration
			for i := 1; i < len(times); i++ {
				gap := times[i].Sub(times[i-1])
				if gap > maxGap {
					maxGap = gap
				}
			}
			if maxGap != 5*time.Second {
				t.Fatalf("max reload gap = %v, want 5s (gaps from %d calls)", maxGap, len(times))
			}
			if errCount != len(times) {
				t.Fatalf("OnError count = %d, want %d", errCount, len(times))
			}
		})
	})

	t.Run("WatchStaysOpenThenEndsReloadsAfterOneTick", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			aggs := []string{"agg-a"}
			hub := newMemHub(aggs, nil)
			bs := &blockingStore{Store: memstore.New()}
			var closes atomic.Int64
			bs.customStream = func(ctx context.Context, inner store.WatchStream) store.WatchStream {
				// First 3 streams end immediately to grow backoff to 400ms; the 4th stays open.
				if closes.Add(1) <= 3 {
					ch := make(chan store.Event)
					close(ch)
					return &closedWatchStream{inner: inner, ch: ch, ctx: ctx}
				}
				return inner
			}

			agg, err := liveness.NewAggregator(liveness.AggregatorConfig{
				Engine:       liveness.DefaultAggregatorEngineConfig("agg-a", aggs),
				Store:        bs,
				Transport:    hub.endpoints[liveness.Addr{ID: "agg-a"}],
				TickInterval: 100 * time.Millisecond,
			})
			if err != nil {
				t.Fatal(err)
			}

			runCtx, cancel := context.WithCancel(context.Background())
			aggErr := make(chan error, 1)
			go func() {
				aggErr <- agg.Run(runCtx)
			}()

			// First 3 immediate closes schedule retries at 0, 100ms, 300ms, 700ms (4th opens at 700ms).
			// Wait until t=1.5s so the 4th watch has stayed open 800ms (>= 400ms backoff).
			time.Sleep(1500 * time.Millisecond)
			synctest.Wait()
			callsBeforeBreak := bs.rangeCalls.Load()

			// Break the long-lived watch once: reload must run after one tick (100ms), not 800ms.
			bs.breakWatch()
			time.Sleep(150 * time.Millisecond)
			synctest.Wait()
			if got := bs.rangeCalls.Load(); got != callsBeforeBreak+1 {
				t.Fatalf("rangeCalls 150ms after ending long-lived watch = %d, want %d", got, callsBeforeBreak+1)
			}

			cancel()
			if err := <-aggErr; !errors.Is(err, context.Canceled) {
				t.Fatalf("agg.Run = %v, want context.Canceled", err)
			}
		})
	})
}

func TestAggregatorRunSingleUse(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		aggs := []string{"agg-a"}
		hub := newMemHub(aggs, nil)
		agg, err := liveness.NewAggregator(liveness.AggregatorConfig{
			Engine:    liveness.DefaultAggregatorEngineConfig("agg-a", aggs),
			Store:     memstore.New(),
			Transport: hub.endpoints[liveness.Addr{ID: "agg-a"}],
		})
		if err != nil {
			t.Fatal(err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- agg.Run(ctx)
		}()
		synctest.Wait()

		if err := agg.Run(context.Background()); !errors.Is(err, liveness.ErrAlreadyRun) {
			t.Fatalf("concurrent second Aggregator.Run = %v, want ErrAlreadyRun", err)
		}

		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("first Aggregator.Run = %v, want context.Canceled", err)
		}
		if err := agg.Run(context.Background()); !errors.Is(err, liveness.ErrAlreadyRun) {
			t.Fatalf("sequential second Aggregator.Run = %v, want ErrAlreadyRun", err)
		}
	})
}

func TestAggregatorOnError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		aggs := []string{"agg-a"}
		nodes := []string{"n-write", "n-evict"}
		hub := newMemHub(aggs, nodes)
		raw := memstore.New()
		bs := &blockingStore{Store: raw}
		reg := liveness.NewRegistry(raw)

		ctx := context.Background()
		nWriteRec, err := reg.Register(ctx, "n-write", "r1", time.Now().UnixMilli())
		if err != nil {
			t.Fatal(err)
		}
		nEvictRec, err := reg.Register(ctx, "n-evict", "r1", time.Now().UnixMilli())
		if err != nil {
			t.Fatal(err)
		}
		if out, err := reg.SetReady(ctx, nEvictRec, time.Now().UnixMilli()); err != nil || out != liveness.Committed {
			t.Fatalf("SetReady(n-evict): (%v, %v)", out, err)
		}
		nEvictRec, _ = reg.Get(ctx, "n-evict")
		if out, err := reg.SetNotReady(ctx, nEvictRec, time.Now().UnixMilli()); err != nil || out != liveness.Committed {
			t.Fatalf("SetNotReady(n-evict): (%v, %v)", out, err)
		}
		badListKey, err := (keyspace.Key{Family: keyspace.FamilyNode, Node: "bad-list"}).Encode()
		if err != nil {
			t.Fatal(err)
		}
		if ok, _, err := raw.Txn(ctx, nil, []store.Op{
			{Type: store.OpPut, Key: badListKey, Value: []byte("corrupt-list")},
			{Type: store.OpPut, Key: keyspace.EvictionBudgetKey, Value: []byte("corrupt-budget")},
		}); err != nil || !ok {
			t.Fatalf("seed corrupt entries = (%v, %v)", ok, err)
		}

		rangeErr := errors.New("injected range failure")
		watchErr := errors.New("injected watch failure")
		txnErr := errors.New("injected txn failure")
		streamErr := errors.New("injected stream failure")

		// Start with failing Range, then failing Watch, then healthy store.
		bs.setErrors(rangeErr, nil, txnErr)

		// Append without a mutex so -race verifies OnError runs on the single Run goroutine.
		var gotErrs []error
		cfg := liveness.DefaultAggregatorEngineConfig("agg-a", aggs)
		cfg.Detector.Owners = 1
		cfg.Detector.EvictionGrace = 0
		cfg.Detector.EvictionRetryInterval = 200
		agg, err := liveness.NewAggregator(liveness.AggregatorConfig{
			Engine:       cfg,
			Store:        bs,
			Transport:    hub.endpoints[liveness.Addr{ID: "agg-a"}],
			TickInterval: 100 * time.Millisecond,
			OnError:      func(err error) { gotErrs = append(gotErrs, err) },
		})
		if err != nil {
			t.Fatal(err)
		}

		runCtx, cancel := context.WithCancel(ctx)
		aggErr := make(chan error, 1)
		go func() {
			aggErr <- agg.Run(runCtx)
		}()

		synctest.Wait()

		bs.setErrors(nil, watchErr, txnErr)
		time.Sleep(150 * time.Millisecond)
		synctest.Wait()

		bs.setErrors(nil, nil, txnErr)
		bs.mu.Lock()
		bs.customStream = func(_ context.Context, inner store.WatchStream) store.WatchStream {
			return &errWatchStream{WatchStream: inner, err: streamErr}
		}
		bs.mu.Unlock()
		time.Sleep(250 * time.Millisecond)
		synctest.Wait()

		// Trigger SetReady Txn error on n-write, and advance past phi suspicion for n-evict GrantEviction.
		aggEp := hub.endpoints[liveness.Addr{ID: "agg-a"}]
		for range 3 {
			aggEp.Send(liveness.Message{
				Kind: liveness.MsgHeartbeat,
				From: liveness.Addr{ID: nWriteRec.Node, Node: true},
				To:   liveness.Addr{ID: "agg-a"},
				Node: nWriteRec.Node,
			})
		}
		time.Sleep(10 * time.Second)
		synctest.Wait()

		// Put an undecodable record over the active watch.
		badWatchKey, err := (keyspace.Key{Family: keyspace.FamilyNode, Node: "bad-watch"}).Encode()
		if err != nil {
			t.Fatal(err)
		}
		if ok, _, err := raw.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: badWatchKey, Value: []byte("corrupt-watch")}}); err != nil || !ok {
			t.Fatalf("put bad-watch = (%v, %v)", ok, err)
		}
		synctest.Wait()

		// Break the active watch so WatchStream.Err() is reported.
		bs.breakWatch()
		synctest.Wait()

		cancel()
		if err := <-aggErr; !errors.Is(err, context.Canceled) {
			t.Fatalf("agg.Run = %v, want context.Canceled", err)
		}

		hasMatch := func(target error, substr string) bool {
			for _, err := range gotErrs {
				if (target == nil || errors.Is(err, target)) && (substr == "" || strings.Contains(err.Error(), substr)) {
					return true
				}
			}
			return false
		}
		if !hasMatch(rangeErr, "list nodes") {
			t.Errorf("missing list error in %v", gotErrs)
		}
		if !hasMatch(watchErr, "watch nodes") {
			t.Errorf("missing watch open error in %v", gotErrs)
		}
		if !hasMatch(streamErr, "watch nodes") {
			t.Errorf("missing watch stream error in %v", gotErrs)
		}
		if !hasMatch(liveness.ErrCorruptRecord, "bad-list") {
			t.Errorf("missing list decode error for bad-list in %v", gotErrs)
		}
		if !hasMatch(liveness.ErrCorruptRecord, "bad-watch") {
			t.Errorf("missing watch decode error for bad-watch in %v", gotErrs)
		}
		if !hasMatch(liveness.ErrCorruptRecord, "n-evict") {
			t.Errorf("missing corrupt budget error for n-evict in %v", gotErrs)
		}
		if !hasMatch(txnErr, "n-write") {
			t.Errorf("missing write txn error for n-write in %v", gotErrs)
		}
	})
}

type errWatchStream struct {
	store.WatchStream
	err error
}

func (e *errWatchStream) Err() error { return e.err }

func TestAggregatorRoundBase(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		aggs := []string{"agg-a", "agg-b"}
		t0 := time.Unix(1_700_000_000, 0).UTC()

		firstRound := func(t *testing.T, clk liveness.Clock, explicitBase uint64) uint64 {
			t.Helper()
			hub := newMemHub(aggs, nil)
			raw := memstore.New()
			reg := liveness.NewRegistry(raw)
			ctx := context.Background()
			rec, err := reg.Register(ctx, "n1", "r1", t0.UnixMilli())
			if err != nil {
				t.Fatal(err)
			}
			if out, err := reg.SetReady(ctx, rec, t0.UnixMilli()); err != nil || out != liveness.Committed {
				t.Fatalf("SetReady: (%v, %v)", out, err)
			}

			cfg := liveness.DefaultAggregatorEngineConfig("agg-a", aggs)
			cfg.Detector.RoundBase = explicitBase
			agg, err := liveness.NewAggregator(liveness.AggregatorConfig{
				Engine:    cfg,
				Store:     raw,
				Transport: hub.endpoints[liveness.Addr{ID: "agg-a"}],
				Clock:     clk,
			})
			if err != nil {
				t.Fatal(err)
			}

			runCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- agg.Run(runCtx)
			}()

			time.Sleep(10 * time.Second)
			synctest.Wait()

			var round uint64
			aggBEp := hub.endpoints[liveness.Addr{ID: "agg-b"}]
			for len(aggBEp.ch) > 0 {
				m := <-aggBEp.ch
				if m.Kind == liveness.MsgVoteRequest && m.Node == "n1" {
					round = m.Round
					break
				}
			}
			cancel()
			<-done
			if round == 0 {
				t.Fatal("expected MsgVoteRequest round > 0")
			}
			return round
		}

		r1 := firstRound(t, offsetClock{offset: t0.Sub(time.Now())}, 0)
		r2 := firstRound(t, offsetClock{offset: t0.Add(time.Millisecond).Sub(time.Now())}, 0)
		wantR1 := (uint64(t0.UnixMilli()) << 10) + 1
		wantR2 := (uint64(t0.Add(time.Millisecond).UnixMilli()) << 10) + 1
		if r1 != wantR1 || r2 != wantR2 {
			t.Fatalf("rounds = (%d, %d), want (%d, %d)", r1, r2, wantR1, wantR2)
		}
		if r2-r1 != 1024 {
			t.Fatalf("r2 - r1 = %d, want 1024 for disjoint first 1024 rounds", r2-r1)
		}

		const explicitBase = uint64(42_000)
		r3 := firstRound(t, offsetClock{offset: t0.Sub(time.Now())}, explicitBase)
		if r3 != explicitBase+1 {
			t.Fatalf("explicit RoundBase first round = %d, want %d", r3, explicitBase+1)
		}
	})
}
