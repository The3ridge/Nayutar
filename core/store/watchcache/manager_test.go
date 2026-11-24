package watchcache_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
	"github.com/Kaikei-e/Nayutar/core/store/memstore"
	"github.com/Kaikei-e/Nayutar/core/store/tenant"
	"github.com/Kaikei-e/Nayutar/core/store/watchcache"
)

func mustObjectKey(t *testing.T, ten, group, kind, name string) (abs, rel string) {
	t.Helper()
	k := keyspace.Key{
		Family: keyspace.FamilyObject,
		Tenant: ten,
		Object: keyspace.Object{Group: group, Kind: kind, Name: name},
	}
	var err error
	abs, err = k.Encode()
	if err != nil {
		t.Fatalf("Encode(%+v): %v", k, err)
	}
	rel, err = k.Rel()
	if err != nil {
		t.Fatalf("Rel(%+v): %v", k, err)
	}
	return abs, rel
}

func TestManagerCacheIdentityAndValidation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ms := memstore.New()
		if _, err := watchcache.NewManager(nil, shortConfig()); !errors.Is(err, store.ErrInvalidArgument) {
			t.Fatalf("NewManager(nil) err = %v, want ErrInvalidArgument", err)
		}
		if _, err := watchcache.NewManager(ms, watchcache.Config{MinEvents: -1}); !errors.Is(err, store.ErrInvalidArgument) {
			t.Fatalf("NewManager(bad cfg) err = %v, want ErrInvalidArgument", err)
		}

		m, err := watchcache.NewManager(ms, shortConfig())
		if err != nil {
			t.Fatalf("NewManager: %v", err)
		}
		t.Cleanup(m.Close)

		c1, err := m.Objects("alpha", "core", "pod")
		if err != nil {
			t.Fatalf("Objects(alpha, core, pod): %v", err)
		}
		c1Again, err := m.Objects("alpha", "core", "pod")
		if err != nil {
			t.Fatalf("Objects(alpha, core, pod) again: %v", err)
		}
		if c1 != c1Again {
			t.Fatal("expected same *Cache for identical (tenant, group, kind)")
		}

		otherTriples := [][3]string{
			{"beta", "core", "pod"},
			{"alpha", "apps.example.com", "pod"},
			{"alpha", "core", "service"},
		}
		for _, tr := range otherTriples {
			c, err := m.Objects(tr[0], tr[1], tr[2])
			if err != nil {
				t.Fatalf("Objects(%v): %v", tr, err)
			}
			if c == c1 {
				t.Fatalf("Objects(%v) returned same *Cache as (alpha, core, pod)", tr)
			}
		}

		for _, badTenant := range []string{"", "BAD", "-a", "a/b"} {
			if _, err := m.Objects(badTenant, "core", "pod"); !errors.Is(err, tenant.ErrInvalidName) {
				t.Fatalf("Objects(tenant=%q) err = %v, want tenant.ErrInvalidName", badTenant, err)
			}
		}
		for _, badGroup := range []string{"", "apps", "CORE", "a/b"} {
			if _, err := m.Objects("alpha", badGroup, "pod"); !errors.Is(err, keyspace.ErrInvalid) {
				t.Fatalf("Objects(group=%q) err = %v, want keyspace.ErrInvalid", badGroup, err)
			}
		}
		for _, badKind := range []string{"", "Pod", "a.b", "a/b"} {
			if _, err := m.Objects("alpha", "core", badKind); !errors.Is(err, keyspace.ErrInvalid) {
				t.Fatalf("Objects(kind=%q) err = %v, want keyspace.ErrInvalid", badKind, err)
			}
		}
	})
}

func TestManagerConcurrentObjects(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ms := memstore.New()
		m, err := watchcache.NewManager(ms, shortConfig())
		if err != nil {
			t.Fatalf("NewManager: %v", err)
		}
		t.Cleanup(m.Close)

		const n = 16
		caches := make([]*watchcache.Cache, n)
		errs := make([]error, n)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				<-start
				caches[idx], errs[idx] = m.Objects("alpha", "core", "pod")
			}(i)
		}
		close(start)
		wg.Wait()

		for i := range n {
			if errs[i] != nil {
				t.Fatalf("goroutine %d Objects err = %v", i, errs[i])
			}
			if caches[i] == nil || caches[i] != caches[0] {
				t.Fatalf("goroutine %d got %p, want %p", i, caches[i], caches[0])
			}
		}
	})
}

