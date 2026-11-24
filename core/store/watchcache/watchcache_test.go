package watchcache_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"runtime"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/memstore"
	"github.com/Kaikei-e/Nayutar/core/store/watchcache"
)

func memstoreFactory(*testing.T) store.Store {
	return memstore.New()
}

func TestInitialState(t *testing.T) {
	checkInitialState(t, memstoreFactory, noFallbackOpts())
}

func checkInitialState(t *testing.T, newStore func(*testing.T) store.Store, opts scenarioOpts) {
	runScenario(t, opts, func(t *testing.T) {
		s := newStore(t)
		mustTxn(t, s,
			putOp("/p/k1", "v1"),
			putOp("/p/k2", "v2"),
			putOp("/p/k3", "v3"),
			putOp("/p/k4", "v4"),
			putOp("/p/k5", "v5"),
			putOp("/other/x", "vx"),
		)

		src := newTestSource(s)
		cfg := opts.cfg
		cfg.ListPageSize = 2
		c := mustNewCache(t, src, "/p/", cfg)
		waitReadyTimeout(t, c, opts.timeout)

		if got := src.ranges.Load(); got < 3 {
			t.Fatalf("expected multi-page initial list (>=3 Range calls with page size 2), got %d", got)
		}
		src.resetCounts()

		ctx := context.Background()
		for i := 1; i <= 5; i++ {
			k := fmt.Sprintf("/p/k%d", i)
			want := fmt.Sprintf("v%d", i)
			kv, _, err := c.Get(ctx, k, 0)
			if err != nil {
				t.Fatalf("Get(%s): %v", k, err)
			}
			if string(kv.Value) != want {
				t.Fatalf("Get(%s) = %q, want %q", k, kv.Value, want)
			}
		}

		kvs, _, cont, err := c.Range(ctx, "/p/", 0, 0, "")
		if err != nil {
			t.Fatalf("Range: %v", err)
		}
		if cont != "" || len(kvs) != 5 {
			t.Fatalf("Range got %d keys cont=%q, want 5 keys empty cont", len(kvs), cont)
		}
		if src.gets.Load() != 0 || src.ranges.Load() != 0 {
			t.Fatalf("expected reads from cache, got src gets=%d ranges=%d", src.gets.Load(), src.ranges.Load())
		}

		if _, _, err := c.Get(ctx, "/other/x", 0); !errors.Is(err, store.ErrInvalidArgument) {
			t.Fatalf("Get outside prefix err = %v, want ErrInvalidArgument", err)
		}
		if _, _, _, err := c.Range(ctx, "/other/", 0, 0, ""); !errors.Is(err, store.ErrInvalidArgument) {
			t.Fatalf("Range outside prefix err = %v, want ErrInvalidArgument", err)
		}
		if _, err := c.Watch(ctx, "/other/", watchcache.WatchOptions{}); !errors.Is(err, store.ErrInvalidArgument) {
			t.Fatalf("Watch outside prefix err = %v, want ErrInvalidArgument", err)
		}
	})
}

func TestReadYourWrites(t *testing.T) {
	checkReadYourWrites(t, memstoreFactory, noFallbackOpts())
}

func checkReadYourWrites(t *testing.T, newStore func(*testing.T) store.Store, opts scenarioOpts) {
	runScenario(t, opts, func(t *testing.T) {
		s := newStore(t)
		src := newTestSource(s)
		c := mustNewCache(t, src, "/p/", opts.cfg)
		waitReadyTimeout(t, c, opts.timeout)
		src.resetCounts()

		ctx := context.Background()
		for i := range opts.iters {
			key := fmt.Sprintf("/p/k%02d", i%10)
			val := fmt.Sprintf("val-%d", i)
			rev := mustTxn(t, s, putOp(key, val))

			kv, gotRev, err := c.Get(ctx, key, 0)
			if err != nil {
				t.Fatalf("iter %d Get: %v", i, err)
			}
			if gotRev < rev || string(kv.Value) != val {
				t.Fatalf("iter %d Get: rev=%d (want >=%d) val=%q (want %q)", i, gotRev, rev, kv.Value, val)
			}

			kvs, rRev, _, err := c.Range(ctx, key, 0, 0, "")
			if err != nil {
				t.Fatalf("iter %d Range: %v", i, err)
			}
			if rRev < rev || len(kvs) != 1 || string(kvs[0].Value) != val {
				t.Fatalf("iter %d Range: rev=%d kvs=%v", i, rRev, kvs)
			}
		}

		if src.gets.Load() != 0 || src.ranges.Load() != 0 {
			t.Fatalf("expected zero source Get/Range calls, got gets=%d ranges=%d", src.gets.Load(), src.ranges.Load())
		}
	})
}

func TestQuietPrefix(t *testing.T) {
	checkQuietPrefix(t, memstoreFactory, noFallbackOpts())
}

func checkQuietPrefix(t *testing.T, newStore func(*testing.T) store.Store, opts scenarioOpts) {
	runScenario(t, opts, func(t *testing.T) {
		s := newStore(t)
		mustTxn(t, s, putOp("/p/k", "initial"))
		src := newTestSource(s)
		c := mustNewCache(t, src, "/p/", opts.cfg)
		waitReadyTimeout(t, c, opts.timeout)
		src.resetCounts()

		ctx := context.Background()
		for i := range 20 {
			rev := mustTxn(t, s, putOp(fmt.Sprintf("/other/%d", i), "x"))
			kv, gotRev, err := c.Get(ctx, "/p/k", 0)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if gotRev < rev || string(kv.Value) != "initial" {
				t.Fatalf("Get got rev=%d val=%q, want rev>=%d val=initial", gotRev, kv.Value, rev)
			}
		}

		if src.gets.Load() != 0 || src.ranges.Load() != 0 {
			t.Fatalf("expected zero source Get/Range calls, got gets=%d ranges=%d", src.gets.Load(), src.ranges.Load())
		}
		if src.progressReqs.Load() == 0 {
			t.Fatal("expected RequestProgress counter to grow on quiet prefix")
		}
	})
}

