//go:build etcd

package etcdstore_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"go.uber.org/zap"

	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/etcdstore"
	"github.com/Kaikei-e/Nayutar/core/store/etcdstore/etcdtest"
	"github.com/Kaikei-e/Nayutar/core/store/storetest"
)

var (
	cluster           *etcdtest.Cluster
	client            *clientv3.Client
	scopes            atomic.Int64
	initialClusterErr error
)

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	var err error
	cluster, err = etcdtest.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer cluster.Stop()
	client, err = cluster.Client()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = client.Close() }()
	initialClusterErr = checkFreshCluster(etcdstore.New(client))
	return m.Run()
}

func checkFreshCluster(s store.Store) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rev, err := s.CurrentRev(ctx)
	if err != nil {
		return fmt.Errorf("CurrentRev on fresh cluster: %w", err)
	}
	if rev != 1 {
		return fmt.Errorf("fresh cluster CurrentRev = %d, want 1", rev)
	}
	for _, atRev := range []store.Rev{0, 1} {
		if _, gotRev, err := s.Get(ctx, "absent", atRev); !errors.Is(err, store.ErrNotFound) || gotRev != 1 {
			return fmt.Errorf("fresh cluster Get(atRev=%d) = (%d, %v), want (1, ErrNotFound)", atRev, gotRev, err)
		}
		if kvs, gotRev, next, err := s.Range(ctx, "", atRev, 0, ""); err != nil || len(kvs) != 0 || gotRev != 1 || next != "" {
			return fmt.Errorf("fresh cluster Range(atRev=%d) = (%d keys, rev=%d, cont=%q, %v), want empty at 1", atRev, len(kvs), gotRev, next, err)
		}
	}
	if _, _, err := s.Get(ctx, "absent", 2); !errors.Is(err, store.ErrFutureRev) {
		return fmt.Errorf("fresh cluster Get(atRev=2) = %v, want ErrFutureRev", err)
	}
	watchCtx, watchCancel := context.WithCancel(ctx)
	defer watchCancel()
	ws, err := s.Watch(watchCtx, "", 1)
	if err != nil {
		return fmt.Errorf("fresh cluster Watch(fromRev=1): %w", err)
	}
	events, progress, err := storetest.Sync(ctx, s, ws)
	if err != nil || len(events) != 0 || progress != 1 {
		return fmt.Errorf("fresh cluster Sync(fromRev=1) = (%d events, progress=%d, %v), want (0, 1, nil)", len(events), progress, err)
	}
	return nil
}

func allocScope(t *testing.T) (string, store.Rev) {
	t.Helper()
	inner := etcdstore.New(client)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	bump := []store.Op{{Type: store.OpPut, Key: "/nayutar-test/bump"}}
	_, base, err := inner.Txn(ctx, nil, bump)
	if err != nil {
		t.Fatalf("bump revision: %v", err)
	}
	return fmt.Sprintf("/nayutar-test/%d/", scopes.Add(1)), base - 1
}

// isolated returns a view of the shared cluster that holds no keys. The write
// moves the revision past any compaction an earlier test left behind.
func isolated(t *testing.T) store.Store {
	t.Helper()
	prefix, offset := allocScope(t)
	return storetest.Scope(etcdstore.New(client), prefix, offset)
}

func TestServerIsEtcd36(t *testing.T) {
	if initialClusterErr != nil {
		t.Fatal(initialClusterErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	versions, err := cluster.Versions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != etcdtest.Members {
		t.Fatalf("got %d member versions, want %d", len(versions), etcdtest.Members)
	}
	for i, version := range versions {
		if !strings.HasPrefix(version, "3.6.") {
			t.Fatalf("member %d version = %s, want 3.6.x", i, version)
		}
	}
}

func TestContract(t *testing.T) {
	storetest.Run(t, isolated)

	t.Run("IsolatedStoresDoNotLeakCompaction", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		first := isolated(t)
		if _, _, err := first.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: "a", Value: []byte("1")}}); err != nil {
			t.Fatal(err)
		}
		_, r2, err := first.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: "a", Value: []byte("2")}})
		if err != nil {
			t.Fatal(err)
		}
		if err := first.Compact(ctx, r2); err != nil {
			t.Fatalf("Compact(%d): %v", r2, err)
		}

		second := isolated(t)
		if rev, err := second.CurrentRev(ctx); err != nil || rev != 1 {
			t.Fatalf("second CurrentRev = (%d, %v), want (1, nil)", rev, err)
		}
		if _, rev, err := second.Get(ctx, "a", 1); !errors.Is(err, store.ErrNotFound) || rev != 1 {
			t.Fatalf("second Get(atRev=1) = (rev=%d, %v), want (1, ErrNotFound)", rev, err)
		}
		watchCtx, watchCancel := context.WithCancel(ctx)
		defer watchCancel()
		ws, err := second.Watch(watchCtx, "", 1)
		if err != nil {
			t.Fatalf("second Watch(fromRev=1): %v", err)
		}
		if events, progress, err := storetest.Sync(ctx, second, ws); err != nil || len(events) != 0 || progress != 1 {
			t.Fatalf("second Sync(fromRev=1) = (%v, %d, %v), want ([], 1, nil)", events, progress, err)
		}
	})
}

