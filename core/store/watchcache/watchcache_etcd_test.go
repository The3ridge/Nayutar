//go:build etcd

package watchcache_test

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/etcdstore"
	"github.com/Kaikei-e/Nayutar/core/store/etcdstore/etcdtest"
	"github.com/Kaikei-e/Nayutar/core/store/storetest"
	"github.com/Kaikei-e/Nayutar/core/store/watchcache"
)

var (
	etcdCluster *etcdtest.Cluster
	etcdClient  *clientv3.Client
	etcdScopes  atomic.Int64
)

func TestMain(m *testing.M) {
	os.Exit(runEtcd(m))
}

func runEtcd(m *testing.M) int {
	cluster, err := etcdtest.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer cluster.Stop()
	etcdCluster = cluster
	etcdClient, err = cluster.Client()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = etcdClient.Close() }()
	return m.Run()
}

func isolatedRoot(t *testing.T) store.Store {
	t.Helper()
	inner := etcdstore.New(etcdClient)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bump := []store.Op{{Type: store.OpPut, Key: "/nayutar-test/bump"}}
	_, base, err := inner.Txn(ctx, nil, bump)
	if err != nil {
		t.Fatalf("bump revision: %v", err)
	}
	return storetest.Scope(inner, fmt.Sprintf("/nayutar-test/%d/", etcdScopes.Add(1)), base-1)
}

func etcdOpts() scenarioOpts {
	return scenarioOpts{
		cfg: watchcache.Config{
			ReadWait:         5 * time.Second,
			ProgressInterval: 30 * time.Millisecond,
			BookmarkInterval: 10 * time.Second,
		},
		timeout:        15 * time.Second,
		iters:          80,
		stressSteps:    40,
		checkGoroutine: false,
	}
}

func TestEtcdInitialState(t *testing.T) {
	checkInitialState(t, isolatedRoot, etcdOpts())
}

func TestEtcdReadYourWrites(t *testing.T) {
	checkReadYourWrites(t, isolatedRoot, etcdOpts())
}

func TestEtcdQuietPrefix(t *testing.T) {
	checkQuietPrefix(t, isolatedRoot, etcdOpts())
}

func TestEtcdFallback(t *testing.T) {
	opts := etcdOpts()
	opts.cfg.ReadWait = 500 * time.Millisecond
	checkFallbackStalled(t, isolatedRoot, opts)
}

func TestEtcdExactRevisions(t *testing.T) {
	opts := etcdOpts()
	opts.cfg.ReadWait = time.Second
	checkExactRevisions(t, isolatedRoot, opts)
}

func TestEtcdWatchFromRev(t *testing.T) {
	checkWatchFromRev(t, isolatedRoot, etcdOpts())
}

func TestEtcdWatchList(t *testing.T) {
	checkWatchList(t, isolatedRoot, etcdOpts())
}

func TestEtcdBookmarks(t *testing.T) {
	opts := etcdOpts()
	opts.cfg.ProgressInterval = 20 * time.Millisecond
	opts.cfg.BookmarkInterval = 100 * time.Millisecond
	checkBookmarks(t, isolatedRoot, opts)
}

func TestEtcdTooOld(t *testing.T) {
	checkTooOld(t, isolatedRoot, etcdOpts())
}

func TestEtcdRelist(t *testing.T) {
	checkRelist(t, isolatedRoot, etcdOpts())
}

func TestEtcdStress(t *testing.T) {
	opts := etcdOpts()
	opts.cfg.BookmarkInterval = 100 * time.Millisecond
	checkStress(t, isolatedRoot, opts)
}

func TestEtcdLinearizability(t *testing.T) {
	cfg := watchcache.Config{
		ReadWait:         5 * time.Second,
		ProgressInterval: 30 * time.Millisecond,
	}
	storetest.RunLinearizability(t, func(t *testing.T) store.Store {
		return newCacheStore(t, isolatedRoot(t), cfg, 15*time.Second)
	})
}

func TestEtcdManagerCompaction(t *testing.T) {
	opts := etcdOpts()
	checkManagerCompaction(t, isolatedRoot, opts.cfg, opts.timeout)
}

func TestEtcdManagerLaggingCache(t *testing.T) {
	opts := etcdOpts()
	opts.cfg.ReadWait = time.Second
	checkManagerLaggingCache(t, isolatedRoot, opts.cfg, opts.timeout)
}

func waitEtcdLeader(ctx context.Context) (int, error) {
	var lastErr error
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		leader, err := etcdCluster.Leader(ctx)
		if err == nil {
			return leader, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return -1, fmt.Errorf("no leader elected: %w", lastErr)
}

// stopLeaderOnce kills the leader, waits for the next election, and restarts
// the old leader, so all three members are running again when it returns.
func stopLeaderOnce(ctx context.Context) error {
	leader, err := waitEtcdLeader(ctx)
	if err != nil {
		return err
	}
	if err := etcdCluster.StopMember(leader); err != nil {
		return fmt.Errorf("stop leader %d: %w", leader, err)
	}
	_, electErr := waitEtcdLeader(ctx)
	if electErr == nil {
		select {
		case <-ctx.Done():
		case <-time.After(300 * time.Millisecond):
		}
	}
	if err := etcdCluster.StartMember(leader); err != nil {
		return fmt.Errorf("restart old leader %d: %w", leader, err)
	}
	return electErr
}

func TestEtcdLinearizabilityAcrossLeaderFailure(t *testing.T) {
	cfg := watchcache.Config{
		ReadWait:         time.Second,
		ProgressInterval: 30 * time.Millisecond,
	}
	for seed := uint64(1); seed <= 3; seed++ {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			s := newCacheStore(t, isolatedRoot(t), cfg, 15*time.Second)
			w := storetest.Workload{Clients: 8, Ops: 60, Seed: seed, Disrupt: stopLeaderOnce}
			if err := storetest.CheckLinearizable(ctx, s, w); err != nil {
				t.Fatal(err)
			}

			rev := mustTxn(t, s.Store, putOp("k/after", "recovered"))
			waitUntil(t, 30*time.Second, func() bool {
				if _, ok := s.cache.OldestRev(); !ok {
					return false
				}
				readCtx, cancelRead := context.WithTimeout(ctx, 5*time.Second)
				defer cancelRead()
				kv, got, err := s.cache.Get(readCtx, "k/after", 0)
				return err == nil && got >= rev && string(kv.Value) == "recovered"
			}, "cache did not serve a consistent read after the leader failure")
		})
	}
}
