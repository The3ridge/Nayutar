package tenant_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/memstore"
	"github.com/Kaikei-e/Nayutar/core/store/storetest"
	"github.com/Kaikei-e/Nayutar/core/store/tenant"
)

type storeAdapter struct {
	tenant.Store
	root store.Store
}

func (a storeAdapter) Compact(ctx context.Context, rev store.Rev) error {
	return a.root.Compact(ctx, rev)
}

func mustAdapter(t *testing.T, root store.Store, name string) store.Store {
	t.Helper()
	view, err := tenant.View(root, name)
	if err != nil {
		t.Fatal(err)
	}
	return storeAdapter{Store: view, root: root}
}

func TestValidateName(t *testing.T) {
	cases := []struct {
		name  string
		valid bool
	}{
		{"a", true},
		{"a1", true},
		{"1a", true},
		{"a-b", true},
		{strings.Repeat("a", 63), true},
		{"", false},
		{"-", false},
		{"A", false},
		{"a_b", false},
		{"-a", false},
		{"a-", false},
		{"a/b", false},
		{"a.b", false},
		{strings.Repeat("a", 64), false},
		{"α", false},
		{"a\x00", false},
	}
	root := memstore.New()
	for _, tc := range cases {
		err := tenant.ValidateName(tc.name)
		if tc.valid && err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", tc.name, err)
		}
		if !tc.valid && !errors.Is(err, tenant.ErrInvalidName) {
			t.Errorf("ValidateName(%q) = %v, want ErrInvalidName", tc.name, err)
		}
		prefix, pErr := tenant.Prefix(tc.name)
		if tc.valid && (pErr != nil || prefix != "/t/"+tc.name+"/") {
			t.Errorf("Prefix(%q) = (%q, %v), want (%q, nil)", tc.name, prefix, pErr, "/t/"+tc.name+"/")
		}
		if !tc.valid && !errors.Is(pErr, tenant.ErrInvalidName) {
			t.Errorf("Prefix(%q) error = %v, want ErrInvalidName", tc.name, pErr)
		}
		_, vErr := tenant.View(root, tc.name)
		if tc.valid && vErr != nil {
			t.Errorf("View(%q) = %v, want nil", tc.name, vErr)
		}
		if !tc.valid && !errors.Is(vErr, tenant.ErrInvalidName) {
			t.Errorf("View(%q) error = %v, want ErrInvalidName", tc.name, vErr)
		}
	}
}

func TestViewHasNoCompact(t *testing.T) {
	v, err := tenant.View(memstore.New(), "acme")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := v.(interface {
		Compact(context.Context, store.Rev) error
	}); ok {
		t.Fatal("tenant view unexpectedly implements Compact")
	}
	if _, ok := v.(store.Store); ok {
		t.Fatal("tenant view unexpectedly implements store.Store")
	}
}

func TestIsolation(t *testing.T) {
	checkIsolation(t, memstore.New())
}

func TestContract(t *testing.T) {
	storetest.RunSynctest(t, func(t *testing.T) store.Store {
		return mustAdapter(t, memstore.New(), "acme")
	})
}

func TestLinearizability(t *testing.T) {
	storetest.RunLinearizabilitySynctest(t, func(t *testing.T) store.Store {
		return mustAdapter(t, memstore.New(), "acme")
	})
}