func TestLinearizability(t *testing.T) {
	storetest.RunLinearizability(t, isolated)
}

type pausedFollower struct {
	follower int
	pinned   store.Store
	quorum   store.Store
	paused   bool
}

func newEndpointClient(t *testing.T, endpoints ...string) *clientv3.Client {
	t.Helper()
	c, err := clientv3.New(clientv3.Config{
		Endpoints:   endpoints,
		DialTimeout: 5 * time.Second,
		Logger:      zap.NewNop(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func splitStores(t *testing.T, member int) (pinned, majority store.Store) {
	t.Helper()
	pinnedClient := newEndpointClient(t, cluster.Endpoints[member])
	var majorityEndpoints []string
	for i, ep := range cluster.Endpoints {
		if i != member {
			majorityEndpoints = append(majorityEndpoints, ep)
		}
	}
	majorityClient := newEndpointClient(t, majorityEndpoints...)
	return etcdstore.New(pinnedClient), etcdstore.New(majorityClient)
}

func pauseFollower(t *testing.T, ctx context.Context) *pausedFollower {
	t.Helper()
	leader, err := waitLeader(ctx, cluster)
	if err != nil {
		t.Fatal(err)
	}
	follower := (leader + 1) % etcdtest.Members

	pinned, quorum := splitStores(t, follower)
	if _, err := pinned.CurrentRev(ctx); err != nil {
		t.Fatalf("warm pinned follower connection: %v", err)
	}

	// Pause one follower so its local MVCC lags behind the quorum; a missing quorum read only shows on a lagging follower.
	if err := cluster.PauseMember(follower); err != nil {
		t.Fatal(err)
	}
	pf := &pausedFollower{
		follower: follower,
		pinned:   pinned,
		quorum:   quorum,
		paused:   true,
	}
	t.Cleanup(func() { _ = pf.resume() })
	return pf
}

func (pf *pausedFollower) resume() error {
	if !pf.paused {
		return nil
	}
	if err := cluster.ResumeMember(pf.follower); err != nil {
		return err
	}
	pf.paused = false
	return nil
}

func TestCompareOnlyTxnIsLinearizable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pf := pauseFollower(t, ctx)
	const key = "/nayutar-test/compare-only/k"

	var r store.Rev
	for i := range 200 {
		ok, rev, err := pf.quorum.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: key, Value: []byte(fmt.Sprint(i))}})
		if err != nil || !ok {
			t.Fatalf("write %d: Txn = (%v, %d, %v), want (true, _, nil)", i, ok, rev, err)
		}
		r = rev
	}

	type txnResult struct {
		ok  bool
		rev store.Rev
		err error
	}
	resCh := make(chan txnResult, 1)
	go func() {
		ok, rev, err := pf.pinned.Txn(ctx, []store.Cmp{{Key: key, Op: store.CmpEqual, ModRev: r}}, nil)
		resCh <- txnResult{ok: ok, rev: rev, err: err}
	}()

	if err := sleepContext(ctx, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := pf.resume(); err != nil {
		t.Fatalf("resume follower %d: %v", pf.follower, err)
	}

	res := <-resCh
	if res.err != nil || !res.ok || res.rev != r {
		t.Fatalf("compare-only Txn on lagging follower = (%v, %d, %v), want (true, %d, nil)", res.ok, res.rev, res.err, r)
	}

	if _, err := cluster.Leader(ctx); err != nil {
		t.Fatalf("Leader after resume: %v", err)
	}
	if rev, err := pf.pinned.CurrentRev(ctx); err != nil || rev < r {
		t.Fatalf("pinned CurrentRev after resume = (%d, %v), want >= %d", rev, err, r)
	}
}

func TestWatchFromNowStartsAfterTheCall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pf := pauseFollower(t, ctx)
	const prefix = "/nayutar-test/watch-from-now/"
	const key = prefix + "k"

	for i := range 200 {
		ok, rev, err := pf.quorum.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: key, Value: []byte(fmt.Sprint(i))}})
		if err != nil || !ok {
			t.Fatalf("write %d: Txn = (%v, %d, %v), want (true, _, nil)", i, ok, rev, err)
		}
	}

	watchCtx, watchCancel := context.WithCancel(ctx)
	defer watchCancel()
	type watchResult struct {
		ws  store.WatchStream
		err error
	}
	wsCh := make(chan watchResult, 1)
	go func() {
		ws, err := pf.pinned.Watch(watchCtx, prefix, 0)
		wsCh <- watchResult{ws: ws, err: err}
	}()

	if err := sleepContext(ctx, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := pf.resume(); err != nil {
		t.Fatalf("resume follower %d: %v", pf.follower, err)
	}

	wres := <-wsCh
	if wres.err != nil {
		t.Fatalf("Watch(fromRev=0): %v", wres.err)
	}

	ok, r, err := pf.quorum.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: key, Value: []byte("after")}})
	if err != nil || !ok {
		t.Fatalf("post-watch Txn = (%v, %d, %v), want (true, _, nil)", ok, r, err)
	}

	events, progress, err := storetest.Sync(ctx, pf.pinned, wres.ws)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if progress < r {
		t.Fatalf("Sync progress = %d, want >= %d", progress, r)
	}
	if len(events) != 1 || events[0].Type != store.EventPut || events[0].Key != key || string(events[0].Value) != "after" || events[0].Rev != r {
		t.Fatalf("Watch(fromRev=0) delivered %+v, want single put of %q=%q at rev %d", events, key, "after", r)
	}

	if _, err := cluster.Leader(ctx); err != nil {
		t.Fatalf("Leader after resume: %v", err)
	}
	if rev, err := pf.pinned.CurrentRev(ctx); err != nil || rev < r {
		t.Fatalf("pinned CurrentRev after resume = (%d, %v), want >= %d", rev, err, r)
	}
}

