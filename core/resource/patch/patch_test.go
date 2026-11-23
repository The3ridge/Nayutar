package patch_test

import (
	"bytes"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/resource/envelope"
	"github.com/Kaikei-e/Nayutar/core/resource/patch"
)

func fld(k string, v lkal.Value) lkal.Field {
	return lkal.Field{Key: k, Value: v}
}

func rec(fields ...lkal.Field) lkal.Record {
	if len(fields) == 0 {
		return lkal.Record{}
	}
	return lkal.Record{Fields: fields}
}

func mustPatchErr(t *testing.T, err error, wantKind patch.ErrorKind, wantSentinel error) {
	t.Helper()
	if err == nil {
		t.Fatalf("got nil error, want %v", wantSentinel)
	}
	if !errors.Is(err, wantSentinel) {
		t.Fatalf("errors.Is(%v, %v) = false", err, wantSentinel)
	}
	var pe *patch.Error
	if !errors.As(err, &pe) {
		t.Fatalf("error %T is not *patch.Error", err)
	}
	if pe.Kind != wantKind {
		t.Fatalf("pe.Kind = %v, want %v", pe.Kind, wantKind)
	}
}

func sampleStored() lkal.Record {
	return rec(
		fld("meta", rec(
			fld("kind", lkal.String("workload")),
			fld("name", lkal.String("web")),
			fld("labels", rec(
				fld("env", lkal.String("dev")),
				fld("tier", lkal.String("frontend")),
			)),
			fld("uid", lkal.String("01234567-89ab-4def-8123-456789abcdef")),
			fld("generation", lkal.Int(2)),
		)),
		fld("spec", rec(
			fld("replicas", lkal.Int(3)),
			fld("container", rec(
				fld("app", rec(fld("image", lkal.String("nginx:1.27")))),
				fld("sidecar", rec(fld("image", lkal.String("envoy:1.30")))),
			)),
		)),
		fld("status", rec(
			fld("observedGeneration", lkal.Int(1)),
			fld("phase", lkal.String("Running")),
			fld("condition", rec(
				fld("Ready", rec(
					fld("state", lkal.Enum("true")),
					fld("reason", lkal.String("Ready")),
					fld("since", lkal.String("2026-11-22T00:00:00Z")),
				)),
			)),
		)),
	)
}

func TestSchemaCanonicalAndValidation(t *testing.T) {
	raw := patch.SchemaBytes()
	formatted, err := lkal.Format(raw)
	if err != nil {
		t.Fatalf("lkal.Format(patch.schema.lkal): %v", err)
	}
	if !bytes.Equal(raw, formatted) {
		t.Fatalf("patch.schema.lkal is not canonical:\n--- got ---\n%s\n--- want ---\n%s", raw, formatted)
	}
	if got := patch.Schema().ID(); got != "nayutar:core/patch/0.1" {
		t.Fatalf("Schema().ID() = %q, want nayutar:core/patch/0.1", got)
	}
	if got := patch.Schema().Version(); got != "0.1.0" {
		t.Fatalf("Schema().Version() = %q, want 0.1.0", got)
	}

	validDocs := []string{
		`{"$lkal":"0.1","patch":{}}`,
		`{"$lkal":"0.1","patch":{"0":{"order":10,"op":{"$enum":"test"},"path":"meta/revision","value":4812},"1":{"order":20,"op":{"$enum":"set"},"path":"spec/replicas","value":5},"2":{"order":30,"op":{"$enum":"del"},"path":"spec/container/sidecar"}}}`,
		`{"$lkal":"0.1","patch":{"a":{"order":-5,"op":{"$enum":"set"},"path":"a/b$c/日本語","value":{"$qty":"512Mi"}}}}`,
	}
	for _, js := range validDocs {
		f, err := lkal.FromJSON([]byte(js))
		if err != nil {
			t.Fatalf("FromJSON(%s): %v", js, err)
		}
		if err := patch.Schema().Validate(f); err != nil {
			t.Fatalf("Schema().Validate(%s): %v", js, err)
		}
	}

	invalidDocs := []string{
		`{"$lkal":"0.1"}`,
		`{"$lkal":"0.1","patch":{},"extra":1}`,
		`{"$lkal":"0.1","patch":{"a":{"order":10,"op":{"$enum":"set"},"path":"x","value":1},"b":{"order":10,"op":{"$enum":"del"},"path":"y"}}}`,
		`{"$lkal":"0.1","patch":{"a":{"order":10,"op":{"$enum":"noop"},"path":"x"}}}`,
		`{"$lkal":"0.1","patch":{"a":{"order":10,"op":{"$enum":"del"},"path":""}}}`,
		`{"$lkal":"0.1","patch":{"a":{"order":10,"op":{"$enum":"del"},"path":"/a"}}}`,
		`{"$lkal":"0.1","patch":{"a":{"order":10,"op":{"$enum":"del"},"path":"a/"}}}`,
		`{"$lkal":"0.1","patch":{"a":{"order":10,"op":{"$enum":"del"},"path":"a//b"}}}`,
		`{"$lkal":"0.1","patch":{"a":{"order":10,"op":{"$enum":"del"},"path":"$a"}}}`,
		`{"$lkal":"0.1","patch":{"a":{"order":10,"op":{"$enum":"del"},"path":"a/$b"}}}`,
		`{"$lkal":"0.1","patch":{"a/b":{"order":10,"op":{"$enum":"del"},"path":"x"}}}`,
		`{"$lkal":"0.1","patch":{"a":{"order":10,"op":{"$enum":"del"},"path":"x","extra":true}}}`,
	}
	for _, js := range invalidDocs {
		f, err := lkal.FromJSON([]byte(js))
		if err != nil {
			t.Fatalf("FromJSON(%s): %v", js, err)
		}
		if err := patch.Schema().Validate(f); err == nil {
			t.Fatalf("Schema().Validate(%s) succeeded, want error", js)
		}
	}
}

