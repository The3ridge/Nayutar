package liveness_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Kaikei-e/Nayutar/core/liveness"
	"github.com/Kaikei-e/Nayutar/core/liveness/detector"
	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
	"github.com/Kaikei-e/Nayutar/core/store/memstore"
)

func TestRegistry(t *testing.T) {
	checkRegistryCore(t, func(*testing.T) store.Store {
		return memstore.New()
	})
}

func checkRegistryCore(t *testing.T, newRoot func(t *testing.T) store.Store) {
	t.Helper()

	t.Run("RegisterAndGet", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		root := newRoot(t)
		reg := liveness.NewRegistry(root)

		rec, err := reg.Register(ctx, "node-1", "rack-a", 1700000000000)
		if err != nil {
			t.Fatalf("Register: %v", err)
		}
		cur, err := root.CurrentRev(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Node != "node-1" || rec.State != detector.StatePending || rec.Since != 1700000000000 ||
			rec.Rack != "rack-a" || rec.Evictable || rec.ModRev != cur {
			t.Fatalf("Register returned %+v, want Pending at ModRev %d", rec, cur)
		}

		got, err := reg.Get(ctx, "node-1")
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got != rec {
			t.Fatalf("Get = %+v, want %+v", got, rec)
		}

		if _, err := reg.Register(ctx, "node-1", "rack-a", 1700000001000); !errors.Is(err, liveness.ErrExists) {
			t.Fatalf("duplicate Register = %v, want ErrExists", err)
		}
	})

	t.Run("ListCountsAndOrder", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		root := newRoot(t)
		reg := liveness.NewRegistry(root)

		base, err := root.CurrentRev(ctx)
		if err != nil {
			t.Fatal(err)
		}
		recs, rev, err := reg.List(ctx)
		if err != nil || len(recs) != 0 || rev != base {
			t.Fatalf("List(0) = (%+v, %d, %v), want ([], %d, nil)", recs, rev, err, base)
		}

		if _, err := reg.Register(ctx, "n-000", "r1", 1000); err != nil {
			t.Fatalf("Register(n-000): %v", err)
		}
		cur, err := root.CurrentRev(ctx)
		if err != nil {
			t.Fatal(err)
		}
		recs, rev, err = reg.List(ctx)
		if err != nil || len(recs) != 1 || rev != cur || recs[0].Node != "n-000" {
			t.Fatalf("List(1) = (%+v, %d, %v), want 1 record at rev %d", recs, rev, err, cur)
		}

		const total = 150
		for i := total - 1; i >= 1; i-- {
			name := fmt.Sprintf("n-%03d", i)
			if _, err := reg.Register(ctx, name, "r1", int64(1000+i)); err != nil {
				t.Fatalf("Register(%s): %v", name, err)
			}
		}
		cur, err = root.CurrentRev(ctx)
		if err != nil {
			t.Fatal(err)
		}
		recs, rev, err = reg.List(ctx)
		if err != nil || len(recs) != total || rev != cur {
			t.Fatalf("List(150) = (len=%d, rev=%d, err=%v), want len=%d rev=%d", len(recs), rev, err, total, cur)
		}
		for i := range total {
			want := fmt.Sprintf("n-%03d", i)
			if recs[i].Node != want {
				t.Fatalf("List[%d].Node = %q, want %q", i, recs[i].Node, want)
			}
		}
	})

	t.Run("SetReadyAndSetNotReady", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		root := newRoot(t)
		reg := liveness.NewRegistry(root)

		rec, err := reg.Register(ctx, "n1", "rack-1", 1000)
		if err != nil {
			t.Fatal(err)
		}
		revBefore, err := root.CurrentRev(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if out, err := reg.SetNotReady(ctx, rec, 2000); err != nil || out != liveness.Stale {
			t.Fatalf("SetNotReady(Pending) = (%v, %v), want (Stale, nil)", out, err)
		}
		if revAfter, err := root.CurrentRev(ctx); err != nil || revAfter != revBefore {
			t.Fatalf("rev changed on Stale SetNotReady: %d -> %d", revBefore, revAfter)
		}

		if out, err := reg.SetReady(ctx, rec, 2000); err != nil || out != liveness.Committed {
			t.Fatalf("SetReady(Pending) = (%v, %v), want (Committed, nil)", out, err)
		}
		readyRec, err := reg.Get(ctx, "n1")
		if err != nil {
			t.Fatal(err)
		}
		if readyRec.State != detector.StateReady || readyRec.Since != 2000 || readyRec.Rack != "rack-1" || readyRec.Evictable || readyRec.ModRev <= rec.ModRev {
			t.Fatalf("after SetReady = %+v", readyRec)
		}

		// Stale when already Ready.
		revBefore = readyRec.ModRev
		if out, err := reg.SetReady(ctx, readyRec, 3000); err != nil || out != liveness.Stale {
			t.Fatalf("SetReady(Ready) = (%v, %v), want (Stale, nil)", out, err)
		}
		if revAfter, err := root.CurrentRev(ctx); err != nil || revAfter != revBefore {
			t.Fatalf("rev changed on Stale SetReady: %d -> %d", revBefore, revAfter)
		}

		// Conflict with old ModRev.
		if out, err := reg.SetNotReady(ctx, rec, 3000); err != nil || out != liveness.Stale {
			t.Fatalf("SetNotReady(old pending) = (%v, %v), want (Stale, nil)", out, err)
		}
		staleReady := readyRec
		staleReady.ModRev--
		if out, err := reg.SetNotReady(ctx, staleReady, 3000); err != nil || out != liveness.Conflict {
			t.Fatalf("SetNotReady(conflict) = (%v, %v), want (Conflict, nil)", out, err)
		}
		if revAfter, err := root.CurrentRev(ctx); err != nil || revAfter != revBefore {
			t.Fatalf("rev changed on Conflict SetNotReady: %d -> %d", revBefore, revAfter)
		}

		// Ready -> NotReady.
		if out, err := reg.SetNotReady(ctx, readyRec, 4000); err != nil || out != liveness.Committed {
			t.Fatalf("SetNotReady(Ready) = (%v, %v), want (Committed, nil)", out, err)
		}
		nrRec, err := reg.Get(ctx, "n1")
		if err != nil {
			t.Fatal(err)
		}
		if nrRec.State != detector.StateNotReady || nrRec.Since != 4000 || nrRec.Rack != "rack-1" || nrRec.Evictable || nrRec.ModRev <= readyRec.ModRev {
			t.Fatalf("after SetNotReady = %+v", nrRec)
		}

		// Stale SetNotReady on NotReady.
		if out, err := reg.SetNotReady(ctx, nrRec, 5000); err != nil || out != liveness.Stale {
			t.Fatalf("SetNotReady(NotReady) = (%v, %v), want (Stale, nil)", out, err)
		}

		// Conflict SetReady with old ModRev.
		if out, err := reg.SetReady(ctx, rec, 5000); err != nil || out != liveness.Conflict {
			t.Fatalf("SetReady(conflict) = (%v, %v), want (Conflict, nil)", out, err)
		}
		if revAfter, err := root.CurrentRev(ctx); err != nil || revAfter != nrRec.ModRev {
			t.Fatalf("rev changed on Conflict SetReady: %d -> %d", nrRec.ModRev, revAfter)
		}

		// Grant eviction then SetReady clears Evictable.
		if out, err := reg.GrantEviction(ctx, nrRec, 5000, 10); err != nil || out != liveness.Committed {
			t.Fatalf("GrantEviction = (%v, %v), want (Committed, nil)", out, err)
		}
		evRec, err := reg.Get(ctx, "n1")
		if err != nil {
			t.Fatal(err)
		}
		if !evRec.Evictable {
			t.Fatalf("expected Evictable=true, got %+v", evRec)
		}
		if out, err := reg.SetReady(ctx, evRec, 6000); err != nil || out != liveness.Committed {
			t.Fatalf("SetReady(NotReady+Evictable) = (%v, %v), want (Committed, nil)", out, err)
		}
		recovered, err := reg.Get(ctx, "n1")
		if err != nil {
			t.Fatal(err)
		}
		if recovered.State != detector.StateReady || recovered.Since != 6000 || recovered.Evictable || recovered.Rack != "rack-1" {
			t.Fatalf("recovered = %+v, want Ready Evictable=false Since=6000", recovered)
		}
	})

	t.Run("GrantEvictionAndBudget", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		root := newRoot(t)
		reg := liveness.NewRegistry(root)

		pRec, err := reg.Register(ctx, "n-pending", "r1", 1000)
		if err != nil {
			t.Fatal(err)
		}
		if out, err := reg.GrantEviction(ctx, pRec, 2000, 10); err != nil || out != liveness.Stale {
			t.Fatalf("GrantEviction(Pending) = (%v, %v), want (Stale, nil)", out, err)
		}

		rRec, err := reg.Register(ctx, "n-ready", "r1", 1000)
		if err != nil {
			t.Fatal(err)
		}
		if out, err := reg.SetReady(ctx, rRec, 1500); err != nil || out != liveness.Committed {
			t.Fatalf("SetReady: (%v, %v)", out, err)
		}
		rRec, err = reg.Get(ctx, "n-ready")
		if err != nil {
			t.Fatal(err)
		}
		if out, err := reg.GrantEviction(ctx, rRec, 2000, 10); err != nil || out != liveness.Stale {
			t.Fatalf("GrantEviction(Ready) = (%v, %v), want (Stale, nil)", out, err)
		}

		// Two NotReady nodes; totalNodes=10 gives limit=1.
		n1, err := reg.Register(ctx, "n1", "r1", 1000)
		if err != nil {
			t.Fatal(err)
		}
		if out, err := reg.SetReady(ctx, n1, 1500); err != nil || out != liveness.Committed {
			t.Fatal(out, err)
		}
		n1, _ = reg.Get(ctx, "n1")
		if out, err := reg.SetNotReady(ctx, n1, 2000); err != nil || out != liveness.Committed {
			t.Fatal(out, err)
		}
		n1, _ = reg.Get(ctx, "n1")

		n2, err := reg.Register(ctx, "n2", "r1", 1000)
		if err != nil {
			t.Fatal(err)
		}
		if out, err := reg.SetReady(ctx, n2, 1500); err != nil || out != liveness.Committed {
			t.Fatal(out, err)
		}
		n2, _ = reg.Get(ctx, "n2")
		if out, err := reg.SetNotReady(ctx, n2, 2000); err != nil || out != liveness.Committed {
			t.Fatal(out, err)
		}
		n2, _ = reg.Get(ctx, "n2")

		// Grant n1 at wall=10000.
		if out, err := reg.GrantEviction(ctx, n1, 10000, 10); err != nil || out != liveness.Committed {
			t.Fatalf("GrantEviction(n1) = (%v, %v), want (Committed, nil)", out, err)
		}
		n1After, err := reg.Get(ctx, "n1")
		if err != nil {
			t.Fatal(err)
		}
		if !n1After.Evictable || n1After.State != detector.StateNotReady || n1After.Since != 2000 || n1After.Rack != "r1" {
			t.Fatalf("n1 after grant = %+v", n1After)
		}
		bKV, _, err := root.Get(ctx, keyspace.EvictionBudgetKey, 0)
		if err != nil {
			t.Fatalf("Get budget: %v", err)
		}
		if n1After.ModRev != bKV.ModRev {
			t.Fatalf("n1 ModRev=%d != budget ModRev=%d", n1After.ModRev, bKV.ModRev)
		}
		if got := decodeTestBudget(t, bKV.Value); len(got) != 1 || got[0] != 10000 {
			t.Fatalf("budget = %v, want [10000]", got)
		}

		// Stale when already evictable.
		if out, err := reg.GrantEviction(ctx, n1After, 11000, 10); err != nil || out != liveness.Stale {
			t.Fatalf("GrantEviction(already evictable) = (%v, %v), want (Stale, nil)", out, err)
		}

		// OverBudget when window is full (limit=1, wall=69999 -> cutoff=9999, 10000 kept).
		revBefore := bKV.ModRev
		if out, err := reg.GrantEviction(ctx, n2, 69999, 10); err != nil || out != liveness.OverBudget {
			t.Fatalf("GrantEviction(over budget) = (%v, %v), want (OverBudget, nil)", out, err)
		}
		// Slow wall clock behind last stamp (wall=5000 < 10000) is also evaluated at 10000 -> OverBudget.
		if out, err := reg.GrantEviction(ctx, n2, 5000, 10); err != nil || out != liveness.OverBudget {
			t.Fatalf("GrantEviction(slow wall over budget) = (%v, %v), want (OverBudget, nil)", out, err)
		}
		if revAfter, err := root.CurrentRev(ctx); err != nil || revAfter != revBefore {
			t.Fatalf("rev changed on OverBudget: %d -> %d", revBefore, revAfter)
		}

		// Conflict when record changed since it was read (n1 has old ModRev).
		if out, err := reg.GrantEviction(ctx, n1, 70000, 10); err != nil || out != liveness.Conflict {
			t.Fatalf("GrantEviction(record conflict) = (%v, %v), want (Conflict, nil)", out, err)
		}

		// Pruning: at wall=70000, cutoff = 70000 - 60000 = 10000, so entry 10000 is pruned.
		if out, err := reg.GrantEviction(ctx, n2, 70000, 10); err != nil || out != liveness.Committed {
			t.Fatalf("GrantEviction(n2 after prune) = (%v, %v), want (Committed, nil)", out, err)
		}
		bKV, _, err = root.Get(ctx, keyspace.EvictionBudgetKey, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := decodeTestBudget(t, bKV.Value); len(got) != 1 || got[0] != 70000 {
			t.Fatalf("budget after prune = %v, want [70000]", got)
		}

		// Slow wall clock when budget has room (totalNodes=200 -> limit=2) clamps recorded stamp to 70000.
		n3, err := reg.Register(ctx, "n3", "r1", 1000)
		if err != nil {
			t.Fatal(err)
		}
		if out, err := reg.SetReady(ctx, n3, 1500); err != nil || out != liveness.Committed {
			t.Fatal(out, err)
		}
		n3, _ = reg.Get(ctx, "n3")
		if out, err := reg.SetNotReady(ctx, n3, 2000); err != nil || out != liveness.Committed {
			t.Fatal(out, err)
		}
		n3, _ = reg.Get(ctx, "n3")
		if out, err := reg.GrantEviction(ctx, n3, 5000, 200); err != nil || out != liveness.Committed {
			t.Fatalf("GrantEviction(n3 slow wall) = (%v, %v), want (Committed, nil)", out, err)
		}
		bKV, _, err = root.Get(ctx, keyspace.EvictionBudgetKey, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := decodeTestBudget(t, bKV.Value); len(got) != 2 || got[0] != 70000 || got[1] != 70000 {
			t.Fatalf("budget after slow wall grant = %v, want [70000 70000]", got)
		}

		// Wall clock ahead of last stamp records the ahead wall timestamp and keeps list non-decreasing.
		n4, err := reg.Register(ctx, "n4", "r1", 1000)
		if err != nil {
			t.Fatal(err)
		}
		if out, err := reg.SetReady(ctx, n4, 1500); err != nil || out != liveness.Committed {
			t.Fatal(out, err)
		}
		n4, _ = reg.Get(ctx, "n4")
		if out, err := reg.SetNotReady(ctx, n4, 2000); err != nil || out != liveness.Committed {
			t.Fatal(out, err)
		}
		n4, _ = reg.Get(ctx, "n4")
		if out, err := reg.GrantEviction(ctx, n4, 75000, 300); err != nil || out != liveness.Committed {
			t.Fatalf("GrantEviction(n4 ahead wall) = (%v, %v), want (Committed, nil)", out, err)
		}
		bKV, _, err = root.Get(ctx, keyspace.EvictionBudgetKey, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := decodeTestBudget(t, bKV.Value); len(got) != 3 || got[0] != 70000 || got[1] != 70000 || got[2] != 75000 {
			t.Fatalf("budget after ahead wall grant = %v, want [70000 70000 75000]", got)
		}
	})

	t.Run("GrantEvictionConcurrency", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		root := newRoot(t)
		reg := liveness.NewRegistry(root)

		const n = 16
		recs := make([]liveness.Record, n)
		for i := range n {
			name := fmt.Sprintf("node-%02d", i)
			r, err := reg.Register(ctx, name, "r1", 1000)
			if err != nil {
				t.Fatal(err)
			}
			if out, err := reg.SetReady(ctx, r, 2000); err != nil || out != liveness.Committed {
				t.Fatalf("SetReady(%s): (%v, %v)", name, out, err)
			}
			r, err = reg.Get(ctx, name)
			if err != nil {
				t.Fatal(err)
			}
			if out, err := reg.SetNotReady(ctx, r, 3000); err != nil || out != liveness.Committed {
				t.Fatalf("SetNotReady(%s): (%v, %v)", name, out, err)
			}
			recs[i], err = reg.Get(ctx, name)
			if err != nil {
				t.Fatal(err)
			}
		}

		outcomes := make([]liveness.Outcome, n)
		errs := make([]error, n)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				<-start
				outcomes[idx], errs[idx] = reg.GrantEviction(ctx, recs[idx], 100000, 200)
			}(i)
		}
		close(start)
		wg.Wait()

		var committed int
		for i := range n {
			if errs[i] != nil {
				t.Fatalf("goroutine %d err = %v", i, errs[i])
			}
			switch outcomes[i] {
			case liveness.Committed:
				committed++
			case liveness.OverBudget, liveness.Conflict:
			default:
				t.Fatalf("goroutine %d unexpected outcome %v", i, outcomes[i])
			}
		}
		if committed < 1 || committed > 2 {
			t.Fatalf("first wave committed=%d, want in [1, 2]", committed)
		}

		bKV, _, err := root.Get(ctx, keyspace.EvictionBudgetKey, 0)
		if err != nil {
			t.Fatalf("Get budget: %v", err)
		}
		if granted := decodeTestBudget(t, bKV.Value); len(granted) != committed {
			t.Fatalf("first wave budget len = %d (%v), want %d", len(granted), granted, committed)
		}
		all, _, err := reg.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		evictableCount := 0
		for _, rec := range all {
			if rec.Evictable {
				evictableCount++
			}
		}
		if evictableCount != committed {
			t.Fatalf("first wave evictableCount = %d, want %d", evictableCount, committed)
		}

		// Sequentially retry every Conflict node until no call returns Conflict.
		for i := range n {
			if outcomes[i] != liveness.Conflict {
				continue
			}
			for {
				rec, err := reg.Get(ctx, recs[i].Node)
				if err != nil {
					t.Fatalf("Get(%s) for retry: %v", recs[i].Node, err)
				}
				out, err := reg.GrantEviction(ctx, rec, 100000, 200)
				if err != nil {
					t.Fatalf("retry GrantEviction(%s): %v", recs[i].Node, err)
				}
				if out == liveness.Conflict {
					continue
				}
				switch out {
				case liveness.Committed:
					if committed >= 2 {
						t.Fatalf("retried node %s committed past limit 2", recs[i].Node)
					}
					committed++
				case liveness.OverBudget:
				default:
					t.Fatalf("retry GrantEviction(%s) unexpected outcome %v", recs[i].Node, out)
				}
				break
			}
		}

		if committed != 2 {
			t.Fatalf("final committed = %d, want 2", committed)
		}
		bKV, _, err = root.Get(ctx, keyspace.EvictionBudgetKey, 0)
		if err != nil {
			t.Fatalf("Get final budget: %v", err)
		}
		if granted := decodeTestBudget(t, bKV.Value); len(granted) != 2 {
			t.Fatalf("final budget len = %d (%v), want 2", len(granted), granted)
		}
		all, _, err = reg.List(ctx)
		if err != nil {
			t.Fatal(err)
		}
		evictableCount = 0
		for _, rec := range all {
			if rec.Evictable {
				evictableCount++
			}
		}
		if evictableCount != 2 {
			t.Fatalf("final evictableCount = %d, want 2", evictableCount)
		}
	})
}

