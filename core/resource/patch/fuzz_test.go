package patch_test

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/resource/patch"
)

func FuzzPatchApplyDiffAndWire(f *testing.F) {
	f.Add([]byte{1, 2, 3, 4, 5, 6, 7, 8}, []byte{9, 8, 7, 6, 5, 4, 3, 2}, []byte{0, 1, 2, 3, 4, 5})
	f.Add([]byte{}, []byte{}, []byte{})
	f.Add([]byte{0xff, 0x10, 0x20, 0x30, 0x40}, []byte{0x01, 0x02, 0x03}, []byte{0xaa, 0xbb, 0xcc, 0xdd})

	f.Fuzz(func(t *testing.T, rawA, rawB, rawOps []byte) {
		rA := newFuzzReader(rawA)
		rB := newFuzzReader(rawB)
		rO := newFuzzReader(rawOps)

		docA := decodeUniqueRecord(rA, 3)
		docB := decodeUniqueRecord(rB, 3)
		ops1 := decodeOps(rO)
		ops2 := decodeOps(rO)

		snapA := cloneRecord(docA)
		snapB := cloneRecord(docB)

		// 1. Diff(docA, docA) == empty and Diff(docA, docB) round-trips through Apply.
		if reflOps, err := patch.Diff(docA, docA); err != nil {
			assertKnownPatchErr(t, err)
		} else if len(reflOps) != 0 {
			t.Fatalf("Diff(docA, docA) returned %d ops", len(reflOps))
		}

		if diffOps, err := patch.Diff(docA, docB); err != nil {
			assertKnownPatchErr(t, err)
		} else {
			applied, err := patch.Apply(docA, diffOps)
			if err != nil {
				t.Fatalf("Apply(docA, Diff(docA, docB)) failed: %v", err)
			}
			if !lkal.Equal(applied, docB) {
				t.Fatalf("Apply(docA, Diff(docA, docB)) != docB")
			}

			// Field-order invariance of Diff.
			permA := permuteRecord(docA, rA)
			permB := permuteRecord(docB, rB)
			diffPerm, err := patch.Diff(permA, permB)
			if err != nil {
				t.Fatalf("Diff(permA, permB) failed: %v", err)
			}
			if len(diffPerm) != len(diffOps) {
				t.Fatalf("Diff permutation changed op count: %d vs %d", len(diffPerm), len(diffOps))
			}
			for i := range diffOps {
				if diffOps[i].Kind != diffPerm[i].Kind || diffOps[i].Path != diffPerm[i].Path || !lkal.Equal(diffOps[i].Value, diffPerm[i].Value) {
					t.Fatalf("Diff permutation changed op[%d]", i)
				}
			}

			// Wire round-trip on Diff ops.
			wireRec, err := patch.ToRecord(diffOps)
			if err != nil {
				t.Fatalf("ToRecord(diffOps) failed: %v", err)
			}
			backOps, err := patch.FromRecord(wireRec)
			if err != nil {
				t.Fatalf("FromRecord(ToRecord(diffOps)) failed: %v", err)
			}
			if len(backOps) != len(diffOps) {
				t.Fatalf("FromRecord len mismatch: %d vs %d", len(backOps), len(diffOps))
			}
			for i := range diffOps {
				if backOps[i].Kind != diffOps[i].Kind || backOps[i].Path != diffOps[i].Path || !lkal.Equal(backOps[i].Value, diffOps[i].Value) {
					t.Fatalf("ToRecord/FromRecord op[%d] mismatch", i)
				}
			}
			if encoded, err := patch.Encode(diffOps); err == nil {
				decoded, err := patch.Decode(encoded)
				if err != nil {
					t.Fatalf("Decode(Encode(diffOps)) failed: %v (json=%s)", err, encoded)
				}
				if len(decoded) != len(diffOps) {
					t.Fatalf("Decode(Encode) len mismatch")
				}
			} else {
				assertKnownPatchErr(t, err)
			}
		}

		// 2. Apply eqv invariance under field permutation.
		permA := permuteRecord(docA, rA)
		out1, err1 := patch.Apply(docA, ops1)
		out2, err2 := patch.Apply(permA, ops1)
		if (err1 == nil) != (err2 == nil) {
			t.Fatalf("permutation changed Apply outcome: err1=%v err2=%v", err1, err2)
		}
		if err1 != nil {
			k1 := assertKnownPatchErr(t, err1)
			k2 := assertKnownPatchErr(t, err2)
			if k1 != k2 {
				t.Fatalf("permutation changed Apply error kind: %v vs %v", k1, k2)
			}
		} else if !lkal.Equal(out1, out2) {
			t.Fatalf("permutation changed Apply result")
		}

		// 3. Apply append law for structurally valid ops1 and ops2.
		if _, err := patch.ToRecord(ops1); err == nil {
			if _, err := patch.ToRecord(ops2); err == nil {
				combined := make([]patch.Op, 0, len(ops1)+len(ops2))
				combined = append(combined, ops1...)
				combined = append(combined, ops2...)
				combOut, combErr := patch.Apply(docA, combined)
				if err1 != nil {
					if combErr == nil || assertKnownPatchErr(t, combErr) != assertKnownPatchErr(t, err1) {
						t.Fatalf("apply_append mismatch on first-step error: %v vs %v", combErr, err1)
					}
				} else {
					seqOut, seqErr := patch.Apply(out1, ops2)
					if (combErr == nil) != (seqErr == nil) {
						t.Fatalf("apply_append outcome mismatch: %v vs %v", combErr, seqErr)
					}
					if combErr != nil {
						if assertKnownPatchErr(t, combErr) != assertKnownPatchErr(t, seqErr) {
							t.Fatalf("apply_append error kind mismatch: %v vs %v", combErr, seqErr)
						}
					} else if !reflect.DeepEqual(combOut, seqOut) {
						t.Fatalf("apply_append result mismatch")
					}
				}
			}
		}

		if !reflect.DeepEqual(docA, snapA) {
			t.Fatalf("fuzz mutated docA")
		}
		if !reflect.DeepEqual(docB, snapB) {
			t.Fatalf("fuzz mutated docB")
		}
	})
}

