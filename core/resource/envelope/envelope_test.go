package envelope_test

import (
	"errors"
	"math"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/resource/envelope"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
)

const (
	testUID1 = "01234567-89ab-cdef-0123-456789abcdef"
	testUID2 = "fedcba98-7654-3210-fedc-ba9876543210"
	testTS1  = "2026-01-02T03:04:05Z"
	testTS2  = "2026-06-15T12:30:45Z"
)

func rec(fields ...lkal.Field) lkal.Record {
	return lkal.Record{Fields: fields}
}

func f(k string, v lkal.Value) lkal.Field {
	return lkal.Field{Key: k, Value: v}
}

func assertErrorKind(t *testing.T, err error, want error) {
	t.Helper()
	if want == nil {
		if err != nil {
			t.Fatalf("got unexpected error: %v", err)
		}
		return
	}
	if err == nil {
		t.Fatalf("got nil error, want %v", want)
	}
	var envErr *envelope.Error
	if !errors.As(err, &envErr) {
		t.Fatalf("error %T (%v) is not *envelope.Error", err, err)
	}
	if !errors.Is(err, want) {
		t.Fatalf("got error %v (kind %v), want %v", err, envErr.Kind, want)
	}
}

func baseCand() lkal.Record {
	return rec(
		f("meta", rec(
			f("kind", lkal.String("workload")),
			f("name", lkal.String("web")),
		)),
		f("spec", rec(
			f("replicas", lkal.Int(3)),
		)),
	)
}

func baseStored() lkal.Record {
	return rec(
		f("meta", rec(
			f("kind", lkal.String("workload")),
			f("name", lkal.String("web")),
			f("uid", lkal.String(testUID1)),
			f("generation", lkal.Int(1)),
		)),
		f("spec", rec(
			f("replicas", lkal.Int(3)),
		)),
	)
}

func TestErrorMethodsAndTimestamp(t *testing.T) {
	var nilErr *envelope.Error
	if nilErr.Unwrap() != nil {
		t.Fatalf("nil Error.Unwrap() = %v, want nil", nilErr.Unwrap())
	}
	if nilErr.Error() == "" {
		t.Fatal("nil Error.Error() is empty")
	}
	e := &envelope.Error{Kind: envelope.ErrForbidden, Path: "meta/uid", Detail: "cannot change"}
	if !errors.Is(e, envelope.ErrForbidden) {
		t.Fatalf("errors.Is failed")
	}
	if !strings.Contains(e.Error(), "meta/uid") || !strings.Contains(e.Error(), "cannot change") {
		t.Fatalf("unexpected Error() string: %q", e.Error())
	}
	noPath := &envelope.Error{Kind: envelope.ErrConflict}
	if noPath.Error() != envelope.ErrConflict.Error() {
		t.Fatalf("unexpected Error() without path/detail: %q", noPath.Error())
	}

	loc := time.FixedZone("JST", 9*3600)
	ts := envelope.Timestamp(time.Date(2026, 11, 21, 1, 2, 3, 999, loc))
	if ts != "2026-11-20T16:02:03Z" {
		t.Fatalf("Timestamp = %q, want 2026-11-20T16:02:03Z", ts)
	}
}