func decodeTestBudget(t *testing.T, raw []byte) []int64 {
	t.Helper()
	var doc struct {
		Lkal    string  `json:"$lkal"`
		Granted []int64 `json:"granted"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal budget %q: %v", raw, err)
	}
	return doc.Granted
}

type hookTxnStore struct {
	store.Store
	onTxn func(cmps []store.Cmp, ops []store.Op)
}

func (h *hookTxnStore) Txn(ctx context.Context, cmps []store.Cmp, ops []store.Op) (bool, store.Rev, error) {
	if h.onTxn != nil {
		h.onTxn(cmps, ops)
	}
	return h.Store.Txn(ctx, cmps, ops)
}

type hookRangeStore struct {
	store.Store
	onRange func(prefix string, atRev store.Rev, limit int, cont string, kvs []store.KV, rev store.Rev, next string)
}

func (h *hookRangeStore) Range(ctx context.Context, prefix string, atRev store.Rev, limit int, cont string) ([]store.KV, store.Rev, string, error) {
	kvs, rev, next, err := h.Store.Range(ctx, prefix, atRev, limit, cont)
	if err == nil && h.onRange != nil {
		h.onRange(prefix, atRev, limit, cont, kvs, rev, next)
	}
	return kvs, rev, next, err
}

type failTxnStore struct {
	store.Store
	err error
}

func (f *failTxnStore) Txn(ctx context.Context, cmps []store.Cmp, ops []store.Op) (bool, store.Rev, error) {
	return false, 0, f.err
}

type noStoreCall struct {
	store.Store
	t *testing.T
}

func (n *noStoreCall) Get(ctx context.Context, key string, atRev store.Rev) (store.KV, store.Rev, error) {
	n.t.Helper()
	n.t.Fatalf("unexpected store.Get(%q)", key)
	return store.KV{}, 0, errors.New("unexpected store call")
}

func (n *noStoreCall) Txn(ctx context.Context, cmps []store.Cmp, ops []store.Op) (bool, store.Rev, error) {
	n.t.Helper()
	n.t.Fatal("unexpected store.Txn")
	return false, 0, errors.New("unexpected store call")
}

func TestRegistryValidationAndEdgeCases(t *testing.T) {
	t.Run("ValidationBeforeWrite", func(t *testing.T) {
		ctx := context.Background()
		root := memstore.New()
		reg := liveness.NewRegistry(root)
		base, err := root.CurrentRev(ctx)
		if err != nil {
			t.Fatal(err)
		}

		badNodes := []string{"", "BAD", "a/b", "-a", "a-", strings.Repeat("a", 254)}
		for _, node := range badNodes {
			if _, err := reg.Register(ctx, node, "r1", 1000); !errors.Is(err, keyspace.ErrInvalid) {
				t.Errorf("Register(%q) = %v, want keyspace.ErrInvalid", node, err)
			}
			if _, err := reg.Get(ctx, node); !errors.Is(err, keyspace.ErrInvalid) {
				t.Errorf("Get(%q) = %v, want keyspace.ErrInvalid", node, err)
			}
			if err := reg.Delete(ctx, node); !errors.Is(err, keyspace.ErrInvalid) {
				t.Errorf("Delete(%q) = %v, want keyspace.ErrInvalid", node, err)
			}
		}

		badRacks := []string{"", "R1", "-r", "r-", "r.1", "r_1", strings.Repeat("a", 64)}
		for _, rack := range badRacks {
			if _, err := reg.Register(ctx, "n1", rack, 1000); !errors.Is(err, liveness.ErrInvalidRack) {
				t.Errorf("Register(rack=%q) = %v, want ErrInvalidRack", rack, err)
			}
		}

		if cur, err := root.CurrentRev(ctx); err != nil || cur != base {
			t.Fatalf("store rev changed on invalid input: %d -> %d", base, cur)
		}
	})

	t.Run("InvalidWallTimeBeforeStore", func(t *testing.T) {
		ctx := context.Background()
		root := memstore.New()
		reg := liveness.NewRegistry(root)

		validPending, err := reg.Register(ctx, "n-pend", "r1", 0)
		if err != nil {
			t.Fatalf("Register(0): %v", err)
		}
		const maxWall = (int64(1) << 53) - 1
		if _, err := reg.Register(ctx, "n-max", "r1", maxWall); err != nil {
			t.Fatalf("Register(maxWall): %v", err)
		}
		if out, err := reg.SetReady(ctx, validPending, maxWall); err != nil || out != liveness.Committed {
			t.Fatalf("SetReady(maxWall) = (%v, %v), want Committed", out, err)
		}
		readyRec, err := reg.Get(ctx, "n-pend")
		if err != nil {
			t.Fatal(err)
		}
		if out, err := reg.SetNotReady(ctx, readyRec, maxWall); err != nil || out != liveness.Committed {
			t.Fatalf("SetNotReady(maxWall) = (%v, %v), want Committed", out, err)
		}
		notReadyRec, err := reg.Get(ctx, "n-pend")
		if err != nil {
			t.Fatal(err)
		}

		// Wrap root with a store that fails on any call so we prove ErrInvalidTime never touches the store.
		untouched := liveness.NewRegistry(&failTxnStore{Store: &noStoreCall{t: t}, err: errors.New("should not touch store")})
		for _, badWall := range []int64{-1, -1000, int64(1) << 53, (int64(1) << 53) + 1} {
			if _, err := untouched.Register(ctx, "n-bad", "r1", badWall); !errors.Is(err, liveness.ErrInvalidTime) {
				t.Errorf("Register(wall=%d) = %v, want ErrInvalidTime", badWall, err)
			}
			if _, err := untouched.SetReady(ctx, validPending, badWall); !errors.Is(err, liveness.ErrInvalidTime) {
				t.Errorf("SetReady(wall=%d) = %v, want ErrInvalidTime", badWall, err)
			}
			if _, err := untouched.SetNotReady(ctx, readyRec, badWall); !errors.Is(err, liveness.ErrInvalidTime) {
				t.Errorf("SetNotReady(wall=%d) = %v, want ErrInvalidTime", badWall, err)
			}
			if _, err := untouched.GrantEviction(ctx, notReadyRec, badWall, 10); !errors.Is(err, liveness.ErrInvalidTime) {
				t.Errorf("GrantEviction(wall=%d) = %v, want ErrInvalidTime", badWall, err)
			}
		}
	})

	t.Run("GetMissingAndCorrupt", func(t *testing.T) {
		ctx := context.Background()
		root := memstore.New()
		reg := liveness.NewRegistry(root)

		if _, err := reg.Get(ctx, "missing"); !errors.Is(err, liveness.ErrNotRegistered) {
			t.Fatalf("Get(missing) = %v, want ErrNotRegistered", err)
		}
		if ok, _, err := root.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: "/sys/nodes/bad", Value: []byte("corrupt")}}); err != nil || !ok {
			t.Fatal(ok, err)
		}
		if _, err := reg.Get(ctx, "bad"); !errors.Is(err, liveness.ErrCorruptRecord) {
			t.Fatalf("Get(bad) = %v, want ErrCorruptRecord", err)
		}
		if _, _, err := reg.List(ctx); !errors.Is(err, liveness.ErrCorruptRecord) {
			t.Fatalf("List with corrupt value = %v, want ErrCorruptRecord", err)
		}
	})

	t.Run("ListForeignKeyUnderNodesPrefix", func(t *testing.T) {
		for _, badKey := range []string{"/sys/nodes/BAD", "/sys/nodes/a/b"} {
			ctx := context.Background()
			root := memstore.New()
			reg := liveness.NewRegistry(root)
			val := []byte(`{"$lkal":"0.1","evictable":false,"rack":"r1","since":0,"state":{"$enum":"pending"}}`)
			if ok, _, err := root.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: badKey, Value: val}}); err != nil || !ok {
				t.Fatal(ok, err)
			}
			if _, _, err := reg.List(ctx); !errors.Is(err, liveness.ErrCorruptRecord) {
				t.Fatalf("List(%q) = %v, want ErrCorruptRecord", badKey, err)
			}
		}
	})

	t.Run("ListSnapshotConsistency", func(t *testing.T) {
		ctx := context.Background()
		root := memstore.New()
		reg := liveness.NewRegistry(root)
		const total = 70
		for i := range total {
			if _, err := reg.Register(ctx, fmt.Sprintf("n-%03d", i), "r1", 1000); err != nil {
				t.Fatal(err)
			}
		}

		var fired bool
		var page1Rev store.Rev
		hooked := &hookRangeStore{
			Store: root,
			onRange: func(prefix string, _ store.Rev, _ int, _ string, _ []store.KV, rev store.Rev, next string) {
				if fired || prefix != keyspace.NodesPrefix || next == "" {
					return
				}
				fired = true
				page1Rev = rev
				if err := reg.Delete(ctx, "n-000"); err != nil {
					t.Errorf("concurrent Delete: %v", err)
				}
				if _, err := reg.Register(ctx, "zz-new", "r1", 2000); err != nil {
					t.Errorf("concurrent Register: %v", err)
				}
			},
		}

		recs, rev, err := liveness.NewRegistry(hooked).List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if !fired || rev != page1Rev || len(recs) != total {
			t.Fatalf("fired=%v rev=%d page1Rev=%d len=%d", fired, rev, page1Rev, len(recs))
		}
		if recs[0].Node != "n-000" || recs[len(recs)-1].Node != "n-069" {
			t.Fatalf("List boundary = (%s, %s), want (n-000, n-069)", recs[0].Node, recs[len(recs)-1].Node)
		}
	})

	t.Run("DeleteAndRetryOnCASFailure", func(t *testing.T) {
		ctx := context.Background()
		root := memstore.New()
		reg := liveness.NewRegistry(root)

		if err := reg.Delete(ctx, "missing"); !errors.Is(err, liveness.ErrNotRegistered) {
			t.Fatalf("Delete(missing) = %v, want ErrNotRegistered", err)
		}

		rec, err := reg.Register(ctx, "n1", "r1", 1000)
		if err != nil {
			t.Fatal(err)
		}
		var attempts atomic.Int32
		hooked := &hookTxnStore{
			Store: root,
			onTxn: func(_ []store.Cmp, ops []store.Op) {
				if len(ops) == 1 && ops[0].Type == store.OpDelete && attempts.Add(1) == 1 {
					if out, err := reg.SetReady(ctx, rec, 2000); err != nil || out != liveness.Committed {
						t.Errorf("concurrent SetReady = (%v, %v)", out, err)
					}
				}
			},
		}
		if err := liveness.NewRegistry(hooked).Delete(ctx, "n1"); err != nil {
			t.Fatalf("Delete with CAS retry: %v", err)
		}
		if attempts.Load() != 2 {
			t.Fatalf("Delete attempts = %d, want 2", attempts.Load())
		}
		if _, err := reg.Get(ctx, "n1"); !errors.Is(err, liveness.ErrNotRegistered) {
			t.Fatalf("Get after Delete = %v, want ErrNotRegistered", err)
		}
	})

	t.Run("GrantEvictionBudgetConflict", func(t *testing.T) {
		ctx := context.Background()
		root := memstore.New()
		reg := liveness.NewRegistry(root)

		rec, err := reg.Register(ctx, "n1", "r1", 1000)
		if err != nil {
			t.Fatal(err)
		}
		if out, err := reg.SetReady(ctx, rec, 2000); err != nil || out != liveness.Committed {
			t.Fatal(out, err)
		}
		rec, _ = reg.Get(ctx, "n1")
		if out, err := reg.SetNotReady(ctx, rec, 3000); err != nil || out != liveness.Committed {
			t.Fatal(out, err)
		}
		rec, _ = reg.Get(ctx, "n1")

		var fired bool
		concurrentVal := []byte(`{"$lkal":"0.1","granted":[5000]}`)
		hooked := &hookTxnStore{
			Store: root,
			onTxn: func(_ []store.Cmp, ops []store.Op) {
				if fired {
					return
				}
				for _, op := range ops {
					if op.Key == keyspace.EvictionBudgetKey {
						fired = true
						if ok, _, err := root.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: keyspace.EvictionBudgetKey, Value: concurrentVal}}); err != nil || !ok {
							t.Errorf("concurrent budget write = (%v, %v)", ok, err)
						}
						return
					}
				}
			},
		}

		out, err := liveness.NewRegistry(hooked).GrantEviction(ctx, rec, 10000, 100)
		if err != nil || out != liveness.Conflict {
			t.Fatalf("GrantEviction(budget conflict) = (%v, %v), want (Conflict, nil)", out, err)
		}
		if !fired {
			t.Fatal("expected budget conflict hook to fire")
		}
		after, err := reg.Get(ctx, "n1")
		if err != nil || after.Evictable {
			t.Fatalf("n1 after budget conflict = (%+v, %v), want Evictable=false", after, err)
		}
		bKV, _, err := root.Get(ctx, keyspace.EvictionBudgetKey, 0)
		if err != nil || string(bKV.Value) != string(concurrentVal) {
			t.Fatalf("budget = (%s, %v), want %s", bKV.Value, err, concurrentVal)
		}
	})

	t.Run("TxnErrorPropagated", func(t *testing.T) {
		ctx := context.Background()
		root := memstore.New()
		reg := liveness.NewRegistry(root)

		nPend, err := reg.Register(ctx, "n-pend", "r1", 1000)
		if err != nil {
			t.Fatal(err)
		}
		nReady, err := reg.Register(ctx, "n-ready", "r1", 1000)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := reg.SetReady(ctx, nReady, 2000); err != nil {
			t.Fatal(err)
		}
		nReady, _ = reg.Get(ctx, "n-ready")

		nNotReady, err := reg.Register(ctx, "n-nr", "r1", 1000)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := reg.SetReady(ctx, nNotReady, 2000); err != nil {
			t.Fatal(err)
		}
		nNotReady, _ = reg.Get(ctx, "n-nr")
		if _, err := reg.SetNotReady(ctx, nNotReady, 3000); err != nil {
			t.Fatal(err)
		}
		nNotReady, _ = reg.Get(ctx, "n-nr")

		wantErr := errors.New("injected store error")
		failing := liveness.NewRegistry(&failTxnStore{Store: root, err: wantErr})

		if _, err := failing.Register(ctx, "n-new", "r1", 1000); !errors.Is(err, wantErr) {
			t.Errorf("Register err = %v, want %v", err, wantErr)
		}
		if err := failing.Delete(ctx, "n-pend"); !errors.Is(err, wantErr) {
			t.Errorf("Delete err = %v, want %v", err, wantErr)
		}
		if _, err := failing.SetReady(ctx, nPend, 4000); !errors.Is(err, wantErr) {
			t.Errorf("SetReady err = %v, want %v", err, wantErr)
		}
		if _, err := failing.SetNotReady(ctx, nReady, 4000); !errors.Is(err, wantErr) {
			t.Errorf("SetNotReady err = %v, want %v", err, wantErr)
		}
		if _, err := failing.GrantEviction(ctx, nNotReady, 4000, 10); !errors.Is(err, wantErr) {
			t.Errorf("GrantEviction err = %v, want %v", err, wantErr)
		}
	})
}
