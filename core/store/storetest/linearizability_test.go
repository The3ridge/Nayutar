package storetest_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/memstore"
	"github.com/Kaikei-e/Nayutar/core/store/storetest"
)

// staleReads answers Get one revision late.
type staleReads struct{ store.Store }

func (s staleReads) Get(ctx context.Context, key string, atRev store.Rev) (store.KV, store.Rev, error) {
	rev, err := s.CurrentRev(ctx)
	if err != nil {
		return store.KV{}, 0, err
	}
	if atRev == 0 && rev > 1 {
		atRev = rev - 1
	}
	return s.Store.Get(ctx, key, atRev)
}

// tornTxn checks the comparisons and applies the writes in two separate steps.
type tornTxn struct{ store.Store }

func (s tornTxn) Txn(ctx context.Context, cmps []store.Cmp, ops []store.Op) (bool, store.Rev, error) {
	ok, rev, err := s.Store.Txn(ctx, cmps, nil)
	if err != nil || !ok {
		return ok, rev, err
	}
	time.Sleep(200 * time.Microsecond)
	return s.Store.Txn(ctx, nil, ops)
}

// splitTxn gives every write of a transaction its own revision.
type splitTxn struct {
	store.Store
	mu *sync.Mutex
}

func (s splitTxn) Txn(ctx context.Context, cmps []store.Cmp, ops []store.Op) (bool, store.Rev, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ok, rev, err := s.Store.Txn(ctx, cmps, nil)
	if err != nil || !ok {
		return ok, rev, err
	}
	for _, op := range ops {
		if _, rev, err = s.Store.Txn(ctx, nil, []store.Op{op}); err != nil {
			return false, 0, err
		}
	}
	return true, rev, nil
}

// staleRev reports the revision before the last write.
type staleRev struct{ store.Store }

func (s staleRev) CurrentRev(ctx context.Context) (store.Rev, error) {
	rev, err := s.Store.CurrentRev(ctx)
	if rev > 1 {
		rev--
	}
	return rev, err
}

// tornRange reads matching keys one at a time instead of from one snapshot.
type tornRange struct{ store.Store }

func (s tornRange) Range(ctx context.Context, prefix string, atRev store.Rev, limit int, cont string) ([]store.KV, store.Rev, string, error) {
	if err := store.ValidateRange(prefix, atRev, limit, cont); err != nil {
		return nil, 0, "", err
	}
	if atRev != 0 {
		return s.Store.Range(ctx, prefix, atRev, limit, cont)
	}
	rev, err := s.CurrentRev(ctx)
	if err != nil {
		return nil, 0, "", err
	}
	var kvs []store.KV
	for _, key := range []string{"j/0", "k/0", "k/1"} {
		if !strings.HasPrefix(key, prefix) || key <= cont {
			continue
		}
		time.Sleep(150 * time.Microsecond)
		kv, r, getErr := s.Get(ctx, key, 0)
		if errors.Is(getErr, store.ErrNotFound) {
			rev = r
			continue
		}
		if getErr != nil {
			return nil, 0, "", getErr
		}
		rev = r
		if limit > 0 && len(kvs) == limit {
			return kvs, rev, kvs[len(kvs)-1].Key, nil
		}
		kvs = append(kvs, kv)
	}
	return kvs, rev, "", nil
}

func TestCheckLinearizableAcceptsTheReferenceStore(t *testing.T) {
	storetest.RunLinearizability(t, func(*testing.T) store.Store { return memstore.New() })
}

func TestCheckLinearizableRejectsBrokenStores(t *testing.T) {
	variants := []struct {
		name  string
		build func() store.Store
	}{
		{"get is one revision late", func() store.Store { return staleReads{memstore.New()} }},
		{"range reads keys without one snapshot", func() store.Store { return tornRange{memstore.New()} }},
		{"txn compares and writes in two steps", func() store.Store { return tornTxn{memstore.New()} }},
		{"txn gives each write its own revision", func() store.Store { return splitTxn{memstore.New(), &sync.Mutex{}} }},
		{"current revision is one write late", func() store.Store { return staleRev{memstore.New()} }},
	}
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			for seed := uint64(1); seed <= 20; seed++ {
				err := storetest.CheckLinearizable(ctx, v.build(), storetest.Workload{Clients: 8, Ops: 60, Seed: seed})
				if errors.Is(err, storetest.ErrNotLinearizable) {
					return
				}
				if err != nil {
					t.Fatalf("seed %d: the check failed for another reason: %v", seed, err)
				}
			}
			t.Fatal("20 workloads found no violation")
		})
	}
}

func TestCheckLinearizableReportsCallErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := storetest.CheckLinearizable(ctx, memstore.New(), storetest.Workload{Clients: 2, Ops: 5, Seed: 1})
	if err == nil || errors.Is(err, storetest.ErrNotLinearizable) {
		t.Fatalf("CheckLinearizable on a canceled context = %v, want a call error", err)
	}
}

// pacedStore adds a short delay per call so paced clients overlap on memstore.
type pacedStore struct{ store.Store }

func (s pacedStore) Get(ctx context.Context, key string, atRev store.Rev) (store.KV, store.Rev, error) {
	time.Sleep(2 * time.Millisecond)
	return s.Store.Get(ctx, key, atRev)
}