func TestManagerTenantIsolationAndRelativeKeys(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ms := memstore.New()
		m, err := watchcache.NewManager(ms, shortConfig())
		if err != nil {
			t.Fatalf("NewManager: %v", err)
		}
		t.Cleanup(m.Close)

		alphaCache, err := m.Objects("alpha", "core", "pod")
		if err != nil {
			t.Fatalf("Objects(alpha): %v", err)
		}
		betaCache, err := m.Objects("beta", "core", "pod")
		if err != nil {
			t.Fatalf("Objects(beta): %v", err)
		}
		waitReady(t, alphaCache)
		waitReady(t, betaCache)

		prefix, err := keyspace.ObjectPrefix("core", "pod")
		if err != nil {
			t.Fatalf("ObjectPrefix: %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		betaWatch, err := betaCache.Watch(ctx, prefix, watchcache.WatchOptions{FromRev: 0})
		if err != nil {
			t.Fatalf("beta Watch: %v", err)
		}

		absAlpha, relAlpha := mustObjectKey(t, "alpha", "core", "pod", "web")
		mustTxn(t, ms, putOp(absAlpha, "alpha-val"))

		kv, _, err := alphaCache.Get(ctx, relAlpha, 0)
		if err != nil {
			t.Fatalf("alpha Get(%q): %v", relAlpha, err)
		}
		if kv.Key != relAlpha || string(kv.Value) != "alpha-val" {
			t.Fatalf("alpha Get = %+v, want key=%q val=alpha-val", kv, relAlpha)
		}
		parsed, err := keyspace.ParseRel("alpha", kv.Key)
		if err != nil || parsed.Family != keyspace.FamilyObject || parsed.Object.Name != "web" {
			t.Fatalf("ParseRel(alpha, %q) = (%+v, %v)", kv.Key, parsed, err)
		}

		if _, _, err := betaCache.Get(ctx, relAlpha, 0); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("beta Get(%q) err = %v, want ErrNotFound", relAlpha, err)
		}
		betaKVs, _, _, err := betaCache.Range(ctx, prefix, 0, 0, "")
		if err != nil || len(betaKVs) != 0 {
			t.Fatalf("beta Range = (%v, %v), want empty", betaKVs, err)
		}

		absBeta, relBeta := mustObjectKey(t, "beta", "core", "pod", "db")
		rBeta := mustTxn(t, ms, putOp(absBeta, "beta-val"))

		ev, ok := nextChange(t, betaWatch, 2*time.Second)
		if !ok {
			t.Fatalf("beta Watch closed unexpectedly: %v", betaWatch.Err())
		}
		if ev.Type != watchcache.EventPut || ev.Rev != rBeta || ev.Key != relBeta || string(ev.Value) != "beta-val" {
			t.Fatalf("beta Watch got unexpected event %+v, want Put %q@%d", ev, relBeta, rBeta)
		}
		if _, err := keyspace.ParseRel("beta", ev.Key); err != nil {
			t.Fatalf("ParseRel(beta, %q): %v", ev.Key, err)
		}
	})
}

func TestManagerCompaction(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		checkManagerCompaction(t, memstoreFactory, noFallbackOpts().cfg, 2*time.Second)
	})
}