func TestCheckShapeAndObject(t *testing.T) {
	cases := []struct {
		name      string
		doc       lkal.Record
		wantErr   error
		wantObj   keyspace.Object
		checkBoth bool
	}{
		{
			name: "valid core minimal",
			doc: rec(
				f("meta", rec(f("kind", lkal.String("pod")), f("name", lkal.String("p1")))),
			),
			wantObj: keyspace.Object{Group: "core", Kind: "pod", Name: "p1"},
		},
		{
			name: "valid grouped with all fields",
			doc: rec(
				f("meta", rec(
					f("group", lkal.String("apps.example.com")),
					f("kind", lkal.String("deployment")),
					f("name", lkal.String("api.v1")),
					f("labels", rec(f("env", lkal.String("prod")))),
					f("uid", lkal.String(testUID1)),
					f("revision", lkal.Int(5)),
					f("generation", lkal.Int(2)),
				)),
				f("spec", rec(
					f("ratio", lkal.Decimal("1.5")),
					f("enabled", lkal.Bool(true)),
					f("mode", lkal.Enum("fast")),
					f("mem", lkal.Quantity{Number: "512", Unit: "Mi"}),
					f("target", lkal.Ref{"secret", "db"}),
					f("items", lkal.List{rec(f("k", lkal.Int(1))), lkal.String("x")}),
				)),
				f("status", rec(
					f("observedGeneration", lkal.Int(2)),
					f("phase", lkal.String("Running")),
					f("condition", rec(
						f("Ready", rec(
							f("state", lkal.Enum("true")),
							f("reason", lkal.String("AllReady:ok,v1_0")),
							f("since", lkal.String(testTS1)),
						)),
					)),
				)),
			),
			wantObj: keyspace.Object{Group: "apps.example.com", Kind: "deployment", Name: "api.v1"},
		},
		{
			name:    "S1 duplicate top-level key",
			doc:     rec(f("meta", rec(f("kind", lkal.String("pod")), f("name", lkal.String("a")))), f("meta", rec())),
			wantErr: envelope.ErrMalformed,
		},
		{
			name: "S1 duplicate nested key inside list record",
			doc: rec(
				f("meta", rec(f("kind", lkal.String("pod")), f("name", lkal.String("a")))),
				f("spec", rec(f("items", lkal.List{rec(f("x", lkal.Int(1)), f("x", lkal.Int(2)))}))),
			),
			wantErr: envelope.ErrMalformed,
		},
		{
			name: "S1 nil value inside spec record",
			doc: rec(
				f("meta", rec(f("kind", lkal.String("pod")), f("name", lkal.String("a")))),
				f("spec", rec(f("x", nil))),
			),
			wantErr: envelope.ErrMalformed,
		},
		{
			name: "S1 nil value inside status list",
			doc: rec(
				f("meta", rec(f("kind", lkal.String("pod")), f("name", lkal.String("a")))),
				f("status", rec(f("extra", lkal.List{nil}))),
			),
			wantErr: envelope.ErrMalformed,
		},
		{
			name: "S2 unexpected top-level key",
			doc: rec(
				f("meta", rec(f("kind", lkal.String("pod")), f("name", lkal.String("a")))),
				f("data", rec()),
			),
			wantErr: envelope.ErrMalformed,
		},
		{
			name:    "S2 non-record meta",
			doc:     rec(f("meta", lkal.String("bad"))),
			wantErr: envelope.ErrMalformed,
		},
		{
			name: "S2 non-record spec",
			doc: rec(
				f("meta", rec(f("kind", lkal.String("pod")), f("name", lkal.String("a")))),
				f("spec", lkal.Int(1)),
			),
			wantErr: envelope.ErrMalformed,
		},
		{
			name: "S2 non-record status",
			doc: rec(
				f("meta", rec(f("kind", lkal.String("pod")), f("name", lkal.String("a")))),
				f("status", lkal.List{}),
			),
			wantErr: envelope.ErrMalformed,
		},
		{
			name:    "S2 missing meta",
			doc:     rec(f("spec", rec())),
			wantErr: envelope.ErrMalformed,
		},
		{
			name: "S3 unexpected meta key",
			doc: rec(f("meta", rec(
				f("kind", lkal.String("pod")),
				f("name", lkal.String("a")),
				f("namespace", lkal.String("default")),
			))),
			wantErr: envelope.ErrMalformed,
		},
		{
			name: "S3 group equals core",
			doc: rec(f("meta", rec(
				f("group", lkal.String("core")),
				f("kind", lkal.String("pod")),
				f("name", lkal.String("a")),
			))),
			wantErr: envelope.ErrMalformed,
		},
		{
			name: "S3 group without dot",
			doc: rec(f("meta", rec(
				f("group", lkal.String("apps")),
				f("kind", lkal.String("pod")),
				f("name", lkal.String("a")),
			))),
			wantErr: envelope.ErrMalformed,
		},
		{
			name: "S3 non-string group",
			doc: rec(f("meta", rec(
				f("group", lkal.Int(1)),
				f("kind", lkal.String("pod")),
				f("name", lkal.String("a")),
			))),
			wantErr: envelope.ErrMalformed,
		},
		{
			name:    "S3 missing kind",
			doc:     rec(f("meta", rec(f("name", lkal.String("a"))))),
			wantErr: envelope.ErrMalformed,
		},
		{
			name:    "S3 non-string kind",
			doc:     rec(f("meta", rec(f("kind", lkal.Int(1)), f("name", lkal.String("a"))))),
			wantErr: envelope.ErrMalformed,
		},
		{
			name:    "S3 invalid kind uppercase",
			doc:     rec(f("meta", rec(f("kind", lkal.String("Pod")), f("name", lkal.String("a"))))),
			wantErr: envelope.ErrMalformed,
		},
		{
			name:    "S3 missing name",
			doc:     rec(f("meta", rec(f("kind", lkal.String("pod"))))),
			wantErr: envelope.ErrMalformed,
		},
		{
			name:    "S3 invalid name",
			doc:     rec(f("meta", rec(f("kind", lkal.String("pod")), f("name", lkal.String("-bad"))))),
			wantErr: envelope.ErrMalformed,
		},
		{
			name: "S3 non-record labels",
			doc: rec(f("meta", rec(
				f("kind", lkal.String("pod")),
				f("name", lkal.String("a")),
				f("labels", lkal.String("bad")),
			))),
			wantErr: envelope.ErrMalformed,
		},
		{
			name: "S3 non-string label value",
			doc: rec(f("meta", rec(
				f("kind", lkal.String("pod")),
				f("name", lkal.String("a")),
				f("labels", rec(f("k", lkal.Int(1)))),
			))),
			wantErr: envelope.ErrMalformed,
		},
		{
			name: "S3 revision zero",
			doc: rec(f("meta", rec(
				f("kind", lkal.String("pod")),
				f("name", lkal.String("a")),
				f("revision", lkal.Int(0)),
			))),
			wantErr: envelope.ErrMalformed,
		},
		{
			name: "S3 generation zero",
			doc: rec(f("meta", rec(
				f("kind", lkal.String("pod")),
				f("name", lkal.String("a")),
				f("generation", lkal.Int(0)),
			))),
			wantErr: envelope.ErrMalformed,
		},
		{
			name: "S4 observedGeneration zero",
			doc: rec(
				f("meta", rec(f("kind", lkal.String("pod")), f("name", lkal.String("a")))),
				f("status", rec(f("observedGeneration", lkal.Int(0)))),
			),
			wantErr: envelope.ErrMalformed,
		},
		{
			name: "S4 non-record condition",
			doc: rec(
				f("meta", rec(f("kind", lkal.String("pod")), f("name", lkal.String("a")))),
				f("status", rec(f("condition", lkal.List{}))),
			),
			wantErr: envelope.ErrMalformed,
		},
		{
			name: "S4 non-record condition entry",
			doc: rec(
				f("meta", rec(f("kind", lkal.String("pod")), f("name", lkal.String("a")))),
				f("status", rec(f("condition", rec(f("Ready", lkal.Bool(true)))))),
			),
			wantErr: envelope.ErrMalformed,
		},
		{
			name: "S4 unexpected condition field",
			doc: rec(
				f("meta", rec(f("kind", lkal.String("pod")), f("name", lkal.String("a")))),
				f("status", rec(f("condition", rec(f("Ready", rec(
					f("state", lkal.Enum("true")),
					f("reason", lkal.String("Ok")),
					f("message", lkal.String("extra")),
				)))))),
			),
			wantErr: envelope.ErrMalformed,
		},
		{
			name: "S4 state string instead of enum",
			doc: rec(
				f("meta", rec(f("kind", lkal.String("pod")), f("name", lkal.String("a")))),
				f("status", rec(f("condition", rec(f("Ready", rec(
					f("state", lkal.String("true")),
					f("reason", lkal.String("Ok")),
				)))))),
			),
			wantErr: envelope.ErrMalformed,
		},
		{
			name: "S4 state invalid enum name",
			doc: rec(
				f("meta", rec(f("kind", lkal.String("pod")), f("name", lkal.String("a")))),
				f("status", rec(f("condition", rec(f("Ready", rec(
					f("state", lkal.Enum("maybe")),
					f("reason", lkal.String("Ok")),
				)))))),
			),
			wantErr: envelope.ErrMalformed,
		},
		{
			name: "S4 missing state",
			doc: rec(
				f("meta", rec(f("kind", lkal.String("pod")), f("name", lkal.String("a")))),
				f("status", rec(f("condition", rec(f("Ready", rec(
					f("reason", lkal.String("Ok")),
				)))))),
			),
			wantErr: envelope.ErrMalformed,
		},
		{
			name: "S4 missing reason",
			doc: rec(
				f("meta", rec(f("kind", lkal.String("pod")), f("name", lkal.String("a")))),
				f("status", rec(f("condition", rec(f("Ready", rec(
					f("state", lkal.Enum("unknown")),
				)))))),
			),
			wantErr: envelope.ErrMalformed,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := envelope.CheckShape(tc.doc)
			assertErrorKind(t, err, tc.wantErr)
			obj, objErr := envelope.Object(tc.doc)
			assertErrorKind(t, objErr, tc.wantErr)
			if tc.wantErr == nil && obj != tc.wantObj {
				t.Fatalf("Object = %+v, want %+v", obj, tc.wantObj)
			}
		})
	}
}