func TestNoIdleProgress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ms := memstore.New()
		src := newTestSource(ms)
		cfg := shortConfig()
		cfg.ProgressInterval = 5 * time.Millisecond
		c := mustNewCache(t, src, "/p/", cfg)
		waitReady(t, c)

		synctest.Wait()
		before := src.progressReqs.Load()
		<-time.After(500 * time.Millisecond)
		synctest.Wait()
		if got := src.progressReqs.Load(); got != before {
			t.Fatalf("RequestProgress grew while idle: before=%d after=%d", before, got)
		}
	})
}

func TestFallback(t *testing.T) {
	t.Run("stalled stream", func(t *testing.T) {
		checkFallbackStalled(t, memstoreFactory, memstoreOpts())
	})

	t.Run("blocked initial list", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			ms := memstore.New()
			mustTxn(t, ms, putOp("/p/k", "v1"))
			src := newTestSource(ms)
			src.setBlockRange(true)

			c := mustNewCache(t, src, "/p/", shortConfig())
			defer src.setBlockRange(false)

			kv, _, err := c.Get(context.Background(), "/p/k", 0)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if string(kv.Value) != "v1" {
				t.Fatalf("Get = %q, want v1", kv.Value)
			}
			if src.gets.Load() == 0 {
				t.Fatal("expected fallback Get on source while initial list is blocked")
			}
		})
	})
}

func checkFallbackStalled(t *testing.T, newStore func(*testing.T) store.Store, opts scenarioOpts) {
	runScenario(t, opts, func(t *testing.T) {
		s := newStore(t)
		mustTxn(t, s, putOp("/p/k", "v1"))
		src := newTestSource(s)
		c := mustNewCache(t, src, "/p/", opts.cfg)
		waitReadyTimeout(t, c, opts.timeout)
		src.resetCounts()

		src.setStalled(true)
		defer src.setStalled(false)
		mustTxn(t, s, putOp("/p/k", "v2"))

		ctx := context.Background()
		kv, _, err := c.Get(ctx, "/p/k", 0)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if string(kv.Value) != "v2" {
			t.Fatalf("Get = %q, want v2", kv.Value)
		}
		if src.gets.Load() == 0 {
			t.Fatal("expected fallback Get on source")
		}

		kvs, _, _, err := c.Range(ctx, "/p/", 0, 0, "")
		if err != nil {
			t.Fatalf("Range: %v", err)
		}
		if len(kvs) != 1 || string(kvs[0].Value) != "v2" {
			t.Fatalf("Range = %v, want v2", kvs)
		}
		if src.ranges.Load() == 0 {
			t.Fatal("expected fallback Range on source")
		}
	})
}

func TestExactRevisions(t *testing.T) {
	checkExactRevisions(t, memstoreFactory, memstoreOpts())
}

func checkExactRevisions(t *testing.T, newStore func(*testing.T) store.Store, opts scenarioOpts) {
	runScenario(t, opts, func(t *testing.T) {
		s := newStore(t)
		r1 := mustTxn(t, s, putOp("/p/a", "a1"), putOp("/p/b", "b1"), putOp("/p/c", "c1"))
		src := newTestSource(s)
		cfg := opts.cfg
		// Five retained events keep inWindowRev inside the window after the two
		// events written between the pages, whenever the cache receives them.
		cfg.MinEvents = 5
		cfg.MaxEvents = 8
		cfg.MinAge = time.Nanosecond
		c := mustNewCache(t, src, "/p/", cfg)
		waitReadyTimeout(t, c, opts.timeout)

		r2 := mustTxn(t, s, putOp("/p/a", "a2"))
		// Sync the cache up to r2 via consistent read so r2 is inside the window.
		ctx := context.Background()
		if _, _, err := c.Get(ctx, "/p/a", 0); err != nil {
			t.Fatalf("sync Get: %v", err)
		}
		// Advance past MinAge so subsequent writes can evict r2 down to MinEvents.
		<-time.After(time.Millisecond)

		// Write enough events to push r1 and r2 below the window floor.
		var revs []store.Rev
		for i := range 8 {
			r := mustTxn(t, s, putOp("/p/d", fmt.Sprintf("d%d", i)))
			revs = append(revs, r)
			if i == 2 {
				// Advance past MinAge so older revisions in this burst can be evicted past r2.
				<-time.After(time.Millisecond)
			}
		}
		latestRev := revs[len(revs)-1]
		if _, _, err := c.Get(ctx, "/p/d", 0); err != nil {
			t.Fatalf("sync Get latest: %v", err)
		}

		floor, ok := c.OldestRev()
		if !ok {
			t.Fatal("expected ready cache")
		}
		if floor <= r2 {
			t.Fatalf("expected floor > %d so r2 is evicted, got floor=%d", r2, floor)
		}

		// Revision inside the window: answered from cache.
		src.resetCounts()
		inWindowRev := revs[len(revs)-2]
		kv, gotRev, err := c.Get(ctx, "/p/d", inWindowRev)
		if err != nil {
			t.Fatalf("Get(inWindowRev=%d): %v", inWindowRev, err)
		}
		if gotRev != inWindowRev || string(kv.Value) != fmt.Sprintf("d%d", len(revs)-2) {
			t.Fatalf("Get(inWindowRev) got rev=%d val=%q", gotRev, kv.Value)
		}
		if src.gets.Load() != 0 {
			t.Fatalf("expected in-window Get from cache, got %d src gets", src.gets.Load())
		}

		// Paging at inWindowRev remains stable while concurrent writes happen.
		page1, pRev, cont, err := c.Range(ctx, "/p/", inWindowRev, 2, "")
		if err != nil || cont == "" || pRev != inWindowRev || len(page1) != 2 {
			t.Fatalf("page1 rev=%d cont=%q len=%d err=%v", pRev, cont, len(page1), err)
		}
		mustTxn(t, s, putOp("/p/b", "b-mutated"), putOp("/p/z", "z-new"))
		page2, pRev2, cont2, err := c.Range(ctx, "/p/", pRev, 2, cont)
		if err != nil || cont2 != "" || pRev2 != inWindowRev || len(page2) != 2 {
			t.Fatalf("page2 rev=%d cont=%q len=%d err=%v", pRev2, cont2, len(page2), err)
		}
		if page1[0].Key != "/p/a" || page1[1].Key != "/p/b" || string(page1[1].Value) != "b1" ||
			page2[0].Key != "/p/c" || page2[1].Key != "/p/d" {
			t.Fatalf("unexpected paged snapshot: page1=%v page2=%v", page1, page2)
		}
		if src.ranges.Load() != 0 {
			t.Fatalf("expected in-window paged Range from cache, got %d src ranges", src.ranges.Load())
		}

		// Below floor, uncompacted: answered by source.
		kv, gotRev, err = c.Get(ctx, "/p/a", r1)
		if err != nil || gotRev != r1 || string(kv.Value) != "a1" {
			t.Fatalf("below-floor Get got kv=%v rev=%d err=%v", kv, gotRev, err)
		}
		if src.gets.Load() == 0 {
			t.Fatal("expected below-floor Get to fall back to source")
		}

		// Compacted in the store: yields store.ErrCompacted.
		if err := s.Compact(ctx, r2); err != nil {
			t.Fatalf("Compact: %v", err)
		}
		if _, _, err := c.Get(ctx, "/p/a", r1); !errors.Is(err, store.ErrCompacted) {
			t.Fatalf("compacted Get err = %v, want ErrCompacted", err)
		}

		// Future revision: yields store.ErrFutureRev.
		if _, _, err := c.Get(ctx, "/p/a", latestRev+100); !errors.Is(err, store.ErrFutureRev) {
			t.Fatalf("future Get err = %v, want ErrFutureRev", err)
		}
	})
}