func TestEnumsAndErrorFormatting(t *testing.T) {
	if got := patch.OpTest.String(); got != "test" {
		t.Fatalf("OpTest.String() = %q", got)
	}
	if got := patch.OpSet.String(); got != "set" {
		t.Fatalf("OpSet.String() = %q", got)
	}
	if got := patch.OpDel.String(); got != "del" {
		t.Fatalf("OpDel.String() = %q", got)
	}
	if got := patch.OpKind(99).String(); got != "unknown(99)" {
		t.Fatalf("OpKind(99).String() = %q", got)
	}

	if got := patch.EntranceSpec.String(); got != "spec" {
		t.Fatalf("EntranceSpec.String() = %q", got)
	}
	if got := patch.EntranceStatus.String(); got != "status" {
		t.Fatalf("EntranceStatus.String() = %q", got)
	}
	if got := patch.Entrance(99).String(); got != "unknown(99)" {
		t.Fatalf("Entrance(99).String() = %q", got)
	}

	kinds := []struct {
		k    patch.ErrorKind
		name string
		sent error
	}{
		{patch.Internal, "internal", patch.ErrInternal},
		{patch.Malformed, "malformed", patch.ErrMalformed},
		{patch.Conflict, "conflict", patch.ErrConflict},
		{patch.NotFound, "notFound", patch.ErrNotFound},
		{patch.Forbidden, "forbidden", patch.ErrForbidden},
		{patch.Observed, "observed", patch.ErrObserved},
	}
	for _, tc := range kinds {
		if tc.k.String() != tc.name {
			t.Fatalf("ErrorKind(%d).String() = %q, want %q", tc.k, tc.k.String(), tc.name)
		}
		if tc.sent.Error() != "patch: "+tc.name {
			t.Fatalf("sentinel = %q, want %q", tc.sent.Error(), "patch: "+tc.name)
		}
		e := &patch.Error{Kind: tc.k, Path: "spec/x", Detail: "bad"}
		if !errors.Is(e, tc.sent) {
			t.Fatalf("errors.Is(%v, %v) = false", e, tc.sent)
		}
		if e.Error() != "patch: "+tc.name+": spec/x: bad" {
			t.Fatalf("Error() = %q", e.Error())
		}
	}
	if got := patch.ErrorKind(99).String(); got != "unknown(99)" {
		t.Fatalf("ErrorKind(99).String() = %q", got)
	}
	var nilErr *patch.Error
	if nilErr.Error() != "<nil>" || nilErr.Unwrap() != nil {
		t.Fatalf("nil *Error = (%q, %v)", nilErr.Error(), nilErr.Unwrap())
	}
	unknownErr := &patch.Error{Kind: patch.ErrorKind(99)}
	if unknownErr.Unwrap() != nil {
		t.Fatalf("unknown ErrorKind Unwrap = %v, want nil", unknownErr.Unwrap())
	}
}

