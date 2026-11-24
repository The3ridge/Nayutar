package watchcache_test

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/watchcache"
)

type testSource struct {
	store store.Store

	gets         atomic.Int64
	ranges       atomic.Int64
	watches      atomic.Int64
	currentRevs  atomic.Int64
	progressReqs atomic.Int64

	wg             sync.WaitGroup
	mu             sync.Mutex
	blockRange     bool
	rangeUnblocked chan struct{}
	failRangeErr   error
	stalled        bool
	stallCh        chan struct{}
	curWatch       *testWatchStream
}

func newTestSource(s store.Store) *testSource {
	return &testSource{
		store:          s,
		rangeUnblocked: make(chan struct{}),
		stallCh:        make(chan struct{}),
	}
}

func (s *testSource) resetCounts() {
	s.gets.Store(0)
	s.ranges.Store(0)
	s.watches.Store(0)
	s.currentRevs.Store(0)
	s.progressReqs.Store(0)
}

func (s *testSource) setBlockRange(block bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.blockRange == block {
		return
	}
	s.blockRange = block
	if !block {
		close(s.rangeUnblocked)
		s.rangeUnblocked = make(chan struct{})
	}
}

func (s *testSource) setFailRange(err error) {
	s.mu.Lock()
	s.failRangeErr = err
	s.mu.Unlock()
}

func (s *testSource) setStalled(stalled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stalled == stalled {
		return
	}
	s.stalled = stalled
	close(s.stallCh)
	s.stallCh = make(chan struct{})
}

func (s *testSource) isStalled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stalled
}

func (s *testSource) stallState() (bool, <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stalled, s.stallCh
}

func (s *testSource) currentWatch() *testWatchStream {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.curWatch
}

func (s *testSource) killWatch(err error) bool {
	w := s.currentWatch()
	if w == nil {
		return false
	}
	w.kill(err)
	return true
}

func (s *testSource) injectEvent(ev store.Event) bool {
	w := s.currentWatch()
	if w == nil {
		return false
	}
	return w.inject(ev)
}

func (s *testSource) Get(ctx context.Context, key string, atRev store.Rev) (store.KV, store.Rev, error) {
	s.gets.Add(1)
	return s.store.Get(ctx, key, atRev)
}

func (s *testSource) Range(ctx context.Context, prefix string, atRev store.Rev, limit int, cont string) ([]store.KV, store.Rev, string, error) {
	s.ranges.Add(1)
	for {
		s.mu.Lock()
		blocked := s.blockRange
		ch := s.rangeUnblocked
		failErr := s.failRangeErr
		s.mu.Unlock()

		if failErr != nil {
			return nil, 0, "", failErr
		}
		if !blocked {
			break
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, 0, "", ctx.Err()
		}
	}
	return s.store.Range(ctx, prefix, atRev, limit, cont)
}

func (s *testSource) CurrentRev(ctx context.Context) (store.Rev, error) {
	s.currentRevs.Add(1)
	return s.store.CurrentRev(ctx)
}

func (s *testSource) Txn(ctx context.Context, cmps []store.Cmp, ops []store.Op) (bool, store.Rev, error) {
	return s.store.Txn(ctx, cmps, ops)
}

func (s *testSource) Compact(ctx context.Context, rev store.Rev) error {
	return s.store.Compact(ctx, rev)
}

func (s *testSource) Watch(ctx context.Context, prefix string, fromRev store.Rev) (store.WatchStream, error) {
	s.watches.Add(1)
	innerCtx, innerCancel := context.WithCancel(ctx)
	inner, err := s.store.Watch(innerCtx, prefix, fromRev)
	if err != nil {
		innerCancel()
		return nil, err
	}
	tw := &testWatchStream{
		src:         s,
		ctx:         ctx,
		inner:       inner,
		innerCancel: innerCancel,
		out:         make(chan store.Event),
		injected:    make(chan store.Event, 16),
		killCh:      make(chan struct{}),
	}
	s.mu.Lock()
	s.curWatch = tw
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		tw.pump(ctx)
	}()
	return tw, nil
}

func (s *testSource) wait() {
	s.wg.Wait()
}

type testWatchStream struct {
	src         *testSource
	ctx         context.Context
	inner       store.WatchStream
	innerCancel context.CancelFunc
	out         chan store.Event
	injected    chan store.Event
	killCh      chan struct{}

	mu     sync.Mutex
	killed bool
	err    error
}

func (w *testWatchStream) Events() <-chan store.Event {
	return w.out
}

func (w *testWatchStream) RequestProgress(ctx context.Context) error {
	w.src.progressReqs.Add(1)
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.src.isStalled() {
		return nil
	}
	w.mu.Lock()
	killed := w.killed
	err := w.err
	w.mu.Unlock()
	if killed {
		return err
	}
	return w.inner.RequestProgress(ctx)
}

func (w *testWatchStream) Err() error {
	w.mu.Lock()
	err := w.err
	w.mu.Unlock()
	if err != nil {
		return err
	}
	if err := w.inner.Err(); err != nil {
		return err
	}
	return w.ctx.Err()
}

func (w *testWatchStream) setErr(err error) {
	w.mu.Lock()
	if w.err == nil {
		w.err = err
	}
	w.mu.Unlock()
}