func TestWatchFromRev(t *testing.T) {
	checkWatchFromRev(t, memstoreFactory, memstoreOpts())
}

func checkWatchFromRev(t *testing.T, newStore func(*testing.T) store.Store, opts scenarioOpts) {
	runScenario(t, opts, func(t *testing.T) {
		s := newStore(t)
		rInit := mustTxn(t, s, putOp("/p/init", "0"))
		src := newTestSource(s)
		c := mustNewCache(t, src, "/p/", opts.cfg)
		waitReadyTimeout(t, c, opts.timeout)

		rMulti := mustTxn(t, s, putOp("/p/k1", "v1"), putOp("/p/k2", "v2"), delOp("/p/init"))
		rNext := mustTxn(t, s, putOp("/p/k3", "v3"))

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		wFrom, err := c.Watch(ctx, "/p/", watchcache.WatchOptions{FromRev: rMulti})
		if err != nil {
			t.Fatalf("Watch(FromRev=%d): %v", rMulti, err)
		}

		want := []watchcache.Event{
			{Type: watchcache.EventPut, Rev: rMulti, Key: "/p/k1", Value: []byte("v1")},
			{Type: watchcache.EventPut, Rev: rMulti, Key: "/p/k2", Value: []byte("v2")},
			{Type: watchcache.EventDelete, Rev: rMulti, Key: "/p/init"},
			{Type: watchcache.EventPut, Rev: rNext, Key: "/p/k3", Value: []byte("v3")},
		}
		for i, w := range want {
			ev, ok := nextChange(t, wFrom, opts.timeout)
			if !ok {
				t.Fatalf("event %d: stream closed unexpectedly (%v)", i, wFrom.Err())
			}
			if ev.Type != w.Type || ev.Rev != w.Rev || ev.Key != w.Key || !bytes.Equal(ev.Value, w.Value) {
				t.Fatalf("event %d = %+v, want %+v", i, ev, w)
			}
		}

		// Ensure cache is caught up to rNext before starting FromRev: 0 watch.
		if _, _, err := c.Get(ctx, "/p/k3", 0); err != nil {
			t.Fatalf("sync Get: %v", err)
		}
		wZero, err := c.Watch(ctx, "/p/", watchcache.WatchOptions{FromRev: 0})
		if err != nil {
			t.Fatalf("Watch(FromRev=0): %v", err)
		}
		rLater := mustTxn(t, s, putOp("/p/k4", "v4"))
		ev, ok := nextChange(t, wZero, opts.timeout)
		if !ok {
			t.Fatalf("wZero closed: %v", wZero.Err())
		}
		if ev.Rev != rLater || ev.Key != "/p/k4" || string(ev.Value) != "v4" || ev.Rev <= rInit {
			t.Fatalf("wZero first event = %+v, want rev=%d key=/p/k4", ev, rLater)
		}
	})
}

func TestWatchList(t *testing.T) {
	checkWatchList(t, memstoreFactory, memstoreOpts())
}

