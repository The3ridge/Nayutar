// Package watchcache serves reads and watches of one key prefix from memory.
package watchcache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/watchcache/window"
)

var (
	// ErrTooOld is returned when a requested revision is older than the cache retains,
	// or when a watch is overtaken by eviction or a re-list.
	ErrTooOld = window.ErrTooOld
	// ErrNotReady is returned when a watch cannot start because the cache has not caught up in time.
	ErrNotReady = errors.New("watchcache: the cache is not ready")
	// ErrClosed is returned when the cache has been closed.
	ErrClosed = errors.New("watchcache: the cache is closed")
)

// Source is the part of the store contract a cache reads.
type Source interface {
	Get(ctx context.Context, key string, atRev store.Rev) (store.KV, store.Rev, error)
	Range(ctx context.Context, prefix string, atRev store.Rev, limit int, cont string) ([]store.KV, store.Rev, string, error)
	Watch(ctx context.Context, prefix string, fromRev store.Rev) (store.WatchStream, error)
	CurrentRev(ctx context.Context) (store.Rev, error)
}

// Clock is the time source of a cache.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Config controls retention, timeouts, and polling intervals of a Cache.
// Zero fields take their defaults.
type Config struct {
	MinEvents        int
	MinAge           time.Duration
	MaxEvents        int
	ReadWait         time.Duration
	ProgressInterval time.Duration
	BookmarkInterval time.Duration
	ListPageSize     int
	Clock            Clock
}

const (
	defaultMinEvents        = 10_000
	defaultMinAge           = 150 * time.Second
	defaultMaxEvents        = 102_400
	defaultReadWait         = 3 * time.Second
	defaultProgressInterval = 100 * time.Millisecond
	defaultBookmarkInterval = 60 * time.Second
	defaultListPageSize     = 1_000

	initialBackoff = 5 * time.Millisecond
	maxBackoff     = time.Second
)

func validateAndDefault(cfg Config) (Config, error) {
	if cfg.MinEvents < 0 || cfg.MinAge < 0 || cfg.MaxEvents < 0 ||
		cfg.ReadWait < 0 || cfg.ProgressInterval < 0 || cfg.BookmarkInterval < 0 ||
		cfg.ListPageSize < 0 {
		return Config{}, fmt.Errorf("%w: negative config value", store.ErrInvalidArgument)
	}
	if cfg.MinEvents == 0 {
		cfg.MinEvents = defaultMinEvents
	}
	if cfg.MinAge == 0 {
		cfg.MinAge = defaultMinAge
	}
	if cfg.MaxEvents == 0 {
		cfg.MaxEvents = defaultMaxEvents
	}
	if cfg.MaxEvents < cfg.MinEvents {
		return Config{}, fmt.Errorf("%w: MaxEvents (%d) < MinEvents (%d)", store.ErrInvalidArgument, cfg.MaxEvents, cfg.MinEvents)
	}
	if cfg.ReadWait == 0 {
		cfg.ReadWait = defaultReadWait
	}
	if cfg.ProgressInterval == 0 {
		cfg.ProgressInterval = defaultProgressInterval
	}
	if cfg.BookmarkInterval == 0 {
		cfg.BookmarkInterval = defaultBookmarkInterval
	}
	if cfg.ListPageSize == 0 {
		cfg.ListPageSize = defaultListPageSize
	}
	if cfg.Clock == nil {
		cfg.Clock = realClock{}
	}
	return cfg, nil
}

// Cache serves reads and watches of a single key prefix from an in-memory window.
type Cache struct {
	src    Source
	prefix string
	cfg    Config
	winCfg window.Config
	clock  Clock

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu          sync.Mutex
	closed      bool
	ready       bool
	win         *window.Window
	gen         uint64
	stream      store.WatchStream
	progressSeq uint64
	waiters     int
	wakeCh      chan struct{}
	progressCh  chan struct{}
}