func (w *testWatchStream) kill(err error) {
	w.mu.Lock()
	if !w.killed {
		w.killed = true
		w.err = err
		close(w.killCh)
	}
	w.mu.Unlock()
	w.innerCancel()
}

func (w *testWatchStream) inject(ev store.Event) bool {
	select {
	case w.injected <- ev:
		return true
	default:
		return false
	}
}

func (w *testWatchStream) pump(ctx context.Context) {
	defer close(w.out)
	for {
		for {
			stalled, stallCh := w.src.stallState()
			if !stalled {
				break
			}
			select {
			case <-stallCh:
			case <-w.killCh:
				return
			case <-ctx.Done():
				return
			}
		}

		var ev store.Event
		var ok bool
		select {
		case ev = <-w.injected:
			ok = true
		case ev, ok = <-w.inner.Events():
			if !ok {
				w.setErr(w.inner.Err())
				return
			}
		case <-w.killCh:
			return
		case <-ctx.Done():
			return
		}

		select {
		case w.out <- ev:
		case <-w.killCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

func shortConfig() watchcache.Config {
	return watchcache.Config{
		ReadWait:         200 * time.Millisecond,
		ProgressInterval: 5 * time.Millisecond,
		BookmarkInterval: 30 * time.Millisecond,
	}
}

func waitUntil(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		<-time.After(2 * time.Millisecond)
	}
	if cond() {
		return
	}
	t.Fatalf("timed out after %v: %s", timeout, msg)
}

type scenarioOpts struct {
	cfg            watchcache.Config
	timeout        time.Duration
	iters          int
	stressSteps    int
	checkGoroutine bool
	inSynctest     bool
}

func runScenario(t *testing.T, opts scenarioOpts, fn func(*testing.T)) {
	t.Helper()
	if opts.inSynctest {
		synctest.Test(t, fn)
		return
	}
	fn(t)
}

func memstoreOpts() scenarioOpts {
	return scenarioOpts{
		cfg:            shortConfig(),
		timeout:        2 * time.Second,
		iters:          500,
		stressSteps:    80,
		checkGoroutine: true,
		inSynctest:     true,
	}
}

// noFallbackOpts is for scenarios asserting that the source is never read:
// a slow machine must not turn a late progress notification into a fallback.
func noFallbackOpts() scenarioOpts {
	opts := memstoreOpts()
	opts.cfg.ReadWait = 10 * time.Second
	opts.cfg.ProgressInterval = time.Millisecond
	return opts
}

// cacheGoroutines counts the goroutines running or created by the cache, its
// tests, or the in-memory store. A count of all goroutines would also see the
// background goroutines of an etcd client, which come and go on their own.
func cacheGoroutines() int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	count := 0
	for block := range strings.SplitSeq(string(buf), "\n\n") {
		if strings.Contains(block, "core/store/watchcache") || strings.Contains(block, "core/store/memstore") {
			count++
		}
	}
	return count
}

func waitReady(t *testing.T, c *watchcache.Cache) {
	t.Helper()
	waitReadyTimeout(t, c, 2*time.Second)
}

func waitReadyTimeout(t *testing.T, c *watchcache.Cache, timeout time.Duration) {
	t.Helper()
	waitUntil(t, timeout, func() bool {
		_, ok := c.OldestRev()
		return ok
	}, "cache did not become ready")
}

func mustTxn(t *testing.T, s store.Store, ops ...store.Op) store.Rev {
	t.Helper()
	ok, rev, err := s.Txn(context.Background(), nil, ops)
	if err != nil || !ok {
		t.Fatalf("Txn failed: ok=%v err=%v", ok, err)
	}
	return rev
}

func putOp(key, val string) store.Op {
	return store.Op{Type: store.OpPut, Key: key, Value: []byte(val)}
}

func delOp(key string) store.Op {
	return store.Op{Type: store.OpDelete, Key: key}
}

func mustNewCache(t *testing.T, src watchcache.Source, prefix string, cfg watchcache.Config) *watchcache.Cache {
	t.Helper()
	c, err := watchcache.New(src, prefix, cfg)
	if err != nil {
		t.Fatalf("watchcache.New: %v", err)
	}
	if ts, ok := src.(*testSource); ok {
		t.Cleanup(ts.wait)
	}
	t.Cleanup(c.Close)
	return c
}

func nextEvent(t *testing.T, w *watchcache.Watch, timeout time.Duration) (watchcache.Event, bool) {
	t.Helper()
	select {
	case ev, ok := <-w.Events():
		return ev, ok
	case <-time.After(timeout):
		t.Fatalf("timed out after %v waiting for watch event", timeout)
		return watchcache.Event{}, false
	}
}

// nextChange is nextEvent that skips periodic bookmarks, which may come at any
// time between changes.
func nextChange(t *testing.T, w *watchcache.Watch, timeout time.Duration) (watchcache.Event, bool) {
	t.Helper()
	for {
		ev, ok := nextEvent(t, w, timeout)
		if ok && ev.Type == watchcache.EventBookmark && !ev.InitialEventsEnd {
			continue
		}
		return ev, ok
	}
}

func expectClosedWith(t *testing.T, w *watchcache.Watch, want error, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case _, ok := <-w.Events():
			if !ok {
				if err := w.Err(); !errors.Is(err, want) {
					t.Fatalf("watch Err() = %v, want %v", err, want)
				}
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for watch to close with %v", want)
		}
	}
}
