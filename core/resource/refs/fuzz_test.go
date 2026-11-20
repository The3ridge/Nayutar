package refs_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/resource/refs"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
)

func FuzzPrepare(f *testing.F) {
	f.Add([]byte("seed-1"), []byte("seed-2"))
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7}, []byte{7, 6, 5, 4, 3, 2, 1, 0})
	f.Add([]byte{}, []byte{})
	f.Add([]byte{0xff, 0x10, 0x20, 0x30, 0x40}, []byte{0x50, 0x60, 0x70})

	f.Fuzz(func(t *testing.T, a, b []byte) {
		ra := &fuzzReader{data: a}
		rb := &fuzzReader{data: b}

		stored := decodeRecord(ra, 0)
		next := decodeRecord(rb, 0)
		hasStored := ra.u8()%2 == 0
		src := keyspace.Object{
			Group: decodeFrom(ra, fuzzGroups),
			Kind:  decodeFrom(ra, fuzzKinds),
			Name:  decodeFrom(ra, fuzzNames),
		}
		schema := decodeSchema(ra)
		allowBitmask := rb.u8()
		existBitmask := rb.u8()

		snapStored := cloneRecord(stored)
		snapNext := cloneRecord(next)

		_ = schema.Validate()
		_ = refs.Occurrences(next)

		if _, err := refs.DeleteDelta(stored); err != nil {
			assertIsRefsError(t, err)
		}

		plan, err := refs.Prepare(refs.Input{
			Source:    src,
			Schema:    schema,
			Stored:    stored,
			HasStored: hasStored,
			Next:      next,
		})
		if err != nil {
			assertIsRefsError(t, err)
		} else {
			_, checkErr := plan.Check(
				func(occ refs.Occurrence) bool {
					return (allowBitmask>>uint(len(occ.Path)%8))&1 == 1
				},
				func(obj keyspace.Object) bool {
					return (existBitmask>>uint(len(obj.Name)%8))&1 == 1
				},
			)
			if checkErr != nil {
				assertIsRefsError(t, checkErr)
			}
		}

		if !reflect.DeepEqual(stored, snapStored) {
			t.Fatalf("Prepare mutated stored")
		}
		if !reflect.DeepEqual(next, snapNext) {
			t.Fatalf("Prepare mutated next")
		}
	})
}

func assertIsRefsError(t *testing.T, err error) {
	t.Helper()
	var re *refs.Error
	if !errors.As(err, &re) {
		t.Fatalf("error %T (%v) is not *refs.Error", err, err)
	}
	switch re.Kind {
	case refs.ErrInternal, refs.ErrMalformed, refs.ErrKind, refs.ErrDenied, refs.ErrNotFound:
	default:
		t.Fatalf("unexpected ErrorKind: %v", re.Kind)
	}
}

var (
	fuzzKeys   = []string{"meta", "spec", "status", "a", "b", "list", "db", "", "spec/x", "o"}
	fuzzKinds  = []string{"secret", "configmap", "workload", "widget.example.com", "secret.core", "..", ""}
	fuzzGroups = []string{"core", "example.com", "bad", ""}
	fuzzNames  = []string{"web", "db-1", "a/b", "", "other"}
	fuzzLeaf   = []string{"meta", "spec", "status", "o", "$ref", "", "a/b", "key"}
)

type fuzzReader struct {
	data []byte
	pos  int
}

func (r *fuzzReader) u8() byte {
	if r.pos >= len(r.data) {
		return 0
	}
	b := r.data[r.pos]
	r.pos++
	return b
}

func (r *fuzzReader) str(maxLen int) string {
	n := int(r.u8()) % (maxLen + 1)
	if n == 0 {
		return ""
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = r.u8()
	}
	return string(b)
}

func decodeFrom(r *fuzzReader, vocab []string) string {
	sel := int(r.u8())
	if sel < len(vocab) {
		return vocab[sel]
	}
	return r.str(16)
}

func decodeRef(r *fuzzReader) lkal.Ref {
	n := int(r.u8()) % 6
	if n == 0 && r.u8()%2 == 0 {
		return nil
	}
	out := make(lkal.Ref, n)
	for i := range n {
		switch i {
		case 0:
			out[i] = decodeFrom(r, fuzzKinds)
		case 1:
			out[i] = decodeFrom(r, fuzzNames)
		default:
			out[i] = decodeFrom(r, fuzzLeaf)
		}
	}
	return out
}

func decodeRecord(r *fuzzReader, depth int) lkal.Record {
	n := int(r.u8()) % 5
	fields := make([]lkal.Field, n)
	for i := range n {
		fields[i] = lkal.Field{
			Key:   decodeFrom(r, fuzzKeys),
			Value: decodeVal(r, depth+1),
		}
	}
	return lkal.Record{Fields: fields}
}

func decodeVal(r *fuzzReader, depth int) lkal.Value {
	tag := r.u8() % 5
	if depth >= 3 && tag >= 3 {
		tag %= 3
	}
	switch tag {
	case 0:
		return decodeRef(r)
	case 1:
		return lkal.String(decodeFrom(r, fuzzNames))
	case 2:
		return lkal.Int(int64(r.u8()))
	case 3:
		n := int(r.u8()) % 4
		items := make(lkal.List, n)
		for i := range n {
			items[i] = decodeVal(r, depth+1)
		}
		return items
	default:
		return decodeRecord(r, depth+1)
	}
}

func decodeSchema(r *fuzzReader) refs.Schema {
	n := int(r.u8()) % 3
	if n == 0 {
		return nil
	}
	s := make(refs.Schema, n)
	for range n {
		k := decodeFrom(r, fuzzKinds)
		nr := int(r.u8()) % 3
		rules := make([]refs.Rule, nr)
		for i := range nr {
			np := int(r.u8()) % 4
			path := make([]string, np)
			for j := range np {
				if j == 0 && r.u8()%2 == 0 {
					path[j] = "spec"
				} else if r.u8()%3 == 0 {
					path[j] = "*"
				} else {
					path[j] = decodeFrom(r, fuzzKeys)
				}
			}
			nt := int(r.u8()) % 3
			targets := make([]string, nt)
			for j := range nt {
				targets[j] = decodeFrom(r, fuzzKinds)
			}
			rules[i] = refs.Rule{Path: path, Targets: targets}
		}
		s[k] = rules
	}
	return s
}

func cloneRecord(rec lkal.Record) lkal.Record {
	if rec.Fields == nil {
		return lkal.Record{}
	}
	fields := make([]lkal.Field, len(rec.Fields))
	for i, fl := range rec.Fields {
		fields[i] = lkal.Field{Key: fl.Key, Value: cloneVal(fl.Value)}
	}
	return lkal.Record{Fields: fields}
}

func cloneVal(v lkal.Value) lkal.Value {
	switch val := v.(type) {
	case lkal.Record:
		return cloneRecord(val)
	case lkal.List:
		if val == nil {
			return lkal.List(nil)
		}
		out := make(lkal.List, len(val))
		for i, elem := range val {
			out[i] = cloneVal(elem)
		}
		return out
	case lkal.Ref:
		if val == nil {
			return lkal.Ref(nil)
		}
		out := make(lkal.Ref, len(val))
		copy(out, val)
		return out
	default:
		return v
	}
}