// New constructs a Cache for prefix backed by src, starts background syncing,
// and returns immediately without waiting for the initial list.
func New(src Source, prefix string, cfg Config) (*Cache, error) {
	if src == nil {
		return nil, fmt.Errorf("%w: nil source", store.ErrInvalidArgument)
	}
	resolved, err := validateAndDefault(cfg)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &Cache{
		src:    src,
		prefix: prefix,
		cfg:    resolved,
		winCfg: window.Config{
			MinEvents: resolved.MinEvents,
			MinAge:    resolved.MinAge,
			MaxEvents: resolved.MaxEvents,
		},
		clock:      resolved.Clock,
		ctx:        ctx,
		cancel:     cancel,
		wakeCh:     make(chan struct{}),
		progressCh: make(chan struct{}),
	}
	c.wg.Add(2)
	go c.syncLoop()
	go c.progressLoop()
	return c, nil
}

// Close ends every watch with ErrClosed, stops all background goroutines, and waits for them.
func (c *Cache) Close() {
	c.mu.Lock()
	already := c.closed
	if !already {
		c.closed = true
		c.ready = false
		c.win = nil
		c.stream = nil
		c.gen++
		c.notifyLocked()
		c.notifyProgressLocked()
		c.cancel()
	}
	c.mu.Unlock()
	c.wg.Wait()
}

// OldestRev returns the oldest revision boundary retained by the window,
// or false while the cache has no ready listed state.
func (c *Cache) OldestRev() (store.Rev, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || !c.ready || c.win == nil {
		return 0, false
	}
	return c.win.Floor(), true
}

// ClosedRev returns the closed revision: the largest revision whose changes
// the cache has all received. It returns false while the cache has no listed
// state.
func (c *Cache) ClosedRev() (store.Rev, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || !c.ready || c.win == nil {
		return 0, false
	}
	return c.win.Rev(), true
}

func (c *Cache) catchUp(ctx context.Context, rev store.Rev) (store.Rev, bool, error) {
	c.mu.Lock()
	var timerCh <-chan time.Time
	timedOut := false
	for {
		if c.closed {
			c.mu.Unlock()
			return 0, false, ErrClosed
		}
		if err := ctx.Err(); err != nil {
			c.mu.Unlock()
			return 0, false, err
		}
		if !c.ready || c.win == nil {
			c.mu.Unlock()
			return 0, false, nil
		}
		closedRev := c.win.Rev()
		if closedRev >= rev || timedOut {
			c.mu.Unlock()
			return closedRev, true, nil
		}
		var err error
		timedOut, err = c.waitStepLocked(ctx, &timerCh)
		if err != nil {
			return 0, false, err
		}
	}
}

func (c *Cache) notifyLocked() {
	close(c.wakeCh)
	c.wakeCh = make(chan struct{})
}

func (c *Cache) notifyProgressLocked() {
	close(c.progressCh)
	c.progressCh = make(chan struct{})
}

func (c *Cache) addWaiterLocked() {
	c.waiters++
	if c.waiters == 1 {
		c.notifyProgressLocked()
	}
}

func (c *Cache) removeWaiterLocked() {
	c.waiters--
}

// waitStepLocked waits once for a window change, ReadWait expiry, or cancellation.
// Caller must hold c.mu on entry and holds c.mu when err == nil.
func (c *Cache) waitStepLocked(ctx context.Context, timerCh *<-chan time.Time) (timedOut bool, err error) {
	if err := ctx.Err(); err != nil {
		c.mu.Unlock()
		return false, err
	}
	if *timerCh == nil {
		*timerCh = c.clock.After(c.cfg.ReadWait)
	}
	ch := c.wakeCh
	c.addWaiterLocked()
	c.mu.Unlock()

	var waitErr error
	select {
	case <-ch:
	case <-*timerCh:
		timedOut = true
	case <-ctx.Done():
		waitErr = ctx.Err()
	case <-c.ctx.Done():
		waitErr = ErrClosed
	}

	c.mu.Lock()
	c.removeWaiterLocked()
	if waitErr != nil {
		c.mu.Unlock()
		return false, waitErr
	}
	if c.closed {
		c.mu.Unlock()
		return false, ErrClosed
	}
	return timedOut, nil
}

func (c *Cache) checkClosed() error {
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return ErrClosed
	}
	return nil
}