func checkWatchList(t *testing.T, newStore func(*testing.T) store.Store, opts scenarioOpts) {
	t.Run("ordered snapshot and initial bookmark", func(t *testing.T) {
		runScenario(t, opts, func(t *testing.T) {
			s := newStore(t)
			r1 := mustTxn(t, s, putOp("/p/b", "vb"), putOp("/p/a", "va"))
			r2 := mustTxn(t, s, putOp("/p/c", "vc"))
			src := newTestSource(s)
			c := mustNewCache(t, src, "/p/", opts.cfg)
			waitReadyTimeout(t, c, opts.timeout)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			curRev, _ := s.CurrentRev(ctx)

			w, err := c.Watch(ctx, "/p/", watchcache.WatchOptions{SendInitialEvents: true})
			if err != nil {
				t.Fatalf("WatchList: %v", err)
			}

			wantSnap := []watchcache.Event{
				{Type: watchcache.EventPut, Rev: r1, Key: "/p/a", Value: []byte("va")},
				{Type: watchcache.EventPut, Rev: r1, Key: "/p/b", Value: []byte("vb")},
				{Type: watchcache.EventPut, Rev: r2, Key: "/p/c", Value: []byte("vc")},
			}
			for i, want := range wantSnap {
				ev, ok := nextEvent(t, w, opts.timeout)
				if !ok {
					t.Fatalf("snap event %d: closed (%v)", i, w.Err())
				}
				if ev.Type != want.Type || ev.Rev != want.Rev || ev.Key != want.Key || !bytes.Equal(ev.Value, want.Value) {
					t.Fatalf("snap event %d = %+v, want %+v", i, ev, want)
				}
			}

			bm, ok := nextEvent(t, w, opts.timeout)
			if !ok {
				t.Fatalf("bookmark closed (%v)", w.Err())
			}
			if bm.Type != watchcache.EventBookmark || !bm.InitialEventsEnd || bm.Rev < curRev {
				t.Fatalf("initial bookmark = %+v, want InitialEventsEnd with rev >= %d", bm, curRev)
			}

			r3 := mustTxn(t, s, putOp("/p/d", "vd"))
			ev, ok := nextChange(t, w, opts.timeout)
			if !ok || ev.Type != watchcache.EventPut || ev.Rev != r3 || ev.Key != "/p/d" || ev.Rev <= bm.Rev {
				t.Fatalf("post-bookmark event = %+v (ok=%v), want rev=%d > %d", ev, ok, r3, bm.Rev)
			}
		})
	})

	t.Run("concurrent writers reconstruct final state", func(t *testing.T) {
		runScenario(t, opts, func(t *testing.T) {
			s := newStore(t)
			mustTxn(t, s, putOp("/p/k0", "init0"), putOp("/p/k1", "init1"))
			src := newTestSource(s)
			c := mustNewCache(t, src, "/p/", opts.cfg)
			waitReadyTimeout(t, c, opts.timeout)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			stopWriters := make(chan struct{})
			var writers sync.WaitGroup
			for wid := range 3 {
				writers.Add(1)
				go func(id int) {
					defer writers.Done()
					for i := range 60 {
						select {
						case <-stopWriters:
							return
						default:
						}
						key := fmt.Sprintf("/p/k%d", (id*7+i)%8)
						if i%4 == 3 {
							mustTxn(t, s, delOp(key))
						} else {
							mustTxn(t, s, putOp(key, fmt.Sprintf("w%d-%d", id, i)))
						}
						<-time.After(time.Millisecond)
					}
				}(wid)
			}

			w, err := c.Watch(ctx, "/p/", watchcache.WatchOptions{SendInitialEvents: true})
			if err != nil {
				t.Fatalf("WatchList: %v", err)
			}

			var stateMu sync.Mutex
			state := make(map[string]string)
			initDone := false
			var lastRev store.Rev
			done := make(chan error, 1)
			go func() {
				for ev := range w.Events() {
					stateMu.Lock()
					switch ev.Type {
					case watchcache.EventPut:
						if initDone && ev.Rev < lastRev {
							stateMu.Unlock()
							done <- fmt.Errorf("rev decreased: %d < %d", ev.Rev, lastRev)
							return
						}
						if initDone {
							lastRev = ev.Rev
						}
						state[ev.Key] = string(ev.Value)
					case watchcache.EventDelete:
						if !initDone {
							stateMu.Unlock()
							done <- errors.New("unexpected delete before InitialEventsEnd")
							return
						}
						if ev.Rev < lastRev {
							stateMu.Unlock()
							done <- fmt.Errorf("rev decreased: %d < %d", ev.Rev, lastRev)
							return
						}
						lastRev = ev.Rev
						delete(state, ev.Key)
					case watchcache.EventBookmark:
						if ev.InitialEventsEnd {
							initDone = true
							lastRev = ev.Rev
						} else if ev.Rev < lastRev {
							stateMu.Unlock()
							done <- fmt.Errorf("bookmark rev decreased: %d < %d", ev.Rev, lastRev)
							return
						} else {
							lastRev = ev.Rev
						}
					}
					stateMu.Unlock()
				}
				done <- w.Err()
			}()

			writers.Wait()
			close(stopWriters)
			finalKVs, finalRev, _, err := s.Range(context.Background(), "/p/", 0, 0, "")
			if err != nil {
				t.Fatalf("final Range: %v", err)
			}
			want := make(map[string]string, len(finalKVs))
			for _, kv := range finalKVs {
				want[kv.Key] = string(kv.Value)
			}

			waitUntil(t, opts.timeout, func() bool {
				select {
				case err := <-done:
					t.Fatalf("watch ended early: %v", err)
				default:
				}
				stateMu.Lock()
				defer stateMu.Unlock()
				return initDone && lastRev >= finalRev && mapsEqual(state, want)
			}, "watch list did not converge to final store state")
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatalf("watch Err() = %v, want context.Canceled", err)
			}
		})
	})
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

func TestPrefixFilteredWatchNoStall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ms := memstore.New()
		src := newTestSource(ms)
		cfg := shortConfig()
		// No bookmark may become due: its timer would wake the watch and hide a stall.
		cfg.BookmarkInterval = time.Hour
		c := mustNewCache(t, src, "p/", cfg)
		waitReady(t, c)

		var firstRev store.Rev
		for i := range 1000 {
			r := mustTxn(t, ms, putOp(fmt.Sprintf("p/a/%03d", i), "x"))
			if i == 0 {
				firstRev = r
			}
		}
		targetRev := mustTxn(t, ms, putOp("p/b/target", "hit"))

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Ensure all writes are already in the window before the watch starts polling.
		if _, _, err := c.Get(ctx, "p/b/target", 0); err != nil {
			t.Fatalf("sync Get: %v", err)
		}
		// A late progress notification would wake the watch and hide a stall.
		src.setStalled(true)

		w, err := c.Watch(ctx, "p/b/", watchcache.WatchOptions{FromRev: firstRev})
		if err != nil {
			t.Fatalf("Watch(p/b/): %v", err)
		}

		ev, ok := nextEvent(t, w, time.Second)
		if !ok {
			t.Fatalf("watch closed unexpectedly: %v", w.Err())
		}
		if ev.Type != watchcache.EventPut || ev.Rev != targetRev || ev.Key != "p/b/target" || string(ev.Value) != "hit" {
			t.Fatalf("got event %+v, want Put p/b/target@%d=hit", ev, targetRev)
		}
	})
}

func TestBookmarks(t *testing.T) {
	opts := memstoreOpts()
	opts.cfg.BookmarkInterval = 25 * time.Millisecond
	checkBookmarks(t, memstoreFactory, opts)
}

