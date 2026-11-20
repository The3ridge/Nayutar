package refs_test

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/resource/refs"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
)

func rec(fields ...lkal.Field) lkal.Record {
	return lkal.Record{Fields: fields}
}

func f(k string, v lkal.Value) lkal.Field {
	return lkal.Field{Key: k, Value: v}
}

func baseDoc(specFields ...lkal.Field) lkal.Record {
	return rec(
		f("meta", rec(
			f("kind", lkal.String("workload")),
			f("name", lkal.String("web")),
		)),
		f("spec", rec(specFields...)),
	)
}

func assertRefsError(t *testing.T, err error, wantKind refs.ErrorKind, wantOcc refs.Occurrence) {
	t.Helper()
	if err == nil {
		t.Fatalf("got nil error, want *refs.Error(%s)", wantKind)
	}
	var re *refs.Error
	if !errors.As(err, &re) {
		t.Fatalf("error %T (%v) is not *refs.Error", err, err)
	}
	if re.Kind != wantKind {
		t.Fatalf("got error kind %s, want %s (err: %v)", re.Kind, wantKind, re)
	}
	if re.Occurrence.Path != wantOcc.Path || !slices.Equal(re.Occurrence.Ref, wantOcc.Ref) {
		t.Fatalf("got occurrence %+v, want %+v", re.Occurrence, wantOcc)
	}
}

type mockAuthorizer struct {
	allow func(refs.Request) bool
}

func (m mockAuthorizer) Authorize(req refs.Request) bool {
	return m.allow(req)
}

func TestErrorKindAndErrorFormatting(t *testing.T) {
	cases := []struct {
		kind refs.ErrorKind
		want string
	}{
		{refs.ErrInternal, "internal"},
		{refs.ErrMalformed, "malformed"},
		{refs.ErrKind, "kind"},
		{refs.ErrDenied, "denied"},
		{refs.ErrNotFound, "notFound"},
		{refs.ErrorKind(99), "unknown(99)"},
	}
	for _, tc := range cases {
		if got := tc.kind.String(); got != tc.want {
			t.Errorf("ErrorKind(%d).String() = %q, want %q", tc.kind, got, tc.want)
		}
	}

	var nilErr *refs.Error
	if nilErr.Error() != "<nil>" {
		t.Errorf("nil Error.Error() = %q, want \"<nil>\"", nilErr.Error())
	}

	err := &refs.Error{
		Kind:       refs.ErrDenied,
		Occurrence: refs.Occurrence{Path: "spec/secret", Ref: lkal.Ref{"secret", "db"}},
		Detail:     "policy denied",
	}
	s := err.Error()
	for _, sub := range []string{"denied", "spec/secret", "&secret/db", "policy denied"} {
		if !strings.Contains(s, sub) {
			t.Errorf("Error() = %q, missing %q", s, sub)
		}
	}

	noOcc := &refs.Error{Kind: refs.ErrMalformed, Detail: "too many targets"}
	if noOcc.Error() != "refs: malformed: too many targets" {
		t.Errorf("noOcc.Error() = %q", noOcc.Error())
	}

	var auth refs.Authorizer = mockAuthorizer{allow: func(req refs.Request) bool {
		return req.Tenant == "acme" && req.Principal.Type == "User" && req.Principal.ID == "alice" && req.TargetExists && req.TargetLabels["opt"] == "in"
	}}
	if !auth.Authorize(refs.Request{
		Tenant:       "acme",
		Principal:    refs.Principal{Type: "User", ID: "alice"},
		Source:       keyspace.Object{Group: "core", Kind: "workload", Name: "web"},
		SourcePath:   "spec/secret",
		Target:       keyspace.Object{Group: "core", Kind: "secret", Name: "db"},
		TargetLabels: map[string]string{"opt": "in"},
		TargetExists: true,
	}) {
		t.Error("Authorizer interface check failed")
	}
}

