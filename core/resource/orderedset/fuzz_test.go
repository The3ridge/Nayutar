package orderedset_test

import (
	"encoding/binary"
	"errors"
	"math"
	"math/big"
	"reflect"
	"slices"
	"testing"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/resource/orderedset"
)

func FuzzOrderedSetRoundTripAndPermutation(f *testing.F) {
	f.Add([]byte("seed-cni-plugins"), []byte{1, 2, 3, 4, 5, 6, 7, 8})
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}, []byte{9, 8, 7, 6, 5, 4})
	f.Add([]byte{}, []byte{})
	f.Add([]byte{0xff, 0x00, 0x10, 0x20, 0x30, 0x40}, []byte{0x55, 0xaa, 0x11})

	f.Fuzz(func(t *testing.T, a, b []byte) {
		rA := newByteReader(a)
		rB := newByteReader(b)

		keyField := decodeKey(rA)
		items := decodeItems(rA)
		elems := decodeRecords(rA)
		set := decodeSetRecord(rB)

		snapItems := cloneItems(items)
		snapElems := cloneRecords(elems)
		snapSet := cloneRecord(set)

		// 1. FromItems -> Entries & EntriesReverse round-trip.
		if built, err := orderedset.FromItems(items); err != nil {
			assertKnownOrderErr(t, err)
		} else {
			es, err := orderedset.Entries(built)
			if err != nil {
				t.Fatalf("Entries(FromItems) failed: %v", err)
			}
			if len(es) != len(items) {
				t.Fatalf("Entries len=%d, want %d", len(es), len(items))
			}
			for i, e := range es {
				if e.Key != items[i].Key || e.Order != orderedset.Step*int64(i+1) {
					t.Fatalf("entry[%d]=(%q,%d), want (%q,%d)", i, e.Key, e.Order, items[i].Key, orderedset.Step*int64(i+1))
				}
				if !lkal.Equal(e.Value, items[i].Value) {
					t.Fatalf("entry[%d].Value mismatch", i)
				}
				for _, fld := range e.Value.Fields {
					if fld.Key == orderedset.OrderField {
						t.Fatalf("entry[%d].Value retained order field", i)
					}
				}
			}
			rev, err := orderedset.EntriesReverse(built)
			if err != nil {
				t.Fatalf("EntriesReverse(FromItems) failed: %v", err)
			}
			slices.Reverse(rev)
			if !reflect.DeepEqual(rev, es) {
				t.Fatalf("EntriesReverse != reverse(Entries)")
			}
		}

		// 2. FromKeyedSlice -> ToKeyedSlice & ToKeyedSliceReverse round-trip.
		if built, err := orderedset.FromKeyedSlice(elems, keyField); err != nil {
			assertKnownOrderErr(t, err)
		} else {
			out, err := orderedset.ToKeyedSlice(built, keyField)
			if err != nil {
				t.Fatalf("ToKeyedSlice(FromKeyedSlice) failed: %v", err)
			}
			if len(out) != len(elems) {
				t.Fatalf("ToKeyedSlice len=%d, want %d", len(out), len(elems))
			}
			for i := range elems {
				if !lkal.Equal(out[i], elems[i]) {
					t.Fatalf("ToKeyedSlice[%d] != original", i)
				}
			}
			revOut, err := orderedset.ToKeyedSliceReverse(built, keyField)
			if err != nil {
				t.Fatalf("ToKeyedSliceReverse(FromKeyedSlice) failed: %v", err)
			}
			slices.Reverse(revOut)
			if !reflect.DeepEqual(revOut, out) {
				t.Fatalf("ToKeyedSliceReverse != reverse(ToKeyedSlice)")
			}
		}

		// 3. Entries / EntriesReverse / Renumber / ToKeyedSlice permutation invariance.
		permuted := permuteRecord(set, rB)
		es1, err1 := orderedset.Entries(set)
		es2, err2 := orderedset.Entries(permuted)
		if (err1 == nil) != (err2 == nil) {
			t.Fatalf("permutation changed Entries outcome: err1=%v err2=%v", err1, err2)
		}
		if err1 != nil {
			k1 := assertKnownOrderErr(t, err1)
			k2 := assertKnownOrderErr(t, err2)
			if k1 != k2 {
				t.Fatalf("permutation changed Entries error kind: %s vs %s", k1, k2)
			}
		} else {
			if len(es1) != len(es2) {
				t.Fatalf("permutation changed Entries length: %d vs %d", len(es1), len(es2))
			}
			for i := range es1 {
				if i > 0 && es1[i-1].Order >= es1[i].Order {
					t.Fatalf("Entries not strictly sorted at %d: %d >= %d", i, es1[i-1].Order, es1[i].Order)
				}
				if es1[i].Key != es2[i].Key || es1[i].Order != es2[i].Order || !lkal.Equal(es1[i].Value, es2[i].Value) {
					t.Fatalf("permutation changed entry[%d]", i)
				}
			}

			ren, err := orderedset.Renumber(set)
			if err != nil {
				t.Fatalf("Renumber(valid set) failed: %v", err)
			}
			renEs, err := orderedset.Entries(ren)
			if err != nil {
				t.Fatalf("Entries(Renumber) failed: %v", err)
			}
			if len(renEs) != len(es1) {
				t.Fatalf("Renumber changed length")
			}
			for i := range es1 {
				if renEs[i].Key != es1[i].Key || renEs[i].Order != orderedset.Step*int64(i+1) || !lkal.Equal(renEs[i].Value, es1[i].Value) {
					t.Fatalf("Renumber changed entry[%d]", i)
				}
			}
		}

		if _, err := orderedset.ToKeyedSlice(set, keyField); err != nil {
			assertKnownOrderErr(t, err)
		}

		if !reflect.DeepEqual(items, snapItems) {
			t.Fatalf("fuzz mutated items")
		}
		if !reflect.DeepEqual(elems, snapElems) {
			t.Fatalf("fuzz mutated elems")
		}
		if !reflect.DeepEqual(set, snapSet) {
			t.Fatalf("fuzz mutated set")
		}
	})
}