func TestValidSegAndParsePath(t *testing.T) {
	validSegs := []string{"a", "0", "a$b", "foo-bar.baz", "日本語", "x y"}
	for _, s := range validSegs {
		if !patch.ValidSeg(s) {
			t.Errorf("ValidSeg(%q) = false, want true", s)
		}
	}
	invalidSegs := []string{"", "$", "$lkal", "/", "a/b", "/a", "a/"}
	for _, s := range invalidSegs {
		if patch.ValidSeg(s) {
			t.Errorf("ValidSeg(%q) = true, want false", s)
		}
	}

	if segs, ok := patch.ParsePath("meta/labels/env"); !ok || !reflect.DeepEqual(segs, []string{"meta", "labels", "env"}) {
		t.Fatalf("ParsePath(meta/labels/env) = (%v, %v)", segs, ok)
	}
	if segs, ok := patch.ParsePath("spec"); !ok || !reflect.DeepEqual(segs, []string{"spec"}) {
		t.Fatalf("ParsePath(spec) = (%v, %v)", segs, ok)
	}
	for _, bad := range []string{"", "/", "/a", "a/", "a//b", "$a", "a/$b", "a/$/b"} {
		if segs, ok := patch.ParsePath(bad); ok || segs != nil {
			t.Errorf("ParsePath(%q) = (%v, %v), want (nil, false)", bad, segs, ok)
		}
	}
}

func TestApplySemantics(t *testing.T) {
	doc := rec(
		fld("a", lkal.Int(1)),
		fld("nested", rec(
			fld("keep", lkal.String("ok")),
			fld("drop", lkal.Bool(true)),
		)),
		fld("only", rec(fld("leaf", lkal.Int(7)))),
		fld("list", lkal.List{lkal.Int(1), lkal.Int(2)}),
	)

	// Empty ops returns an equal clone without aliasing.
	got, err := patch.Apply(doc, nil)
	if err != nil {
		t.Fatalf("Apply(nil): %v", err)
	}
	if !reflect.DeepEqual(got, doc) {
		t.Fatalf("Apply(nil) = %#v, want %#v", got, doc)
	}

	// Sequential test, set (overwrite in-place, create intermediate), del (retain empty parent).
	ops := []patch.Op{
		{Kind: patch.OpTest, Path: "a", Value: lkal.Int(1)},
		{Kind: patch.OpSet, Path: "a", Value: lkal.Int(2)},
		{Kind: patch.OpTest, Path: "a", Value: lkal.Int(2)},
		{Kind: patch.OpSet, Path: "created/deep/leaf", Value: lkal.Decimal("1.5")},
		{Kind: patch.OpDel, Path: "nested/drop"},
		{Kind: patch.OpDel, Path: "only/leaf"},
	}
	out, err := patch.Apply(doc, ops)
	if err != nil {
		t.Fatalf("Apply(ops): %v", err)
	}
	want := rec(
		fld("a", lkal.Int(2)),
		fld("nested", rec(fld("keep", lkal.String("ok")))),
		fld("only", rec()),
		fld("list", lkal.List{lkal.Int(1), lkal.Int(2)}),
		fld("created", rec(fld("deep", rec(fld("leaf", lkal.Decimal("1.5")))))),
	)
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("Apply exact order mismatch:\ngot:  %#v\nwant: %#v", out, want)
	}
	// Original doc must remain untouched.
	if v, ok := doc.Fields[0].Value.(lkal.Int); !ok || v != 1 {
		t.Fatalf("Apply mutated original doc: %#v", doc)
	}

	// Int(1) vs Decimal("1.0") in OpTest must conflict.
	_, err = patch.Apply(doc, []patch.Op{{Kind: patch.OpTest, Path: "a", Value: lkal.Decimal("1.0")}})
	mustPatchErr(t, err, patch.Conflict, patch.ErrConflict)

	// OpTest on absent path -> Conflict.
	_, err = patch.Apply(doc, []patch.Op{{Kind: patch.OpTest, Path: "missing", Value: lkal.Int(1)}})
	mustPatchErr(t, err, patch.Conflict, patch.ErrConflict)

	// OpDel on absent leaf or absent intermediate -> NotFound.
	_, err = patch.Apply(doc, []patch.Op{{Kind: patch.OpDel, Path: "missing"}})
	mustPatchErr(t, err, patch.NotFound, patch.ErrNotFound)
	_, err = patch.Apply(doc, []patch.Op{{Kind: patch.OpDel, Path: "missing/sub"}})
	mustPatchErr(t, err, patch.NotFound, patch.ErrNotFound)

	// OpSet / OpDel through non-record intermediate -> Malformed.
	_, err = patch.Apply(doc, []patch.Op{{Kind: patch.OpSet, Path: "a/sub", Value: lkal.Int(1)}})
	mustPatchErr(t, err, patch.Malformed, patch.ErrMalformed)
	_, err = patch.Apply(doc, []patch.Op{{Kind: patch.OpSet, Path: "list/0", Value: lkal.Int(9)}})
	mustPatchErr(t, err, patch.Malformed, patch.ErrMalformed)
	_, err = patch.Apply(doc, []patch.Op{{Kind: patch.OpDel, Path: "a/sub"}})
	mustPatchErr(t, err, patch.Malformed, patch.ErrMalformed)

	// Phase-1 upfront check catches malformed later op even when earlier op would conflict.
	_, err = patch.Apply(doc, []patch.Op{
		{Kind: patch.OpTest, Path: "missing", Value: lkal.Int(1)},
		{Kind: patch.OpDel, Path: "a", Value: lkal.Int(1)},
	})
	mustPatchErr(t, err, patch.Malformed, patch.ErrMalformed)

	// Duplicate keys in doc or op.Value -> Malformed.
	dupDoc := rec(fld("x", lkal.Int(1)), fld("x", lkal.Int(2)))
	_, err = patch.Apply(dupDoc, nil)
	mustPatchErr(t, err, patch.Malformed, patch.ErrMalformed)
	_, err = patch.Apply(doc, []patch.Op{{Kind: patch.OpSet, Path: "x", Value: dupDoc}})
	mustPatchErr(t, err, patch.Malformed, patch.ErrMalformed)

	// Nil value on set/test, invalid op kind, bad path -> Malformed.
	for _, bad := range []patch.Op{
		{Kind: patch.OpSet, Path: "x", Value: nil},
		{Kind: patch.OpTest, Path: "x", Value: nil},
		{Kind: patch.OpDel, Path: "x", Value: lkal.Int(1)},
		{Kind: patch.OpKind(0), Path: "x", Value: lkal.Int(1)},
		{Kind: patch.OpSet, Path: "x/$bad", Value: lkal.Int(1)},
	} {
		_, err = patch.Apply(doc, []patch.Op{bad})
		mustPatchErr(t, err, patch.Malformed, patch.ErrMalformed)
	}
}