func TestParse(t *testing.T) {
	cases := []struct {
		name    string
		ref     lkal.Ref
		want    refs.Target
		wantErr bool
	}{
		{
			name: "core kind without leaf",
			ref:  lkal.Ref{"secret", "db-creds"},
			want: refs.Target{Object: keyspace.Object{Group: "core", Kind: "secret", Name: "db-creds"}},
		},
		{
			name: "grouped kind with leaf",
			ref:  lkal.Ref{"certificate.cert.example.com", "tls.v1", "spec", "issuer"},
			want: refs.Target{
				Object: keyspace.Object{Group: "cert.example.com", Kind: "certificate", Name: "tls.v1"},
				Leaf:   []string{"spec", "issuer"},
			},
		},
		{
			name: "leaf meta only",
			ref:  lkal.Ref{"configmap", "cfg", "meta"},
			want: refs.Target{
				Object: keyspace.Object{Group: "core", Kind: "configmap", Name: "cfg"},
				Leaf:   []string{"meta"},
			},
		},
		{
			name: "leaf status nested",
			ref:  lkal.Ref{"pod", "p1", "status", "condition", "Ready", "state"},
			want: refs.Target{
				Object: keyspace.Object{Group: "core", Kind: "pod", Name: "p1"},
				Leaf:   []string{"status", "condition", "Ready", "state"},
			},
		},
		{name: "nil ref", ref: nil, wantErr: true},
		{name: "empty ref", ref: lkal.Ref{}, wantErr: true},
		{name: "single segment", ref: lkal.Ref{"secret"}, wantErr: true},
		{name: "secret.core rejected", ref: lkal.Ref{"secret.core", "x"}, wantErr: true},
		{name: "dotdot kind rejected", ref: lkal.Ref{"..", "x"}, wantErr: true},
		{name: "uppercase kind rejected", ref: lkal.Ref{"Secret", "x"}, wantErr: true},
		{name: "kind with slash rejected", ref: lkal.Ref{"a/b", "x"}, wantErr: true},
		{name: "name with slash rejected", ref: lkal.Ref{"secret", "a/b"}, wantErr: true},
		{name: "empty name rejected", ref: lkal.Ref{"secret", ""}, wantErr: true},
		{name: "key imitating notation", ref: lkal.Ref{"t", "other", "o", "core", "secret", "x"}, wantErr: true},
		{name: "leaf starting with o", ref: lkal.Ref{"secret", "x", "o"}, wantErr: true},
		{name: "leaf starting with other", ref: lkal.Ref{"secret", "x", "other", "y"}, wantErr: true},
		{name: "leaf empty segment", ref: lkal.Ref{"secret", "x", "spec", ""}, wantErr: true},
		{name: "leaf segment containing slash", ref: lkal.Ref{"secret", "x", "spec", "a/b"}, wantErr: true},
		{name: "leaf segment starting with dollar", ref: lkal.Ref{"secret", "x", "spec", "$ref"}, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := refs.Parse(tc.ref)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Parse(%v) = %+v, want error", tc.ref, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%v) unexpected error: %v", tc.ref, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Parse(%v) = %+v, want %+v", tc.ref, got, tc.want)
			}
		})
	}
}

func TestOccurrences(t *testing.T) {
	doc := rec(
		f("status", rec(
			f("target", lkal.Ref{"secret", "st"}),
		)),
		f("spec", rec(
			f("z", lkal.Ref{"secret", "a", "spec", "key"}),
			f("a", lkal.List{
				lkal.Ref{"secret", "b"},
				lkal.Ref{"secret", "a"},
				lkal.Ref{"secret", "a", "meta"},
				lkal.Ref{"secret", "a"},
				lkal.String("ignored"),
			}),
			f("nested", rec(
				f("deep", lkal.Ref{"configmap", "c"}),
			)),
		)),
	)

	want := []refs.Occurrence{
		{Path: "spec/a", Ref: lkal.Ref{"secret", "a"}},
		{Path: "spec/a", Ref: lkal.Ref{"secret", "a", "meta"}},
		{Path: "spec/a", Ref: lkal.Ref{"secret", "b"}},
		{Path: "spec/nested/deep", Ref: lkal.Ref{"configmap", "c"}},
		{Path: "spec/z", Ref: lkal.Ref{"secret", "a", "spec", "key"}},
		{Path: "status/target", Ref: lkal.Ref{"secret", "st"}},
	}

	got := refs.Occurrences(doc)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Occurrences = %#v, want %#v", got, want)
	}

	if gotEmpty := refs.Occurrences(lkal.Record{}); gotEmpty != nil {
		t.Fatalf("Occurrences(empty) = %#v, want nil", gotEmpty)
	}

	ambig := rec(
		f("spec", rec(
			f("db/password", lkal.Ref{"secret", "db"}),
			f("db", rec(
				f("password", lkal.Ref{"secret", "db"}),
			)),
		)),
	)
	gotAmbig := refs.Occurrences(ambig)
	wantAmbig := []refs.Occurrence{
		{Path: "spec/db/password", Ref: lkal.Ref{"secret", "db"}},
		{Path: "spec/db/password", Ref: lkal.Ref{"secret", "db"}},
	}
	if !reflect.DeepEqual(gotAmbig, wantAmbig) {
		t.Fatalf("Occurrences(ambiguous) = %#v, want %#v", gotAmbig, wantAmbig)
	}
}