func FuzzOrderBetweenAndStepBounds(f *testing.F) {
	f.Add(int64(10), int64(20))
	f.Add(int64(-3), int64(0))
	f.Add(int64(-1), int64(0))
	f.Add(int64(math.MinInt64), int64(math.MaxInt64))
	f.Add(int64(math.MinInt64), int64(math.MinInt64+1))
	f.Add(int64(math.MinInt64), int64(math.MinInt64+2))
	f.Add(int64(math.MaxInt64-1), int64(math.MaxInt64))
	f.Add(int64(math.MaxInt64-2), int64(math.MaxInt64))
	f.Add(int64(math.MaxInt64), int64(math.MinInt64))

	bigMin := big.NewInt(math.MinInt64)
	bigMax := big.NewInt(math.MaxInt64)
	bigStep := big.NewInt(orderedset.Step)
	bigTwo := big.NewInt(2)
	bigOne := big.NewInt(1)

	f.Fuzz(func(t *testing.T, lo, hi int64) {
		bLo := big.NewInt(lo)
		bHi := big.NewInt(hi)

		// OrderBetween vs exact big.Int floor midpoint.
		mid, err := orderedset.OrderBetween(lo, hi)
		switch {
		case lo >= hi:
			if !errors.Is(err, orderedset.ErrMalformed) {
				t.Fatalf("OrderBetween(%d, %d) = (%d, %v), want ErrMalformed", lo, hi, mid, err)
			}
		case lo == hi-1:
			if !errors.Is(err, orderedset.ErrGapExhausted) {
				t.Fatalf("OrderBetween(%d, %d) = (%d, %v), want ErrGapExhausted", lo, hi, mid, err)
			}
		default:
			if err != nil {
				t.Fatalf("OrderBetween(%d, %d) unexpected err: %v", lo, hi, err)
			}
			if lo >= mid || mid >= hi {
				t.Fatalf("OrderBetween(%d, %d) = %d violates strict bounds", lo, hi, mid)
			}
			sum := new(big.Int).Add(bLo, bHi)
			rem := new(big.Int)
			quo, _ := new(big.Int).QuoRem(sum, bigTwo, rem)
			if rem.Sign() < 0 {
				quo.Sub(quo, bigOne)
			}
			if !quo.IsInt64() || quo.Int64() != mid {
				t.Fatalf("OrderBetween(%d, %d) = %d, want exact floor midpoint %s", lo, hi, mid, quo.String())
			}
		}

		// OrderBefore vs exact big.Int subtraction.
		wantBefore := new(big.Int).Sub(bLo, bigStep)
		gotBefore, err := orderedset.OrderBefore(lo)
		if wantBefore.Cmp(bigMin) < 0 {
			if !errors.Is(err, orderedset.ErrOverflow) {
				t.Fatalf("OrderBefore(%d) = (%d, %v), want ErrOverflow", lo, gotBefore, err)
			}
		} else if err != nil || gotBefore != wantBefore.Int64() {
			t.Fatalf("OrderBefore(%d) = (%d, %v), want (%s, nil)", lo, gotBefore, err, wantBefore.String())
		}

		// OrderAfter vs exact big.Int addition.
		wantAfter := new(big.Int).Add(bHi, bigStep)
		gotAfter, err := orderedset.OrderAfter(hi)
		if wantAfter.Cmp(bigMax) > 0 {
			if !errors.Is(err, orderedset.ErrOverflow) {
				t.Fatalf("OrderAfter(%d) = (%d, %v), want ErrOverflow", hi, gotAfter, err)
			}
		} else if err != nil || gotAfter != wantAfter.Int64() {
			t.Fatalf("OrderAfter(%d) = (%d, %v), want (%s, nil)", hi, gotAfter, err, wantAfter.String())
		}
	})
}