// Get returns the key as of atRev (or a consistent read when atRev is 0).
func (c *Cache) Get(ctx context.Context, key string, atRev store.Rev) (store.KV, store.Rev, error) {
	if err := c.checkClosed(); err != nil {
		return store.KV{}, 0, err
	}
	if err := store.ValidateGet(key, atRev); err != nil {
		return store.KV{}, 0, err
	}
	if !strings.HasPrefix(key, c.prefix) {
		return store.KV{}, 0, fmt.Errorf("%w: key %q is outside prefix %q", store.ErrInvalidArgument, key, c.prefix)
	}
	if err := ctx.Err(); err != nil {
		return store.KV{}, 0, err
	}

	var kv store.KV
	var rev store.Rev
	err := c.read(
		ctx,
		atRev,
		func(w *window.Window) error {
			var err error
			kv, rev, err = w.Get(key, atRev)
			if err == nil {
				kv.Value = bytes.Clone(kv.Value)
			}
			return err
		},
		func() error {
			var err error
			kv, rev, err = c.src.Get(ctx, key, atRev)
			return err
		},
	)
	return kv, rev, err
}

// Range returns keys starting with prefix and strictly after cont as of atRev
// (or a consistent read when atRev is 0).
func (c *Cache) Range(ctx context.Context, prefix string, atRev store.Rev, limit int, cont string) ([]store.KV, store.Rev, string, error) {
	if err := c.checkClosed(); err != nil {
		return nil, 0, "", err
	}
	if err := store.ValidateRange(prefix, atRev, limit, cont); err != nil {
		return nil, 0, "", err
	}
	if !strings.HasPrefix(prefix, c.prefix) {
		return nil, 0, "", fmt.Errorf("%w: prefix %q is outside cache prefix %q", store.ErrInvalidArgument, prefix, c.prefix)
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, "", err
	}

	var kvs []store.KV
	var rev store.Rev
	var nextCont string
	err := c.read(
		ctx,
		atRev,
		func(w *window.Window) error {
			var err error
			kvs, rev, nextCont, err = w.Range(prefix, atRev, limit, cont)
			if err == nil {
				for i := range kvs {
					kvs[i].Value = bytes.Clone(kvs[i].Value)
				}
			}
			return err
		},
		func() error {
			var err error
			kvs, rev, nextCont, err = c.src.Range(ctx, prefix, atRev, limit, cont)
			return err
		},
	)
	return kvs, rev, nextCont, err
}

func (c *Cache) read(
	ctx context.Context,
	atRev store.Rev,
	fromWin func(*window.Window) error,
	fromSrc func() error,
) error {
	if atRev == 0 {
		target, err := c.src.CurrentRev(ctx)
		if err != nil {
			return err
		}
		c.mu.Lock()
		var timerCh <-chan time.Time
		timedOut := false
		for {
			if c.closed {
				c.mu.Unlock()
				return ErrClosed
			}
			if c.ready && c.win.Rev() >= target {
				err := fromWin(c.win)
				c.mu.Unlock()
				return err
			}
			if timedOut {
				c.mu.Unlock()
				return fromSrc()
			}
			timedOut, err = c.waitStepLocked(ctx, &timerCh)
			if err != nil {
				return err
			}
		}
	}

	c.mu.Lock()
	var timerCh <-chan time.Time
	timedOut := false
	for {
		if c.closed {
			c.mu.Unlock()
			return ErrClosed
		}
		if c.ready {
			err := fromWin(c.win)
			if err == window.ErrTooOld {
				c.mu.Unlock()
				return fromSrc()
			}
			if err != window.ErrNotYet {
				c.mu.Unlock()
				return err
			}
		}
		if timedOut {
			c.mu.Unlock()
			return fromSrc()
		}
		var err error
		timedOut, err = c.waitStepLocked(ctx, &timerCh)
		if err != nil {
			return err
		}
	}
}