func checkManagerCompaction(t *testing.T, newStore func(*testing.T) store.Store, baseCfg watchcache.Config, timeout time.Duration) {
	s := newStore(t)
	m, err := watchcache.NewManager(s, baseCfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(m.Close)

	ctx := context.Background()
	if bound, ok := m.CompactBound(); ok {
		t.Fatalf("CompactBound before any cache = (%d, true), want (0, false)", bound)
	}
	if rev, err := m.Compact(ctx, 0); err != nil || rev != 0 {
		t.Fatalf("Compact(0) = (%d, %v), want (0, nil)", rev, err)
	}

	absAlpha, relAlpha := mustObjectKey(t, "alpha", "core", "pod", "p1")
	absBeta, relBeta := mustObjectKey(t, "beta", "core", "pod", "p2")
	mustTxn(t, s, putOp(absAlpha, "a0"), putOp(absBeta, "b0"))

	c1, err := m.Objects("alpha", "core", "pod")
	if err != nil {
		t.Fatalf("Objects(alpha): %v", err)
	}
	c2, err := m.Objects("beta", "core", "pod")
	if err != nil {
		t.Fatalf("Objects(beta): %v", err)
	}
	waitReadyTimeout(t, c1, timeout)
	waitReadyTimeout(t, c2, timeout)

	closed1, ok1 := c1.ClosedRev()
	closed2, ok2 := c2.ClosedRev()
	if !ok1 || !ok2 {
		t.Fatalf("expected both caches ready: ok1=%v ok2=%v", ok1, ok2)
	}
	if bound, ok := m.CompactBound(); !ok || bound != min(closed1, closed2) {
		t.Fatalf("CompactBound = (%d, %v), want (%d, true)", bound, ok, min(closed1, closed2))
	}

	var midRev store.Rev
	var cur1 store.Rev
	for i := 1; i <= 25; i++ {
		rev := mustTxn(t, s, putOp(absAlpha, fmt.Sprintf("a%d", i)))
		if i == 10 {
			midRev = rev
		}
		cur1 = rev
	}

	compacted1, err := m.Compact(ctx, cur1)
	if err != nil || compacted1 != cur1 {
		t.Fatalf("Compact(cur1=%d) = (%d, %v), want (%d, nil)", cur1, compacted1, err, cur1)
	}
	if _, _, err := s.Get(ctx, absAlpha, cur1-1); !errors.Is(err, store.ErrCompacted) {
		t.Fatalf("store Get below cur1=%d err = %v, want ErrCompacted", cur1, err)
	}
	if kv, gotRev, err := s.Get(ctx, absAlpha, cur1); err != nil || gotRev != cur1 || string(kv.Value) != "a25" {
		t.Fatalf("store Get at cur1=%d = (%+v, %d, %v), want a25", cur1, kv, gotRev, err)
	}

	// Default retention keeps midRev inside c1's window, so reads at midRev still succeed from memory.
	oldest, ok := c1.OldestRev()
	if !ok || oldest > midRev {
		t.Fatalf("expected OldestRev (%d, %v) <= midRev (%d)", oldest, ok, midRev)
	}
	if _, _, err := s.Get(ctx, absAlpha, midRev); !errors.Is(err, store.ErrCompacted) {
		t.Fatalf("store Get at compacted midRev=%d err = %v, want ErrCompacted", midRev, err)
	}
	if kv, gotRev, err := c1.Get(ctx, relAlpha, midRev); err != nil || gotRev != midRev || string(kv.Value) != "a10" {
		t.Fatalf("cache Get at midRev=%d = (%+v, %d, %v), want a10", midRev, kv, gotRev, err)
	}

	var cur2 store.Rev
	for i := 26; i <= 35; i++ {
		cur2 = mustTxn(t, s, putOp(absAlpha, fmt.Sprintf("a%d", i)))
	}
	compacted2, err := m.Compact(ctx, cur2)
	if err != nil || compacted2 != cur2 {
		t.Fatalf("second Compact(cur2=%d) = (%d, %v), want (%d, nil)", cur2, compacted2, err, cur2)
	}
	if compactedAgain, err := m.Compact(ctx, cur2); err != nil || compactedAgain != 0 {
		t.Fatalf("repeat Compact(cur2=%d) = (%d, %v), want (0, nil)", cur2, compactedAgain, err)
	}

	if kv, gotRev, err := c1.Get(ctx, relAlpha, 0); err != nil || gotRev < cur2 || string(kv.Value) != "a35" {
		t.Fatalf("c1 consistent Get = (%+v, %d, %v), want rev>=%d val=a35", kv, gotRev, err, cur2)
	}
	if kv, gotRev, err := c2.Get(ctx, relBeta, 0); err != nil || gotRev < cur2 || string(kv.Value) != "b0" {
		t.Fatalf("c2 consistent Get = (%+v, %d, %v), want rev>=%d val=b0", kv, gotRev, err, cur2)
	}

	prefix, err := keyspace.ObjectPrefix("core", "pod")
	if err != nil {
		t.Fatalf("ObjectPrefix: %v", err)
	}
	watchCtx, cancelWatch := context.WithCancel(ctx)
	defer cancelWatch()

	w1, err := c1.Watch(watchCtx, prefix, watchcache.WatchOptions{FromRev: 0})
	if err != nil {
		t.Fatalf("c1.Watch: %v", err)
	}
	w2, err := c2.Watch(watchCtx, prefix, watchcache.WatchOptions{FromRev: 0})
	if err != nil {
		t.Fatalf("c2.Watch: %v", err)
	}

	rAlphaLater := mustTxn(t, s, putOp(absAlpha, "a-later"))
	rBetaLater := mustTxn(t, s, putOp(absBeta, "b-later"))

	ev1, ok := nextChange(t, w1, timeout)
	if !ok || ev1.Type != watchcache.EventPut || ev1.Rev != rAlphaLater || ev1.Key != relAlpha || string(ev1.Value) != "a-later" {
		t.Fatalf("w1 nextChange = (%+v, %v), want Put %q@%d=a-later", ev1, ok, relAlpha, rAlphaLater)
	}
	ev2, ok := nextChange(t, w2, timeout)
	if !ok || ev2.Type != watchcache.EventPut || ev2.Rev != rBetaLater || ev2.Key != relBeta || string(ev2.Value) != "b-later" {
		t.Fatalf("w2 nextChange = (%+v, %v), want Put %q@%d=b-later", ev2, ok, relBeta, rBetaLater)
	}

	curFuture, err := s.CurrentRev(ctx)
	if err != nil {
		t.Fatalf("CurrentRev: %v", err)
	}
	if _, err := m.Compact(ctx, curFuture+100); !errors.Is(err, store.ErrFutureRev) {
		t.Fatalf("Compact(future) err = %v, want ErrFutureRev", err)
	}
}

func TestManagerCompactLaggingCache(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := shortConfig()
		cfg.ReadWait = 100 * time.Millisecond
		checkManagerLaggingCache(t, memstoreFactory, cfg, 2*time.Second)
	})
}