func TestStringPredicatesEdgeCases(t *testing.T) {
	uidCases := []struct {
		uid   string
		valid bool
	}{
		{testUID1, true},
		{"00000000-0000-0000-0000-000000000000", true},
		{"ffffffff-ffff-ffff-ffff-ffffffffffff", true},
		{"01234567-89ab-cdef-0123-456789abcde", false},   // 35 bytes
		{"01234567-89ab-cdef-0123-456789abcdef0", false}, // 37 bytes
		{"01234567-89AB-cdef-0123-456789abcdef", false},  // uppercase
		{"01234567_89ab-cdef-0123-456789abcdef", false},  // wrong separator at 8
		{"01234567-89ab_cdef-0123-456789abcdef", false},  // wrong separator at 13
		{"01234567-89ab-cdef_0123-456789abcdef", false},  // wrong separator at 18
		{"01234567-89ab-cdef-0123_456789abcdef", false},  // wrong separator at 23
		{"01234567-89ab-cdeg-0123-456789abcdef", false},  // non-hex 'g'
		{"01234567-89ab-cdef--123-456789abcdef", false},  // extra dash
	}
	for _, tc := range uidCases {
		doc := rec(f("meta", rec(
			f("kind", lkal.String("pod")),
			f("name", lkal.String("a")),
			f("uid", lkal.String(tc.uid)),
		)))
		err := envelope.CheckShape(doc)
		if tc.valid {
			assertErrorKind(t, err, nil)
		} else {
			assertErrorKind(t, err, envelope.ErrMalformed)
		}
		_, cErr := envelope.Create(baseCand(), tc.uid)
		if tc.valid {
			assertErrorKind(t, cErr, nil)
		} else {
			assertErrorKind(t, cErr, envelope.ErrInternal)
		}
	}

	condTypeCases := []struct {
		ct    string
		valid bool
	}{
		{"A", true},
		{"Ready", true},
		{"Ready2Go", true},
		{strings.Repeat("A", 63), true},
		{"", false},
		{"a", false},
		{"1Ready", false},
		{"Ready_1", false},
		{"Ready-1", false},
		{strings.Repeat("A", 64), false},
	}
	for _, tc := range condTypeCases {
		doc := rec(
			f("meta", rec(f("kind", lkal.String("pod")), f("name", lkal.String("a")))),
			f("status", rec(f("condition", rec(f(tc.ct, rec(
				f("state", lkal.Enum("true")),
				f("reason", lkal.String("Ok")),
			)))))),
		)
		err := envelope.CheckShape(doc)
		if tc.valid {
			assertErrorKind(t, err, nil)
		} else {
			assertErrorKind(t, err, envelope.ErrMalformed)
		}
	}

	reasonCases := []struct {
		reason string
		valid  bool
	}{
		{"a", true},
		{"A", true},
		{"a_", true},
		{"A1:b,c_2", true},
		{strings.Repeat("a", 1024), true},
		{"", false},
		{"1abc", false},
		{"_abc", false},
		{":abc", false},
		{",abc", false},
		{"abc:", false},
		{"abc,", false},
		{"abc-def", false},
		{"abc def", false},
		{strings.Repeat("a", 1025), false},
	}
	for _, tc := range reasonCases {
		doc := rec(
			f("meta", rec(f("kind", lkal.String("pod")), f("name", lkal.String("a")))),
			f("status", rec(f("condition", rec(f("Ready", rec(
				f("state", lkal.Enum("true")),
				f("reason", lkal.String(tc.reason)),
			)))))),
		)
		err := envelope.CheckShape(doc)
		if tc.valid {
			assertErrorKind(t, err, nil)
		} else {
			assertErrorKind(t, err, envelope.ErrMalformed)
		}
	}

	tsCases := []struct {
		ts    string
		valid bool
	}{
		{"0000-01-01T00:00:00Z", true},
		{"9999-12-31T23:59:59Z", true},
		{"2026-02-31T23:59:59Z", true}, // no calendar check beyond range
		{"2026-00-01T00:00:00Z", false},
		{"2026-13-01T00:00:00Z", false},
		{"2026-01-00T00:00:00Z", false},
		{"2026-01-32T00:00:00Z", false},
		{"2026-01-01T24:00:00Z", false},
		{"2026-01-01T00:60:00Z", false},
		{"2026-01-01T00:00:60Z", false},
		{"2026-01-01t00:00:00Z", false},
		{"2026-01-01T00:00:00z", false},
		{"2026-01-01T00:00:00+00:00", false},
		{"2026-1-01T00:00:00Z", false},
		{"2026/01/01T00:00:00Z", false},
	}
	for _, tc := range tsCases {
		doc := rec(
			f("meta", rec(f("kind", lkal.String("pod")), f("name", lkal.String("a")))),
			f("status", rec(f("condition", rec(f("Ready", rec(
				f("state", lkal.Enum("true")),
				f("reason", lkal.String("Ok")),
				f("since", lkal.String(tc.ts)),
			)))))),
		)
		err := envelope.CheckShape(doc)
		if tc.valid {
			assertErrorKind(t, err, nil)
		} else {
			assertErrorKind(t, err, envelope.ErrMalformed)
		}
		_, uErr := envelope.UpdateStatus(baseStored(), 1, baseCand(), tc.ts)
		if tc.valid {
			assertErrorKind(t, uErr, nil)
		} else {
			assertErrorKind(t, uErr, envelope.ErrInternal)
		}
	}
}

func TestCheckStored(t *testing.T) {
	cases := []struct {
		name    string
		doc     lkal.Record
		wantErr error
	}{
		{name: "valid stored", doc: baseStored()},
		{
			name:    "shape invalid maps to ErrInternal",
			doc:     rec(f("meta", rec(f("kind", lkal.String("Pod")), f("name", lkal.String("a")), f("uid", lkal.String(testUID1)), f("generation", lkal.Int(1))))),
			wantErr: envelope.ErrInternal,
		},
		{
			name:    "missing uid",
			doc:     rec(f("meta", rec(f("kind", lkal.String("pod")), f("name", lkal.String("a")), f("generation", lkal.Int(1))))),
			wantErr: envelope.ErrInternal,
		},
		{
			name:    "missing generation",
			doc:     rec(f("meta", rec(f("kind", lkal.String("pod")), f("name", lkal.String("a")), f("uid", lkal.String(testUID1))))),
			wantErr: envelope.ErrInternal,
		},
		{
			name:    "revision present",
			doc:     envelope.WithRevision(baseStored(), 3),
			wantErr: envelope.ErrInternal,
		},
		{
			name: "condition missing since",
			doc: rec(
				f("meta", rec(f("kind", lkal.String("pod")), f("name", lkal.String("a")), f("uid", lkal.String(testUID1)), f("generation", lkal.Int(1)))),
				f("status", rec(f("condition", rec(f("Ready", rec(f("state", lkal.Enum("true")), f("reason", lkal.String("Ok")))))))),
			),
			wantErr: envelope.ErrInternal,
		},
		{
			name: "observedGeneration exceeds generation",
			doc: rec(
				f("meta", rec(f("kind", lkal.String("pod")), f("name", lkal.String("a")), f("uid", lkal.String(testUID1)), f("generation", lkal.Int(1)))),
				f("status", rec(f("observedGeneration", lkal.Int(2)))),
			),
			wantErr: envelope.ErrInternal,
		},
		{
			name: "observedGeneration equals generation",
			doc: rec(
				f("meta", rec(f("kind", lkal.String("pod")), f("name", lkal.String("a")), f("uid", lkal.String(testUID1)), f("generation", lkal.Int(2)))),
				f("status", rec(f("observedGeneration", lkal.Int(2)))),
			),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertErrorKind(t, envelope.CheckStored(tc.doc), tc.wantErr)
		})
	}
}

func TestCreateRulesAndOrder(t *testing.T) {
	cases := []struct {
		name    string
		cand    lkal.Record
		uid     string
		wantErr error
		want    lkal.Record
	}{
		{
			name: "valid minimal without spec",
			cand: rec(f("meta", rec(f("name", lkal.String("web")), f("kind", lkal.String("workload"))))),
			uid:  testUID1,
			want: rec(f("meta", rec(
				f("kind", lkal.String("workload")),
				f("name", lkal.String("web")),
				f("uid", lkal.String(testUID1)),
				f("generation", lkal.Int(1)),
			))),
		},
		{
			name: "valid with group labels and spec in canonical meta order",
			cand: rec(
				f("spec", rec(f("replicas", lkal.Int(2)))),
				f("meta", rec(
					f("labels", rec(f("app", lkal.String("web")))),
					f("name", lkal.String("web")),
					f("group", lkal.String("apps.example.com")),
					f("kind", lkal.String("workload")),
				)),
			),
			uid: testUID1,
			want: rec(
				f("meta", rec(
					f("group", lkal.String("apps.example.com")),
					f("kind", lkal.String("workload")),
					f("name", lkal.String("web")),
					f("labels", rec(f("app", lkal.String("web")))),
					f("uid", lkal.String(testUID1)),
					f("generation", lkal.Int(1)),
				)),
				f("spec", rec(f("replicas", lkal.Int(2)))),
			),
		},
		{
			name:    "rule 1 invalid uid beats malformed cand",
			cand:    rec(),
			uid:     "bad-uid",
			wantErr: envelope.ErrInternal,
		},
		{
			name: "rule 2 malformed cand beats forbidden fields",
			cand: rec(
				f("meta", rec(f("kind", lkal.String("Pod")), f("name", lkal.String("web")), f("uid", lkal.String(testUID1)))),
				f("status", rec()),
			),
			uid:     testUID1,
			wantErr: envelope.ErrMalformed,
		},
		{
			name:    "rule 3 forbidden meta/uid",
			cand:    rec(f("meta", rec(f("kind", lkal.String("pod")), f("name", lkal.String("web")), f("uid", lkal.String(testUID1))))),
			uid:     testUID1,
			wantErr: envelope.ErrForbidden,
		},
		{
			name:    "rule 3 forbidden meta/revision",
			cand:    rec(f("meta", rec(f("kind", lkal.String("pod")), f("name", lkal.String("web")), f("revision", lkal.Int(1))))),
			uid:     testUID1,
			wantErr: envelope.ErrForbidden,
		},
		{
			name:    "rule 3 forbidden meta/generation",
			cand:    rec(f("meta", rec(f("kind", lkal.String("pod")), f("name", lkal.String("web")), f("generation", lkal.Int(1))))),
			uid:     testUID1,
			wantErr: envelope.ErrForbidden,
		},
		{
			name:    "rule 3 forbidden status",
			cand:    rec(f("meta", rec(f("kind", lkal.String("pod")), f("name", lkal.String("web")))), f("status", rec())),
			uid:     testUID1,
			wantErr: envelope.ErrForbidden,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := envelope.Create(tc.cand, tc.uid)
			assertErrorKind(t, err, tc.wantErr)
			if tc.wantErr == nil {
				if !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("Create = %#v, want %#v", got, tc.want)
				}
				assertErrorKind(t, envelope.CheckStored(got), nil)
			}
		})
	}
}