func (c *Cache) listAll(ctx context.Context) ([]store.KV, store.Rev, error) {
	var all []store.KV
	page, rev, cont, err := c.src.Range(ctx, c.prefix, 0, c.cfg.ListPageSize, "")
	if err != nil {
		return nil, 0, err
	}
	all = append(all, page...)
	for cont != "" {
		page, _, cont, err = c.src.Range(ctx, c.prefix, rev, c.cfg.ListPageSize, cont)
		if err != nil {
			return nil, 0, err
		}
		all = append(all, page...)
	}
	return all, rev, nil
}

func (c *Cache) clearReadyLocked() {
	c.ready = false
	c.win = nil
	c.stream = nil
	c.gen++
	c.notifyLocked()
}

func (c *Cache) syncLoop() {
	defer c.wg.Done()
	backoff := initialBackoff
	for {
		if c.ctx.Err() != nil {
			return
		}
		kvs, listRev, err := c.listAll(c.ctx)
		if err != nil {
			if !c.waitBackoff(backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		watchCtx, cancelWatch := context.WithCancel(c.ctx)
		ws, err := c.src.Watch(watchCtx, c.prefix, listRev+1)
		if err != nil {
			cancelWatch()
			if !c.waitBackoff(backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		win := window.New(c.winCfg, listRev, kvs)
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			cancelWatch()
			return
		}
		c.win = win
		c.stream = ws
		c.ready = true
		c.gen++
		c.notifyLocked()
		if c.waiters > 0 {
			c.notifyProgressLocked()
		}
		c.mu.Unlock()

		establishedAt := c.clock.Now()
		receivedAny := c.pumpStream(ws, win, &backoff)
		cancelWatch()

		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return
		}
		c.clearReadyLocked()
		c.mu.Unlock()

		if receivedAny || c.clock.Now().Sub(establishedAt) >= maxBackoff {
			backoff = initialBackoff
		}
		if !c.waitBackoff(backoff) {
			return
		}
		backoff = nextBackoff(backoff)
	}
}

func (c *Cache) pumpStream(ws store.WatchStream, win *window.Window, backoff *time.Duration) bool {
	receivedAny := false
	for {
		select {
		case <-c.ctx.Done():
			return receivedAny
		case ev, ok := <-ws.Events():
			if !ok {
				return receivedAny
			}
			c.mu.Lock()
			if c.closed {
				c.mu.Unlock()
				return receivedAny
			}
			var err error
			switch ev.Type {
			case store.EventPut, store.EventDelete:
				err = win.Event(ev, c.clock.Now())
			case store.EventProgress:
				err = win.Progress(ev.Rev)
				if err == nil {
					c.progressSeq++
				}
			default:
				err = window.ErrOutOfOrder
			}
			if err != nil {
				c.mu.Unlock()
				return receivedAny
			}
			receivedAny = true
			*backoff = initialBackoff
			c.notifyLocked()
			c.mu.Unlock()
		}
	}
}

func (c *Cache) waitBackoff(d time.Duration) bool {
	select {
	case <-c.clock.After(d):
		return true
	case <-c.ctx.Done():
		return false
	}
}

func nextBackoff(d time.Duration) time.Duration {
	d *= 2
	if d > maxBackoff {
		return maxBackoff
	}
	return d
}

// progressLoop asks the upstream watch for progress while somebody waits for
// the window to advance: at once when the last request is older than
// ProgressInterval, and at most once per ProgressInterval after that.
func (c *Cache) progressLoop() {
	defer c.wg.Done()
	var last time.Time
	for {
		c.mu.Lock()
		for !c.closed && (c.waiters == 0 || c.stream == nil) {
			ch := c.progressCh
			c.mu.Unlock()
			select {
			case <-ch:
			case <-c.ctx.Done():
				return
			}
			c.mu.Lock()
		}
		if c.closed {
			c.mu.Unlock()
			return
		}
		stream := c.stream
		c.mu.Unlock()

		if !last.IsZero() {
			if wait := c.cfg.ProgressInterval - c.clock.Now().Sub(last); wait > 0 {
				select {
				case <-c.clock.After(wait):
				case <-c.ctx.Done():
					return
				}
				continue
			}
		}
		last = c.clock.Now()
		_ = stream.RequestProgress(c.ctx)
	}
}