func checkManagerLaggingCache(t *testing.T, newStore func(*testing.T) store.Store, cfg watchcache.Config, timeout time.Duration) {
	src := newTestSource(newStore(t))
	m, err := watchcache.NewManager(src, cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(src.wait)
	t.Cleanup(m.Close)

	ctx := context.Background()
	absAlpha, relAlpha := mustObjectKey(t, "alpha", "core", "pod", "p1")
	absBeta, relBeta := mustObjectKey(t, "beta", "core", "pod", "p2")
	rPre := mustTxn(t, src, putOp(absAlpha, "a0"), putOp(absBeta, "b0"))

	c1, err := m.Objects("alpha", "core", "pod")
	if err != nil {
		t.Fatalf("Objects(alpha): %v", err)
	}
	c2, err := m.Objects("beta", "core", "pod")
	if err != nil {
		t.Fatalf("Objects(beta): %v", err)
	}
	waitReadyTimeout(t, c1, timeout)
	waitReadyTimeout(t, c2, timeout)

	if _, _, err := c1.Get(ctx, relAlpha, 0); err != nil {
		t.Fatalf("sync c1: %v", err)
	}
	if _, _, err := c2.Get(ctx, relBeta, 0); err != nil {
		t.Fatalf("sync c2: %v", err)
	}

	src.setStalled(true)
	boundBefore, ok := m.CompactBound()
	if !ok || boundBefore < rPre {
		src.setStalled(false)
		t.Fatalf("CompactBound before stall = (%d, %v), want >= %d", boundBefore, ok, rPre)
	}

	var curAfter store.Rev
	for i := 1; i <= 3; i++ {
		curAfter = mustTxn(t, src, putOp(absAlpha, fmt.Sprintf("a%d", i)))
	}

	compacted, err := m.Compact(ctx, curAfter)
	if err != nil || compacted != boundBefore || compacted >= curAfter {
		src.setStalled(false)
		t.Fatalf("Compact while stalled = (%d, %v), want (%d, nil) < %d", compacted, err, boundBefore, curAfter)
	}
	if kv, gotRev, err := src.Get(ctx, absAlpha, boundBefore); err != nil || gotRev != boundBefore || string(kv.Value) != "a0" {
		src.setStalled(false)
		t.Fatalf("store Get at boundBefore=%d = (%+v, %d, %v), want a0", boundBefore, kv, gotRev, err)
	}

	src.setStalled(false)
	// One ReadWait may be too short for both caches on a slow machine; each
	// attempt compacts as far as the caches have come.
	waitUntil(t, timeout, func() bool {
		compactedAfter, err := m.Compact(ctx, curAfter)
		if err != nil {
			t.Fatalf("Compact after unstall: %v", err)
		}
		return compactedAfter == curAfter
	}, "Compact did not reach the current revision after the stall ended")
}

func TestManagerCompactBoundAndListing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ms := memstore.New()
		src := newTestSource(ms)
		cfg := shortConfig()
		cfg.ReadWait = 5 * time.Second

		m1, err := watchcache.NewManager(src, cfg)
		if err != nil {
			t.Fatalf("NewManager m1: %v", err)
		}
		src.setBlockRange(true)
		if _, err := m1.Objects("pending-only", "core", "pod"); err != nil {
			src.setBlockRange(false)
			m1.Close()
			src.wait()
			t.Fatalf("Objects(pending-only): %v", err)
		}
		if bound, ok := m1.CompactBound(); ok {
			src.setBlockRange(false)
			m1.Close()
			src.wait()
			t.Fatalf("CompactBound while only cache is listing = (%d, true), want (0, false)", bound)
		}

		ctx := context.Background()
		absAlpha, _ := mustObjectKey(t, "alpha", "core", "pod", "p1")
		r1 := mustTxn(t, ms, putOp(absAlpha, "v0"))

		start := time.Now()
		if compacted, err := m1.Compact(ctx, r1); err != nil || compacted != r1 {
			src.setBlockRange(false)
			m1.Close()
			src.wait()
			t.Fatalf("Compact with only listing cache = (%d, %v), want (%d, nil)", compacted, err, r1)
		}
		if elapsed := time.Since(start); elapsed >= cfg.ReadWait/2 {
			src.setBlockRange(false)
			m1.Close()
			src.wait()
			t.Fatalf("Compact with only listing cache took %v, expected well under ReadWait %v", elapsed, cfg.ReadWait)
		}

		start = time.Now()
		if _, err := m1.Compact(ctx, r1+100); !errors.Is(err, store.ErrFutureRev) {
			src.setBlockRange(false)
			m1.Close()
			src.wait()
			t.Fatalf("Compact(future) err = %v, want ErrFutureRev", err)
		}
		if elapsed := time.Since(start); elapsed >= cfg.ReadWait/2 {
			src.setBlockRange(false)
			m1.Close()
			src.wait()
			t.Fatalf("Compact(future) took %v, expected well under ReadWait %v", elapsed, cfg.ReadWait)
		}
		src.setBlockRange(false)
		m1.Close()
		src.wait()

		ms2 := memstore.New()
		src2 := newTestSource(ms2)
		m2, err := watchcache.NewManager(src2, cfg)
		if err != nil {
			t.Fatalf("NewManager m2: %v", err)
		}
		t.Cleanup(src2.wait)
		t.Cleanup(m2.Close)

		rBase := mustTxn(t, ms2, putOp(absAlpha, "v0"))
		c1, err := m2.Objects("alpha", "core", "pod")
		if err != nil {
			t.Fatalf("Objects(alpha): %v", err)
		}
		c2, err := m2.Objects("beta", "core", "pod")
		if err != nil {
			t.Fatalf("Objects(beta): %v", err)
		}
		waitReady(t, c1)
		waitReady(t, c2)

		// Two writes to alpha close rA1 in c1 without sending anything to idle c2.
		rA1 := mustTxn(t, ms2, putOp(absAlpha, "v1"))
		rA2 := mustTxn(t, ms2, putOp(absAlpha, "v2"))
		waitUntil(t, 2*time.Second, func() bool {
			cr, ok := c1.ClosedRev()
			return ok && cr >= rA1
		}, "c1 ClosedRev did not advance")

		closed2, ok := c2.ClosedRev()
		if !ok || closed2 != rBase {
			t.Fatalf("c2 ClosedRev = (%d, %v), want (%d, true)", closed2, ok, rBase)
		}
		if bound, ok := m2.CompactBound(); !ok || bound != rBase {
			t.Fatalf("CompactBound with lagging idle c2 = (%d, %v), want (%d, true)", bound, ok, rBase)
		}

		src2.setBlockRange(true)
		defer src2.setBlockRange(false)
		cListing, err := m2.Objects("listing", "core", "pod")
		if err != nil {
			t.Fatalf("Objects(listing): %v", err)
		}
		if _, ok := cListing.ClosedRev(); ok {
			t.Fatal("expected listing cache ClosedRev to be false")
		}
		if bound, ok := m2.CompactBound(); !ok || bound != rBase {
			t.Fatalf("CompactBound with listing cache = (%d, %v), want (%d, true)", bound, ok, rBase)
		}

		start = time.Now()
		if compacted, err := m2.Compact(ctx, rA2); err != nil || compacted != rA2 {
			t.Fatalf("Compact with listing cache = (%d, %v), want (%d, nil)", compacted, err, rA2)
		}
		if elapsed := time.Since(start); elapsed >= cfg.ReadWait/2 {
			t.Fatalf("Compact with listing cache took %v, expected well under ReadWait %v", elapsed, cfg.ReadWait)
		}

		start = time.Now()
		if _, err := m2.Compact(ctx, rA2+100); !errors.Is(err, store.ErrFutureRev) {
			t.Fatalf("m2 Compact(future) err = %v, want ErrFutureRev", err)
		}
		if elapsed := time.Since(start); elapsed >= cfg.ReadWait/2 {
			t.Fatalf("m2 Compact(future) took %v, expected well under ReadWait %v", elapsed, cfg.ReadWait)
		}
	})
}