func (s pacedStore) Range(ctx context.Context, prefix string, atRev store.Rev, limit int, cont string) ([]store.KV, store.Rev, string, error) {
	time.Sleep(2 * time.Millisecond)
	return s.Store.Range(ctx, prefix, atRev, limit, cont)
}

func (s pacedStore) CurrentRev(ctx context.Context) (store.Rev, error) {
	time.Sleep(2 * time.Millisecond)
	return s.Store.CurrentRev(ctx)
}

func (s pacedStore) Txn(ctx context.Context, cmps []store.Cmp, ops []store.Op) (bool, store.Rev, error) {
	time.Sleep(2 * time.Millisecond)
	return s.Store.Txn(ctx, cmps, ops)
}

// errAfterAppliedTxn fails every 10th Txn after applying it and every 8th read.
type errAfterAppliedTxn struct {
	store.Store
	reads *atomic.Int64
	txns  *atomic.Int64
}

func newErrAfterAppliedTxn(inner store.Store) errAfterAppliedTxn {
	return errAfterAppliedTxn{Store: inner, reads: &atomic.Int64{}, txns: &atomic.Int64{}}
}

func (s errAfterAppliedTxn) Get(ctx context.Context, key string, atRev store.Rev) (store.KV, store.Rev, error) {
	if s.reads.Add(1)%8 == 0 {
		return store.KV{}, 0, errors.New("injected read error")
	}
	return s.Store.Get(ctx, key, atRev)
}

func (s errAfterAppliedTxn) Range(ctx context.Context, prefix string, atRev store.Rev, limit int, cont string) ([]store.KV, store.Rev, string, error) {
	if s.reads.Add(1)%8 == 0 {
		return nil, 0, "", errors.New("injected range error")
	}
	return s.Store.Range(ctx, prefix, atRev, limit, cont)
}

func (s errAfterAppliedTxn) CurrentRev(ctx context.Context) (store.Rev, error) {
	if s.reads.Add(1)%8 == 0 {
		return 0, errors.New("injected rev error")
	}
	return s.Store.CurrentRev(ctx)
}

func (s errAfterAppliedTxn) Txn(ctx context.Context, cmps []store.Cmp, ops []store.Op) (bool, store.Rev, error) {
	n := s.txns.Add(1)
	if n%15 == 0 {
		return false, 0, errors.New("injected pre-apply txn error")
	}
	ok, rev, err := s.Store.Txn(ctx, cmps, ops)
	if err != nil {
		return false, 0, err
	}
	if n%10 == 0 {
		return false, 0, errors.New("injected post-apply txn error")
	}
	return ok, rev, nil
}

func TestCheckLinearizableAllowsAmbiguousErrorsWithDisrupt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s := newErrAfterAppliedTxn(pacedStore{memstore.New()})
	w := storetest.Workload{
		Clients: 8,
		Ops:     20,
		Seed:    1,
		Disrupt: func(context.Context) error {
			time.Sleep(50 * time.Millisecond)
			return nil
		},
	}
	if err := storetest.CheckLinearizable(ctx, s, w); err != nil {
		t.Fatalf("CheckLinearizable with Disrupt and ambiguous errors = %v, want nil", err)
	}
}

func TestCheckLinearizableRejectsBrokenStoreWithDisrupt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for seed := uint64(1); seed <= 20; seed++ {
		s := newErrAfterAppliedTxn(staleReads{pacedStore{memstore.New()}})
		w := storetest.Workload{
			Clients: 8,
			Ops:     25,
			Seed:    seed,
			Disrupt: func(context.Context) error { return nil },
		}
		err := storetest.CheckLinearizable(ctx, s, w)
		if errors.Is(err, storetest.ErrNotLinearizable) {
			return
		}
		if err != nil {
			t.Fatalf("seed %d: unexpected error = %v", seed, err)
		}
	}
	t.Fatal("20 workloads found no violation")
}

func TestCheckLinearizableWithoutDisruptFailsOnError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s := newErrAfterAppliedTxn(memstore.New())
	err := storetest.CheckLinearizable(ctx, s, storetest.Workload{Clients: 4, Ops: 20, Seed: 1})
	if err == nil || errors.Is(err, storetest.ErrNotLinearizable) {
		t.Fatalf("CheckLinearizable without Disrupt = %v, want a call error", err)
	}
}

func TestCheckLinearizableReportsDisruptError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	want := errors.New("disrupt failed")
	err := storetest.CheckLinearizable(ctx, pacedStore{memstore.New()}, storetest.Workload{
		Clients: 4,
		Ops:     10,
		Seed:    1,
		Disrupt: func(context.Context) error { return want },
	})
	if !errors.Is(err, want) {
		t.Fatalf("CheckLinearizable error = %v, want %v", err, want)
	}
}

func TestScopeIsolatesKeysAndShiftsRevisions(t *testing.T) {
	inner := memstore.New()
	ctx := context.Background()
	if _, _, err := inner.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: "outside", Value: []byte("x")}}); err != nil {
		t.Fatal(err)
	}
	storetest.Run(t, func(t *testing.T) store.Store {
		base, err := inner.CurrentRev(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return storetest.Scope(inner, "scope/"+t.Name()+"/", base-1)
	})
	kvs, _, _, err := inner.Range(ctx, "outside", 0, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(kvs) != 1 || string(kvs[0].Value) != "x" {
		t.Fatalf("a scoped test touched a key outside its prefix: %v", kvs)
	}
}
