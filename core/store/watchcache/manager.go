package watchcache

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
	storetenant "github.com/Kaikei-e/Nayutar/core/store/tenant"
)

type cacheKey struct {
	tenant string
	group  string
	kind   string
}

// Manager owns the caches of one process: one per tenant and object kind.
type Manager struct {
	root store.Store
	cfg  Config

	mu        sync.Mutex
	closed    bool
	closeDone chan struct{}
	caches    map[cacheKey]*Cache
}

// NewManager creates a Manager that opens object caches over root with cfg.
func NewManager(root store.Store, cfg Config) (*Manager, error) {
	if root == nil {
		return nil, fmt.Errorf("%w: nil store", store.ErrInvalidArgument)
	}
	resolved, err := validateAndDefault(cfg)
	if err != nil {
		return nil, err
	}
	return &Manager{
		root:      root,
		cfg:       resolved,
		closeDone: make(chan struct{}),
		caches:    make(map[cacheKey]*Cache),
	}, nil
}

// Objects returns the cache of the objects of (group, kind) in tenant, creating it on first use.
// The cache reads through tenant.View(root, tenant) and covers keyspace.ObjectPrefix(group, kind),
// so its keys are tenant-relative. The Manager owns the cache; callers must not Close it.
func (m *Manager) Objects(tenant, group, kind string) (*Cache, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrClosed
	}
	key := cacheKey{tenant: tenant, group: group, kind: kind}
	if c, ok := m.caches[key]; ok {
		return c, nil
	}
	view, err := storetenant.View(m.root, tenant)
	if err != nil {
		return nil, err
	}
	prefix, err := keyspace.ObjectPrefix(group, kind)
	if err != nil {
		return nil, err
	}
	c, err := New(view, prefix, m.cfg)
	if err != nil {
		return nil, err
	}
	m.caches[key] = c
	return c, nil
}

// CompactBound returns the smallest ClosedRev among the caches that are ready, and false when none is.
func (m *Manager) CompactBound() (store.Rev, bool) {
	m.mu.Lock()
	if m.closed || len(m.caches) == 0 {
		m.mu.Unlock()
		return 0, false
	}
	caches := make([]*Cache, 0, len(m.caches))
	for _, c := range m.caches {
		caches = append(caches, c)
	}
	m.mu.Unlock()

	var minRev store.Rev
	hasBound := false
	for _, c := range caches {
		r, ok := c.ClosedRev()
		if !ok {
			continue
		}
		if !hasBound || r < minRev {
			minRev = r
			hasBound = true
		}
	}
	return minRev, hasBound
}

// Compact compacts the root store at rev after giving every ready cache up to
// ReadWait to receive the changes up to rev. A cache that has not caught up
// lowers the revision to its closed revision, so the history its upstream
// watch still has to deliver is kept. It returns the revision it compacted at,
// which is below rev when a cache lagged, or 0 when the store was already
// compacted that far.
func (m *Manager) Compact(ctx context.Context, rev store.Rev) (store.Rev, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return 0, ErrClosed
	}
	caches := make([]*Cache, 0, len(m.caches))
	for _, c := range m.caches {
		caches = append(caches, c)
	}
	m.mu.Unlock()

	cur, err := m.root.CurrentRev(ctx)
	if err != nil {
		return 0, err
	}
	if rev > cur {
		return 0, fmt.Errorf("%w: revision %d is above current revision %d", store.ErrFutureRev, rev, cur)
	}

	type catchUpResult struct {
		rev store.Rev
		ok  bool
		err error
	}
	results := make([]catchUpResult, len(caches))
	var wg sync.WaitGroup
	for i, c := range caches {
		wg.Add(1)
		go func(idx int, cache *Cache) {
			defer wg.Done()
			r, ok, err := cache.catchUp(ctx, rev)
			results[idx] = catchUpResult{rev: r, ok: ok, err: err}
		}(i, c)
	}
	wg.Wait()

	target := rev
	for _, res := range results {
		if res.err != nil {
			return 0, res.err
		}
		if res.ok && res.rev < target {
			target = res.rev
		}
	}
	if target == 0 {
		return 0, nil
	}
	if err := m.root.Compact(ctx, target); err != nil {
		if errors.Is(err, store.ErrCompacted) {
			return 0, nil
		}
		return 0, err
	}
	return target, nil
}

// Close closes every cache owned by the Manager and waits for them to stop.
// Close is idempotent; Objects and Compact return ErrClosed afterwards.
func (m *Manager) Close() {
	m.mu.Lock()
	already := m.closed
	var caches []*Cache
	if !already {
		m.closed = true
		caches = make([]*Cache, 0, len(m.caches))
		for _, c := range m.caches {
			caches = append(caches, c)
		}
		m.caches = nil
	}
	m.mu.Unlock()

	if already {
		<-m.closeDone
		return
	}
	for _, c := range caches {
		c.Close()
	}
	close(m.closeDone)
}