func checkIsolation(t *testing.T, root store.Store) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	va, err := tenant.View(root, "a")
	if err != nil {
		t.Fatal(err)
	}
	vab, err := tenant.View(root, "ab")
	if err != nil {
		t.Fatal(err)
	}

	watchCtx, watchCancel := context.WithCancel(ctx)
	defer watchCancel()
	wa, err := va.Watch(watchCtx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	wab, err := vab.Watch(watchCtx, "", 0)
	if err != nil {
		t.Fatal(err)
	}

	base, err := root.CurrentRev(ctx)
	if err != nil {
		t.Fatal(err)
	}

	ok, r1, err := va.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: "k", Value: []byte("a-1")}})
	if err != nil || !ok || r1 != base+1 {
		t.Fatalf("va initial Txn = (%v, %d, %v), want (true, %d, nil)", ok, r1, err, base+1)
	}

	// Revisions observed by a view advance when the other tenant writes.
	revAB, err := vab.CurrentRev(ctx)
	if err != nil || revAB != r1 {
		t.Fatalf("vab.CurrentRev = (%d, %v), want (%d, nil)", revAB, err, r1)
	}
	if _, gotRev, err := vab.Get(ctx, "k", 0); !errors.Is(err, store.ErrNotFound) || gotRev != r1 {
		t.Fatalf("vab.Get(\"k\") = (rev=%d, %v), want (rev=%d, ErrNotFound)", gotRev, err, r1)
	}
	if kvs, gotRev, next, err := vab.Range(ctx, "", 0, 0, ""); err != nil || len(kvs) != 0 || gotRev != r1 || next != "" {
		t.Fatalf("vab.Range(\"\") = (%v, %d, %q, %v), want empty at %d", kvs, gotRev, next, err, r1)
	}

	// A Txn Cmp in "ab" checks "/t/ab/k", which is absent even though "/t/a/k" exists.
	ok, r2, err := vab.Txn(ctx,
		[]store.Cmp{{Key: "k", Op: store.CmpEqual, ModRev: 0}},
		[]store.Op{{Type: store.OpPut, Key: "k", Value: []byte("ab-1")}},
	)
	if err != nil || !ok || r2 != r1+1 {
		t.Fatalf("vab absent-cmp Txn = (%v, %d, %v), want (true, %d, nil)", ok, r2, err, r1+1)
	}
	// A Cmp using tenant "a"'s modRev must fail in tenant "ab", and vice versa.
	if ok, _, err := vab.Txn(ctx, []store.Cmp{{Key: "k", Op: store.CmpEqual, ModRev: r1}}, []store.Op{{Type: store.OpPut, Key: "k", Value: []byte("bad")}}); err != nil || ok {
		t.Fatalf("vab Txn comparing against tenant a's modRev = (%v, %v), want (false, nil)", ok, err)
	}
	if ok, _, err := va.Txn(ctx, []store.Cmp{{Key: "k", Op: store.CmpEqual, ModRev: r2}}, []store.Op{{Type: store.OpPut, Key: "k", Value: []byte("bad")}}); err != nil || ok {
		t.Fatalf("va Txn comparing against tenant ab's modRev = (%v, %v), want (false, nil)", ok, err)
	}

	ok, r3, err := va.Txn(ctx,
		[]store.Cmp{{Key: "k", Op: store.CmpEqual, ModRev: r1}},
		[]store.Op{{Type: store.OpPut, Key: "k", Value: []byte("a-2")}},
	)
	if err != nil || !ok || r3 != r2+1 {
		t.Fatalf("va update Txn = (%v, %d, %v), want (true, %d, nil)", ok, r3, err, r2+1)
	}

	checkKV := func(v tenant.Store, tenantName, wantVal string, wantMod, wantReadRev store.Rev) {
		t.Helper()
		kv, rev, err := v.Get(ctx, "k", 0)
		if err != nil || rev != wantReadRev || kv.Key != "k" || !bytes.Equal(kv.Value, []byte(wantVal)) || kv.ModRev != wantMod {
			t.Fatalf("%s Get(\"k\") = (%+v, rev=%d, %v), want key=\"k\" val=%q mod=%d rev=%d", tenantName, kv, rev, err, wantVal, wantMod, wantReadRev)
		}
		kvs, rev, next, err := v.Range(ctx, "", 0, 0, "")
		if err != nil || rev != wantReadRev || next != "" || len(kvs) != 1 || kvs[0].Key != "k" || !bytes.Equal(kvs[0].Value, []byte(wantVal)) || kvs[0].ModRev != wantMod {
			t.Fatalf("%s Range(\"\") = (%+v, rev=%d, next=%q, %v), want single k=%q mod=%d rev=%d", tenantName, kvs, rev, next, err, wantVal, wantMod, wantReadRev)
		}
	}
	checkKV(va, "a", "a-2", r3, r3)
	checkKV(vab, "ab", "ab-1", r2, r3)

	// Root store sees both tenants at their full prefixed keys.
	rootKVs, _, _, err := root.Range(ctx, "", 0, 0, "")
	if err != nil || len(rootKVs) != 2 || rootKVs[0].Key != "/t/a/k" || !bytes.Equal(rootKVs[0].Value, []byte("a-2")) || rootKVs[1].Key != "/t/ab/k" || !bytes.Equal(rootKVs[1].Value, []byte("ab-1")) {
		t.Fatalf("root Range(\"\") = (%+v, %v), want [/t/a/k=a-2 /t/ab/k=ab-1]", rootKVs, err)
	}

	// Deleting in one tenant leaves the other intact and delivers events only to the owning watch.
	ok, r4, err := va.Txn(ctx, nil, []store.Op{{Type: store.OpDelete, Key: "k"}})
	if err != nil || !ok || r4 != r3+1 {
		t.Fatalf("va delete Txn = (%v, %d, %v), want (true, %d, nil)", ok, r4, err, r3+1)
	}
	checkKV(vab, "ab", "ab-1", r2, r4)

	evsA, progA, err := storetest.Sync(ctx, root, wa)
	if err != nil {
		t.Fatalf("Sync wa: %v", err)
	}
	if progA != r4 || len(evsA) != 3 ||
		evsA[0].Type != store.EventPut || evsA[0].Key != "k" || string(evsA[0].Value) != "a-1" || evsA[0].Rev != r1 ||
		evsA[1].Type != store.EventPut || evsA[1].Key != "k" || string(evsA[1].Value) != "a-2" || evsA[1].Rev != r3 ||
		evsA[2].Type != store.EventDelete || evsA[2].Key != "k" || evsA[2].Rev != r4 {
		t.Fatalf("wa events = (%+v, prog=%d), want 3 events for a at revs %d, %d, %d", evsA, progA, r1, r3, r4)
	}

	evsAB, progAB, err := storetest.Sync(ctx, root, wab)
	if err != nil {
		t.Fatalf("Sync wab: %v", err)
	}
	if progAB != r4 || len(evsAB) != 1 ||
		evsAB[0].Type != store.EventPut || evsAB[0].Key != "k" || string(evsAB[0].Value) != "ab-1" || evsAB[0].Rev != r2 {
		t.Fatalf("wab events = (%+v, prog=%d), want 1 event for ab at rev %d", evsAB, progAB, r2)
	}
}
