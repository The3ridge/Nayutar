package tenant_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
	"github.com/Kaikei-e/Nayutar/core/store/memstore"
	"github.com/Kaikei-e/Nayutar/core/store/storetest"
	"github.com/Kaikei-e/Nayutar/core/store/tenant"
)

func mustFencedAdapter(t *testing.T, root store.Store, name string) store.Store {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	reg := tenant.NewRegistry(root)
	if err := reg.Register(ctx, name); err != nil {
		t.Fatal(err)
	}
	base, err := root.CurrentRev(ctx)
	if err != nil {
		t.Fatal(err)
	}
	view, err := reg.View(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	return storetest.Scope(storeAdapter{Store: view, root: root}, "", base-1)
}

func TestRegistry(t *testing.T) {
	checkRegistry(t, func(*testing.T) store.Store {
		return memstore.New()
	})
}

func TestFencedContract(t *testing.T) {
	storetest.RunSynctest(t, func(t *testing.T) store.Store {
		return mustFencedAdapter(t, memstore.New(), "acme")
	})
}

func TestFencedLinearizability(t *testing.T) {
	storetest.RunLinearizabilitySynctest(t, func(t *testing.T) store.Store {
		return mustFencedAdapter(t, memstore.New(), "acme")
	})
}

type failAfterTxnStore struct {
	store.Store
	remaining atomic.Int64
	err       error
}

func (f *failAfterTxnStore) Txn(ctx context.Context, cmps []store.Cmp, ops []store.Op) (bool, store.Rev, error) {
	if f.remaining.Add(-1) < 0 {
		return false, 0, f.err
	}
	return f.Store.Txn(ctx, cmps, ops)
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

type maxDeleteOpsStore struct {
	store.Store
	maxOps   int
	rejected atomic.Int64
}

func isTenantDeleteOps(ops []store.Op) bool {
	if len(ops) == 0 {
		return false
	}
	for _, op := range ops {
		if op.Type != store.OpDelete || !strings.HasPrefix(op.Key, "/t/") {
			return false
		}
	}
	return true
}

func (m *maxDeleteOpsStore) Txn(ctx context.Context, cmps []store.Cmp, ops []store.Op) (bool, store.Rev, error) {
	if len(ops) > m.maxOps && isTenantDeleteOps(ops) {
		m.rejected.Add(1)
		return false, 0, store.ErrTooLarge
	}
	return m.Store.Txn(ctx, cmps, ops)
}

type getBarrierStore struct {
	store.Store
	key   string
	mu    sync.Mutex
	calls int
	ready chan struct{}
}

func newGetBarrierStore(inner store.Store, key string) *getBarrierStore {
	return &getBarrierStore{
		Store: inner,
		key:   key,
		ready: make(chan struct{}),
	}
}

func (b *getBarrierStore) Get(ctx context.Context, key string, atRev store.Rev) (store.KV, store.Rev, error) {
	kv, rev, err := b.Store.Get(ctx, key, atRev)
	if key != b.key {
		return kv, rev, err
	}
	b.mu.Lock()
	b.calls++
	call := b.calls
	if call == 2 {
		close(b.ready)
	}
	b.mu.Unlock()
	if call <= 2 {
		select {
		case <-b.ready:
		case <-ctx.Done():
			return store.KV{}, 0, ctx.Err()
		}
	}
	return kv, rev, err
}

func TestDeleteStopsIfReregistered(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	root := memstore.New()
	reg := tenant.NewRegistry(root)
	if err := reg.Register(ctx, "acme"); err != nil {
		t.Fatal(err)
	}
	v, err := reg.View(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if ok, _, err := v.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: "k", Value: []byte("old")}}); err != nil || !ok {
		t.Fatalf("seed write = (%v, %v)", ok, err)
	}

	var replaced bool
	hooked := &hookTxnStore{
		Store: root,
		onTxn: func(_ []store.Cmp, ops []store.Op) {
			if replaced || len(ops) == 0 || ops[0].Type != store.OpDelete || !strings.HasPrefix(ops[0].Key, "/t/acme/") {
				return
			}
			replaced = true
			// Finish deleting the old registration and register anew before the drain Txn runs.
			if err := reg.Delete(ctx, "acme"); err != nil {
				t.Errorf("concurrent Delete: %v", err)
			}
			if err := reg.Register(ctx, "acme"); err != nil {
				t.Errorf("concurrent Register: %v", err)
			}
			nv, err := reg.View(ctx, "acme")
			if err != nil {
				t.Errorf("concurrent View: %v", err)
				return
			}
			if ok, _, err := nv.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: "fresh", Value: []byte("new")}}); err != nil || !ok {
				t.Errorf("fresh write = (%v, %v)", ok, err)
			}
		},
	}

	if err := tenant.NewRegistry(hooked).Delete(ctx, "acme"); err != nil {
		t.Fatalf("Delete = %v, want nil", err)
	}
	rec, err := reg.Get(ctx, "acme")
	if err != nil || rec.State != tenant.StateActive {
		t.Fatalf("Get(acme) = (%+v, %v), want active", rec, err)
	}
	nv, err := reg.View(ctx, "acme")
	if err != nil {
		t.Fatal(err)
	}
	kv, _, err := nv.Get(ctx, "fresh", 0)
	if err != nil || string(kv.Value) != "new" {
		t.Fatalf("Get(fresh) = (%+v, %v), want new", kv, err)
	}
}