func TestWatchBelowCompactionEndsCompactedOnALaggingFollower(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pf := pauseFollower(t, ctx)
	const prefix = "/nayutar-test/watch-compacted/"
	const key = prefix + "k"

	var first, last store.Rev
	for i := range 50 {
		ok, rev, err := pf.quorum.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: key, Value: []byte(fmt.Sprint(i))}})
		if err != nil || !ok {
			t.Fatalf("write %d: Txn = (%v, %d, %v), want (true, _, nil)", i, ok, rev, err)
		}
		if i == 0 {
			first = rev
		}
		last = rev
	}
	if err := pf.quorum.Compact(ctx, last); err != nil {
		t.Fatalf("Compact(%d): %v", last, err)
	}

	type watchResult struct {
		ws  store.WatchStream
		err error
	}
	wsCh := make(chan watchResult, 1)
	go func() {
		ws, err := pf.pinned.Watch(ctx, prefix, first)
		wsCh <- watchResult{ws: ws, err: err}
	}()

	if err := sleepContext(ctx, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := pf.resume(); err != nil {
		t.Fatalf("resume follower %d: %v", pf.follower, err)
	}

	wres := <-wsCh
	if wres.err != nil {
		t.Fatalf("Watch(fromRev=%d): %v", first, wres.err)
	}
	select {
	case ev, open := <-wres.ws.Events():
		if open {
			t.Fatalf("Watch(fromRev=%d) delivered %+v, want closed channel", first, ev)
		}
	case <-ctx.Done():
		t.Fatalf("Watch(fromRev=%d) did not close Events: %v", first, ctx.Err())
	}
	if err := wres.ws.Err(); !errors.Is(err, store.ErrCompacted) {
		t.Fatalf("Watch(fromRev=%d).Err() = %v, want ErrCompacted", first, err)
	}

	if _, err := cluster.Leader(ctx); err != nil {
		t.Fatalf("Leader after resume: %v", err)
	}
	if rev, err := pf.pinned.CurrentRev(ctx); err != nil || rev < last {
		t.Fatalf("pinned CurrentRev after resume = (%d, %v), want >= %d", rev, err, last)
	}
}