func TestUpdateRulesAndOrder(t *testing.T) {
	storedWithStatus := rec(
		f("meta", rec(
			f("group", lkal.String("apps.example.com")),
			f("kind", lkal.String("workload")),
			f("name", lkal.String("web")),
			f("labels", rec(f("a", lkal.String("1")))),
			f("uid", lkal.String(testUID1)),
			f("generation", lkal.Int(2)),
		)),
		f("spec", rec(f("x", lkal.Int(1)), f("y", lkal.Int(2)))),
		f("status", rec(f("observedGeneration", lkal.Int(2)))),
	)

	cases := []struct {
		name    string
		stored  lkal.Record
		rev     int64
		cand    lkal.Record
		wantErr error
		wantGen int64
	}{
		{
			name:    "rule 1 invalid stored beats bad cand",
			stored:  rec(),
			rev:     1,
			cand:    rec(),
			wantErr: envelope.ErrInternal,
		},
		{
			name:    "rule 1 rev < 1 beats bad cand",
			stored:  baseStored(),
			rev:     0,
			cand:    rec(),
			wantErr: envelope.ErrInternal,
		},
		{
			name:   "rule 2 malformed cand beats stale revision",
			stored: baseStored(),
			rev:    5,
			cand: rec(f("meta", rec(
				f("kind", lkal.String("Pod")),
				f("name", lkal.String("web")),
				f("revision", lkal.Int(3)),
			))),
			wantErr: envelope.ErrMalformed,
		},
		{
			name:   "rule 3 identity mismatch beats stale revision",
			stored: baseStored(),
			rev:    5,
			cand: rec(f("meta", rec(
				f("kind", lkal.String("workload")),
				f("name", lkal.String("other")),
				f("revision", lkal.Int(3)),
			))),
			wantErr: envelope.ErrForbidden,
		},
		{
			name:   "rule 3 group absent vs present mismatch",
			stored: baseStored(),
			rev:    5,
			cand: rec(f("meta", rec(
				f("group", lkal.String("apps.example.com")),
				f("kind", lkal.String("workload")),
				f("name", lkal.String("web")),
			))),
			wantErr: envelope.ErrForbidden,
		},
		{
			name:   "rule 4 stale revision beats forbidden uid/generation/status",
			stored: baseStored(),
			rev:    5,
			cand: rec(
				f("meta", rec(
					f("kind", lkal.String("workload")),
					f("name", lkal.String("web")),
					f("revision", lkal.Int(4)),
					f("uid", lkal.String(testUID2)),
					f("generation", lkal.Int(99)),
				)),
				f("status", rec(f("x", lkal.Int(1)))),
			),
			wantErr: envelope.ErrConflict,
		},
		{
			name:   "rule 5 forbidden uid change",
			stored: baseStored(),
			rev:    5,
			cand: rec(f("meta", rec(
				f("kind", lkal.String("workload")),
				f("name", lkal.String("web")),
				f("revision", lkal.Int(5)),
				f("uid", lkal.String(testUID2)),
			))),
			wantErr: envelope.ErrForbidden,
		},
		{
			name:   "rule 5 forbidden generation change",
			stored: baseStored(),
			rev:    5,
			cand: rec(f("meta", rec(
				f("kind", lkal.String("workload")),
				f("name", lkal.String("web")),
				f("generation", lkal.Int(2)),
			))),
			wantErr: envelope.ErrForbidden,
		},
		{
			name:   "rule 5 forbidden status when old has none",
			stored: baseStored(),
			rev:    5,
			cand: rec(
				f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("web")))),
				f("status", rec()),
			),
			wantErr: envelope.ErrForbidden,
		},
		{
			name:   "rule 5 forbidden status change when old has status",
			stored: storedWithStatus,
			rev:    5,
			cand: rec(
				f("meta", rec(f("group", lkal.String("apps.example.com")), f("kind", lkal.String("workload")), f("name", lkal.String("web")))),
				f("spec", rec(f("x", lkal.Int(1)), f("y", lkal.Int(2)))),
				f("status", rec(f("observedGeneration", lkal.Int(1)))),
			),
			wantErr: envelope.ErrForbidden,
		},
		{
			name:   "rule 6 same spec reordered fields and labels changed does not bump generation",
			stored: storedWithStatus,
			rev:    5,
			cand: rec(
				f("meta", rec(
					f("group", lkal.String("apps.example.com")),
					f("kind", lkal.String("workload")),
					f("name", lkal.String("web")),
					f("labels", rec(f("b", lkal.String("2")))),
					f("revision", lkal.Int(5)),
				)),
				f("spec", rec(f("y", lkal.Int(2)), f("x", lkal.Int(1)))),
			),
			wantGen: 2,
		},
		{
			name:   "rule 6 spec changed bumps generation and keeps old status",
			stored: storedWithStatus,
			rev:    5,
			cand: rec(
				f("meta", rec(
					f("group", lkal.String("apps.example.com")),
					f("kind", lkal.String("workload")),
					f("name", lkal.String("web")),
				)),
				f("spec", rec(f("x", lkal.Int(99)), f("y", lkal.Int(2)))),
				f("status", rec(f("observedGeneration", lkal.Int(2)))),
			),
			wantGen: 3,
		},
		{
			name:   "rule 6 spec removed bumps generation",
			stored: storedWithStatus,
			rev:    5,
			cand: rec(
				f("meta", rec(
					f("group", lkal.String("apps.example.com")),
					f("kind", lkal.String("workload")),
					f("name", lkal.String("web")),
				)),
			),
			wantGen: 3,
		},
		{
			name: "rule 6 generation MaxInt64 unchanged spec succeeds",
			stored: rec(
				f("meta", rec(
					f("kind", lkal.String("workload")),
					f("name", lkal.String("web")),
					f("uid", lkal.String(testUID1)),
					f("generation", lkal.Int(math.MaxInt64)),
				)),
				f("spec", rec(f("replicas", lkal.Int(3)))),
			),
			rev:     1,
			cand:    baseCand(),
			wantGen: math.MaxInt64,
		},
		{
			name: "rule 6 generation MaxInt64 overflow on spec change returns internal",
			stored: rec(
				f("meta", rec(
					f("kind", lkal.String("workload")),
					f("name", lkal.String("web")),
					f("uid", lkal.String(testUID1)),
					f("generation", lkal.Int(math.MaxInt64)),
				)),
				f("spec", rec(f("replicas", lkal.Int(3)))),
			),
			rev: 1,
			cand: rec(
				f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("web")))),
				f("spec", rec(f("replicas", lkal.Int(4)))),
			),
			wantErr: envelope.ErrInternal,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := envelope.Update(tc.stored, tc.rev, tc.cand)
			assertErrorKind(t, err, tc.wantErr)
			if tc.wantErr == nil {
				assertErrorKind(t, envelope.CheckStored(got), nil)
				meta := got.Fields[0].Value.(lkal.Record)
				var gotGen int64
				for _, mf := range meta.Fields {
					if mf.Key == "generation" {
						gotGen = int64(mf.Value.(lkal.Int))
					}
				}
				if gotGen != tc.wantGen {
					t.Fatalf("generation = %d, want %d", gotGen, tc.wantGen)
				}
			}
		})
	}
}

