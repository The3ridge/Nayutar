package envelope_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/resource/envelope"
)

func FuzzTransitions(f *testing.F) {
	f.Add([]byte("seed-valid-1"), []byte("seed-valid-2"))
	f.Add([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, []byte{9, 8, 7, 6, 5, 4, 3, 2, 1, 0})
	f.Add([]byte{}, []byte{})
	f.Add([]byte{0xff, 0x00, 0x10, 0x20, 0x30}, []byte{0x40, 0x50, 0x60})

	f.Fuzz(func(t *testing.T, a, b []byte) {
		rA := newByteReader(a)
		rB := newByteReader(b)

		docA := decodeEnvelopeRecord(rA, 0)
		docB := decodeEnvelopeRecord(rB, 0)
		uid := decodeString(rA, testUID1)
		now := decodeString(rB, testTS1)
		rev := int64(rA.u8()) - 2

		snapA := deepCopyRecord(docA)
		snapB := deepCopyRecord(docB)

		shapeErr := envelope.CheckShape(docA)
		if shapeErr != nil {
			assertIsEnvelopeError(t, shapeErr)
		}
		storedErr := envelope.CheckStored(docA)
		if storedErr != nil {
			assertIsEnvelopeError(t, storedErr)
		}
		if _, err := envelope.Object(docA); err != nil {
			assertIsEnvelopeError(t, err)
		}
		_ = envelope.WithRevision(docA, rev)

		if created, err := envelope.Create(docA, uid); err != nil {
			assertIsEnvelopeError(t, err)
		} else {
			if err := envelope.CheckStored(created); err != nil {
				t.Fatalf("Create result failed CheckStored: %v", err)
			}
		}

		if updated, err := envelope.Update(docA, rev, docB); err != nil {
			assertIsEnvelopeError(t, err)
		} else {
			if err := envelope.CheckStored(updated); err != nil {
				t.Fatalf("Update result failed CheckStored: %v", err)
			}
		}

		if stUpdated, err := envelope.UpdateStatus(docA, rev, docB, now); err != nil {
			assertIsEnvelopeError(t, err)
		} else {
			if err := envelope.CheckStored(stUpdated); err != nil {
				t.Fatalf("UpdateStatus result failed CheckStored: %v", err)
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

func assertIsEnvelopeError(t *testing.T, err error) {
	t.Helper()
	var envErr *envelope.Error
	if !errors.As(err, &envErr) {
		t.Fatalf("error %T (%v) is not *envelope.Error", err, err)
	}
	if !errors.Is(err, envelope.ErrInternal) &&
		!errors.Is(err, envelope.ErrMalformed) &&
		!errors.Is(err, envelope.ErrConflict) &&
		!errors.Is(err, envelope.ErrForbidden) &&
		!errors.Is(err, envelope.ErrObserved) {
		t.Fatalf("unknown error kind: %v", envErr.Kind)
	}
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

func (r *byteReader) str(maxLen int) string {
	n := int(r.u8()) % (maxLen + 1)
	if n == 0 {
		return ""
	}
	out := make([]byte, n)
	for i := range out {
		out[i] = r.u8()
	}
	return string(out)
}

var vocab = []string{
	"meta", "spec", "status", "group", "kind", "name", "labels",
	"uid", "revision", "generation", "observedGeneration", "condition",
	"state", "reason", "since", "core", "workload", "web", "apps.example.com",
	"Ready", "true", "false", "unknown", "Ok", testUID1, testTS1, testTS2,
}

func decodeString(r *byteReader, fallback string) string {
	sel := r.u8()
	if sel == 0 {
		return fallback
	}
	if int(sel) <= len(vocab) {
		return vocab[sel-1]
	}
	return r.str(40)
}

func decodeEnvelopeRecord(r *byteReader, depth int) lkal.Record {
	n := int(r.u8()) % 6
	fields := make([]lkal.Field, n)
	for i := range n {
		k := decodeString(r, "meta")
		fields[i] = lkal.Field{Key: k, Value: decodeValue(r, depth+1)}
	}
	return lkal.Record{Fields: fields}
}

func decodeValue(r *byteReader, depth int) lkal.Value {
	tag := r.u8() % 9
	if depth >= 4 && tag >= 7 {
		tag %= 7
	}
	switch tag {
	case 0:
		return lkal.String(decodeString(r, "web"))
	case 1:
		return lkal.Int(int64(r.u8()) - 1)
	case 2:
		return lkal.Decimal("1.0")
	case 3:
		return lkal.Bool(r.u8()%2 == 0)
	case 4:
		return lkal.Enum(decodeString(r, "true"))
	case 5:
		return lkal.Quantity{Number: "1", Unit: "m"}
	case 6:
		return lkal.Ref{decodeString(r, "pod"), decodeString(r, "web")}
	case 7:
		n := int(r.u8()) % 4
		items := make(lkal.List, n)
		for i := range n {
			items[i] = decodeValue(r, depth+1)
		}
		return items
	default:
		return decodeEnvelopeRecord(r, depth+1)
	}
}