func TestSchemaValidateAndAllows(t *testing.T) {
	validSchema := refs.Schema{
		"workload": {
			{Path: []string{"spec", "db", "secret"}, Targets: []string{"secret"}},
			{Path: []string{"spec", "volumes", "*"}, Targets: []string{"configmap", "secret"}},
			{Path: []string{"spec", "volumes", "extra"}, Targets: []string{"pvc.storage.example.com"}},
		},
		"app.apps.example.com": {},
	}
	if err := validSchema.Validate(); err != nil {
		t.Fatalf("validSchema.Validate() unexpected error: %v", err)
	}

	allowCases := []struct {
		src, path, tgt string
		want           bool
	}{
		{"workload", "spec/db/secret", "secret", true},
		{"workload", "spec/db/secret", "configmap", false},
		{"workload", "spec/volumes/0", "configmap", true},
		{"workload", "spec/volumes/0", "secret", true},
		{"workload", "spec/volumes/extra", "pvc.storage.example.com", true},
		{"workload", "spec/volumes/extra", "secret", true},
		{"workload", "spec/volumes/0/nested", "secret", false},
		{"workload", "spec/volumes", "secret", false},
		{"workload", "", "secret", false},
		{"missing", "spec/db/secret", "secret", false},
		{"app.apps.example.com", "spec/db/secret", "secret", false},
	}
	for _, tc := range allowCases {
		if got := validSchema.Allows(tc.src, tc.path, tc.tgt); got != tc.want {
			t.Errorf("Allows(%q, %q, %q) = %v, want %v", tc.src, tc.path, tc.tgt, got, tc.want)
		}
	}

	invalidSchemas := []struct {
		name   string
		schema refs.Schema
	}{
		{
			name:   "invalid source qualified kind",
			schema: refs.Schema{"secret.core": {}},
		},
		{
			name:   "empty source kind",
			schema: refs.Schema{"": {}},
		},
		{
			name: "empty path",
			schema: refs.Schema{
				"workload": {{Path: nil, Targets: []string{"secret"}}},
			},
		},
		{
			name: "path not starting with spec",
			schema: refs.Schema{
				"workload": {{Path: []string{"status", "ref"}, Targets: []string{"secret"}}},
			},
		},
		{
			name: "empty path segment",
			schema: refs.Schema{
				"workload": {{Path: []string{"spec", "", "a"}, Targets: []string{"secret"}}},
			},
		},
		{
			name: "invalid target qualified kind",
			schema: refs.Schema{
				"workload": {{Path: []string{"spec", "a"}, Targets: []string{"secret.core"}}},
			},
		},
	}
	for _, tc := range invalidSchemas {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.schema.Validate(); err == nil {
				t.Fatalf("Schema.Validate() succeeded, want error")
			}
		})
	}
}