func TestDeleteStopsIfReregisteredBeforeFinalDelete(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	root := memstore.New()
	reg := tenant.NewRegistry(root)
	if err := reg.Register(ctx, "acme"); err != nil {
		t.Fatal(err)
	}

	var fired bool
	hooked := &hookTxnStore{
		Store: root,
		onTxn: func(_ []store.Cmp, ops []store.Op) {
			if fired || len(ops) != 1 || ops[0].Type != store.OpDelete || ops[0].Key != "/sys/tenants/acme" {
				return
			}
			fired = true
			if err := reg.Delete(ctx, "acme"); err != nil {
				t.Errorf("concurrent Delete: %v", err)
			}
			if err := reg.Register(ctx, "acme"); err != nil {
				t.Errorf("concurrent Register: %v", err)
			}
			nv, err := reg.View(ctx, "acme")
			if err != nil {
				t.Errorf("concurrent View: %v", err)
				return
			}
			if ok, _, err := nv.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: "fresh", Value: []byte("new")}}); err != nil || !ok {
				t.Errorf("fresh write = (%v, %v)", ok, err)
			}
		},
	}

	if err := tenant.NewRegistry(hooked).Delete(ctx, "acme"); err != nil {
		t.Fatalf("Delete = %v, want nil", err)
	}
	if !fired {
		t.Fatal("expected final delete hook to fire")
	}
	rec, err := reg.Get(ctx, "acme")
	if err != nil || rec.State != tenant.StateActive {
		t.Fatalf("Get(acme) = (%+v, %v), want StateActive", rec, err)
	}
	kv, _, err := root.Get(ctx, "/t/acme/fresh", 0)
	if err != nil || string(kv.Value) != "new" {
		t.Fatalf("root Get(/t/acme/fresh) = (%+v, %v), want new", kv, err)
	}
}

func TestDeleteStopsIfDeletedBeforeTerminatingWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	root := memstore.New()
	reg := tenant.NewRegistry(root)
	if err := reg.Register(ctx, "acme"); err != nil {
		t.Fatal(err)
	}

	var fired bool
	hooked := &hookTxnStore{
		Store: root,
		onTxn: func(_ []store.Cmp, ops []store.Op) {
			if fired || len(ops) != 1 || ops[0].Type != store.OpPut || ops[0].Key != "/sys/tenants/acme" || !strings.Contains(string(ops[0].Value), "terminating") {
				return
			}
			fired = true
			if err := reg.Delete(ctx, "acme"); err != nil {
				t.Errorf("concurrent Delete: %v", err)
			}
		},
	}
	// Reject any second Txn so a comparison-free write cannot silently recreate and delete a ghost record.
	guarded := &failAfterTxnStore{Store: hooked, err: errors.New("unexpected txn after concurrent delete")}
	guarded.remaining.Store(1)

	if err := tenant.NewRegistry(guarded).Delete(ctx, "acme"); err != nil {
		t.Fatalf("Delete = %v, want nil", err)
	}
	if !fired {
		t.Fatal("expected terminating write hook to fire")
	}
	if _, err := reg.Get(ctx, "acme"); !errors.Is(err, tenant.ErrNotRegistered) {
		t.Fatalf("Get(acme) = %v, want ErrNotRegistered", err)
	}
	if err := reg.Register(ctx, "acme"); err != nil {
		t.Fatalf("Register(acme) after concurrent Delete = %v, want nil", err)
	}
}

func TestDeleteStopsIfReregisteredBeforeTerminatingWrite(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	root := memstore.New()
	reg := tenant.NewRegistry(root)
	if err := reg.Register(ctx, "acme"); err != nil {
		t.Fatal(err)
	}

	var fired bool
	hooked := &hookTxnStore{
		Store: root,
		onTxn: func(_ []store.Cmp, ops []store.Op) {
			if fired || len(ops) != 1 || ops[0].Type != store.OpPut || ops[0].Key != "/sys/tenants/acme" || !strings.Contains(string(ops[0].Value), "terminating") {
				return
			}
			fired = true
			if err := reg.Delete(ctx, "acme"); err != nil {
				t.Errorf("concurrent Delete: %v", err)
			}
			if err := reg.Register(ctx, "acme"); err != nil {
				t.Errorf("concurrent Register: %v", err)
			}
			nv, err := reg.View(ctx, "acme")
			if err != nil {
				t.Errorf("concurrent View: %v", err)
				return
			}
			if ok, _, err := nv.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: "fresh", Value: []byte("new")}}); err != nil || !ok {
				t.Errorf("fresh write = (%v, %v)", ok, err)
			}
		},
	}

	if err := tenant.NewRegistry(hooked).Delete(ctx, "acme"); err != nil {
		t.Fatalf("Delete = %v, want nil", err)
	}
	if !fired {
		t.Fatal("expected terminating write hook to fire")
	}
	rec, err := reg.Get(ctx, "acme")
	if err != nil || rec.State != tenant.StateActive {
		t.Fatalf("Get(acme) = (%+v, %v), want StateActive", rec, err)
	}
	kv, _, err := root.Get(ctx, "/t/acme/fresh", 0)
	if err != nil || string(kv.Value) != "new" {
		t.Fatalf("root Get(/t/acme/fresh) = (%+v, %v), want new", kv, err)
	}
}