func TestUpdateStatusRulesAndOrder(t *testing.T) {
	stored := rec(
		f("meta", rec(
			f("name", lkal.String("web")),
			f("uid", lkal.String(testUID1)),
			f("kind", lkal.String("workload")),
			f("labels", rec(f("env", lkal.String("prod")))),
			f("generation", lkal.Int(3)),
		)),
		f("spec", rec(f("replicas", lkal.Int(2)))),
		f("status", rec(
			f("observedGeneration", lkal.Int(2)),
			f("condition", rec(
				f("Ready", rec(
					f("state", lkal.Enum("false")),
					f("reason", lkal.String("Starting")),
					f("since", lkal.String(testTS1)),
				)),
			)),
		)),
	)

	storedNoObs := rec(
		f("meta", rec(
			f("kind", lkal.String("workload")),
			f("name", lkal.String("web")),
			f("uid", lkal.String(testUID1)),
			f("generation", lkal.Int(2)),
		)),
		f("status", rec(f("note", lkal.String("old")))),
	)

	cases := []struct {
		name    string
		stored  lkal.Record
		rev     int64
		cand    lkal.Record
		now     string
		wantErr error
		check   func(t *testing.T, got lkal.Record)
	}{
		{
			name:    "rule 1 bad stored",
			stored:  rec(),
			rev:     1,
			cand:    rec(),
			now:     testTS2,
			wantErr: envelope.ErrInternal,
		},
		{
			name:    "rule 1 rev < 1",
			stored:  stored,
			rev:     0,
			cand:    rec(),
			now:     testTS2,
			wantErr: envelope.ErrInternal,
		},
		{
			name:    "rule 1 invalid now beats bad cand",
			stored:  stored,
			rev:     1,
			cand:    rec(),
			now:     "bad-ts",
			wantErr: envelope.ErrInternal,
		},
		{
			name:   "rule 2 malformed cand beats stale rev",
			stored: stored,
			rev:    5,
			cand: rec(f("meta", rec(
				f("kind", lkal.String("Workload")),
				f("name", lkal.String("web")),
				f("revision", lkal.Int(1)),
			))),
			now:     testTS2,
			wantErr: envelope.ErrMalformed,
		},
		{
			name:   "rule 3 identity mismatch beats stale rev",
			stored: stored,
			rev:    5,
			cand: rec(f("meta", rec(
				f("kind", lkal.String("workload")),
				f("name", lkal.String("other")),
				f("revision", lkal.Int(1)),
			))),
			now:     testTS2,
			wantErr: envelope.ErrForbidden,
		},
		{
			name:   "rule 4 stale rev beats forbidden labels/spec/since",
			stored: stored,
			rev:    5,
			cand: rec(
				f("meta", rec(
					f("kind", lkal.String("workload")),
					f("name", lkal.String("web")),
					f("revision", lkal.Int(4)),
					f("labels", rec(f("env", lkal.String("dev")))),
				)),
				f("spec", rec(f("replicas", lkal.Int(9)))),
			),
			now:     testTS2,
			wantErr: envelope.ErrConflict,
		},
		{
			name:   "rule 5 forbidden uid",
			stored: stored,
			rev:    5,
			cand: rec(f("meta", rec(
				f("kind", lkal.String("workload")),
				f("name", lkal.String("web")),
				f("uid", lkal.String(testUID2)),
			))),
			now:     testTS2,
			wantErr: envelope.ErrForbidden,
		},
		{
			name:   "rule 5 forbidden generation",
			stored: stored,
			rev:    5,
			cand: rec(f("meta", rec(
				f("kind", lkal.String("workload")),
				f("name", lkal.String("web")),
				f("generation", lkal.Int(4)),
			))),
			now:     testTS2,
			wantErr: envelope.ErrForbidden,
		},
		{
			name:   "rule 5 forbidden labels change",
			stored: stored,
			rev:    5,
			cand: rec(f("meta", rec(
				f("kind", lkal.String("workload")),
				f("name", lkal.String("web")),
				f("labels", rec(f("env", lkal.String("dev")))),
			))),
			now:     testTS2,
			wantErr: envelope.ErrForbidden,
		},
		{
			name:   "rule 5 forbidden spec change",
			stored: stored,
			rev:    5,
			cand: rec(
				f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("web")))),
				f("spec", rec(f("replicas", lkal.Int(9)))),
			),
			now:     testTS2,
			wantErr: envelope.ErrForbidden,
		},
		{
			name:   "rule 5 forbidden since on new condition",
			stored: stored,
			rev:    5,
			cand: rec(
				f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("web")))),
				f("status", rec(
					f("observedGeneration", lkal.Int(2)),
					f("condition", rec(
						f("Degraded", rec(
							f("state", lkal.Enum("false")),
							f("reason", lkal.String("Healthy")),
							f("since", lkal.String(testTS2)),
						)),
					)),
				)),
			),
			now:     testTS2,
			wantErr: envelope.ErrForbidden,
		},
		{
			name:   "rule 5 forbidden since changed on existing condition",
			stored: stored,
			rev:    5,
			cand: rec(
				f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("web")))),
				f("status", rec(
					f("observedGeneration", lkal.Int(2)),
					f("condition", rec(
						f("Ready", rec(
							f("state", lkal.Enum("true")),
							f("reason", lkal.String("Running")),
							f("since", lkal.String(testTS2)),
						)),
					)),
				)),
			),
			now:     testTS2,
			wantErr: envelope.ErrForbidden,
		},
		{
			name:   "rule 5 forbidden since beats observed error",
			stored: stored,
			rev:    5,
			cand: rec(
				f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("web")))),
				f("status", rec(
					f("observedGeneration", lkal.Int(99)),
					f("condition", rec(
						f("Ready", rec(
							f("state", lkal.Enum("true")),
							f("reason", lkal.String("Running")),
							f("since", lkal.String(testTS2)),
						)),
					)),
				)),
			),
			now:     testTS2,
			wantErr: envelope.ErrForbidden,
		},
		{
			name:   "rule 6 observedGeneration exceeds generation",
			stored: stored,
			rev:    5,
			cand: rec(
				f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("web")))),
				f("status", rec(f("observedGeneration", lkal.Int(4)))),
			),
			now:     testTS2,
			wantErr: envelope.ErrObserved,
		},
		{
			name:   "rule 6 observedGeneration regresses",
			stored: stored,
			rev:    5,
			cand: rec(
				f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("web")))),
				f("status", rec(f("observedGeneration", lkal.Int(1)))),
			),
			now:     testTS2,
			wantErr: envelope.ErrObserved,
		},
		{
			name:   "rule 6 observedGeneration removed when status omitted",
			stored: stored,
			rev:    5,
			cand: rec(
				f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("web")))),
			),
			now:     testTS2,
			wantErr: envelope.ErrObserved,
		},
		{
			name:   "rule 6 observedGeneration removed when status has no observedGeneration",
			stored: stored,
			rev:    5,
			cand: rec(
				f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("web")))),
				f("status", rec(f("note", lkal.String("hi")))),
			),
			now:     testTS2,
			wantErr: envelope.ErrObserved,
		},
		{
			name:   "rule 7 omitting labels and spec preserves old meta/spec and updates conditions",
			stored: stored,
			rev:    5,
			cand: rec(
				f("meta", rec(
					f("kind", lkal.String("workload")),
					f("name", lkal.String("web")),
					f("revision", lkal.Int(5)),
				)),
				f("status", rec(
					f("extraA", lkal.String("a")),
					f("condition", rec(
						f("Degraded", rec(
							f("reason", lkal.String("Ok")),
							f("state", lkal.Enum("false")),
						)),
						f("Ready", rec(
							f("state", lkal.Enum("false")),
							f("reason", lkal.String("StillStarting")),
						)),
					)),
					f("observedGeneration", lkal.Int(3)),
					f("extraB", lkal.Int(42)),
				)),
			),
			now: testTS2,
			check: func(t *testing.T, got lkal.Record) {
				t.Helper()
				if !reflect.DeepEqual(got.Fields[0], stored.Fields[0]) {
					t.Fatalf("meta changed: got %#v, want %#v", got.Fields[0], stored.Fields[0])
				}
				if !reflect.DeepEqual(got.Fields[1], stored.Fields[1]) {
					t.Fatalf("spec changed: got %#v, want %#v", got.Fields[1], stored.Fields[1])
				}
				wantStatus := rec(
					f("extraA", lkal.String("a")),
					f("condition", rec(
						f("Degraded", rec(
							f("state", lkal.Enum("false")),
							f("reason", lkal.String("Ok")),
							f("since", lkal.String(testTS2)),
						)),
						f("Ready", rec(
							f("state", lkal.Enum("false")),
							f("reason", lkal.String("StillStarting")),
							f("since", lkal.String(testTS1)),
						)),
					)),
					f("observedGeneration", lkal.Int(3)),
					f("extraB", lkal.Int(42)),
				)
				if !reflect.DeepEqual(got.Fields[2].Value, wantStatus) {
					t.Fatalf("status = %#v, want %#v", got.Fields[2].Value, wantStatus)
				}
			},
		},
		{
			name:   "rule 7 state changed with old since provided updates since to now",
			stored: stored,
			rev:    5,
			cand: rec(
				f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("web")))),
				f("status", rec(
					f("observedGeneration", lkal.Int(3)),
					f("condition", rec(
						f("Ready", rec(
							f("state", lkal.Enum("true")),
							f("reason", lkal.String("BecameReady")),
							f("since", lkal.String(testTS1)),
						)),
					)),
				)),
			),
			now: testTS2,
			check: func(t *testing.T, got lkal.Record) {
				t.Helper()
				cond := got.Fields[2].Value.(lkal.Record).Fields[1].Value.(lkal.Record).Fields[0].Value.(lkal.Record)
				if cond.Fields[2] != f("since", lkal.String(testTS2)) {
					t.Fatalf("since = %#v, want %s", cond.Fields[2], testTS2)
				}
			},
		},
		{
			name:   "rule 7 cand without status drops status when old had no observedGeneration",
			stored: storedNoObs,
			rev:    1,
			cand:   rec(f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("web"))))),
			now:    testTS2,
			check: func(t *testing.T, got lkal.Record) {
				t.Helper()
				if len(got.Fields) != 1 {
					t.Fatalf("got %d top-level fields, want 1", len(got.Fields))
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := envelope.UpdateStatus(tc.stored, tc.rev, tc.cand, tc.now)
			assertErrorKind(t, err, tc.wantErr)
			if tc.wantErr == nil {
				assertErrorKind(t, envelope.CheckStored(got), nil)
				if tc.check != nil {
					tc.check(t, got)
				}
			}
		})
	}
}