func checkBookmarks(t *testing.T, newStore func(*testing.T) store.Store, opts scenarioOpts) {
	runScenario(t, opts, func(t *testing.T) {
		s := newStore(t)
		mustTxn(t, s, putOp("/p/k", "v0"))
		src := newTestSource(s)
		c := mustNewCache(t, src, "/p/", opts.cfg)
		waitReadyTimeout(t, c, opts.timeout)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		w, err := c.Watch(ctx, "/p/", watchcache.WatchOptions{FromRev: 0})
		if err != nil {
			t.Fatalf("Watch: %v", err)
		}
		curRevsBefore := src.currentRevs.Load()

		// Idle watch receives periodic bookmarks with non-decreasing revisions and zero CurrentRev calls.
		var lastBM store.Rev
		for range 3 {
			ev, ok := nextEvent(t, w, opts.timeout)
			if !ok {
				t.Fatalf("watch closed: %v", w.Err())
			}
			if ev.Type != watchcache.EventBookmark {
				t.Fatalf("expected bookmark, got %+v", ev)
			}
			if ev.Rev < lastBM {
				t.Fatalf("bookmark rev decreased: %d < %d", ev.Rev, lastBM)
			}
			lastBM = ev.Rev
		}
		if got := src.currentRevs.Load(); got != curRevsBefore {
			t.Fatalf("idle watch bookmarks triggered %d CurrentRev calls, want 0", got-curRevsBefore)
		}

		// Quiet prefix: writing to another prefix still advances bookmark revision.
		otherRev := mustTxn(t, s, putOp("/other/x", "1"))
		waitUntil(t, opts.timeout, func() bool {
			ev, ok := nextEvent(t, w, opts.timeout)
			if !ok {
				t.Fatalf("watch closed: %v", w.Err())
			}
			if ev.Type != watchcache.EventBookmark {
				t.Fatalf("unexpected non-bookmark event on quiet prefix: %+v", ev)
			}
			if ev.Rev < lastBM {
				t.Fatalf("bookmark rev decreased: %d < %d", ev.Rev, lastBM)
			}
			lastBM = ev.Rev
			return ev.Rev >= otherRev
		}, "bookmark rev did not advance after write to another prefix")

		if got := src.currentRevs.Load(); got != curRevsBefore {
			t.Fatalf("quiet-prefix bookmarks triggered %d CurrentRev calls, want 0", got-curRevsBefore)
		}

		// Interleaved writes: no event at or below a bookmark's revision arrives after it.
		doneWriting := make(chan struct{})
		go func() {
			defer close(doneWriting)
			for i := range 40 {
				mustTxn(t, s, putOp("/p/k", fmt.Sprintf("v-%d", i)))
				<-time.After(2 * time.Millisecond)
			}
		}()

		var maxBM store.Rev
		seenBM := 0
		for seenBM < 2 {
			ev, ok := nextEvent(t, w, opts.timeout)
			if !ok {
				t.Fatalf("watch closed: %v", w.Err())
			}
			if ev.Type == watchcache.EventBookmark {
				if ev.Rev < maxBM {
					t.Fatalf("bookmark rev decreased: %d < %d", ev.Rev, maxBM)
				}
				maxBM = ev.Rev
				seenBM++
			} else if ev.Rev <= maxBM {
				t.Fatalf("event %+v arrived after bookmark %d", ev, maxBM)
			}
		}
		<-doneWriting
	})
}

func TestTooOld(t *testing.T) {
	checkTooOld(t, memstoreFactory, memstoreOpts())
}

func checkTooOld(t *testing.T, newStore func(*testing.T) store.Store, opts scenarioOpts) {
	runScenario(t, opts, func(t *testing.T) {
		baseGoroutines := 0
		if opts.checkGoroutine {
			baseGoroutines = cacheGoroutines()
		}

		s := newStore(t)
		r1 := mustTxn(t, s, putOp("/p/k", "v1"))
		src := newTestSource(s)
		cfg := opts.cfg
		cfg.MinEvents = 1
		cfg.MaxEvents = 4
		cfg.MinAge = time.Nanosecond
		c := mustNewCache(t, src, "/p/", cfg)
		waitReadyTimeout(t, c, opts.timeout)

		for i := range 10 {
			mustTxn(t, s, putOp("/p/k", fmt.Sprintf("v-%d", i)))
		}
		if _, _, err := c.Get(context.Background(), "/p/k", 0); err != nil {
			t.Fatalf("sync Get: %v", err)
		}

		if _, err := c.Watch(context.Background(), "/p/", watchcache.WatchOptions{FromRev: r1}); !errors.Is(err, watchcache.ErrTooOld) {
			t.Fatalf("Watch(old rev %d) err = %v, want ErrTooOld", r1, err)
		}

		// A consumer that stops reading resumes later, receives at most one batch already held,
		// and then ends with ErrTooOld.
		watchCtx, cancelWatch := context.WithCancel(context.Background())
		w, err := c.Watch(watchCtx, "/p/", watchcache.WatchOptions{FromRev: 0})
		if err != nil {
			t.Fatalf("Watch(0): %v", err)
		}

		for i := range 15 {
			mustTxn(t, s, putOp("/p/slow", fmt.Sprintf("s-%d", i)))
		}
		if _, _, err := c.Get(context.Background(), "/p/slow", 0); err != nil {
			t.Fatalf("sync Get slow: %v", err)
		}

		// Give the watch goroutine a moment to poll its first batch and park in send, then resume reading.
		received := 0
		deadline := time.After(opts.timeout)
		for {
			select {
			case _, ok := <-w.Events():
				if !ok {
					if err := w.Err(); !errors.Is(err, watchcache.ErrTooOld) {
						t.Fatalf("watch Err() = %v, want ErrTooOld", err)
					}
					goto drainedTooOld
				}
				received++
			case <-deadline:
				t.Fatalf("timed out waiting for resumed watch to end with ErrTooOld")
			}
		}
	drainedTooOld:
		cancelWatch()
		if received > 64 {
			t.Fatalf("received %d events after overtaking, expected at most one batch (64)", received)
		}

		// A watch parked in send must exit when its context is cancelled.
		parkCtx, cancelPark := context.WithCancel(context.Background())
		wParked, err := c.Watch(parkCtx, "/p/", watchcache.WatchOptions{FromRev: 0})
		if err != nil {
			t.Fatalf("Watch parked: %v", err)
		}
		mustTxn(t, s, putOp("/p/park", "1"))
		if _, _, err := c.Get(context.Background(), "/p/park", 0); err != nil {
			t.Fatalf("sync Get park: %v", err)
		}
		// Allow wParked goroutine to reach the send before cancelling its context.
		if opts.inSynctest {
			synctest.Wait()
		} else {
			<-time.After(10 * time.Millisecond)
		}
		cancelPark()
		expectClosedWith(t, wParked, context.Canceled, opts.timeout)

		c.Close()
		src.wait()
		if opts.checkGoroutine {
			waitUntil(t, opts.timeout, func() bool {
				return cacheGoroutines() <= baseGoroutines
			}, "goroutine leak after overtaken/cancelled watch and Close")
		}
	})
}