func TestWatchFromNowAnswersProgressOnAnIdleStore(t *testing.T) {
	s := isolated(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cur, err := s.CurrentRev(ctx)
	if err != nil {
		t.Fatal(err)
	}
	watchCtx, watchCancel := context.WithCancel(ctx)
	defer watchCancel()
	ws, err := s.Watch(watchCtx, "", 0)
	if err != nil {
		t.Fatalf("Watch(fromRev=0): %v", err)
	}
	events, progress, err := storetest.Sync(ctx, s, ws)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(events) != 0 || progress != cur {
		t.Fatalf("Sync on idle store = (%d events, progress=%d), want (0, %d)", len(events), progress, cur)
	}
}

// The contract tests reach etcd through a key prefix, so the empty prefix and
// all-0xff prefixes are checked here on the cluster's whole key space.
func TestEmptyPrefixCoversEveryKey(t *testing.T) {
	s := etcdstore.New(client)
	low, high := "\x01empty-prefix", "\xffempty-prefix"
	ff1, ff2 := "\xff\xff", "\xff\xff/a"
	t.Cleanup(func() {
		cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanCancel()
		if ok, _, err := s.Txn(cleanCtx, nil, []store.Op{
			{Type: store.OpDelete, Key: low},
			{Type: store.OpDelete, Key: high},
			{Type: store.OpDelete, Key: ff1},
			{Type: store.OpDelete, Key: ff2},
		}); err != nil || !ok {
			t.Errorf("cleanup keys: Txn = (%v, _, %v)", ok, err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ws, err := s.Watch(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	ops := []store.Op{{Type: store.OpPut, Key: high, Value: []byte("h")}, {Type: store.OpPut, Key: low, Value: []byte("l")}}
	ok, rev, err := s.Txn(ctx, nil, ops)
	if err != nil || !ok {
		t.Fatalf("Txn = %v, %v", ok, err)
	}

	all, readRev, next, err := s.Range(ctx, "", 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if readRev != rev || next != "" || len(all) < 2 || all[0].Key != low || all[len(all)-1].Key != high {
		t.Fatalf("Range over the empty prefix = %d keys, revision %d, continuation %q", len(all), readRev, next)
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].Key >= all[i].Key {
			t.Fatalf("keys out of order: %q before %q", all[i-1].Key, all[i].Key)
		}
	}
	var paged []store.KV
	for cont, pages := "", 0; ; pages++ {
		page, _, more, err := s.Range(ctx, "", rev, 1, cont)
		if err != nil {
			t.Fatal(err)
		}
		paged = append(paged, page...)
		if more == "" {
			break
		}
		if pages > len(all) {
			t.Fatal("pagination over the empty prefix does not end")
		}
		cont = more
	}
	if len(paged) != len(all) {
		t.Fatalf("pagination returned %d keys, want %d", len(paged), len(all))
	}

	events, progress, err := storetest.Sync(ctx, s, ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Key != high || events[1].Key != low || events[0].Rev != rev || progress != rev {
		t.Fatalf("watch on the empty prefix delivered %+v with progress %d, want both keys at revision %d", events, progress, rev)
	}

	ffWatchCtx, ffCancel := context.WithCancel(ctx)
	defer ffCancel()
	ffWatch, err := s.Watch(ffWatchCtx, "\xff\xff", 0)
	if err != nil {
		t.Fatal(err)
	}
	ok, ffRev, err := s.Txn(ctx, nil, []store.Op{
		{Type: store.OpPut, Key: ff1, Value: []byte("1")},
		{Type: store.OpPut, Key: ff2, Value: []byte("2")},
	})
	if err != nil || !ok {
		t.Fatalf("Txn on 0xff prefix = %v, %v", ok, err)
	}
	ffAll, _, ffNext, err := s.Range(ctx, "\xff", ffRev, 0, "")
	if err != nil || ffNext != "" || len(ffAll) != 3 || ffAll[0].Key != high || ffAll[1].Key != ff1 || ffAll[2].Key != ff2 {
		t.Fatalf("Range(\\xff) = %+v, next=%q, err=%v", ffAll, ffNext, err)
	}
	ffSub, _, ffSubNext, err := s.Range(ctx, "\xff\xff", ffRev, 1, "")
	if err != nil || len(ffSub) != 1 || ffSub[0].Key != ff1 || ffSubNext != ff1 {
		t.Fatalf("Range(\\xff\\xff, limit=1) = %+v, next=%q, err=%v", ffSub, ffSubNext, err)
	}
	ffRest, _, ffRestNext, err := s.Range(ctx, "\xff\xff", ffRev, 1, ffSubNext)
	if err != nil || len(ffRest) != 1 || ffRest[0].Key != ff2 || ffRestNext != "" {
		t.Fatalf("Range(\\xff\\xff, cont) = %+v, next=%q, err=%v", ffRest, ffRestNext, err)
	}
	ffEvents, ffProg, err := storetest.Sync(ctx, s, ffWatch)
	if err != nil || len(ffEvents) != 2 || ffEvents[0].Key != ff1 || ffEvents[1].Key != ff2 || ffProg != ffRev {
		t.Fatalf("Watch(\\xff\\xff) = %+v, progress=%d, err=%v", ffEvents, ffProg, err)
	}
}