func TestManagerClose(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		baseGoroutines := cacheGoroutines()

		ms := memstore.New()
		m, err := watchcache.NewManager(ms, shortConfig())
		if err != nil {
			t.Fatalf("NewManager: %v", err)
		}

		c1, err := m.Objects("alpha", "core", "pod")
		if err != nil {
			t.Fatalf("Objects(alpha): %v", err)
		}
		c2, err := m.Objects("beta", "core", "pod")
		if err != nil {
			t.Fatalf("Objects(beta): %v", err)
		}
		waitReady(t, c1)
		waitReady(t, c2)

		m.Close()
		m.Close()

		ctx := context.Background()
		if _, _, err := c1.Get(ctx, "o/core/pod/x", 0); !errors.Is(err, watchcache.ErrClosed) {
			t.Fatalf("c1 Get after Manager.Close err = %v, want ErrClosed", err)
		}
		if _, _, err := c2.Get(ctx, "o/core/pod/x", 0); !errors.Is(err, watchcache.ErrClosed) {
			t.Fatalf("c2 Get after Manager.Close err = %v, want ErrClosed", err)
		}
		if _, err := m.Objects("gamma", "core", "pod"); !errors.Is(err, watchcache.ErrClosed) {
			t.Fatalf("Objects after Close err = %v, want ErrClosed", err)
		}
		if _, err := m.Compact(ctx, 1); !errors.Is(err, watchcache.ErrClosed) {
			t.Fatalf("Compact after Close err = %v, want ErrClosed", err)
		}
		if _, ok := m.CompactBound(); ok {
			t.Fatal("CompactBound after Close should be false")
		}

		waitUntil(t, 2*time.Second, func() bool {
			return cacheGoroutines() <= baseGoroutines
		}, "goroutine count did not return to baseline after Manager.Close")
	})
}