func TestProgressRequestsArePaced(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ms := memstore.New()
		mustTxn(t, ms, putOp("/p/k", "v1"))
		src := newTestSource(ms)
		cfg := shortConfig()
		cfg.ReadWait = 400 * time.Millisecond
		cfg.ProgressInterval = 40 * time.Millisecond
		c := mustNewCache(t, src, "/p/", cfg)
		waitReady(t, c)
		// The store answers progress requests only once it has a revision the watch covers.
		r2 := mustTxn(t, ms, putOp("/p/k", "v2"))
		if _, _, err := c.Get(context.Background(), "/p/k", 0); err != nil {
			t.Fatalf("sync Get: %v", err)
		}
		src.resetCounts()

		// Every answered request wakes the waiter, which must not trigger the next request.
		if _, _, err := c.Get(context.Background(), "/p/k", r2+1000); !errors.Is(err, store.ErrFutureRev) {
			t.Fatalf("Get(future) err = %v, want ErrFutureRev", err)
		}
		// 400ms / 40ms allows 11 requests; the bound leaves room for a slow machine.
		if got := src.progressReqs.Load(); got == 0 || got > 15 {
			t.Fatalf("RequestProgress calls during one %v wait = %d, want 1..15", cfg.ReadWait, got)
		}
	})
}

func TestFutureWatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ms := memstore.New()
		r1 := mustTxn(t, ms, putOp("/p/k", "v1"))
		src := newTestSource(ms)
		cfg := shortConfig()
		cfg.ReadWait = 80 * time.Millisecond
		c := mustNewCache(t, src, "/p/", cfg)
		waitReady(t, c)

		start := time.Now()
		_, err := c.Watch(context.Background(), "/p/", watchcache.WatchOptions{FromRev: r1 + 1000})
		elapsed := time.Since(start)
		if !errors.Is(err, store.ErrFutureRev) {
			t.Fatalf("Watch(future) err = %v, want ErrFutureRev", err)
		}
		if elapsed < cfg.ReadWait/2 {
			t.Fatalf("Watch(future) returned too quickly (%v < %v/2)", elapsed, cfg.ReadWait)
		}

		_, err = c.Watch(context.Background(), "/p/", watchcache.WatchOptions{
			FromRev:           r1 + 1000,
			SendInitialEvents: true,
		})
		if !errors.Is(err, store.ErrFutureRev) {
			t.Fatalf("WatchList(future) err = %v, want ErrFutureRev", err)
		}
	})
}

func TestRelist(t *testing.T) {
	checkRelist(t, memstoreFactory, memstoreOpts())
}

func checkRelist(t *testing.T, newStore func(*testing.T) store.Store, opts scenarioOpts) {
	t.Run("upstream ErrCompacted", func(t *testing.T) {
		runScenario(t, opts, func(t *testing.T) {
			s := newStore(t)
			mustTxn(t, s, putOp("/p/k", "v1"))
			src := newTestSource(s)
			c := mustNewCache(t, src, "/p/", opts.cfg)
			waitReadyTimeout(t, c, opts.timeout)

			w, err := c.Watch(context.Background(), "/p/", watchcache.WatchOptions{FromRev: 0})
			if err != nil {
				t.Fatalf("Watch: %v", err)
			}

			mustTxn(t, s, putOp("/p/k", "v2"))
			if !src.killWatch(store.ErrCompacted) {
				t.Fatal("no active upstream watch to kill")
			}
			expectClosedWith(t, w, watchcache.ErrTooOld, opts.timeout)

			// Cache re-lists and serves fresh state and new watches.
			waitUntil(t, opts.timeout, func() bool {
				kv, _, err := c.Get(context.Background(), "/p/k", 0)
				return err == nil && string(kv.Value) == "v2"
			}, "cache did not recover after re-list")

			w2, err := c.Watch(context.Background(), "/p/", watchcache.WatchOptions{FromRev: 0})
			if err != nil {
				t.Fatalf("new Watch after re-list: %v", err)
			}
			r3 := mustTxn(t, s, putOp("/p/k", "v3"))
			ev, ok := nextChange(t, w2, opts.timeout)
			if !ok || ev.Rev != r3 || string(ev.Value) != "v3" {
				t.Fatalf("new Watch event = %+v (ok=%v), want rev=%d v3", ev, ok, r3)
			}
		})
	})

	t.Run("injected out-of-order event", func(t *testing.T) {
		runScenario(t, opts, func(t *testing.T) {
			s := newStore(t)
			r1 := mustTxn(t, s, putOp("/p/k", "v1"))
			src := newTestSource(s)
			c := mustNewCache(t, src, "/p/", opts.cfg)
			waitReadyTimeout(t, c, opts.timeout)

			w, err := c.Watch(context.Background(), "/p/", watchcache.WatchOptions{FromRev: 0})
			if err != nil {
				t.Fatalf("Watch: %v", err)
			}

			mustTxn(t, s, putOp("/p/k", "v2"))
			if !src.injectEvent(store.Event{Type: store.EventPut, Rev: r1, Key: "/p/k", Value: []byte("stale")}) {
				t.Fatal("failed to inject out-of-order event")
			}
			expectClosedWith(t, w, watchcache.ErrTooOld, opts.timeout)

			waitUntil(t, opts.timeout, func() bool {
				kv, _, err := c.Get(context.Background(), "/p/k", 0)
				return err == nil && string(kv.Value) == "v2"
			}, "cache did not recover after out-of-order re-list")

			w2, err := c.Watch(context.Background(), "/p/", watchcache.WatchOptions{FromRev: 0})
			if err != nil {
				t.Fatalf("new Watch after out-of-order re-list: %v", err)
			}
			r3 := mustTxn(t, s, putOp("/p/k", "v3"))
			ev, ok := nextChange(t, w2, opts.timeout)
			if !ok || ev.Rev != r3 || string(ev.Value) != "v3" {
				t.Fatalf("new Watch event = %+v (ok=%v), want rev=%d v3", ev, ok, r3)
			}
		})
	})
}