func TestDiffSemanticsAndRoundTrip(t *testing.T) {
	old := rec(
		fld("z", lkal.Int(1)),
		fld("keep", rec(fld("b", lkal.String("same")), fld("a", lkal.Int(2)))),
		fld("nested", rec(
			fld("removed", lkal.Bool(false)),
			fld("changed", lkal.Int(1)),
			fld("emptyToNon", rec()),
			fld("nonToEmpty", rec(fld("x", lkal.Int(9)))),
		)),
		fld("labels", rec(fld("app.example.com/name", lkal.String("old")))),
		fld("drop", lkal.String("bye")),
	)
	next := rec(
		fld("keep", rec(fld("a", lkal.Int(2)), fld("b", lkal.String("same")))),
		fld("z", lkal.Int(1)),
		fld("added", lkal.Quantity{Number: "512", Unit: "Mi"}),
		fld("nested", rec(
			fld("changed", lkal.Decimal("1.0")),
			fld("emptyToNon", rec(fld("y", lkal.Enum("ready")))),
			fld("nonToEmpty", rec()),
		)),
		fld("labels", rec(fld("app.example.com/name", lkal.String("new")))),
	)

	// Identical (modulo field order) -> empty diff.
	sameOps, err := patch.Diff(old, rec(
		fld("drop", lkal.String("bye")),
		fld("labels", rec(fld("app.example.com/name", lkal.String("old")))),
		fld("nested", rec(
			fld("nonToEmpty", rec(fld("x", lkal.Int(9)))),
			fld("emptyToNon", rec()),
			fld("changed", lkal.Int(1)),
			fld("removed", lkal.Bool(false)),
		)),
		fld("keep", rec(fld("a", lkal.Int(2)), fld("b", lkal.String("same")))),
		fld("z", lkal.Int(1)),
	))
	if err != nil {
		t.Fatalf("Diff(same): %v", err)
	}
	if len(sameOps) != 0 {
		t.Fatalf("Diff(same) = %#v, want empty", sameOps)
	}

	ops, err := patch.Diff(old, next)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	// Verify deterministic sorted order: top-level dels before sets, slash-keyed labels replaced as whole record.
	wantOps := []patch.Op{
		{Kind: patch.OpDel, Path: "drop"},
		{Kind: patch.OpSet, Path: "added", Value: lkal.Quantity{Number: "512", Unit: "Mi"}},
		{Kind: patch.OpSet, Path: "labels", Value: rec(fld("app.example.com/name", lkal.String("new")))},
		{Kind: patch.OpDel, Path: "nested/removed"},
		{Kind: patch.OpSet, Path: "nested/changed", Value: lkal.Decimal("1.0")},
		{Kind: patch.OpSet, Path: "nested/emptyToNon/y", Value: lkal.Enum("ready")},
		{Kind: patch.OpDel, Path: "nested/nonToEmpty/x"},
	}
	if !reflect.DeepEqual(ops, wantOps) {
		t.Fatalf("Diff ops mismatch:\ngot:  %#v\nwant: %#v", ops, wantOps)
	}

	applied, err := patch.Apply(old, ops)
	if err != nil {
		t.Fatalf("Apply(old, Diff): %v", err)
	}
	if !lkal.Equal(applied, next) {
		t.Fatalf("Apply(old, Diff) != next:\ngot:  %#v\nwant: %#v", applied, next)
	}

	// Invalid top-level key or duplicate key -> Malformed.
	_, err = patch.Diff(rec(fld("a/b", lkal.Int(1))), rec())
	mustPatchErr(t, err, patch.Malformed, patch.ErrMalformed)
	_, err = patch.Diff(rec(), rec(fld("$x", lkal.Int(1))))
	mustPatchErr(t, err, patch.Malformed, patch.ErrMalformed)
	_, err = patch.Diff(rec(fld("a", lkal.Int(1)), fld("a", lkal.Int(2))), rec())
	mustPatchErr(t, err, patch.Malformed, patch.ErrMalformed)
}