func TestPrepareAndCheckPhases(t *testing.T) {
	src := keyspace.Object{Group: "core", Kind: "workload", Name: "web"}
	baseSchema := refs.Schema{
		"workload": {
			{Path: []string{"spec", "*"}, Targets: []string{"secret", "configmap", "workload", "widget.example.com"}},
			{Path: []string{"spec", "list"}, Targets: []string{"secret", "configmap"}},
			{Path: []string{"spec", "onlycm"}, Targets: []string{"configmap"}},
		},
	}
	allowAll := func(refs.Occurrence) bool { return true }
	existAll := func(keyspace.Object) bool { return true }

	t.Run("phase 1 internal stored spec ref beats all later phases", func(t *testing.T) {
		stored := baseDoc(
			f("b", lkal.Ref{"secret.core", "x"}),
			f("a", lkal.Ref{"..", "x"}),
		)
		next := rec(
			f("status", rec(f("bad", lkal.Ref{"secret", "s"}))),
			f("spec", rec(f("c", lkal.Ref{"secret", "a/b"}))),
		)
		_, err := refs.Prepare(refs.Input{
			Source:    src,
			Schema:    baseSchema,
			Stored:    stored,
			HasStored: true,
			Next:      next,
		})
		assertRefsError(t, err, refs.ErrInternal, refs.Occurrence{Path: "spec/a", Ref: lkal.Ref{"..", "x"}})
	})

	t.Run("phase 1 internal stored ref in status or badly placed", func(t *testing.T) {
		cases := []struct {
			name    string
			stored  lkal.Record
			wantOcc refs.Occurrence
		}{
			{
				name: "ref in status",
				stored: rec(
					f("status", rec(f("target", lkal.Ref{"secret", "st"}))),
					f("spec", rec(f("ok", lkal.Ref{"secret", "s"}))),
				),
				wantOcc: refs.Occurrence{Path: "status/target", Ref: lkal.Ref{"secret", "st"}},
			},
			{
				name: "status ref and invalid spec ref reports smallest",
				stored: rec(
					f("status", rec(f("target", lkal.Ref{"secret", "st"}))),
					f("spec", rec(f("a", lkal.Ref{"..", "x"}))),
				),
				wantOcc: refs.Occurrence{Path: "spec/a", Ref: lkal.Ref{"..", "x"}},
			},
			{
				name:    "key containing slash in stored spec",
				stored:  baseDoc(f("db/password", lkal.Ref{"secret", "s"})),
				wantOcc: refs.Occurrence{Path: "spec/db/password", Ref: lkal.Ref{"secret", "s"}},
			},
			{
				name:    "key containing slash at stored root",
				stored:  rec(f("spec/a", lkal.Ref{"secret", "s"})),
				wantOcc: refs.Occurrence{Path: "spec/a", Ref: lkal.Ref{"secret", "s"}},
			},
			{
				name:    "empty key in stored spec",
				stored:  baseDoc(f("", lkal.Ref{"secret", "s"})),
				wantOcc: refs.Occurrence{Path: "spec/", Ref: lkal.Ref{"secret", "s"}},
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := refs.Prepare(refs.Input{
					Source:    src,
					Schema:    baseSchema,
					Stored:    tc.stored,
					HasStored: true,
					Next:      baseDoc(f("ok", lkal.Ref{"secret", "s"})),
				})
				assertRefsError(t, err, refs.ErrInternal, tc.wantOcc)
			})
		}
	})

	t.Run("phase 1 stored target limit 32 accepted and 33 internal with zero Occurrence", func(t *testing.T) {
		makeFields := func(n int) []lkal.Field {
			fields := make([]lkal.Field, n)
			for i := range n {
				fields[i] = f(fmt.Sprintf("f%02d", i), lkal.Ref{"secret", fmt.Sprintf("s%02d", i)})
			}
			return fields
		}

		// 33 stored targets fails phase 1 even when next reduces targets to 10 and even if next violates phase 2.
		stored33 := baseDoc(makeFields(refs.MaxTargets + 1)...)
		next10 := baseDoc(makeFields(10)...)
		_, err := refs.Prepare(refs.Input{
			Source:    src,
			Schema:    baseSchema,
			Stored:    stored33,
			HasStored: true,
			Next:      next10,
		})
		assertRefsError(t, err, refs.ErrInternal, refs.Occurrence{})

		nextMalformed := rec(
			f("status", rec(f("bad", lkal.Ref{"secret", "s"}))),
			f("spec", rec(makeFields(10)...)),
		)
		_, err = refs.Prepare(refs.Input{
			Source:    src,
			Schema:    baseSchema,
			Stored:    stored33,
			HasStored: true,
			Next:      nextMalformed,
		})
		assertRefsError(t, err, refs.ErrInternal, refs.Occurrence{})

		// Phase-1 occurrence checks run before the phase-1 target bound.
		stored33AndBad := baseDoc(append(makeFields(refs.MaxTargets+1), f("zz", lkal.Ref{"..", "x"}))...)
		_, err = refs.Prepare(refs.Input{
			Source:    src,
			Schema:    baseSchema,
			Stored:    stored33AndBad,
			HasStored: true,
			Next:      next10,
		})
		assertRefsError(t, err, refs.ErrInternal, refs.Occurrence{Path: "spec/zz", Ref: lkal.Ref{"..", "x"}})

		// Exactly 32 stored targets passes phase 1 and deletes removed targets when next reduces to 10.
		stored32 := baseDoc(makeFields(refs.MaxTargets)...)
		plan, err := refs.Prepare(refs.Input{
			Source:    src,
			Schema:    baseSchema,
			Stored:    stored32,
			HasStored: true,
			Next:      next10,
		})
		if err != nil {
			t.Fatalf("32 stored targets failed: %v", err)
		}
		delta, err := plan.Check(allowAll, existAll)
		if err != nil {
			t.Fatalf("Check failed: %v", err)
		}
		if len(delta.Delete) != refs.MaxTargets-10 || len(delta.Put) != 0 {
			t.Fatalf("unexpected delta: %+v", delta)
		}
	})

	t.Run("phase 2 outside spec beats added grammar and ordering", func(t *testing.T) {
		next := rec(
			f("meta", rec(
				f("kind", lkal.String("workload")),
				f("name", lkal.String("web")),
				f("bad", lkal.Ref{"secret", "m"}),
			)),
			f("spec", rec(
				f("bad", lkal.Ref{"..", "x"}),
			)),
			f("status", rec(
				f("bad", lkal.Ref{"secret", "s"}),
			)),
		)
		_, err := refs.Prepare(refs.Input{Source: src, Schema: baseSchema, Next: next})
		assertRefsError(t, err, refs.ErrMalformed, refs.Occurrence{Path: "meta/bad", Ref: lkal.Ref{"secret", "m"}})
	})

	t.Run("phase 2 spec itself as Ref", func(t *testing.T) {
		next := rec(
			f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("web")))),
			f("spec", lkal.Ref{"secret", "s"}),
		)
		_, err := refs.Prepare(refs.Input{Source: src, Schema: baseSchema, Next: next})
		assertRefsError(t, err, refs.ErrMalformed, refs.Occurrence{Path: "spec", Ref: lkal.Ref{"secret", "s"}})
	})

	t.Run("phase 2 slash or empty key in next", func(t *testing.T) {
		cases := []struct {
			name    string
			stored  lkal.Record
			hasSt   bool
			next    lkal.Record
			wantOcc refs.Occurrence
		}{
			{
				name:    "slash key under spec",
				next:    baseDoc(f("db/password", lkal.Ref{"secret", "s"})),
				wantOcc: refs.Occurrence{Path: "spec/db/password", Ref: lkal.Ref{"secret", "s"}},
			},
			{
				name:    "slash key at root",
				next:    rec(f("spec/a", lkal.Ref{"secret", "s"})),
				wantOcc: refs.Occurrence{Path: "spec/a", Ref: lkal.Ref{"secret", "s"}},
			},
			{
				name:    "empty key under spec",
				next:    baseDoc(f("", lkal.Ref{"secret", "s"})),
				wantOcc: refs.Occurrence{Path: "spec/", Ref: lkal.Ref{"secret", "s"}},
			},
			{
				name:    "empty key at root",
				next:    rec(f("", lkal.Ref{"secret", "s"})),
				wantOcc: refs.Occurrence{Path: "", Ref: lkal.Ref{"secret", "s"}},
			},
			{
				name:    "slash key moved from nested well-placed stored path",
				stored:  baseDoc(f("db", rec(f("password", lkal.Ref{"secret", "s"})))),
				hasSt:   true,
				next:    baseDoc(f("db/password", lkal.Ref{"secret", "s"})),
				wantOcc: refs.Occurrence{Path: "spec/db/password", Ref: lkal.Ref{"secret", "s"}},
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := refs.Prepare(refs.Input{
					Source:    src,
					Schema:    baseSchema,
					Stored:    tc.stored,
					HasStored: tc.hasSt,
					Next:      tc.next,
				})
				assertRefsError(t, err, refs.ErrMalformed, tc.wantOcc)
			})
		}
	})

	t.Run("phase 3 added grammar key-imitating and smallest order", func(t *testing.T) {
		badCases := []struct {
			name string
			ref  lkal.Ref
		}{
			{name: "key-imitating", ref: lkal.Ref{"t", "other", "o", "core", "secret", "x"}},
			{name: "dotdot", ref: lkal.Ref{"..", "x"}},
			{name: "slash in segment", ref: lkal.Ref{"secret", "x", "spec", "a/b"}},
			{name: "secret.core", ref: lkal.Ref{"secret.core", "x"}},
			{name: "leaf starting with o", ref: lkal.Ref{"secret", "x", "o", "core"}},
		}
		for _, tc := range badCases {
			t.Run(tc.name, func(t *testing.T) {
				next := baseDoc(f("a", tc.ref))
				_, err := refs.Prepare(refs.Input{Source: src, Schema: baseSchema, Next: next})
				assertRefsError(t, err, refs.ErrMalformed, refs.Occurrence{Path: "spec/a", Ref: tc.ref})
			})
		}

		// Ordering between two bad refs in same path (prefix first) and across paths.
		next := baseDoc(
			f("b", lkal.Ref{"..", "x"}),
			f("a", lkal.List{
				lkal.Ref{"secret", "x", "o", "y"},
				lkal.Ref{"secret", "x", "o"},
			}),
		)
		_, err := refs.Prepare(refs.Input{Source: src, Schema: baseSchema, Next: next})
		assertRefsError(t, err, refs.ErrMalformed, refs.Occurrence{Path: "spec/a", Ref: lkal.Ref{"secret", "x", "o"}})
	})

	t.Run("phase 4 target limit 32 passes and 33 fails with zero Occurrence", func(t *testing.T) {
		makeFields := func(n int) []lkal.Field {
			fields := make([]lkal.Field, n)
			for i := range n {
				fields[i] = f(fmt.Sprintf("f%02d", i), lkal.Ref{"secret", fmt.Sprintf("s%02d", i)})
			}
			return fields
		}

		// Exactly 32 distinct targets passes (even if schema would reject in phase 5, test with valid schema here).
		plan32, err := refs.Prepare(refs.Input{
			Source: src,
			Schema: baseSchema,
			Next:   baseDoc(makeFields(refs.MaxTargets)...),
		})
		if err != nil {
			t.Fatalf("32 targets failed: %v", err)
		}
		if len(plan32.Targets) != refs.MaxTargets {
			t.Fatalf("len(plan32.Targets) = %d, want %d", len(plan32.Targets), refs.MaxTargets)
		}

		// 34 occurrences pointing to 32 distinct targets passes.
		fields34 := append(makeFields(refs.MaxTargets),
			f("dup1", lkal.Ref{"secret", "s00", "spec", "k1"}),
			f("dup2", lkal.Ref{"secret", "s00", "spec", "k2"}),
		)
		if _, err := refs.Prepare(refs.Input{Source: src, Schema: baseSchema, Next: baseDoc(fields34...)}); err != nil {
			t.Fatalf("34 occurrences / 32 targets failed: %v", err)
		}

		// 33 distinct targets fails phase 4 even when schema is empty (phase 5 comes after phase 4).
		_, err = refs.Prepare(refs.Input{
			Source: src,
			Schema: nil,
			Next:   baseDoc(makeFields(refs.MaxTargets + 1)...),
		})
		assertRefsError(t, err, refs.ErrMalformed, refs.Occurrence{})
	})

	t.Run("phase 5 kind rejected and missing source kind in schema", func(t *testing.T) {
		schema := refs.Schema{
			"workload": {
				{Path: []string{"spec", "onlycm"}, Targets: []string{"configmap"}},
				{Path: []string{"spec", "z"}, Targets: []string{"configmap"}},
			},
		}
		next := baseDoc(
			f("z", lkal.Ref{"secret", "s1"}),
			f("onlycm", lkal.Ref{"secret", "s2"}),
		)
		// spec/onlycm comes before spec/z lexicographically.
		_, err := refs.Prepare(refs.Input{Source: src, Schema: schema, Next: next})
		assertRefsError(t, err, refs.ErrKind, refs.Occurrence{Path: "spec/onlycm", Ref: lkal.Ref{"secret", "s2"}})

		// Missing source kind in schema means empty allowed set.
		_, err = refs.Prepare(refs.Input{
			Source: keyspace.Object{Group: "core", Kind: "other", Name: "o1"},
			Schema: baseSchema,
			Next:   baseDoc(f("a", lkal.Ref{"secret", "s1"})),
		})
		assertRefsError(t, err, refs.ErrKind, refs.Occurrence{Path: "spec/a", Ref: lkal.Ref{"secret", "s1"}})
	})

	t.Run("phase 6 denied beats phase 7 notFound and checks smallest", func(t *testing.T) {
		next := baseDoc(
			f("a", lkal.Ref{"secret", "missing"}),
			f("b", lkal.Ref{"secret", "denied"}),
		)
		plan, err := refs.Prepare(refs.Input{Source: src, Schema: baseSchema, Next: next})
		if err != nil {
			t.Fatalf("Prepare unexpected error: %v", err)
		}
		_, err = plan.Check(
			func(occ refs.Occurrence) bool {
				return !slices.Equal(occ.Ref, lkal.Ref{"secret", "denied"})
			},
			func(obj keyspace.Object) bool {
				return obj.Name != "missing"
			},
		)
		assertRefsError(t, err, refs.ErrDenied, refs.Occurrence{Path: "spec/b", Ref: lkal.Ref{"secret", "denied"}})

		// Nil authorize fails closed with denied.
		_, err = plan.Check(nil, existAll)
		assertRefsError(t, err, refs.ErrDenied, refs.Occurrence{Path: "spec/a", Ref: lkal.Ref{"secret", "missing"}})
	})

	t.Run("phase 7 notFound checks smallest", func(t *testing.T) {
		next := baseDoc(
			f("b", lkal.Ref{"secret", "missing-b"}),
			f("a", lkal.Ref{"secret", "missing-a"}),
		)
		plan, err := refs.Prepare(refs.Input{Source: src, Schema: baseSchema, Next: next})
		if err != nil {
			t.Fatalf("Prepare unexpected error: %v", err)
		}
		_, err = plan.Check(allowAll, func(keyspace.Object) bool { return false })
		assertRefsError(t, err, refs.ErrNotFound, refs.Occurrence{Path: "spec/a", Ref: lkal.Ref{"secret", "missing-a"}})

		// Nil exists fails closed with notFound.
		_, err = plan.Check(allowAll, nil)
		assertRefsError(t, err, refs.ErrNotFound, refs.Occurrence{Path: "spec/a", Ref: lkal.Ref{"secret", "missing-a"}})
	})

	t.Run("moved ref, list refs, self ref, unchanged ref, and delta sorting", func(t *testing.T) {
		stored := baseDoc(
			f("kept", lkal.Ref{"secret", "kept"}),
			f("moved", lkal.Ref{"secret", "mv"}),
			f("removed", lkal.Ref{"configmap", "old"}),
			f("unchangedDenied", lkal.Ref{"secret", "legacy"}),
		)
		next := baseDoc(
			f("kept", lkal.Ref{"secret", "kept"}),
			f("newslot", lkal.Ref{"secret", "mv"}),
			f("unchangedDenied", lkal.Ref{"secret", "legacy"}),
			f("list", lkal.List{
				lkal.Ref{"configmap", "cm1"},
				lkal.Ref{"secret", "sec1"},
				lkal.Ref{"configmap", "cm1"},
			}),
			f("self", lkal.Ref{"workload", "web"}),
			f("grouped", lkal.Ref{"widget.example.com", "w1"}),
		)
		// Notice unchangedDenied is NOT allowed by schema at spec/unchangedDenied if schema omits it!
		restrictedSchema := refs.Schema{
			"workload": {
				{Path: []string{"spec", "newslot"}, Targets: []string{"secret"}},
				{Path: []string{"spec", "list"}, Targets: []string{"configmap", "secret"}},
				{Path: []string{"spec", "self"}, Targets: []string{"workload"}},
				{Path: []string{"spec", "grouped"}, Targets: []string{"widget.example.com"}},
			},
		}

		plan, err := refs.Prepare(refs.Input{
			Source:    src,
			Schema:    restrictedSchema,
			Stored:    stored,
			HasStored: true,
			Next:      next,
		})
		if err != nil {
			t.Fatalf("Prepare unexpected error: %v", err)
		}

		wantAdded := []refs.Occurrence{
			{Path: "spec/grouped", Ref: lkal.Ref{"widget.example.com", "w1"}},
			{Path: "spec/list", Ref: lkal.Ref{"configmap", "cm1"}},
			{Path: "spec/list", Ref: lkal.Ref{"secret", "sec1"}},
			{Path: "spec/newslot", Ref: lkal.Ref{"secret", "mv"}},
			{Path: "spec/self", Ref: lkal.Ref{"workload", "web"}},
		}
		if !reflect.DeepEqual(plan.Added, wantAdded) {
			t.Fatalf("plan.Added = %#v, want %#v", plan.Added, wantAdded)
		}

		wantCompare := []keyspace.Object{
			{Group: "core", Kind: "configmap", Name: "cm1"},
			{Group: "core", Kind: "secret", Name: "mv"},
			{Group: "core", Kind: "secret", Name: "sec1"},
			{Group: "example.com", Kind: "widget", Name: "w1"},
			{Group: "core", Kind: "workload", Name: "web"},
		}
		if !reflect.DeepEqual(plan.Targets, wantCompare) {
			t.Fatalf("plan.Targets = %#v, want %#v", plan.Targets, wantCompare)
		}

		var authed []refs.Occurrence
		delta, err := plan.Check(func(occ refs.Occurrence) bool {
			authed = append(authed, occ)
			return true
		}, existAll)
		if err != nil {
			t.Fatalf("Check unexpected error: %v", err)
		}
		if !reflect.DeepEqual(authed, wantAdded) {
			t.Fatalf("authed occurrences = %#v, want %#v", authed, wantAdded)
		}

		wantDelta := refs.Delta{
			Put: []keyspace.Object{
				{Group: "core", Kind: "configmap", Name: "cm1"},
				{Group: "core", Kind: "secret", Name: "sec1"},
				{Group: "example.com", Kind: "widget", Name: "w1"},
				{Group: "core", Kind: "workload", Name: "web"},
			},
			Delete: []keyspace.Object{
				{Group: "core", Kind: "configmap", Name: "old"},
			},
			Compare: wantCompare,
		}
		if !reflect.DeepEqual(delta, wantDelta) {
			t.Fatalf("delta = %#v, want %#v", delta, wantDelta)
		}
	})
}