func seedTenantKeys(t *testing.T, ctx context.Context, reg *tenant.Registry, name string, count int) {
	t.Helper()
	v, err := reg.View(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	for start := 0; start < count; start += 50 {
		batch := min(50, count-start)
		ops := make([]store.Op, batch)
		for j := range batch {
			ops[j] = store.Op{Type: store.OpPut, Key: fmt.Sprintf("k/%03d", start+j), Value: []byte("v")}
		}
		if ok, _, err := v.Txn(ctx, nil, ops); err != nil || !ok {
			t.Fatalf("seed put = (%v, %v)", ok, err)
		}
	}
}

func TestDeleteLeavesReregistrationAloneAtEveryStep(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const totalKeys = 150
	var numTxns int
	{
		root := memstore.New()
		reg := tenant.NewRegistry(root)
		if err := reg.Register(ctx, "acme"); err != nil {
			t.Fatal(err)
		}
		seedTenantKeys(t, ctx, reg, "acme", totalKeys)
		counter := &hookTxnStore{
			Store: root,
			onTxn: func([]store.Cmp, []store.Op) {
				numTxns++
			},
		}
		if err := tenant.NewRegistry(counter).Delete(ctx, "acme"); err != nil {
			t.Fatalf("counting Delete = %v", err)
		}
		if numTxns < 4 {
			t.Fatalf("expected multi-page Delete to issue at least 4 Txns, got %d", numTxns)
		}
	}

	for k := 1; k <= numTxns; k++ {
		t.Run(fmt.Sprintf("%d", k), func(t *testing.T) {
			root := memstore.New()
			reg := tenant.NewRegistry(root)
			if err := reg.Register(ctx, "acme"); err != nil {
				t.Fatal(err)
			}
			seedTenantKeys(t, ctx, reg, "acme", totalKeys)

			var seen int
			var fired bool
			hooked := &hookTxnStore{
				Store: root,
				onTxn: func([]store.Cmp, []store.Op) {
					seen++
					if seen != k {
						return
					}
					fired = true
					if err := reg.Delete(ctx, "acme"); err != nil {
						t.Errorf("concurrent Delete: %v", err)
					}
					if err := reg.Register(ctx, "acme"); err != nil {
						t.Errorf("concurrent Register: %v", err)
					}
					nv, err := reg.View(ctx, "acme")
					if err != nil {
						t.Errorf("concurrent View: %v", err)
						return
					}
					if ok, _, err := nv.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: "fresh", Value: []byte("new")}}); err != nil || !ok {
						t.Errorf("fresh write = (%v, %v)", ok, err)
					}
				},
			}

			if err := tenant.NewRegistry(hooked).Delete(ctx, "acme"); err != nil {
				t.Fatalf("Delete = %v, want nil", err)
			}
			if !fired {
				t.Fatalf("expected hook to fire at Txn %d", k)
			}
			rec, err := reg.Get(ctx, "acme")
			if err != nil || rec.State != tenant.StateActive {
				t.Fatalf("Get(acme) = (%+v, %v), want StateActive", rec, err)
			}
			kvs, _, _, err := root.Range(ctx, "/t/acme/", 0, 0, "")
			if err != nil {
				t.Fatalf("root Range(/t/acme/) = %v", err)
			}
			if len(kvs) != 1 || kvs[0].Key != "/t/acme/fresh" || string(kvs[0].Value) != "new" {
				t.Fatalf("root Range(/t/acme/) = %+v, want only /t/acme/fresh=new", kvs)
			}
		})
	}
}

func TestDeleteFinishesTerminatingRegistrationLeftByAnotherCaller(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	root := memstore.New()
	reg := tenant.NewRegistry(root)
	if err := reg.Register(ctx, "acme"); err != nil {
		t.Fatal(err)
	}

	var fired bool
	hooked := &hookTxnStore{
		Store: root,
		onTxn: func(_ []store.Cmp, ops []store.Op) {
			if fired || len(ops) != 1 || ops[0].Type != store.OpPut || ops[0].Key != "/sys/tenants/acme" || !strings.Contains(string(ops[0].Value), "terminating") {
				return
			}
			fired = true
			if err := reg.Delete(ctx, "acme"); err != nil {
				t.Errorf("concurrent Delete: %v", err)
			}
			if err := reg.Register(ctx, "acme"); err != nil {
				t.Errorf("concurrent Register: %v", err)
			}
			nv, err := reg.View(ctx, "acme")
			if err != nil {
				t.Errorf("concurrent View: %v", err)
				return
			}
			if ok, _, err := nv.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: "k", Value: []byte("v")}}); err != nil || !ok {
				t.Errorf("new write = (%v, %v)", ok, err)
			}
			rec, err := reg.Get(ctx, "acme")
			if err != nil {
				t.Errorf("concurrent Get: %v", err)
				return
			}
			ok, _, err := root.Txn(ctx,
				[]store.Cmp{{Key: "/sys/tenants/acme", Op: store.CmpEqual, ModRev: rec.ModRev}},
				[]store.Op{{Type: store.OpPut, Key: "/sys/tenants/acme", Value: []byte(`{"$lkal":"0.1","state":{"$enum":"terminating"}}`)}},
			)
			if err != nil || !ok {
				t.Errorf("mark new registration terminating = (%v, %v)", ok, err)
			}
		},
	}

	if err := tenant.NewRegistry(hooked).Delete(ctx, "acme"); err != nil {
		t.Fatalf("Delete = %v, want nil", err)
	}
	if !fired {
		t.Fatal("expected terminating write hook to fire")
	}
	if _, err := reg.Get(ctx, "acme"); !errors.Is(err, tenant.ErrNotRegistered) {
		t.Fatalf("Get(acme) = %v, want ErrNotRegistered", err)
	}
	if kvs, _, _, err := root.Range(ctx, "/t/acme/", 0, 0, ""); err != nil || len(kvs) != 0 {
		t.Fatalf("root Range(/t/acme/) = (%+v, %v), want empty", kvs, err)
	}
}