func TestWireRoundTripAndAllLKALTypes(t *testing.T) {
	// Empty ops encodes to exact compact LKAL-JSON.
	emptyJSON, err := patch.Encode(nil)
	if err != nil {
		t.Fatalf("Encode(nil): %v", err)
	}
	if string(emptyJSON) != `{"$lkal":"0.1","patch":{}}` {
		t.Fatalf("Encode(nil) = %s, want {\"$lkal\":\"0.1\",\"patch\":{}}", emptyJSON)
	}
	decodedEmpty, err := patch.Decode(emptyJSON)
	if err != nil || len(decodedEmpty) != 0 {
		t.Fatalf("Decode(empty) = (%v, %v), want empty", decodedEmpty, err)
	}

	allOps := []patch.Op{
		{Kind: patch.OpTest, Path: "meta/revision", Value: lkal.Int(4812)},
		{Kind: patch.OpSet, Path: "spec/s", Value: lkal.String("hello")},
		{Kind: patch.OpSet, Path: "spec/d", Value: lkal.Decimal("0.25")},
		{Kind: patch.OpSet, Path: "spec/b", Value: lkal.Bool(true)},
		{Kind: patch.OpSet, Path: "spec/e", Value: lkal.Enum("tcp")},
		{Kind: patch.OpSet, Path: "spec/q", Value: lkal.Quantity{Number: "512", Unit: "Mi"}},
		{Kind: patch.OpSet, Path: "spec/r", Value: lkal.Ref{"secret", "db-creds", "spec", "password"}},
		{Kind: patch.OpSet, Path: "spec/l", Value: lkal.List{lkal.String("a"), lkal.String("b")}},
		{Kind: patch.OpSet, Path: "meta/labels", Value: rec(fld("app.example.com/name", lkal.String("web")))},
		{Kind: patch.OpDel, Path: "spec/container/sidecar"},
	}

	wireRec, err := patch.ToRecord(allOps)
	if err != nil {
		t.Fatalf("ToRecord: %v", err)
	}
	fromRec, err := patch.FromRecord(wireRec)
	if err != nil {
		t.Fatalf("FromRecord: %v", err)
	}
	if len(fromRec) != len(allOps) {
		t.Fatalf("FromRecord len = %d, want %d", len(fromRec), len(allOps))
	}
	for i := range allOps {
		if fromRec[i].Kind != allOps[i].Kind || fromRec[i].Path != allOps[i].Path || !lkal.Equal(fromRec[i].Value, allOps[i].Value) {
			t.Fatalf("FromRecord[%d] = %#v, want %#v", i, fromRec[i], allOps[i])
		}
	}

	encoded, err := patch.Encode(allOps)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !strings.HasPrefix(string(encoded), `{"$lkal":"0.1",`) || bytes.ContainsRune(encoded, '\n') {
		t.Fatalf("Encode output not compact single-line LKAL-JSON: %s", encoded)
	}
	decoded, err := patch.Decode(encoded)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(decoded) != len(allOps) {
		t.Fatalf("Decode len = %d, want %d", len(decoded), len(allOps))
	}
	for i := range allOps {
		if decoded[i].Kind != allOps[i].Kind || decoded[i].Path != allOps[i].Path || !lkal.Equal(decoded[i].Value, allOps[i].Value) {
			t.Fatalf("Decode[%d] = %#v, want %#v", i, decoded[i], allOps[i])
		}
	}

	// Out-of-order entries sorted strictly by integer order (including negative order).
	unorderedJSON := []byte(`{"$lkal":"0.1","patch":{"second":{"order":100,"op":{"$enum":"del"},"path":"spec/b"},"first":{"order":-5,"op":{"$enum":"set"},"path":"spec/a","value":1}}}`)
	sorted, err := patch.Decode(unorderedJSON)
	if err != nil {
		t.Fatalf("Decode(unordered): %v", err)
	}
	if len(sorted) != 2 || sorted[0].Path != "spec/a" || sorted[1].Path != "spec/b" {
		t.Fatalf("Decode(unordered) = %#v", sorted)
	}

	// Malformed wire inputs.
	badInputs := [][]byte{
		[]byte(`not json`),
		[]byte(`{"patch":{}}`),
		[]byte(`{"$lkal":"0.1","patch":{"0":{"order":10,"op":{"$enum":"del"},"path":"spec/a","value":1}}}`),
		[]byte(`{"$lkal":"0.1","patch":{"0":{"order":10,"op":{"$enum":"set"},"path":"spec/a"}}}`),
		[]byte(`{"$lkal":"0.1","patch":{"0":{"order":10,"op":{"$enum":"test"},"path":"spec/a"}}}`),
		[]byte(`{"$lkal":"0.1","patch":{"0":{"order":10,"op":{"$enum":"del"},"path":"spec/a"},"1":{"order":10,"op":{"$enum":"del"},"path":"spec/b"}}}`),
	}
	for _, raw := range badInputs {
		_, err := patch.Decode(raw)
		mustPatchErr(t, err, patch.Malformed, patch.ErrMalformed)
	}

	// Encode rejects non-LKAL values (e.g. mixed-type list or $-prefixed nested key).
	_, err = patch.Encode([]patch.Op{{Kind: patch.OpSet, Path: "spec/bad", Value: lkal.List{lkal.Int(1), lkal.String("x")}}})
	mustPatchErr(t, err, patch.Malformed, patch.ErrMalformed)
	_, err = patch.Encode([]patch.Op{{Kind: patch.OpSet, Path: "spec/bad", Value: rec(fld("$x", lkal.Int(1)))}})
	mustPatchErr(t, err, patch.Malformed, patch.ErrMalformed)

	// FromRecord rejects extra top-level fields, non-record patch, and extra fields in op record.
	_, err = patch.FromRecord(rec())
	mustPatchErr(t, err, patch.Malformed, patch.ErrMalformed)
	_, err = patch.FromRecord(rec(fld("patch", lkal.Int(1))))
	mustPatchErr(t, err, patch.Malformed, patch.ErrMalformed)
	_, err = patch.FromRecord(rec(fld("patch", rec(
		fld("0", rec(
			fld("order", lkal.Int(10)),
			fld("op", lkal.Enum("set")),
			fld("path", lkal.String("spec/a")),
			fld("value", lkal.Int(1)),
			fld("extra", lkal.Bool(true)),
		)),
	))))
	mustPatchErr(t, err, patch.Malformed, patch.ErrMalformed)
}