func TestInitialListFailureIsRetried(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ms := memstore.New()
		mustTxn(t, ms, putOp("/p/k", "v1"))
		src := newTestSource(ms)
		src.setFailRange(errors.New("injected list failure"))
		c := mustNewCache(t, src, "/p/", shortConfig())

		waitUntil(t, 2*time.Second, func() bool {
			return src.ranges.Load() >= 3
		}, "the cache did not retry its initial list")
		if _, ok := c.OldestRev(); ok {
			t.Fatal("cache is ready although every list failed")
		}

		src.setFailRange(nil)
		waitReady(t, c)
		kv, _, err := c.Get(context.Background(), "/p/k", 0)
		if err != nil || string(kv.Value) != "v1" {
			t.Fatalf("Get after recovery = (%+v, %v), want v1", kv, err)
		}
	})
}

func TestClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		baseGoroutines := cacheGoroutines()

		ms := memstore.New()
		mustTxn(t, ms, putOp("/p/k", "v1"))
		src := newTestSource(ms)
		c, err := watchcache.New(src, "/p/", shortConfig())
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		waitReady(t, c)

		w1, err := c.Watch(context.Background(), "/p/", watchcache.WatchOptions{FromRev: 0})
		if err != nil {
			t.Fatalf("Watch 1: %v", err)
		}
		w2, err := c.Watch(context.Background(), "/p/", watchcache.WatchOptions{SendInitialEvents: true})
		if err != nil {
			t.Fatalf("Watch 2: %v", err)
		}
		// Trigger an event so w1 also parks in send without a reader.
		mustTxn(t, ms, putOp("/p/k", "v2"))
		if _, _, err := c.Get(context.Background(), "/p/k", 0); err != nil {
			t.Fatalf("sync Get: %v", err)
		}

		c.Close()
		c.Close()
		src.wait()

		expectClosedWith(t, w1, watchcache.ErrClosed, time.Second)
		expectClosedWith(t, w2, watchcache.ErrClosed, time.Second)

		ctx := context.Background()
		if _, _, err := c.Get(ctx, "/p/k", 0); !errors.Is(err, watchcache.ErrClosed) {
			t.Fatalf("Get after Close err = %v, want ErrClosed", err)
		}
		if _, _, _, err := c.Range(ctx, "/p/", 0, 0, ""); !errors.Is(err, watchcache.ErrClosed) {
			t.Fatalf("Range after Close err = %v, want ErrClosed", err)
		}
		if _, err := c.Watch(ctx, "/p/", watchcache.WatchOptions{}); !errors.Is(err, watchcache.ErrClosed) {
			t.Fatalf("Watch after Close err = %v, want ErrClosed", err)
		}
		if _, ok := c.OldestRev(); ok {
			t.Fatal("OldestRev after Close should be false")
		}

		waitUntil(t, 2*time.Second, func() bool {
			return cacheGoroutines() <= baseGoroutines
		}, "goroutine count did not return to baseline after Close")
	})
}

func TestStress(t *testing.T) {
	opts := memstoreOpts()
	opts.cfg.ReadWait = 250 * time.Millisecond
	opts.cfg.BookmarkInterval = 20 * time.Millisecond
	opts.timeout = 3 * time.Second
	checkStress(t, memstoreFactory, opts)
}