func assertKnownOrderErr(t *testing.T, err error) orderedset.ErrorKind {
	t.Helper()
	var osErr *orderedset.Error
	if !errors.As(err, &osErr) {
		t.Fatalf("error %T (%v) is not *orderedset.Error", err, err)
	}
	if !errors.Is(err, orderedset.ErrMalformed) &&
		!errors.Is(err, orderedset.ErrDuplicateKey) &&
		!errors.Is(err, orderedset.ErrDuplicateOrder) &&
		!errors.Is(err, orderedset.ErrReservedField) &&
		!errors.Is(err, orderedset.ErrGapExhausted) &&
		!errors.Is(err, orderedset.ErrOverflow) {
		t.Fatalf("unknown error kind: %v", osErr.Kind)
	}
	return osErr.Kind
}

type byteReader struct {
	data []byte
	pos  int
}

func newByteReader(b []byte) *byteReader {
	return &byteReader{data: b}
}

func (r *byteReader) u8() byte {
	if r.pos >= len(r.data) {
		return 0
	}
	b := r.data[r.pos]
	r.pos++
	return b
}

func (r *byteReader) i64() int64 {
	var buf [8]byte
	for i := range buf {
		buf[i] = r.u8()
	}
	return int64(binary.LittleEndian.Uint64(buf[:]))
}

var keyVocab = []string{
	"order", "name", "type", "bridge", "portmap", "firewall", "migrate", "seed",
	"app", "", "$bad", "a/b", "image", "policy",
}

func decodeKey(r *byteReader) string {
	sel := r.u8()
	if int(sel) < len(keyVocab) {
		return keyVocab[sel]
	}
	n := int(r.u8()) % 8
	buf := make([]byte, n)
	for i := range buf {
		buf[i] = 'a' + (r.u8() % 26)
	}
	return string(buf)
}

func decodeItems(r *byteReader) []orderedset.Item {
	n := int(r.u8()) % 6
	out := make([]orderedset.Item, n)
	for i := range n {
		out[i] = orderedset.Item{
			Key:   decodeKey(r),
			Value: decodePayloadRecord(r, 0),
		}
	}
	return out
}