func TestWithRevision(t *testing.T) {
	s := baseStored()
	r1 := envelope.WithRevision(s, 7)
	meta1 := r1.Fields[0].Value.(lkal.Record)
	last := meta1.Fields[len(meta1.Fields)-1]
	if last.Key != "revision" || last.Value != lkal.Int(7) {
		t.Fatalf("last meta field = %#v, want revision=7", last)
	}
	assertErrorKind(t, envelope.CheckShape(r1), nil)

	// Calling again drops existing revision and puts new one at trailing end.
	r2 := envelope.WithRevision(r1, 9)
	meta2 := r2.Fields[0].Value.(lkal.Record)
	revCount := 0
	for _, mf := range meta2.Fields {
		if mf.Key == "revision" {
			revCount++
		}
	}
	if revCount != 1 || meta2.Fields[len(meta2.Fields)-1] != f("revision", lkal.Int(9)) {
		t.Fatalf("unexpected meta after second WithRevision: %#v", meta2)
	}
}

func TestImmutabilityAndNoAliasing(t *testing.T) {
	cand := rec(
		f("meta", rec(
			f("kind", lkal.String("workload")),
			f("name", lkal.String("web")),
			f("labels", rec(f("env", lkal.String("prod")))),
		)),
		f("spec", rec(
			f("list", lkal.List{rec(f("k", lkal.String("v")))}),
			f("ref", lkal.Ref{"secret", "db"}),
		)),
	)
	candSnap := deepCopyRecord(cand)

	created, err := envelope.Create(cand, testUID1)
	assertErrorKind(t, err, nil)
	if !reflect.DeepEqual(cand, candSnap) {
		t.Fatalf("Create mutated candidate")
	}

	// Mutate slices inside created and verify cand is unaffected.
	created.Fields[0].Value.(lkal.Record).Fields[2].Value.(lkal.Record).Fields[0].Value = lkal.String("mutated")
	createdSpec := created.Fields[1].Value.(lkal.Record)
	createdSpec.Fields[0].Value.(lkal.List)[0].(lkal.Record).Fields[0].Value = lkal.String("mutated")
	createdSpec.Fields[1].Value.(lkal.Ref)[0] = "mutated"
	if !reflect.DeepEqual(cand, candSnap) {
		t.Fatalf("mutating Create result affected candidate")
	}

	stored, err := envelope.Create(cand, testUID1)
	assertErrorKind(t, err, nil)
	storedSnap := deepCopyRecord(stored)

	updated, err := envelope.Update(stored, 1, cand)
	assertErrorKind(t, err, nil)
	if !reflect.DeepEqual(stored, storedSnap) || !reflect.DeepEqual(cand, candSnap) {
		t.Fatalf("Update mutated inputs")
	}
	updated.Fields[0].Value.(lkal.Record).Fields[2].Value.(lkal.Record).Fields[0].Value = lkal.String("mutated")
	updated.Fields[1].Value.(lkal.Record).Fields[1].Value.(lkal.Ref)[0] = "mutated"
	if !reflect.DeepEqual(stored, storedSnap) || !reflect.DeepEqual(cand, candSnap) {
		t.Fatalf("mutating Update result affected inputs")
	}

	candStatus := rec(
		f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("web")))),
		f("status", rec(
			f("observedGeneration", lkal.Int(1)),
			f("items", lkal.List{lkal.String("x")}),
			f("condition", rec(f("Ready", rec(f("state", lkal.Enum("true")), f("reason", lkal.String("Ok")))))),
		)),
	)
	candStatusSnap := deepCopyRecord(candStatus)

	withStatus, err := envelope.UpdateStatus(stored, 1, candStatus, testTS1)
	assertErrorKind(t, err, nil)
	if !reflect.DeepEqual(stored, storedSnap) || !reflect.DeepEqual(candStatus, candStatusSnap) {
		t.Fatalf("UpdateStatus mutated inputs")
	}
	withStatus.Fields[1].Value.(lkal.Record).Fields[1].Value.(lkal.Ref)[0] = "mutated"
	withStatus.Fields[2].Value.(lkal.Record).Fields[1].Value.(lkal.List)[0] = lkal.String("mutated")
	if !reflect.DeepEqual(stored, storedSnap) || !reflect.DeepEqual(candStatus, candStatusSnap) {
		t.Fatalf("mutating UpdateStatus result affected inputs")
	}

	withRev := envelope.WithRevision(stored, 4)
	withRev.Fields[1].Value.(lkal.Record).Fields[1].Value.(lkal.Ref)[0] = "mutated"
	if !reflect.DeepEqual(stored, storedSnap) {
		t.Fatalf("mutating WithRevision result affected input")
	}
}