func checkStress(t *testing.T, newStore func(*testing.T) store.Store, opts scenarioOpts) {
	runScenario(t, opts, func(t *testing.T) {
		s := newStore(t)
		for i := range 5 {
			mustTxn(t, s, putOp(fmt.Sprintf("/p/k%02d", i), "seed"))
		}
		src := newTestSource(s)
		cfg := opts.cfg
		cfg.MinEvents = 2
		cfg.MinAge = time.Nanosecond
		cfg.MaxEvents = 16
		cfg.ListPageSize = 4
		c := mustNewCache(t, src, "/p/", cfg)
		waitReadyTimeout(t, c, opts.timeout)

		const numKeys = 12
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var writersWG sync.WaitGroup
		for wid := range 3 {
			writersWG.Add(1)
			go func(id int) {
				defer writersWG.Done()
				rng := rand.New(rand.NewPCG(uint64(100+id), uint64(200+id)))
				for step := range opts.stressSteps {
					var ops []store.Op
					nOps := 1 + rng.IntN(3)
					used := make(map[string]bool)
					for range nOps {
						var key string
						if rng.IntN(4) == 0 {
							key = fmt.Sprintf("/other/k%02d", rng.IntN(6))
						} else {
							key = fmt.Sprintf("/p/k%02d", rng.IntN(numKeys))
						}
						if used[key] {
							continue
						}
						used[key] = true
						if rng.IntN(4) == 0 {
							ops = append(ops, delOp(key))
						} else {
							ops = append(ops, putOp(key, fmt.Sprintf("w%d-%d", id, step)))
						}
					}
					if len(ops) > 0 {
						mustTxn(t, s, ops...)
					}
					if d := rng.IntN(3); d > 0 {
						<-time.After(time.Duration(d) * time.Millisecond)
					} else {
						runtime.Gosched()
					}
				}
			}(wid)
		}

		readersDone := make(chan struct{})
		errCh := make(chan error, 16)
		var readersWG sync.WaitGroup
		for rid := range 3 {
			readersWG.Add(1)
			go func(id int) {
				defer readersWG.Done()
				rng := rand.New(rand.NewPCG(uint64(300+id), uint64(400+id)))
				var lastRev store.Rev
				for {
					select {
					case <-readersDone:
						return
					case <-time.After(time.Millisecond):
					}
					key := fmt.Sprintf("/p/k%02d", rng.IntN(numKeys))
					preRev, err := s.CurrentRev(ctx)
					if err != nil {
						return
					}
					kv, gotRev, err := c.Get(ctx, key, 0)
					if err != nil && !errors.Is(err, store.ErrNotFound) {
						errCh <- fmt.Errorf("reader %d Get: %w", id, err)
						return
					}
					if gotRev < preRev {
						errCh <- fmt.Errorf("reader %d gotRev=%d < preRev=%d", id, gotRev, preRev)
						return
					}
					if gotRev < lastRev {
						errCh <- fmt.Errorf("reader %d rev decreased: %d < %d", id, gotRev, lastRev)
						return
					}
					lastRev = gotRev

					wantKV, _, wantErr := s.Get(ctx, key, gotRev)
					if errors.Is(err, store.ErrNotFound) {
						if !errors.Is(wantErr, store.ErrNotFound) {
							errCh <- fmt.Errorf("reader %d key %s at %d: cache ErrNotFound, store err=%v", id, key, gotRev, wantErr)
							return
						}
					} else if wantErr != nil || !bytes.Equal(kv.Value, wantKV.Value) {
						errCh <- fmt.Errorf("reader %d key %s at %d: cache=%q, store=%q err=%v", id, key, gotRev, kv.Value, wantKV.Value, wantErr)
						return
					}
				}
			}(rid)
		}

		type consumerState struct {
			mu       sync.Mutex
			state    map[string]string
			lastRev  store.Rev
			initDone bool
			relisted bool
		}
		consumers := make([]*consumerState, 4)
		var consumersWG sync.WaitGroup
		for cid := range len(consumers) {
			cs := &consumerState{state: make(map[string]string)}
			consumers[cid] = cs
			consumersWG.Add(1)
			go func(id int, cs *consumerState) {
				defer consumersWG.Done()
				rng := rand.New(rand.NewPCG(uint64(500+id), uint64(600+id)))
				slow := id%2 == 1
				for {
					if ctx.Err() != nil {
						return
					}
					w, err := c.Watch(ctx, "/p/", watchcache.WatchOptions{SendInitialEvents: true})
					if err != nil {
						if errors.Is(err, watchcache.ErrNotReady) || errors.Is(err, context.Canceled) {
							<-time.After(2 * time.Millisecond)
							continue
						}
						errCh <- fmt.Errorf("consumer %d Watch: %w", id, err)
						return
					}
					cs.mu.Lock()
					cs.state = make(map[string]string)
					cs.lastRev = 0
					cs.initDone = false
					cs.mu.Unlock()

					for ev := range w.Events() {
						if slow && rng.IntN(4) == 0 {
							<-time.After(8 * time.Millisecond)
						}
						cs.mu.Lock()
						switch ev.Type {
						case watchcache.EventPut:
							cs.state[ev.Key] = string(ev.Value)
							if cs.initDone {
								cs.lastRev = ev.Rev
							}
						case watchcache.EventDelete:
							delete(cs.state, ev.Key)
							cs.lastRev = ev.Rev
						case watchcache.EventBookmark:
							if ev.InitialEventsEnd {
								cs.initDone = true
							}
							cs.lastRev = ev.Rev
						}
						cs.mu.Unlock()
					}

					wErr := w.Err()
					if errors.Is(wErr, context.Canceled) || errors.Is(wErr, watchcache.ErrClosed) {
						return
					}
					if !errors.Is(wErr, watchcache.ErrTooOld) {
						errCh <- fmt.Errorf("consumer %d ended with unexpected err: %v", id, wErr)
						return
					}
					cs.mu.Lock()
					cs.relisted = true
					cs.mu.Unlock()
					// Once evicted/relisted once, stop intentional slowness so it converges after writers stop.
					slow = false
				}
			}(cid, cs)
		}

		writersWG.Wait()
		close(readersDone)
		readersWG.Wait()

		finalKVs, finalRev, _, err := s.Range(context.Background(), "/p/", 0, 0, "")
		if err != nil {
			t.Fatalf("final Range: %v", err)
		}
		want := make(map[string]string, len(finalKVs))
		for _, kv := range finalKVs {
			want[kv.Key] = string(kv.Value)
		}

		waitUntil(t, opts.timeout, func() bool {
			select {
			case err := <-errCh:
				t.Fatalf("stress error: %v", err)
			default:
			}
			for _, cs := range consumers {
				cs.mu.Lock()
				ok := cs.initDone && cs.lastRev >= finalRev && mapsEqual(cs.state, want)
				cs.mu.Unlock()
				if !ok {
					return false
				}
			}
			return true
		}, "stress consumers did not converge to final store state")

		cancel()
		consumersWG.Wait()
		close(errCh)
		for err := range errCh {
			t.Fatalf("stress error: %v", err)
		}
	})
}

func TestConfig(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ms := memstore.New()
		src := newTestSource(ms)

		// Zero config applies defaults and constructs a working cache.
		c, err := watchcache.New(src, "/p/", watchcache.Config{})
		if err != nil {
			t.Fatalf("New with zero Config: %v", err)
		}
		waitReady(t, c)
		c.Close()
		src.wait()
	})

	badConfigs := []struct {
		name string
		cfg  watchcache.Config
	}{
		{"negative MinEvents", watchcache.Config{MinEvents: -1}},
		{"negative MinAge", watchcache.Config{MinAge: -time.Second}},
		{"negative MaxEvents", watchcache.Config{MaxEvents: -1}},
		{"negative ReadWait", watchcache.Config{ReadWait: -time.Millisecond}},
		{"negative ProgressInterval", watchcache.Config{ProgressInterval: -time.Millisecond}},
		{"negative BookmarkInterval", watchcache.Config{BookmarkInterval: -time.Millisecond}},
		{"negative ListPageSize", watchcache.Config{ListPageSize: -1}},
		{"MaxEvents < MinEvents explicit", watchcache.Config{MinEvents: 10, MaxEvents: 5}},
		{"MaxEvents < default MinEvents", watchcache.Config{MaxEvents: 100}},
		{"default MaxEvents < MinEvents", watchcache.Config{MinEvents: 200_000}},
	}

	ms := memstore.New()
	src := newTestSource(ms)
	for _, tc := range badConfigs {
		t.Run(tc.name, func(t *testing.T) {
			c, err := watchcache.New(src, "/p/", tc.cfg)
			if err == nil {
				c.Close()
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}