func TestDeleteDelta(t *testing.T) {
	stored := baseDoc(
		f("a", lkal.Ref{"secret", "s1"}),
		f("b", lkal.Ref{"secret", "s1", "spec", "k"}),
		f("c", lkal.Ref{"widget.example.com", "w1"}),
		f("d", lkal.List{lkal.Ref{"configmap", "cm1"}}),
	)
	delta, err := refs.DeleteDelta(stored)
	if err != nil {
		t.Fatalf("DeleteDelta unexpected error: %v", err)
	}
	want := refs.Delta{
		Delete: []keyspace.Object{
			{Group: "core", Kind: "configmap", Name: "cm1"},
			{Group: "core", Kind: "secret", Name: "s1"},
			{Group: "example.com", Kind: "widget", Name: "w1"},
		},
	}
	if !reflect.DeepEqual(delta, want) {
		t.Fatalf("DeleteDelta = %#v, want %#v", delta, want)
	}

	makeFields := func(n int) []lkal.Field {
		fields := make([]lkal.Field, n)
		for i := range n {
			fields[i] = f(fmt.Sprintf("f%02d", i), lkal.Ref{"secret", fmt.Sprintf("s%02d", i)})
		}
		return fields
	}
	if delta32, err := refs.DeleteDelta(baseDoc(makeFields(refs.MaxTargets)...)); err != nil {
		t.Fatalf("DeleteDelta(32) unexpected error: %v", err)
	} else if len(delta32.Delete) != refs.MaxTargets || len(delta32.Put) != 0 || len(delta32.Compare) != 0 {
		t.Fatalf("DeleteDelta(32) = %#v", delta32)
	}

	_, err = refs.DeleteDelta(baseDoc(makeFields(refs.MaxTargets + 1)...))
	assertRefsError(t, err, refs.ErrInternal, refs.Occurrence{})

	errCases := []struct {
		name    string
		stored  lkal.Record
		wantOcc refs.Occurrence
	}{
		{
			name: "bad grammar in spec",
			stored: baseDoc(
				f("b", lkal.Ref{"secret.core", "x"}),
				f("a", lkal.Ref{"secret", "x", "o"}),
			),
			wantOcc: refs.Occurrence{Path: "spec/a", Ref: lkal.Ref{"secret", "x", "o"}},
		},
		{
			name:    "slash key in spec",
			stored:  baseDoc(f("a/b", lkal.Ref{"secret", "s1"})),
			wantOcc: refs.Occurrence{Path: "spec/a/b", Ref: lkal.Ref{"secret", "s1"}},
		},
		{
			name:    "empty key in spec",
			stored:  baseDoc(f("", lkal.Ref{"secret", "s1"})),
			wantOcc: refs.Occurrence{Path: "spec/", Ref: lkal.Ref{"secret", "s1"}},
		},
		{
			name:    "ref in status",
			stored:  rec(f("status", rec(f("r", lkal.Ref{"secret", "s1"})))),
			wantOcc: refs.Occurrence{Path: "status/r", Ref: lkal.Ref{"secret", "s1"}},
		},
	}
	for _, tc := range errCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := refs.DeleteDelta(tc.stored)
			assertRefsError(t, err, refs.ErrInternal, tc.wantOcc)
		})
	}
}