func TestPropertyTransitions(t *testing.T) {
	for seed := uint64(1); seed <= 25; seed++ {
		rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
		for step := range 40 {
			group := ""
			if rng.IntN(2) == 0 {
				group = "apps.example.com"
			}
			kind := []string{"pod", "workload", "job"}[rng.IntN(3)]
			name := []string{"web", "api.v1", "worker-0"}[rng.IntN(3)]
			uid := randomUID(rng)

			cand := randomCreateCand(rng, group, kind, name)
			candShuffled := shuffleRecord(rng, cand)
			candSnap := deepCopyRecord(cand)

			doc, err := envelope.Create(cand, uid)
			if err != nil {
				t.Fatalf("seed %d step %d Create failed: %v", seed, step, err)
			}
			if !reflect.DeepEqual(cand, candSnap) {
				t.Fatalf("Create mutated cand")
			}
			assertErrorKind(t, envelope.CheckStored(doc), nil)

			docShuffled, errShuf := envelope.Create(candShuffled, uid)
			if errShuf != nil || !lkal.Equal(doc, docShuffled) {
				t.Fatalf("Create shuffle mismatch: err=%v equal=%v", errShuf, lkal.Equal(doc, docShuffled))
			}

			cur := doc
			for tx := range 8 {
				rev := int64(tx + 1)
				oldSpec, hasOldSpec := lookup(cur, "spec")
				oldMeta, _ := lookupRecord(cur, "meta")
				oldUID, _ := lookup(oldMeta, "uid")
				oldGen, _ := lookup(oldMeta, "generation")
				oldStatus, hasOldStatus := lookup(cur, "status")

				if rng.IntN(2) == 0 {
					updCand := mutateCandidateForUpdate(rng, cur, rev)
					updShuf := shuffleRecord(rng, updCand)
					curSnap := deepCopyRecord(cur)
					updSnap := deepCopyRecord(updCand)

					next, err := envelope.Update(cur, rev, updCand)
					nextShuf, errShuf := envelope.Update(cur, rev, updShuf)
					if !reflect.DeepEqual(cur, curSnap) || !reflect.DeepEqual(updCand, updSnap) {
						t.Fatalf("Update mutated inputs")
					}
					if errKind(err) != errKind(errShuf) {
						t.Fatalf("Update shuffle error kind mismatch: %v vs %v", err, errShuf)
					}
					if err == nil {
						assertErrorKind(t, envelope.CheckStored(next), nil)
						if !lkal.Equal(next, nextShuf) {
							t.Fatalf("Update shuffle result mismatch")
						}
						candSpec, hasCandSpec := lookup(updCand, "spec")
						nextMeta, _ := lookupRecord(next, "meta")
						nextUID, _ := lookup(nextMeta, "uid")
						nextGen, _ := lookup(nextMeta, "generation")
						nextStatus, hasNextStatus := lookup(next, "status")

						if !lkal.Equal(nextUID, oldUID) {
							t.Fatalf("Update changed uid")
						}
						if hasOldStatus != hasNextStatus || (hasOldStatus && !lkal.Equal(oldStatus, nextStatus)) {
							t.Fatalf("Update changed status")
						}
						specSame := (hasOldSpec == hasCandSpec) && (!hasOldSpec || lkal.Equal(oldSpec, candSpec))
						wantGen := int64(oldGen.(lkal.Int))
						if !specSame {
							wantGen++
						}
						if int64(nextGen.(lkal.Int)) != wantGen {
							t.Fatalf("Update generation = %v, want %d (specSame=%v)", nextGen, wantGen, specSame)
						}
						cur = next
					}
				} else {
					now := randomTimestamp(rng)
					stCand := mutateCandidateForStatus(rng, cur, rev)
					stShuf := shuffleRecord(rng, stCand)
					curSnap := deepCopyRecord(cur)
					stSnap := deepCopyRecord(stCand)

					next, err := envelope.UpdateStatus(cur, rev, stCand, now)
					nextShuf, errShuf := envelope.UpdateStatus(cur, rev, stShuf, now)
					if !reflect.DeepEqual(cur, curSnap) || !reflect.DeepEqual(stCand, stSnap) {
						t.Fatalf("UpdateStatus mutated inputs")
					}
					if errKind(err) != errKind(errShuf) {
						t.Fatalf("UpdateStatus shuffle error kind mismatch: %v vs %v", err, errShuf)
					}
					if err == nil {
						assertErrorKind(t, envelope.CheckStored(next), nil)
						if !lkal.Equal(next, nextShuf) {
							t.Fatalf("UpdateStatus shuffle result mismatch")
						}
						nextMeta, _ := lookup(next, "meta")
						nextSpec, hasNextSpec := lookup(next, "spec")
						if !lkal.Equal(nextMeta, oldMeta) {
							t.Fatalf("UpdateStatus changed meta")
						}
						if hasOldSpec != hasNextSpec || (hasOldSpec && !lkal.Equal(oldSpec, nextSpec)) {
							t.Fatalf("UpdateStatus changed spec")
						}
						checkConditionSinceInvariant(t, cur, stCand, next, now)
						cur = next
					}
				}
			}
		}
	}
}

func checkConditionSinceInvariant(t *testing.T, oldDoc, candDoc, nextDoc lkal.Record, now string) {
	t.Helper()
	var oldCond lkal.Record
	if oldStatus, ok := lookupRecord(oldDoc, "status"); ok {
		oldCond, _ = lookupRecord(oldStatus, "condition")
	}
	nextStatus, hasNextStatus := lookupRecord(nextDoc, "status")
	if !hasNextStatus {
		return
	}
	nextCond, hasNextCond := lookupRecord(nextStatus, "condition")
	if !hasNextCond {
		return
	}
	for _, nf := range nextCond.Fields {
		nc := nf.Value.(lkal.Record)
		ns, _ := lookup(nc, "state")
		nSince, _ := lookup(nc, "since")
		wantSince := now
		if oldC, ok := lookupRecord(oldCond, nf.Key); ok {
			if os, ok := lookup(oldC, "state"); ok && os.(lkal.Enum) == ns.(lkal.Enum) {
				oSince, _ := lookup(oldC, "since")
				wantSince = string(oSince.(lkal.String))
			}
		}
		if string(nSince.(lkal.String)) != wantSince {
			t.Fatalf("condition %s since = %v, want %q", nf.Key, nSince, wantSince)
		}
	}
}

func errKind(err error) error {
	if err == nil {
		return nil
	}
	var envErr *envelope.Error
	if errors.As(err, &envErr) {
		return envErr.Kind
	}
	return err
}

func lookup(r lkal.Record, k string) (lkal.Value, bool) {
	for _, fld := range r.Fields {
		if fld.Key == k {
			return fld.Value, true
		}
	}
	return nil, false
}

func lookupRecord(r lkal.Record, k string) (lkal.Record, bool) {
	v, ok := lookup(r, k)
	if !ok {
		return lkal.Record{}, false
	}
	rec, ok := v.(lkal.Record)
	return rec, ok
}