func TestListSnapshotConsistency(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	root := memstore.New()
	reg := tenant.NewRegistry(root)

	const total = 70
	for i := range total {
		name := fmt.Sprintf("t-%03d", i)
		if err := reg.Register(ctx, name); err != nil {
			t.Fatalf("Register(%q): %v", name, err)
		}
	}

	var fired bool
	var page1Rev store.Rev
	hooked := &hookRangeStore{
		Store: root,
		onRange: func(prefix string, _ store.Rev, _ int, _ string, _ []store.KV, rev store.Rev, next string) {
			if fired || prefix != keyspace.TenantsPrefix || next == "" {
				return
			}
			fired = true
			page1Rev = rev
			if ok, _, err := root.Txn(ctx, nil, []store.Op{{Type: store.OpDelete, Key: "/sys/tenants/t-000"}}); err != nil || !ok {
				t.Errorf("concurrent delete t-000 = (%v, %v)", ok, err)
			}
			if err := reg.Register(ctx, "zz"); err != nil {
				t.Errorf("concurrent Register(zz): %v", err)
			}
		},
	}

	recs, rev, err := tenant.NewRegistry(hooked).List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !fired {
		t.Fatal("expected first List page hook to fire")
	}
	if rev != page1Rev {
		t.Fatalf("List rev = %d, want first page rev %d", rev, page1Rev)
	}
	wantKVs, _, _, err := root.Range(ctx, keyspace.TenantsPrefix, rev, 0, "")
	if err != nil {
		t.Fatalf("root Range at rev %d: %v", rev, err)
	}
	if len(recs) != len(wantKVs) || len(recs) != total {
		t.Fatalf("List len = %d, root Range len = %d, want %d", len(recs), len(wantKVs), total)
	}
	for i, kv := range wantKVs {
		parsed, err := keyspace.Parse(kv.Key)
		if err != nil {
			t.Fatalf("Parse(%q): %v", kv.Key, err)
		}
		if recs[i].Name != parsed.Tenant || recs[i].ModRev != kv.ModRev || recs[i].State != tenant.StateActive {
			t.Fatalf("List[%d] = %+v, want {Name:%s State:Active ModRev:%d}", i, recs[i], parsed.Tenant, kv.ModRev)
		}
	}
	if recs[0].Name != "t-000" || recs[len(recs)-1].Name != "t-069" {
		t.Fatalf("List boundary = (%s, %s), want (t-000, t-069)", recs[0].Name, recs[len(recs)-1].Name)
	}
}