func TestApplyEntrancePatchAndPatchStatus(t *testing.T) {
	stored := sampleStored()

	// Invalid server inputs -> Internal.
	_, err := patch.ApplyEntrance(patch.Entrance(99), stored, 10, nil)
	mustPatchErr(t, err, patch.Internal, patch.ErrInternal)
	_, err = patch.ApplyEntrance(patch.EntranceSpec, rec(), 10, nil)
	mustPatchErr(t, err, patch.Internal, patch.ErrInternal)
	_, err = patch.ApplyEntrance(patch.EntranceSpec, stored, 0, nil)
	mustPatchErr(t, err, patch.Internal, patch.ErrInternal)

	// PatchStatus checks timestamp upfront BEFORE opsOk (matching Lean order).
	_, err = patch.PatchStatus(stored, 10, []patch.Op{{Kind: patch.OpKind(0), Path: ""}}, "bad-ts")
	mustPatchErr(t, err, patch.Internal, patch.ErrInternal)

	// Test meta/revision, meta/uid, spec, status all readable by OpTest in both entrances.
	testOps := []patch.Op{
		{Kind: patch.OpTest, Path: "meta/revision", Value: lkal.Int(42)},
		{Kind: patch.OpTest, Path: "meta/uid", Value: lkal.String("01234567-89ab-4def-8123-456789abcdef")},
		{Kind: patch.OpTest, Path: "spec/replicas", Value: lkal.Int(3)},
		{Kind: patch.OpTest, Path: "status/phase", Value: lkal.String("Running")},
	}
	if _, err := patch.Patch(stored, 42, testOps); err != nil {
		t.Fatalf("Patch(testOps): %v", err)
	}
	if _, err := patch.PatchStatus(stored, 42, testOps, "2026-11-22T01:00:00Z"); err != nil {
		t.Fatalf("PatchStatus(testOps): %v", err)
	}
	// Wrong revision in OpTest -> Conflict.
	_, err = patch.Patch(stored, 42, []patch.Op{{Kind: patch.OpTest, Path: "meta/revision", Value: lkal.Int(41)}})
	mustPatchErr(t, err, patch.Conflict, patch.ErrConflict)

	// EntranceSpec: labels-only change preserves generation; spec change bumps generation.
	afterLabels, err := patch.Patch(stored, 42, []patch.Op{
		{Kind: patch.OpSet, Path: "meta/labels/env", Value: lkal.String("prod")},
		{Kind: patch.OpDel, Path: "meta/labels/tier"},
	})
	if err != nil {
		t.Fatalf("Patch(labels): %v", err)
	}
	if err := envelope.CheckStored(afterLabels); err != nil {
		t.Fatalf("CheckStored(afterLabels): %v", err)
	}
	if gen := getInt(t, afterLabels, "meta", "generation"); gen != 2 {
		t.Fatalf("labels-only generation = %d, want 2", gen)
	}

	afterSpec, err := patch.Patch(stored, 42, []patch.Op{
		{Kind: patch.OpTest, Path: "meta/revision", Value: lkal.Int(42)},
		{Kind: patch.OpSet, Path: "spec/replicas", Value: lkal.Int(5)},
		{Kind: patch.OpDel, Path: "spec/container/sidecar"},
	})
	if err != nil {
		t.Fatalf("Patch(spec): %v", err)
	}
	if err := envelope.CheckStored(afterSpec); err != nil {
		t.Fatalf("CheckStored(afterSpec): %v", err)
	}
	if gen := getInt(t, afterSpec, "meta", "generation"); gen != 3 {
		t.Fatalf("spec patch generation = %d, want 3", gen)
	}

	// EntranceSpec forbidden paths (including del status / del meta/uid so nothing is silently ignored!).
	specForbidden := []patch.Op{
		{Kind: patch.OpSet, Path: "meta", Value: rec()},
		{Kind: patch.OpDel, Path: "meta"},
		{Kind: patch.OpSet, Path: "meta/uid", Value: lkal.String("11111111-1111-4111-8111-111111111111")},
		{Kind: patch.OpDel, Path: "meta/uid"},
		{Kind: patch.OpSet, Path: "meta/revision", Value: lkal.Int(99)},
		{Kind: patch.OpDel, Path: "meta/revision"},
		{Kind: patch.OpSet, Path: "meta/generation", Value: lkal.Int(99)},
		{Kind: patch.OpDel, Path: "meta/generation"},
		{Kind: patch.OpSet, Path: "meta/kind", Value: lkal.String("other")},
		{Kind: patch.OpSet, Path: "meta/name", Value: lkal.String("other")},
		{Kind: patch.OpSet, Path: "meta/group", Value: lkal.String("apps.example.com")},
		{Kind: patch.OpSet, Path: "status/phase", Value: lkal.String("Failed")},
		{Kind: patch.OpDel, Path: "status"},
		{Kind: patch.OpDel, Path: "status/phase"},
	}
	for _, op := range specForbidden {
		_, err := patch.Patch(stored, 42, []patch.Op{op})
		mustPatchErr(t, err, patch.Forbidden, patch.ErrForbidden)
	}

	// Unknown top-level section -> Malformed in both entrances.
	for _, op := range []patch.Op{
		{Kind: patch.OpSet, Path: "unknown/x", Value: lkal.Int(1)},
		{Kind: patch.OpDel, Path: "unknown"},
	} {
		_, err := patch.Patch(stored, 42, []patch.Op{op})
		mustPatchErr(t, err, patch.Malformed, patch.ErrMalformed)
		_, err = patch.PatchStatus(stored, 42, []patch.Op{op}, "2026-11-22T01:00:00Z")
		mustPatchErr(t, err, patch.Malformed, patch.ErrMalformed)
	}

	// EntranceStatus: forbidden paths (meta, spec, condition/<T>/since).
	statusForbidden := []patch.Op{
		{Kind: patch.OpSet, Path: "meta/labels/env", Value: lkal.String("prod")},
		{Kind: patch.OpDel, Path: "meta/labels"},
		{Kind: patch.OpSet, Path: "spec/replicas", Value: lkal.Int(9)},
		{Kind: patch.OpDel, Path: "spec"},
		{Kind: patch.OpSet, Path: "status/condition/Ready/since", Value: lkal.String("2026-11-22T01:00:00Z")},
		{Kind: patch.OpDel, Path: "status/condition/Ready/since"},
		{Kind: patch.OpSet, Path: "status/condition/Ready/since/nested", Value: lkal.Int(1)},
		// Whole-condition set with mismatched since caught by envelope.UpdateStatus -> Forbidden.
		{Kind: patch.OpSet, Path: "status/condition/Ready", Value: rec(
			fld("state", lkal.Enum("true")),
			fld("reason", lkal.String("Ready")),
			fld("since", lkal.String("2026-11-22T01:00:00Z")),
		)},
	}
	for _, op := range statusForbidden {
		_, err := patch.PatchStatus(stored, 42, []patch.Op{op}, "2026-11-22T01:00:00Z")
		mustPatchErr(t, err, patch.Forbidden, patch.ErrForbidden)
	}

	// EntranceStatus: condition reason-only change keeps since; state change updates since to now.
	stSameState, err := patch.PatchStatus(stored, 42, []patch.Op{
		{Kind: patch.OpSet, Path: "status/observedGeneration", Value: lkal.Int(2)},
		{Kind: patch.OpSet, Path: "status/condition/Ready/reason", Value: lkal.String("AllReady")},
	}, "2026-11-22T01:00:00Z")
	if err != nil {
		t.Fatalf("PatchStatus(same state): %v", err)
	}
	if err := envelope.CheckStored(stSameState); err != nil {
		t.Fatalf("CheckStored(stSameState): %v", err)
	}
	if got := getString(t, stSameState, "status", "condition", "Ready", "since"); got != "2026-11-22T00:00:00Z" {
		t.Fatalf("same-state since = %q, want 2026-11-22T00:00:00Z", got)
	}

	stChangedState, err := patch.PatchStatus(stored, 42, []patch.Op{
		{Kind: patch.OpSet, Path: "status/condition/Ready/state", Value: lkal.Enum("false")},
	}, "2026-11-22T02:00:00Z")
	if err != nil {
		t.Fatalf("PatchStatus(changed state): %v", err)
	}
	if got := getString(t, stChangedState, "status", "condition", "Ready", "since"); got != "2026-11-22T02:00:00Z" {
		t.Fatalf("changed-state since = %q, want 2026-11-22T02:00:00Z", got)
	}

	// ObservedGeneration errors -> Observed.
	_, err = patch.PatchStatus(stored, 42, []patch.Op{{Kind: patch.OpSet, Path: "status/observedGeneration", Value: lkal.Int(3)}}, "2026-11-22T02:00:00Z")
	mustPatchErr(t, err, patch.Observed, patch.ErrObserved)
	_, err = patch.PatchStatus(stored, 42, []patch.Op{{Kind: patch.OpDel, Path: "status/observedGeneration"}}, "2026-11-22T02:00:00Z")
	mustPatchErr(t, err, patch.Observed, patch.ErrObserved)
	_, err = patch.PatchStatus(stored, 42, []patch.Op{{Kind: patch.OpDel, Path: "status"}}, "2026-11-22T02:00:00Z")
	mustPatchErr(t, err, patch.Observed, patch.ErrObserved)

	// Generation overflow -> Internal.
	maxGenDoc := rec(
		fld("meta", rec(
			fld("kind", lkal.String("workload")),
			fld("name", lkal.String("maxgen")),
			fld("uid", lkal.String("01234567-89ab-4def-8123-456789abcdef")),
			fld("generation", lkal.Int(math.MaxInt64)),
		)),
	)
	_, err = patch.Patch(maxGenDoc, 1, []patch.Op{{Kind: patch.OpSet, Path: "spec/replicas", Value: lkal.Int(1)}})
	mustPatchErr(t, err, patch.Internal, patch.ErrInternal)
}

func getInt(t *testing.T, doc lkal.Record, path ...string) int64 {
	t.Helper()
	var cur lkal.Value = doc
	for _, seg := range path {
		r, ok := cur.(lkal.Record)
		if !ok {
			t.Fatalf("segment %q parent not record", seg)
		}
		found := false
		for _, f := range r.Fields {
			if f.Key == seg {
				cur = f.Value
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("segment %q not found", seg)
		}
	}
	n, ok := cur.(lkal.Int)
	if !ok {
		t.Fatalf("value at %v is %T, want lkal.Int", path, cur)
	}
	return int64(n)
}

func getString(t *testing.T, doc lkal.Record, path ...string) string {
	t.Helper()
	var cur lkal.Value = doc
	for _, seg := range path {
		r, ok := cur.(lkal.Record)
		if !ok {
			t.Fatalf("segment %q parent not record", seg)
		}
		found := false
		for _, f := range r.Fields {
			if f.Key == seg {
				cur = f.Value
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("segment %q not found", seg)
		}
	}
	s, ok := cur.(lkal.String)
	if !ok {
		t.Fatalf("value at %v is %T, want lkal.String", path, cur)
	}
	return string(s)
}