func decodeRecords(r *byteReader) []lkal.Record {
	n := int(r.u8()) % 6
	out := make([]lkal.Record, n)
	for i := range n {
		out[i] = decodePayloadRecord(r, 0)
	}
	return out
}

func decodeSetRecord(r *byteReader) lkal.Record {
	n := int(r.u8()) % 6
	fields := make([]lkal.Field, n)
	for i := range n {
		k := decodeKey(r)
		if r.u8()%8 == 0 {
			fields[i] = lkal.Field{Key: k, Value: decodeValue(r, 1)}
			continue
		}
		fields[i] = lkal.Field{Key: k, Value: decodePayloadRecord(r, 0)}
	}
	return lkal.Record{Fields: fields}
}

func decodePayloadRecord(r *byteReader, depth int) lkal.Record {
	n := int(r.u8()) % 5
	fields := make([]lkal.Field, n)
	for i := range n {
		k := decodeKey(r)
		fields[i] = lkal.Field{Key: k, Value: decodeValue(r, depth+1)}
	}
	return lkal.Record{Fields: fields}
}

func decodeValue(r *byteReader, depth int) lkal.Value {
	tag := r.u8() % 9
	if depth >= 3 && tag >= 7 {
		tag %= 7
	}
	switch tag {
	case 0:
		return lkal.String(decodeKey(r))
	case 1:
		switch r.u8() % 4 {
		case 0:
			return lkal.Int(int64(r.u8()) - 20)
		case 1:
			return lkal.Int(math.MinInt64)
		case 2:
			return lkal.Int(math.MaxInt64)
		default:
			return lkal.Int(r.i64())
		}
	case 2:
		return lkal.Decimal("1.5")
	case 3:
		return lkal.Bool(r.u8()%2 == 0)
	case 4:
		return lkal.Enum("allow")
	case 5:
		return lkal.Quantity{Number: "10", Unit: "s"}
	case 6:
		return lkal.Ref{"secret", decodeKey(r)}
	case 7:
		n := int(r.u8()) % 3
		items := make(lkal.List, n)
		for i := range n {
			items[i] = decodeValue(r, depth+1)
		}
		return items
	default:
		return decodePayloadRecord(r, depth+1)
	}
}

func permuteRecord(rec lkal.Record, r *byteReader) lkal.Record {
	cp := cloneRecord(rec)
	for i := len(cp.Fields) - 1; i > 0; i-- {
		j := int(r.u8()) % (i + 1)
		cp.Fields[i], cp.Fields[j] = cp.Fields[j], cp.Fields[i]
	}
	for i, fld := range cp.Fields {
		if sub, ok := fld.Value.(lkal.Record); ok {
			cp.Fields[i].Value = permuteRecord(sub, r)
		}
	}
	return cp
}

func cloneItems(items []orderedset.Item) []orderedset.Item {
	out := make([]orderedset.Item, len(items))
	for i, it := range items {
		out[i] = orderedset.Item{Key: it.Key, Value: cloneRecord(it.Value)}
	}
	return out
}

func cloneRecords(recs []lkal.Record) []lkal.Record {
	out := make([]lkal.Record, len(recs))
	for i, r := range recs {
		out[i] = cloneRecord(r)
	}
	return out
}

func cloneRecord(r lkal.Record) lkal.Record {
	if r.Fields == nil {
		return lkal.Record{}
	}
	fields := make([]lkal.Field, len(r.Fields))
	for i, fld := range r.Fields {
		fields[i] = lkal.Field{Key: fld.Key, Value: cloneVal(fld.Value)}
	}
	return lkal.Record{Fields: fields}
}

func cloneVal(v lkal.Value) lkal.Value {
	switch val := v.(type) {
	case lkal.Record:
		return cloneRecord(val)
	case lkal.List:
		if val == nil {
			return nil
		}
		out := make(lkal.List, len(val))
		for i, el := range val {
			out[i] = cloneVal(el)
		}
		return out
	case lkal.Ref:
		return lkal.Ref(slices.Clone(val))
	default:
		return v
	}
}