func checkRegistry(t *testing.T, newRoot func(t *testing.T) store.Store) {
	t.Helper()

	t.Run("RegisterGetList", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		root := newRoot(t)
		reg := tenant.NewRegistry(root)

		base, err := root.CurrentRev(ctx)
		if err != nil {
			t.Fatal(err)
		}
		recs, rev, err := reg.List(ctx)
		if err != nil || len(recs) != 0 || rev != base {
			t.Fatalf("empty List = (%+v, %d, %v), want ([], %d, nil)", recs, rev, err, base)
		}

		for _, name := range []string{"charlie", "alpha", "bravo"} {
			if err := reg.Register(ctx, name); err != nil {
				t.Fatalf("Register(%q): %v", name, err)
			}
		}

		wantOrder := []string{"alpha", "bravo", "charlie"}
		wantRevs := map[string]store.Rev{}
		for _, name := range wantOrder {
			rec, err := reg.Get(ctx, name)
			if err != nil || rec.Name != name || rec.State != tenant.StateActive || rec.ModRev <= base {
				t.Fatalf("Get(%q) = (%+v, %v)", name, rec, err)
			}
			wantRevs[name] = rec.ModRev
		}

		cur, err := root.CurrentRev(ctx)
		if err != nil {
			t.Fatal(err)
		}
		recs, rev, err = reg.List(ctx)
		if err != nil || rev != cur || len(recs) != len(wantOrder) {
			t.Fatalf("List = (%+v, %d, %v), want %d records at rev %d", recs, rev, err, len(wantOrder), cur)
		}
		for i, name := range wantOrder {
			if recs[i].Name != name || recs[i].State != tenant.StateActive || recs[i].ModRev != wantRevs[name] {
				t.Fatalf("List[%d] = %+v, want {Name:%s State:Active ModRev:%d}", i, recs[i], name, wantRevs[name])
			}
		}
	})

	t.Run("ListPagination", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		root := newRoot(t)
		reg := tenant.NewRegistry(root)

		const total = 130
		for i := total - 1; i >= 0; i-- {
			name := fmt.Sprintf("t-%03d", i)
			if err := reg.Register(ctx, name); err != nil {
				t.Fatalf("Register(%q): %v", name, err)
			}
		}
		cur, err := root.CurrentRev(ctx)
		if err != nil {
			t.Fatal(err)
		}
		recs, rev, err := reg.List(ctx)
		if err != nil || rev != cur || len(recs) != total {
			t.Fatalf("List = (len=%d, rev=%d, err=%v), want len=%d rev=%d", len(recs), rev, err, total, cur)
		}
		for i := range total {
			want := fmt.Sprintf("t-%03d", i)
			if recs[i].Name != want || recs[i].State != tenant.StateActive {
				t.Fatalf("List[%d] = %+v, want %s active", i, recs[i], want)
			}
		}
	})

	t.Run("DuplicateRegister", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		reg := tenant.NewRegistry(newRoot(t))
		if err := reg.Register(ctx, "acme"); err != nil {
			t.Fatal(err)
		}
		if err := reg.Register(ctx, "acme"); !errors.Is(err, tenant.ErrExists) {
			t.Fatalf("duplicate Register = %v, want ErrExists", err)
		}
	})

	t.Run("InvalidNames", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		reg := tenant.NewRegistry(newRoot(t))
		for _, bad := range []string{"", "-", "A", "a_b", "a/b", "a.b", "-a", "a-", strings.Repeat("a", 64)} {
			if err := reg.Register(ctx, bad); !errors.Is(err, tenant.ErrInvalidName) {
				t.Errorf("Register(%q) = %v, want ErrInvalidName", bad, err)
			}
			if _, err := reg.Get(ctx, bad); !errors.Is(err, tenant.ErrInvalidName) {
				t.Errorf("Get(%q) = %v, want ErrInvalidName", bad, err)
			}
			if err := reg.Delete(ctx, bad); !errors.Is(err, tenant.ErrInvalidName) {
				t.Errorf("Delete(%q) = %v, want ErrInvalidName", bad, err)
			}
			if _, err := reg.View(ctx, bad); !errors.Is(err, tenant.ErrInvalidName) {
				t.Errorf("View(%q) = %v, want ErrInvalidName", bad, err)
			}
		}
	})

	t.Run("Unregistered", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		reg := tenant.NewRegistry(newRoot(t))
		if _, err := reg.Get(ctx, "missing"); !errors.Is(err, tenant.ErrNotRegistered) {
			t.Fatalf("Get(missing) = %v, want ErrNotRegistered", err)
		}
		if _, err := reg.View(ctx, "missing"); !errors.Is(err, tenant.ErrNotRegistered) {
			t.Fatalf("View(missing) = %v, want ErrNotRegistered", err)
		}
		if err := reg.Delete(ctx, "missing"); !errors.Is(err, tenant.ErrNotRegistered) {
			t.Fatalf("Delete(missing) = %v, want ErrNotRegistered", err)
		}
	})

	t.Run("FencedViewConfinementAndCallerCmp", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		root := newRoot(t)
		reg := tenant.NewRegistry(root)
		if err := reg.Register(ctx, "a"); err != nil {
			t.Fatal(err)
		}
		if err := reg.Register(ctx, "b"); err != nil {
			t.Fatal(err)
		}
		va, err := reg.View(ctx, "a")
		if err != nil {
			t.Fatal(err)
		}
		if ok, _, err := root.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: "/t/b/k1", Value: []byte("other")}}); err != nil || !ok {
			t.Fatalf("root put b = (%v, %v)", ok, err)
		}

		ok, r1, err := va.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: "k1", Value: []byte("v1")}})
		if err != nil || !ok {
			t.Fatalf("va.Txn put = (%v, %d, %v)", ok, r1, err)
		}
		kvs, _, _, err := root.Range(ctx, "/t/a/", 0, 0, "")
		if err != nil || len(kvs) != 1 || kvs[0].Key != "/t/a/k1" || string(kvs[0].Value) != "v1" {
			t.Fatalf("root Range(/t/a/) = (%+v, %v)", kvs, err)
		}
		if kvs, _, _, err := va.Range(ctx, "", 0, 0, ""); err != nil || len(kvs) != 1 || kvs[0].Key != "k1" {
			t.Fatalf("va.Range = (%+v, %v)", kvs, err)
		}

		ok, rev, err := va.Txn(ctx,
			[]store.Cmp{{Key: "k1", Op: store.CmpEqual, ModRev: 0}},
			[]store.Op{{Type: store.OpPut, Key: "k1", Value: []byte("bad")}, {Type: store.OpPut, Key: "k2", Value: []byte("bad")}},
		)
		if err != nil || ok || rev != r1 {
			t.Fatalf("failed caller Cmp Txn = (%v, %d, %v), want (false, %d, nil)", ok, rev, err, r1)
		}
		kv, _, err := va.Get(ctx, "k1", 0)
		if err != nil || string(kv.Value) != "v1" {
			t.Fatalf("va.Get(k1) after failed Cmp = (%+v, %v)", kv, err)
		}
		if _, _, err := va.Get(ctx, "k2", 0); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("va.Get(k2) after failed Cmp = %v, want ErrNotFound", err)
		}
	})

	t.Run("DeleteDrainsPrefixesAndFences", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		root := newRoot(t)
		reg := tenant.NewRegistry(root)
		for _, name := range []string{"a", "a-b", "ab"} {
			if err := reg.Register(ctx, name); err != nil {
				t.Fatalf("Register(%q): %v", name, err)
			}
		}
		va, err := reg.View(ctx, "a")
		if err != nil {
			t.Fatal(err)
		}
		vaDashB, err := reg.View(ctx, "a-b")
		if err != nil {
			t.Fatal(err)
		}
		vab, err := reg.View(ctx, "ab")
		if err != nil {
			t.Fatal(err)
		}

		for start := 0; start < 200; start += 50 {
			ops := make([]store.Op, 50)
			for j := range 50 {
				ops[j] = store.Op{Type: store.OpPut, Key: fmt.Sprintf("k/%03d", start+j), Value: []byte("v")}
			}
			if ok, _, err := va.Txn(ctx, nil, ops); err != nil || !ok {
				t.Fatalf("va batch put = (%v, %v)", ok, err)
			}
		}
		if ok, _, err := vaDashB.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: "k", Value: []byte("dash")}}); err != nil || !ok {
			t.Fatalf("a-b put = (%v, %v)", ok, err)
		}
		if ok, _, err := vab.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: "k", Value: []byte("ab")}}); err != nil || !ok {
			t.Fatalf("ab put = (%v, %v)", ok, err)
		}

		if err := reg.Delete(ctx, "a"); err != nil {
			t.Fatalf("Delete(a): %v", err)
		}
		if kvs, _, _, err := root.Range(ctx, "/t/a/", 0, 0, ""); err != nil || len(kvs) != 0 {
			t.Fatalf("root Range(/t/a/) after Delete = (%+v, %v), want empty", kvs, err)
		}
		if _, err := reg.Get(ctx, "a"); !errors.Is(err, tenant.ErrNotRegistered) {
			t.Fatalf("Get(a) after Delete = %v, want ErrNotRegistered", err)
		}

		if ok, _, err := va.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: "after", Value: []byte("bad")}}); !errors.Is(err, tenant.ErrFenced) || ok {
			t.Fatalf("old view Txn after Delete = (%v, %v), want (false, ErrFenced)", ok, err)
		}
		if ok, _, err := va.Txn(ctx, nil, nil); !errors.Is(err, tenant.ErrFenced) || ok {
			t.Fatalf("old view empty Txn after Delete = (%v, %v), want (false, ErrFenced)", ok, err)
		}
		if kvs, _, _, err := root.Range(ctx, "/t/a/", 0, 0, ""); err != nil || len(kvs) != 0 {
			t.Fatalf("root Range(/t/a/) after fenced Txn = (%+v, %v), want empty", kvs, err)
		}

		for _, tc := range []struct {
			name string
			v    tenant.Store
			want string
		}{
			{"a-b", vaDashB, "dash"},
			{"ab", vab, "ab"},
		} {
			if rec, err := reg.Get(ctx, tc.name); err != nil || rec.State != tenant.StateActive {
				t.Fatalf("Get(%q) = (%+v, %v), want active", tc.name, rec, err)
			}
			kv, _, err := tc.v.Get(ctx, "k", 0)
			if err != nil || string(kv.Value) != tc.want {
				t.Fatalf("%s Get(k) = (%+v, %v), want %q", tc.name, kv, err, tc.want)
			}
			if ok, _, err := tc.v.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: "k2", Value: []byte("ok")}}); err != nil || !ok {
				t.Fatalf("%s Txn = (%v, %v)", tc.name, ok, err)
			}
		}
	})

	t.Run("ReregisterAfterDelete", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		root := newRoot(t)
		reg := tenant.NewRegistry(root)
		if err := reg.Register(ctx, "acme"); err != nil {
			t.Fatal(err)
		}
		v1, err := reg.View(ctx, "acme")
		if err != nil {
			t.Fatal(err)
		}
		if ok, _, err := v1.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: "k", Value: []byte("v1")}}); err != nil || !ok {
			t.Fatalf("v1 put = (%v, %v)", ok, err)
		}
		if err := reg.Delete(ctx, "acme"); err != nil {
			t.Fatal(err)
		}
		if err := reg.Register(ctx, "acme"); err != nil {
			t.Fatal(err)
		}

		if ok, _, err := v1.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: "k", Value: []byte("stale")}}); !errors.Is(err, tenant.ErrFenced) || ok {
			t.Fatalf("v1 Txn after re-register = (%v, %v), want (false, ErrFenced)", ok, err)
		}
		v2, err := reg.View(ctx, "acme")
		if err != nil {
			t.Fatal(err)
		}
		if kvs, _, _, err := v2.Range(ctx, "", 0, 0, ""); err != nil || len(kvs) != 0 {
			t.Fatalf("v2 Range = (%+v, %v), want empty", kvs, err)
		}
		if ok, _, err := v2.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: "k", Value: []byte("v2")}}); err != nil || !ok {
			t.Fatalf("v2 put = (%v, %v)", ok, err)
		}
		kv, _, err := v2.Get(ctx, "k", 0)
		if err != nil || string(kv.Value) != "v2" {
			t.Fatalf("v2 Get(k) = (%+v, %v), want v2", kv, err)
		}
	})

	t.Run("TerminatingAndResumeDelete", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		root := newRoot(t)
		reg := tenant.NewRegistry(root)
		if err := reg.Register(ctx, "acme"); err != nil {
			t.Fatal(err)
		}
		vOld, err := reg.View(ctx, "acme")
		if err != nil {
			t.Fatal(err)
		}
		for start := 0; start < 150; start += 50 {
			ops := make([]store.Op, 50)
			for j := range 50 {
				ops[j] = store.Op{Type: store.OpPut, Key: fmt.Sprintf("k/%03d", start+j), Value: []byte("v")}
			}
			if ok, _, err := vOld.Txn(ctx, nil, ops); err != nil || !ok {
				t.Fatalf("seed put = (%v, %v)", ok, err)
			}
		}

		injected := errors.New("injected txn failure")
		failing := &failAfterTxnStore{Store: root, err: injected}
		failing.remaining.Store(1) // allow transition to terminating, fail first drain batch
		if err := tenant.NewRegistry(failing).Delete(ctx, "acme"); !errors.Is(err, injected) {
			t.Fatalf("interrupted Delete = %v, want injected error", err)
		}

		rec, err := reg.Get(ctx, "acme")
		if err != nil || rec.State != tenant.StateTerminating {
			t.Fatalf("Get(acme) while terminating = (%+v, %v), want StateTerminating", rec, err)
		}
		if err := reg.Register(ctx, "acme"); !errors.Is(err, tenant.ErrTerminating) {
			t.Fatalf("Register while terminating = %v, want ErrTerminating", err)
		}
		if _, err := reg.View(ctx, "acme"); !errors.Is(err, tenant.ErrTerminating) {
			t.Fatalf("View while terminating = %v, want ErrTerminating", err)
		}
		if ok, _, err := vOld.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: "x", Value: []byte("y")}}); !errors.Is(err, tenant.ErrFenced) || ok {
			t.Fatalf("old view Txn while terminating = (%v, %v), want (false, ErrFenced)", ok, err)
		}

		if err := reg.Delete(ctx, "acme"); err != nil {
			t.Fatalf("resumed Delete = %v, want nil", err)
		}
		if kvs, _, _, err := root.Range(ctx, "/t/acme/", 0, 0, ""); err != nil || len(kvs) != 0 {
			t.Fatalf("root Range(/t/acme/) after resumed Delete = (%+v, %v), want empty", kvs, err)
		}
		if _, err := reg.Get(ctx, "acme"); !errors.Is(err, tenant.ErrNotRegistered) {
			t.Fatalf("Get(acme) after resumed Delete = %v, want ErrNotRegistered", err)
		}
	})

	t.Run("CorruptRecords", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		root := newRoot(t)
		reg := tenant.NewRegistry(root)

		badValues := []string{
			"not json",
			"{}",
			`{"$lkal":"0.2","state":{"$enum":"active"}}`,
			`{"$lkal":"0.1","state":{"$enum":"other"}}`,
			`{"$lkal":"0.1","$lkal":"0.1","state":{"$enum":"active"}}`,
			`{"$lkal":"0.1","state":{"$enum":"active"}} trailing`,
			`{"$lkal":"0.1","state":null}`,
			`{"$lkal":"0.1","state":{"$enum":"active"},"extra":1}`,
			`{"$lkal":"0.1","state":{"$enum":"active","extra":1}}`,
			`null`,
			``,
		}
		for i, raw := range badValues {
			if ok, _, err := root.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: "/sys/tenants/bad", Value: []byte(raw)}}); err != nil || !ok {
				t.Fatalf("case %d root put = (%v, %v)", i, ok, err)
			}
			if _, err := reg.Get(ctx, "bad"); !errors.Is(err, tenant.ErrCorruptRecord) {
				t.Errorf("case %d Get = %v, want ErrCorruptRecord", i, err)
			}
			if _, err := reg.View(ctx, "bad"); !errors.Is(err, tenant.ErrCorruptRecord) {
				t.Errorf("case %d View = %v, want ErrCorruptRecord", i, err)
			}
			if err := reg.Delete(ctx, "bad"); !errors.Is(err, tenant.ErrCorruptRecord) {
				t.Errorf("case %d Delete = %v, want ErrCorruptRecord", i, err)
			}
			if err := reg.Register(ctx, "bad"); !errors.Is(err, tenant.ErrCorruptRecord) {
				t.Errorf("case %d Register = %v, want ErrCorruptRecord", i, err)
			}
			if _, _, err := reg.List(ctx); !errors.Is(err, tenant.ErrCorruptRecord) {
				t.Errorf("case %d List = %v, want ErrCorruptRecord", i, err)
			}
		}

		if ok, _, err := root.Txn(ctx, nil, []store.Op{
			{Type: store.OpDelete, Key: "/sys/tenants/bad"},
			{Type: store.OpPut, Key: "/sys/tenants/BAD/extra", Value: []byte(`{"$lkal":"0.1","state":{"$enum":"active"}}`)},
		}); err != nil || !ok {
			t.Fatalf("put invalid key = (%v, %v)", ok, err)
		}
		if _, _, err := reg.List(ctx); !errors.Is(err, tenant.ErrCorruptRecord) {
			t.Fatalf("List with invalid key under prefix = %v, want ErrCorruptRecord", err)
		}
	})

	t.Run("ConcurrentDeletes", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		var wg sync.WaitGroup
		defer func() {
			cancel()
			wg.Wait()
		}()
		root := newRoot(t)
		reg := tenant.NewRegistry(root)
		for _, name := range []string{"a", "a-b", "ab"} {
			if err := reg.Register(ctx, name); err != nil {
				t.Fatalf("Register(%q): %v", name, err)
			}
		}
		va, err := reg.View(ctx, "a")
		if err != nil {
			t.Fatal(err)
		}
		vaDashB, err := reg.View(ctx, "a-b")
		if err != nil {
			t.Fatal(err)
		}
		vab, err := reg.View(ctx, "ab")
		if err != nil {
			t.Fatal(err)
		}
		for start := 0; start < 200; start += 50 {
			ops := make([]store.Op, 50)
			for j := range 50 {
				ops[j] = store.Op{Type: store.OpPut, Key: fmt.Sprintf("k/%03d", start+j), Value: []byte("v")}
			}
			if ok, _, err := va.Txn(ctx, nil, ops); err != nil || !ok {
				t.Fatalf("seed put = (%v, %v)", ok, err)
			}
		}
		if ok, _, err := vaDashB.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: "k", Value: []byte("dash")}}); err != nil || !ok {
			t.Fatalf("a-b put = (%v, %v)", ok, err)
		}
		if ok, _, err := vab.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: "k", Value: []byte("ab")}}); err != nil || !ok {
			t.Fatalf("ab put = (%v, %v)", ok, err)
		}

		barrier := newGetBarrierStore(root, "/sys/tenants/a")
		errs := make([]error, 2)
		for i := range 2 {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				errs[id] = tenant.NewRegistry(barrier).Delete(ctx, "a")
			}(i)
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("concurrent Delete[%d] = %v, want nil", i, err)
			}
		}
		if kvs, _, _, err := root.Range(ctx, "/t/a/", 0, 0, ""); err != nil || len(kvs) != 0 {
			t.Fatalf("root Range(/t/a/) = (%+v, %v), want empty", kvs, err)
		}
		if _, _, err := root.Get(ctx, "/sys/tenants/a", 0); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("root Get(/sys/tenants/a) = %v, want ErrNotFound", err)
		}
		if _, err := reg.Get(ctx, "a"); !errors.Is(err, tenant.ErrNotRegistered) {
			t.Fatalf("Get(a) = %v, want ErrNotRegistered", err)
		}
		for _, tc := range []struct {
			name string
			v    tenant.Store
			want string
		}{
			{"a-b", vaDashB, "dash"},
			{"ab", vab, "ab"},
		} {
			if rec, err := reg.Get(ctx, tc.name); err != nil || rec.State != tenant.StateActive {
				t.Fatalf("Get(%q) = (%+v, %v), want active", tc.name, rec, err)
			}
			kv, _, err := tc.v.Get(ctx, "k", 0)
			if err != nil || string(kv.Value) != tc.want {
				t.Fatalf("%s Get(k) = (%+v, %v), want %q", tc.name, kv, err, tc.want)
			}
			if ok, _, err := tc.v.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: "k2", Value: []byte("ok")}}); err != nil || !ok {
				t.Fatalf("%s Txn = (%v, %v)", tc.name, ok, err)
			}
		}
	})

	t.Run("SequentialDeletes", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		root := newRoot(t)
		reg := tenant.NewRegistry(root)
		if err := reg.Register(ctx, "acme"); err != nil {
			t.Fatal(err)
		}
		v, err := reg.View(ctx, "acme")
		if err != nil {
			t.Fatal(err)
		}
		if ok, _, err := v.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: "k", Value: []byte("v")}}); err != nil || !ok {
			t.Fatalf("seed put = (%v, %v)", ok, err)
		}

		if err := reg.Delete(ctx, "acme"); err != nil {
			t.Fatalf("first Delete = %v, want nil", err)
		}
		if err := reg.Delete(ctx, "acme"); !errors.Is(err, tenant.ErrNotRegistered) {
			t.Fatalf("second Delete = %v, want ErrNotRegistered", err)
		}
		if kvs, _, _, err := root.Range(ctx, "/t/acme/", 0, 0, ""); err != nil || len(kvs) != 0 {
			t.Fatalf("root Range(/t/acme/) = (%+v, %v), want empty", kvs, err)
		}
		if _, _, err := root.Get(ctx, "/sys/tenants/acme", 0); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("root Get(/sys/tenants/acme) = %v, want ErrNotFound", err)
		}
	})

	t.Run("ConcurrentWritersRaceDelete", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		var wg sync.WaitGroup
		defer func() {
			cancel()
			wg.Wait()
		}()
		root := newRoot(t)
		reg := tenant.NewRegistry(root)
		if err := reg.Register(ctx, "acme"); err != nil {
			t.Fatal(err)
		}

		const numWriters = 4
		views := make([]tenant.Store, numWriters)
		for i := range numWriters {
			v, err := reg.View(ctx, "acme")
			if err != nil {
				t.Fatal(err)
			}
			views[i] = v
		}

		writerErrs := make([]error, numWriters)
		var started sync.WaitGroup
		started.Add(numWriters)
		for id := range numWriters {
			wg.Add(1)
			go func(id int, v tenant.Store) {
				defer wg.Done()
				var once sync.Once
				defer once.Do(started.Done)
				for seq := 0; ; seq++ {
					key := fmt.Sprintf("w%d/%d", id, seq)
					ok, _, err := v.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: key, Value: []byte("v")}})
					if err != nil {
						if !errors.Is(err, tenant.ErrFenced) {
							writerErrs[id] = fmt.Errorf("writer %d seq %d unexpected error: %w", id, seq, err)
						}
						return
					}
					if !ok {
						writerErrs[id] = fmt.Errorf("writer %d seq %d Txn returned ok=false, err=nil", id, seq)
						return
					}
					if seq >= 2 {
						once.Do(started.Done)
					}
				}
			}(id, views[id])
		}

		ready := make(chan struct{})
		go func() {
			started.Wait()
			close(ready)
		}()
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}

		if err := reg.Delete(ctx, "acme"); err != nil {
			t.Fatalf("Delete(acme): %v", err)
		}
		if kvs, _, _, err := root.Range(ctx, "/t/acme/", 0, 0, ""); err != nil || len(kvs) != 0 {
			t.Fatalf("root Range(/t/acme/) right after Delete = (%+v, %v), want empty", kvs, err)
		}
		wg.Wait()
		for _, err := range writerErrs {
			if err != nil {
				t.Fatal(err)
			}
		}
		if kvs, _, _, err := root.Range(ctx, "/t/acme/", 0, 0, ""); err != nil || len(kvs) != 0 {
			t.Fatalf("root Range(/t/acme/) after writers stopped = (%+v, %v), want empty", kvs, err)
		}
	})

	t.Run("DeleteTooLargeDrain", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		root := newRoot(t)
		reg := tenant.NewRegistry(root)

		if err := reg.Register(ctx, "acme"); err != nil {
			t.Fatal(err)
		}
		va, err := reg.View(ctx, "acme")
		if err != nil {
			t.Fatal(err)
		}
		for start := 0; start < 70; start += 35 {
			ops := make([]store.Op, 35)
			for j := range 35 {
				ops[j] = store.Op{Type: store.OpPut, Key: fmt.Sprintf("k/%03d", start+j), Value: []byte("v")}
			}
			if ok, _, err := va.Txn(ctx, nil, ops); err != nil || !ok {
				t.Fatalf("seed acme put = (%v, %v)", ok, err)
			}
		}

		limited := &maxDeleteOpsStore{Store: root, maxOps: 5}
		if err := tenant.NewRegistry(limited).Delete(ctx, "acme"); err != nil {
			t.Fatalf("Delete(acme) with maxOps=5: %v", err)
		}
		if limited.rejected.Load() == 0 {
			t.Fatal("expected maxOps=5 wrapper to reject at least one drain Txn")
		}
		if kvs, _, _, err := root.Range(ctx, "/t/acme/", 0, 0, ""); err != nil || len(kvs) != 0 {
			t.Fatalf("root Range(/t/acme/) after Delete = (%+v, %v), want empty", kvs, err)
		}
		if _, err := reg.Get(ctx, "acme"); !errors.Is(err, tenant.ErrNotRegistered) {
			t.Fatalf("Get(acme) after Delete = %v, want ErrNotRegistered", err)
		}

		if err := reg.Register(ctx, "bravo"); err != nil {
			t.Fatal(err)
		}
		vb, err := reg.View(ctx, "bravo")
		if err != nil {
			t.Fatal(err)
		}
		ops := make([]store.Op, 3)
		for j := range 3 {
			ops[j] = store.Op{Type: store.OpPut, Key: fmt.Sprintf("k/%d", j), Value: []byte("v")}
		}
		if ok, _, err := vb.Txn(ctx, nil, ops); err != nil || !ok {
			t.Fatalf("seed bravo put = (%v, %v)", ok, err)
		}

		zero := &maxDeleteOpsStore{Store: root, maxOps: 0}
		if err := tenant.NewRegistry(zero).Delete(ctx, "bravo"); !errors.Is(err, store.ErrTooLarge) {
			t.Fatalf("Delete(bravo) with maxOps=0 = %v, want ErrTooLarge", err)
		}
		if zero.rejected.Load() == 0 {
			t.Fatal("expected maxOps=0 wrapper to reject drain Txn")
		}
		rec, err := reg.Get(ctx, "bravo")
		if err != nil || rec.State != tenant.StateTerminating {
			t.Fatalf("Get(bravo) = (%+v, %v), want StateTerminating", rec, err)
		}
	})
}
