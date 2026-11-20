package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/memstore"
)

func invalid(err error) bool { return errors.Is(err, store.ErrInvalidArgument) }

// A validator error must be an invalid-argument error, and the reference store must agree with it.
func requireAgreement(t *testing.T, what string, verr, serr error) {
	t.Helper()
	if verr != nil && !invalid(verr) {
		t.Fatalf("%s: validator error %v does not wrap ErrInvalidArgument", what, verr)
	}
	if (verr != nil) != invalid(serr) {
		t.Fatalf("%s: validator error = %v, memstore error = %v", what, verr, serr)
	}
}

func FuzzValidateGet(f *testing.F) {
	f.Add("", int64(0))
	f.Add("k", int64(0))
	f.Add("k", int64(1))
	f.Add("k", int64(-1))
	f.Add("\x00", int64(1<<62))
	f.Add("k", int64(-1<<63))
	f.Fuzz(func(t *testing.T, key string, rev int64) {
		err := store.ValidateGet(key, store.Rev(rev))
		if want := key != "" && rev >= 0; (err == nil) != want {
			t.Fatalf("ValidateGet(%q, %d) = %v", key, rev, err)
		}
		_, _, serr := memstore.New().Get(context.Background(), key, store.Rev(rev))
		requireAgreement(t, "Get", err, serr)
	})
}

func FuzzValidateRange(f *testing.F) {
	f.Add("", int64(0), 0, "")
	f.Add("/a/", int64(0), 10, "/a/b")
	f.Add("/a/", int64(0), 10, "/b/b")
	f.Add("/a/", int64(0), 10, "/a")
	f.Add("/a/", int64(0), 10, "/a/")
	f.Add("/a/", int64(-1), 0, "")
	f.Add("/a/", int64(1), -1, "")
	f.Add("", int64(0), 0, "anything")
	f.Fuzz(func(t *testing.T, prefix string, rev int64, limit int, cont string) {
		err := store.ValidateRange(prefix, store.Rev(rev), limit, cont)
		want := rev >= 0 && limit >= 0 && (cont == "" || strings.HasPrefix(cont, prefix))
		if (err == nil) != want {
			t.Fatalf("ValidateRange(%q, %d, %d, %q) = %v", prefix, rev, limit, cont, err)
		}
		_, _, _, serr := memstore.New().Range(context.Background(), prefix, store.Rev(rev), limit, cont)
		requireAgreement(t, "Range", err, serr)
	})
}

func FuzzValidateWatchAndCompact(f *testing.F) {
	for _, rev := range []int64{0, 1, 2, -1, 1 << 62, -1 << 63, 1<<63 - 1} {
		f.Add(rev)
	}
	f.Fuzz(func(t *testing.T, rev int64) {
		werr := store.ValidateWatch(store.Rev(rev))
		if (werr == nil) != (rev >= 0) {
			t.Fatalf("ValidateWatch(%d) = %v", rev, werr)
		}
		cerr := store.ValidateCompact(store.Rev(rev))
		if (cerr == nil) != (rev >= 1) {
			t.Fatalf("ValidateCompact(%d) = %v", rev, cerr)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ws, serr := memstore.New().Watch(ctx, "", store.Rev(rev))
		if ws != nil {
			cancel()
			for range ws.Events() {
			}
		}
		requireAgreement(t, "Watch", werr, serr)
		requireAgreement(t, "Compact", cerr, memstore.New().Compact(context.Background(), store.Rev(rev)))
	})
}

func FuzzValidateTxn(f *testing.F) {
	f.Add("a", uint8(1), int64(0), "a", uint8(1), "b", uint8(2))
	f.Add("a", uint8(4), int64(3), "a", uint8(1), "a", uint8(2))
	f.Add("", uint8(1), int64(0), "a", uint8(1), "b", uint8(1))
	f.Add("a", uint8(0), int64(0), "a", uint8(1), "b", uint8(1))
	f.Add("a", uint8(5), int64(0), "a", uint8(1), "b", uint8(1))
	f.Add("a", uint8(2), int64(-1), "a", uint8(1), "b", uint8(1))
	f.Add("a", uint8(3), int64(1), "", uint8(1), "b", uint8(1))
	f.Add("a", uint8(3), int64(1), "a", uint8(0), "b", uint8(1))
	f.Add("a", uint8(3), int64(1), "a", uint8(3), "b", uint8(1))
	f.Fuzz(func(t *testing.T, cmpKey string, cmpOp uint8, cmpRev int64, opKey1 string, opType1 uint8, opKey2 string, opType2 uint8) {
		cmps := []store.Cmp{{Key: cmpKey, Op: store.CmpOp(cmpOp), ModRev: store.Rev(cmpRev)}}
		ops := []store.Op{
			{Type: store.OpType(opType1), Key: opKey1, Value: []byte("v")},
			{Type: store.OpType(opType2), Key: opKey2},
		}
		err := store.ValidateTxn(cmps, ops)
		want := cmpKey != "" && cmpOp >= 1 && cmpOp <= 4 && cmpRev >= 0 &&
			opKey1 != "" && opKey2 != "" && opKey1 != opKey2 &&
			(opType1 == 1 || opType1 == 2) && (opType2 == 1 || opType2 == 2)
		if (err == nil) != want {
			t.Fatalf("ValidateTxn(%+v, %+v) = %v", cmps, ops, err)
		}
		_, _, serr := memstore.New().Txn(context.Background(), cmps, ops)
		requireAgreement(t, "Txn", err, serr)

		if err != nil {
			return
		}
		// Valid requests stay valid when the ops are reordered, dropped or the comparisons repeated.
		if rerr := store.ValidateTxn(cmps, nil); rerr != nil {
			t.Fatalf("empty ops rejected: %v", rerr)
		}
		if rerr := store.ValidateTxn(nil, []store.Op{ops[1], ops[0]}); rerr != nil {
			t.Fatalf("reordered ops rejected: %v", rerr)
		}
		if rerr := store.ValidateTxn([]store.Cmp{cmps[0], cmps[0]}, ops[:1]); rerr != nil {
			t.Fatalf("repeated cmps rejected: %v", rerr)
		}
		if rerr := store.ValidateTxn(cmps, []store.Op{ops[0], ops[0]}); !invalid(rerr) {
			t.Fatalf("duplicate op key accepted: %v", rerr)
		}
	})
}
