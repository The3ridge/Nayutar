package watchcache_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/memstore"
	"github.com/Kaikei-e/Nayutar/core/store/storetest"
	"github.com/Kaikei-e/Nayutar/core/store/watchcache"
)

type cacheStore struct {
	store.Store
	cache  *watchcache.Cache
	gets   atomic.Int64
	ranges atomic.Int64
}

func newCacheStore(t *testing.T, inner store.Store, cfg watchcache.Config, readyTimeout time.Duration) *cacheStore {
	t.Helper()
	c, err := watchcache.New(inner, "", cfg)
	if err != nil {
		t.Fatalf("watchcache.New: %v", err)
	}
	t.Cleanup(c.Close)
	waitReadyTimeout(t, c, readyTimeout)
	return &cacheStore{Store: inner, cache: c}
}

func (s *cacheStore) Get(ctx context.Context, key string, atRev store.Rev) (store.KV, store.Rev, error) {
	s.gets.Add(1)
	return s.cache.Get(ctx, key, atRev)
}

func (s *cacheStore) Range(ctx context.Context, prefix string, atRev store.Rev, limit int, cont string) ([]store.KV, store.Rev, string, error) {
	s.ranges.Add(1)
	return s.cache.Range(ctx, prefix, atRev, limit, cont)
}

func TestLinearizability(t *testing.T) {
	storetest.RunLinearizability(t, func(t *testing.T) store.Store {
		return newCacheStore(t, memstore.New(), watchcache.Config{ProgressInterval: 5 * time.Millisecond}, 2*time.Second)
	})
}

func TestLinearizabilityServesFromCache(t *testing.T) {
	src := newTestSource(memstore.New())
	adapter := newCacheStore(t, src, watchcache.Config{}, 2*time.Second)
	src.resetCounts()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := storetest.CheckLinearizable(ctx, adapter, storetest.Workload{Clients: 8, Ops: 60, Seed: 1}); err != nil {
		t.Fatal(err)
	}

	totalReads := adapter.gets.Load() + adapter.ranges.Load()
	fallbacks := src.gets.Load() + src.ranges.Load()
	if totalReads == 0 {
		t.Fatal("expected workload to issue Get/Range calls")
	}
	if fallbacks*20 > totalReads {
		t.Fatalf("too many underlying store fallbacks: %d of %d reads", fallbacks, totalReads)
	}
}

type staleCacheStore struct {
	store.Store
	cache    *watchcache.Cache
	staleRev atomic.Int64
}

func (s *staleCacheStore) Get(ctx context.Context, key string, atRev store.Rev) (store.KV, store.Rev, error) {
	if atRev != 0 {
		return s.cache.Get(ctx, key, atRev)
	}
	kv, curRev, err := s.cache.Get(ctx, key, 0)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.KV{}, 0, err
	}
	s.staleRev.CompareAndSwap(0, int64(curRev))
	oldRev := store.Rev(s.staleRev.Load())
	if oldRev > 0 && oldRev < curRev {
		staleKV, _, staleErr := s.cache.Get(ctx, key, oldRev)
		return staleKV, curRev, staleErr
	}
	return kv, curRev, err
}

func (s *staleCacheStore) Range(ctx context.Context, prefix string, atRev store.Rev, limit int, cont string) ([]store.KV, store.Rev, string, error) {
	if atRev != 0 {
		return s.cache.Range(ctx, prefix, atRev, limit, cont)
	}
	kvs, curRev, next, err := s.cache.Range(ctx, prefix, 0, limit, cont)
	if err != nil {
		return nil, 0, "", err
	}
	s.staleRev.CompareAndSwap(0, int64(curRev))
	oldRev := store.Rev(s.staleRev.Load())
	if oldRev > 0 && oldRev < curRev {
		staleKVs, _, staleNext, staleErr := s.cache.Range(ctx, prefix, oldRev, limit, cont)
		return staleKVs, curRev, staleNext, staleErr
	}
	return kvs, curRev, next, nil
}

func TestLinearizabilityCatchesStaleCacheReads(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	for seed := uint64(1); seed <= 20; seed++ {
		ms := memstore.New()
		c, err := watchcache.New(ms, "", watchcache.Config{ProgressInterval: 5 * time.Millisecond})
		if err != nil {
			t.Fatalf("watchcache.New: %v", err)
		}
		waitReady(t, c)
		s := &staleCacheStore{Store: ms, cache: c}
		err = storetest.CheckLinearizable(ctx, s, storetest.Workload{Clients: 8, Ops: 60, Seed: seed})
		c.Close()
		if errors.Is(err, storetest.ErrNotLinearizable) {
			return
		}
		if err != nil {
			t.Fatalf("seed %d: unexpected error = %v", seed, err)
		}
	}
	t.Fatal("20 workloads found no linearizability violation")
}