func FuzzDecodeBytes(f *testing.F) {
	f.Add([]byte(`{"$lkal":"0.1","patch":{}}`))
	f.Add([]byte(`{"$lkal":"0.1","patch":{"0":{"order":10,"op":{"$enum":"set"},"path":"spec/replicas","value":3}}}`))
	f.Add([]byte(`{"$lkal":"0.1","patch":{"0":{"order":10,"op":{"$enum":"del"},"path":"spec/a","value":1}}}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(``))

	f.Fuzz(func(t *testing.T, raw []byte) {
		ops, err := patch.Decode(raw)
		if err != nil {
			assertKnownPatchErr(t, err)
			return
		}
		reEncoded, err := patch.Encode(ops)
		if err != nil {
			t.Fatalf("Encode(Decode(raw)) failed: %v", err)
		}
		ops2, err := patch.Decode(reEncoded)
		if err != nil {
			t.Fatalf("Decode(Encode(Decode(raw))) failed: %v", err)
		}
		if len(ops) != len(ops2) {
			t.Fatalf("round-trip len mismatch: %d vs %d", len(ops), len(ops2))
		}
		for i := range ops {
			if ops[i].Kind != ops2[i].Kind || ops[i].Path != ops2[i].Path || !lkal.Equal(ops[i].Value, ops2[i].Value) {
				t.Fatalf("round-trip op[%d] mismatch", i)
			}
		}
	})
}

func assertKnownPatchErr(t *testing.T, err error) patch.ErrorKind {
	t.Helper()
	var pe *patch.Error
	if !errors.As(err, &pe) {
		t.Fatalf("error %T (%v) is not *patch.Error", err, err)
	}
	if pe.Unwrap() == nil {
		t.Fatalf("unknown patch.ErrorKind %v", pe.Kind)
	}
	return pe.Kind
}

type fuzzReader struct {
	data []byte
	pos  int
}

func newFuzzReader(b []byte) *fuzzReader {
	return &fuzzReader{data: b}
}

func (r *fuzzReader) nextByte() byte {
	if r.pos >= len(r.data) {
		return 0
	}
	b := r.data[r.pos]
	r.pos++
	return b
}

var segPool = []string{"a", "b", "c", "meta", "spec", "status", "replicas", "env", "x"}

func (r *fuzzReader) seg() string {
	return segPool[int(r.nextByte())%len(segPool)]
}

func (r *fuzzReader) path() string {
	n := int(r.nextByte()%3) + 1
	segs := make([]string, n)
	for i := range n {
		segs[i] = r.seg()
	}
	return strings.Join(segs, "/")
}

func decodeUniqueRecord(r *fuzzReader, depth int) lkal.Record {
	n := int(r.nextByte() % 4)
	if depth == 0 || n == 0 {
		return lkal.Record{}
	}
	seen := make(map[string]bool, n)
	fields := make([]lkal.Field, 0, n)
	for range n {
		k := r.seg()
		if seen[k] {
			continue
		}
		seen[k] = true
		fields = append(fields, lkal.Field{Key: k, Value: decodeValue(r, depth-1)})
	}
	if len(fields) == 0 {
		return lkal.Record{}
	}
	return lkal.Record{Fields: fields}
}

func decodeValue(r *fuzzReader, depth int) lkal.Value {
	tag := r.nextByte() % 8
	if depth == 0 && tag == 7 {
		tag = 0
	}
	switch tag {
	case 0:
		return lkal.String(r.seg())
	case 1:
		return lkal.Int(int64(int8(r.nextByte())))
	case 2:
		return lkal.Decimal("1.5")
	case 3:
		return lkal.Bool(r.nextByte()%2 == 0)
	case 4:
		return lkal.Enum("ok")
	case 5:
		return lkal.Quantity{Number: "10", Unit: "Mi"}
	case 6:
		return lkal.List{lkal.Int(1), lkal.Int(2)}
	default:
		return decodeUniqueRecord(r, depth)
	}
}

func decodeOps(r *fuzzReader) []patch.Op {
	n := int(r.nextByte() % 4)
	if n == 0 {
		return nil
	}
	ops := make([]patch.Op, n)
	for i := range n {
		p := r.path()
		switch r.nextByte() % 3 {
		case 0:
			ops[i] = patch.Op{Kind: patch.OpTest, Path: p, Value: decodeValue(r, 1)}
		case 1:
			ops[i] = patch.Op{Kind: patch.OpSet, Path: p, Value: decodeValue(r, 2)}
		default:
			ops[i] = patch.Op{Kind: patch.OpDel, Path: p}
		}
	}
	return ops
}

func permuteRecord(rec lkal.Record, r *fuzzReader) lkal.Record {
	if len(rec.Fields) == 0 {
		return lkal.Record{}
	}
	out := make([]lkal.Field, len(rec.Fields))
	for i, fld := range rec.Fields {
		v := fld.Value
		if sub, ok := v.(lkal.Record); ok {
			v = permuteRecord(sub, r)
		}
		out[i] = lkal.Field{Key: fld.Key, Value: v}
	}
	for i := len(out) - 1; i > 0; i-- {
		j := int(r.nextByte()) % (i + 1)
		out[i], out[j] = out[j], out[i]
	}
	return lkal.Record{Fields: out}
}

func cloneRecord(rec lkal.Record) lkal.Record {
	if len(rec.Fields) == 0 {
		return lkal.Record{}
	}
	fields := make([]lkal.Field, len(rec.Fields))
	for i, f := range rec.Fields {
		fields[i] = lkal.Field{Key: f.Key, Value: cloneVal(f.Value)}
	}
	return lkal.Record{Fields: fields}
}

func cloneVal(v lkal.Value) lkal.Value {
	switch val := v.(type) {
	case lkal.Record:
		return cloneRecord(val)
	case lkal.List:
		if len(val) == 0 {
			return lkal.List(nil)
		}
		out := make(lkal.List, len(val))
		for i, e := range val {
			out[i] = cloneVal(e)
		}
		return out
	case lkal.Ref:
		return lkal.Ref(slices.Clone(val))
	default:
		return v
	}
}