func randomUID(rng *rand.Rand) string {
	const hex = "0123456789abcdef"
	buf := make([]byte, 36)
	for i := range buf {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			buf[i] = '-'
		} else {
			buf[i] = hex[rng.IntN(16)]
		}
	}
	return string(buf)
}

func randomTimestamp(rng *rand.Rand) string {
	y := 2020 + rng.IntN(10)
	m := 1 + rng.IntN(12)
	d := 1 + rng.IntN(28)
	hh := rng.IntN(24)
	mm := rng.IntN(60)
	ss := rng.IntN(60)
	return envelope.Timestamp(time.Date(y, time.Month(m), d, hh, mm, ss, 0, time.UTC))
}

func randomLabels(rng *rand.Rand) lkal.Record {
	n := rng.IntN(4)
	keys := []string{"app", "tier", "env", "owner"}
	rng.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })
	fields := make([]lkal.Field, n)
	for i := range n {
		fields[i] = f(keys[i], lkal.String([]string{"a", "b", "c"}[rng.IntN(3)]))
	}
	return rec(fields...)
}

func randomSpec(rng *rand.Rand) lkal.Record {
	return rec(
		f("replicas", lkal.Int(rng.IntN(5)+1)),
		f("nested", rec(
			f("flag", lkal.Bool(rng.IntN(2) == 0)),
			f("tag", lkal.String([]string{"x", "y"}[rng.IntN(2)])),
		)),
		f("list", lkal.List{rec(f("a", lkal.Int(rng.IntN(3))), f("b", lkal.String("s")))}),
	)
}

func randomCreateCand(rng *rand.Rand, group, kind, name string) lkal.Record {
	metaFields := []lkal.Field{f("kind", lkal.String(kind)), f("name", lkal.String(name))}
	if group != "" {
		metaFields = append(metaFields, f("group", lkal.String(group)))
	}
	if rng.IntN(2) == 0 {
		metaFields = append(metaFields, f("labels", randomLabels(rng)))
	}
	fields := []lkal.Field{f("meta", lkal.Record{Fields: metaFields})}
	if rng.IntN(2) == 0 {
		fields = append(fields, f("spec", randomSpec(rng)))
	}
	return lkal.Record{Fields: fields}
}

func mutateCandidateForUpdate(rng *rand.Rand, cur lkal.Record, rev int64) lkal.Record {
	oldMeta, _ := lookupRecord(cur, "meta")
	var metaFields []lkal.Field
	if g, ok := lookup(oldMeta, "group"); ok {
		metaFields = append(metaFields, f("group", g))
	}
	k, _ := lookup(oldMeta, "kind")
	n, _ := lookup(oldMeta, "name")
	metaFields = append(metaFields, f("kind", k), f("name", n))
	if rng.IntN(2) == 0 {
		metaFields = append(metaFields, f("labels", randomLabels(rng)))
	}
	switch rng.IntN(5) {
	case 0:
		metaFields = append(metaFields, f("revision", lkal.Int(rev)))
	case 1:
		metaFields = append(metaFields, f("revision", lkal.Int(rev+1)))
	case 2:
		u, _ := lookup(oldMeta, "uid")
		metaFields = append(metaFields, f("uid", u))
	case 3:
		metaFields = append(metaFields, f("uid", lkal.String(randomUID(rng))))
	}
	fields := []lkal.Field{f("meta", lkal.Record{Fields: metaFields})}
	switch rng.IntN(4) {
	case 0:
		if sp, ok := lookup(cur, "spec"); ok {
			fields = append(fields, f("spec", deepCopyValue(sp)))
		}
	case 1:
		fields = append(fields, f("spec", randomSpec(rng)))
	case 2:
		// omit spec
	case 3:
		if sp, ok := lookup(cur, "spec"); ok {
			fields = append(fields, f("spec", deepCopyValue(sp)))
		}
		if st, ok := lookup(cur, "status"); ok && rng.IntN(2) == 0 {
			fields = append(fields, f("status", deepCopyValue(st)))
		} else {
			fields = append(fields, f("status", rec(f("extra", lkal.Int(1)))))
		}
	}
	return lkal.Record{Fields: fields}
}

func mutateCandidateForStatus(rng *rand.Rand, cur lkal.Record, rev int64) lkal.Record {
	oldMeta, _ := lookupRecord(cur, "meta")
	var metaFields []lkal.Field
	if g, ok := lookup(oldMeta, "group"); ok {
		metaFields = append(metaFields, f("group", g))
	}
	k, _ := lookup(oldMeta, "kind")
	n, _ := lookup(oldMeta, "name")
	metaFields = append(metaFields, f("kind", k), f("name", n))
	if rng.IntN(2) == 0 {
		if lb, ok := lookup(oldMeta, "labels"); ok {
			metaFields = append(metaFields, f("labels", deepCopyValue(lb)))
		}
	}
	if rng.IntN(4) == 0 {
		metaFields = append(metaFields, f("revision", lkal.Int(rev+int64(rng.IntN(2)))))
	}
	fields := []lkal.Field{f("meta", lkal.Record{Fields: metaFields})}
	if sp, ok := lookup(cur, "spec"); ok && rng.IntN(2) == 0 {
		fields = append(fields, f("spec", deepCopyValue(sp)))
	}
	if rng.IntN(6) != 0 {
		oldGen, _ := lookup(oldMeta, "generation")
		gen := int64(oldGen.(lkal.Int))
		obs := int64(rng.IntN(int(gen)+2) + 1)
		states := []lkal.Enum{"true", "false", "unknown"}
		condFields := []lkal.Field{
			f("Ready", rec(
				f("state", states[rng.IntN(3)]),
				f("reason", lkal.String("Probe")),
			)),
		}
		if rng.IntN(2) == 0 {
			condFields = append(condFields, f("Synced", rec(
				f("state", states[rng.IntN(3)]),
				f("reason", lkal.String("Sync")),
			)))
		}
		stFields := []lkal.Field{
			f("observedGeneration", lkal.Int(obs)),
			f("condition", lkal.Record{Fields: condFields}),
		}
		if rng.IntN(2) == 0 {
			stFields = append(stFields, f("note", lkal.String("ok")))
		}
		fields = append(fields, f("status", lkal.Record{Fields: stFields}))
	}
	return lkal.Record{Fields: fields}
}

func shuffleRecord(rng *rand.Rand, r lkal.Record) lkal.Record {
	fields := make([]lkal.Field, len(r.Fields))
	for i, fld := range r.Fields {
		fields[i] = f(fld.Key, shuffleValue(rng, fld.Value))
	}
	rng.Shuffle(len(fields), func(i, j int) { fields[i], fields[j] = fields[j], fields[i] })
	return lkal.Record{Fields: fields}
}

func shuffleValue(rng *rand.Rand, v lkal.Value) lkal.Value {
	switch val := v.(type) {
	case lkal.Record:
		return shuffleRecord(rng, val)
	case lkal.List:
		out := make(lkal.List, len(val))
		for i, e := range val {
			out[i] = shuffleValue(rng, e)
		}
		return out
	case lkal.Ref:
		out := make(lkal.Ref, len(val))
		copy(out, val)
		return out
	default:
		return v
	}
}

func deepCopyRecord(r lkal.Record) lkal.Record {
	if r.Fields == nil {
		return lkal.Record{}
	}
	fields := make([]lkal.Field, len(r.Fields))
	for i, fld := range r.Fields {
		fields[i] = f(fld.Key, deepCopyValue(fld.Value))
	}
	return lkal.Record{Fields: fields}
}

func deepCopyValue(v lkal.Value) lkal.Value {
	switch val := v.(type) {
	case lkal.Record:
		return deepCopyRecord(val)
	case lkal.List:
		if val == nil {
			return lkal.List(nil)
		}
		out := make(lkal.List, len(val))
		for i, e := range val {
			out[i] = deepCopyValue(e)
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
