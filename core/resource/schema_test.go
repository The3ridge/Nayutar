package resource_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/resource"
	"github.com/Kaikei-e/Nayutar/core/resource/envelope"
	"github.com/Kaikei-e/Nayutar/core/resource/orderedset"
	"github.com/Kaikei-e/Nayutar/core/resource/refs"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
	"github.com/Kaikei-e/Nayutar/core/store/memstore"
	"github.com/Kaikei-e/Nayutar/core/store/tenant"
)

func TestCompileKindSchemaRejections(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		wantSub string
	}{
		{
			name:    "syntax error",
			src:     `{`,
			wantSub: "L106",
		},
		{
			name: "invalid LKAL schema version",
			src: `@lkal "0.1"
lkal-schema = "0.1"
root {}
`,
			wantSub: "L607",
		},
		{
			name: "default violating ref targets L605",
			src: `@lkal "0.1"
lkal-schema = "0.2"
root {
  field "spec" {
    type = .record
    field "secretRef" {
      type = .ref
      targets = [&secret]
      default = &configmap/wrong
    }
  }
}
`,
			wantSub: "L605",
		},
		{
			name: "structural recursive def",
			src: `@lkal "0.1"
lkal-schema = "0.2"
def "node" {
  type = .record
  field "next" { use = &def/node }
}
root {
  field "spec" { use = &def/node }
}
`,
			wantSub: "recursive schema definition",
		},
		{
			name: "root open true",
			src: `@lkal "0.1"
lkal-schema = "0.2"
root {
  open = true
}
`,
			wantSub: "root cannot set open = true",
		},
		{
			name: "unknown root field",
			src: `@lkal "0.1"
lkal-schema = "0.2"
root {
  field "extra" { type = .string }
}
`,
			wantSub: `unexpected schema root field "extra"`,
		},
		{
			name: "meta default disallowed",
			src: `@lkal "0.1"
lkal-schema = "0.2"
root {
  field "meta" {
    type = .record
    default {}
  }
}
`,
			wantSub: "meta cannot declare default",
		},
		{
			name: "nested meta default disallowed",
			src: `@lkal "0.1"
lkal-schema = "0.2"
root {
  field "meta" {
    type = .record
    field "labels" {
      type = .record
      field "env" {
        type = .string
        default = "dev"
      }
    }
  }
}
`,
			wantSub: "meta/labels/env: default is only allowed under spec",
		},
		{
			name: "status default disallowed",
			src: `@lkal "0.1"
lkal-schema = "0.2"
root {
  field "status" {
    type = .record
    field "phase" {
      type = .string
      default = "Pending"
    }
  }
}
`,
			wantSub: "status/phase: default is only allowed under spec",
		},
		{
			name: "ref under status disallowed",
			src: `@lkal "0.1"
lkal-schema = "0.2"
root {
  field "status" {
    type = .record
    field "owner" {
      type = .ref
      targets = [&workload]
    }
  }
}
`,
			wantSub: "status/owner: .ref type is only allowed under spec",
		},
		{
			name: "ref under meta disallowed",
			src: `@lkal "0.1"
lkal-schema = "0.2"
root {
  field "meta" {
    type = .record
    field "owner" {
      type = .ref
      targets = [&workload]
    }
  }
}
`,
			wantSub: "meta/owner: .ref type is only allowed under spec",
		},
		{
			name: "ref without targets",
			src: `@lkal "0.1"
lkal-schema = "0.2"
root {
  field "spec" {
    type = .record
    field "secretRef" { type = .ref }
  }
}
`,
			wantSub: "spec/secretRef: .ref field must declare targets",
		},
		{
			name: "union ref without targets",
			src: `@lkal "0.1"
lkal-schema = "0.2"
root {
  field "spec" {
    type = .record
    field "from" { types = [.string, .ref] }
  }
}
`,
			wantSub: "spec/from: .ref field must declare targets",
		},
		{
			name: "multi-segment target ref",
			src: `@lkal "0.1"
lkal-schema = "0.2"
root {
  field "spec" {
    type = .record
    field "secretRef" {
      type = .ref
      targets = [&secret/specific]
    }
  }
}
`,
			wantSub: "single-segment qualified kind reference",
		},
		{
			name: "invalid qualified kind target secret.core",
			src: `@lkal "0.1"
lkal-schema = "0.2"
root {
  field "spec" {
    type = .record
    field "secretRef" {
      type = .ref
      targets = [&secret.core]
    }
  }
}
`,
			wantSub: `invalid target kind "secret.core"`,
		},
		{
			name: "ref path containing slash key",
			src: `@lkal "0.1"
lkal-schema = "0.2"
root {
  field "spec" {
    type = .record
    field "a/b" {
      type = .ref
      targets = [&secret]
    }
  }
}
`,
			wantSub: `invalid field key "a/b"`,
		},
		{
			name: "ref path containing wildcard key",
			src: `@lkal "0.1"
lkal-schema = "0.2"
root {
  field "spec" {
    type = .record
    field "*" {
      type = .ref
      targets = [&secret]
    }
  }
}
`,
			wantSub: `invalid field key "*"`,
		},
		{
			name: "ordered set missing required on order",
			src: `@lkal "0.1"
lkal-schema = "0.2"
root {
  field "spec" {
    type = .record
    field "plugins" {
      type = .map
      unique-by = ["order"]
      value {
        type = .record
        field "order" { type = .int }
      }
    }
  }
}
`,
			wantSub: "spec/plugins/*/order: ordered set order field must set required = true",
		},
		{
			name: "ordered set explicit required false on order",
			src: `@lkal "0.1"
lkal-schema = "0.2"
root {
  field "spec" {
    type = .record
    field "plugins" {
      type = .map
      unique-by = ["order"]
      value {
        type = .record
        field "order" { type = .int, required = false }
      }
    }
  }
}
`,
			wantSub: "spec/plugins/*/order: ordered set order field must set required = true",
		},
		{
			name: "ordered set open value record missing order field",
			src: `@lkal "0.1"
lkal-schema = "0.2"
root {
  field "spec" {
    type = .record
    field "plugins" {
      type = .map
      unique-by = ["order"]
      value {
        type = .record
        open = true
      }
    }
  }
}
`,
			wantSub: `spec/plugins: ordered set map value must explicitly declare field "order"`,
		},
		{
			name: "ordered set non-int order field",
			src: `@lkal "0.1"
lkal-schema = "0.2"
root {
  field "spec" {
    type = .record
    field "plugins" {
      type = .map
      unique-by = ["order"]
      value {
        type = .record
        field "order" { type = .string, required = true }
      }
    }
  }
}
`,
			wantSub: "spec/plugins/*/order: ordered set order field must have single type .int",
		},
		{
			name: "ordered set union order field",
			src: `@lkal "0.1"
lkal-schema = "0.2"
root {
  field "spec" {
    type = .record
    field "plugins" {
      type = .map
      unique-by = ["order"]
      value {
        type = .record
        field "order" { types = [.int, .decimal], required = true }
      }
    }
  }
}
`,
			wantSub: "spec/plugins/*/order: ordered set order field must have single type .int",
		},
		{
			name: "ordered set default on optional order field",
			src: `@lkal "0.1"
lkal-schema = "0.2"
root {
  field "spec" {
    type = .record
    field "plugins" {
      type = .map
      unique-by = ["order"]
      value {
        type = .record
        field "order" { type = .int, default = 10 }
      }
    }
  }
}
`,
			wantSub: "spec/plugins/*/order: ordered set order field cannot declare default",
		},
		{
			name: "ordered set field order without unique-by",
			src: `@lkal "0.1"
lkal-schema = "0.2"
root {
  field "spec" {
    type = .record
    field "plugins" {
      type = .map
      value {
        type = .record
        field "order" { type = .int, required = true }
      }
    }
  }
}
`,
			wantSub: `spec/plugins: map value declares field "order" without unique-by = ["order"]`,
		},
		{
			name: "ordered set field order with unrelated unique-by",
			src: `@lkal "0.1"
lkal-schema = "0.2"
root {
  field "spec" {
    type = .record
    field "plugins" {
      type = .map
      unique-by = ["port"]
      value {
        type = .record
        field "order" { type = .int, required = true }
        field "port" { type = .int }
      }
    }
  }
}
`,
			wantSub: `spec/plugins: map value declares field "order" without unique-by = ["order"]`,
		},
		{
			name: "ordered set invalid via def indirection on value and order",
			src: `@lkal "0.1"
lkal-schema = "0.2"
def "ord-type" {
  types = [.int, .string]
}
def "plugin-item" {
  type = .record
  field "order" { use = &def/ord-type, required = true }
}
def "plugin-map" {
  type = .map
  unique-by = ["order"]
  value { use = &def/plugin-item }
}
root {
  field "spec" {
    type = .record
    field "plugins" { use = &def/plugin-map }
  }
}
`,
			wantSub: "spec/plugins/*/order: ordered set order field must have single type .int",
		},
		{
			name: "ordered set invalid inside unused def",
			src: `@lkal "0.1"
lkal-schema = "0.2"
def "unused-map" {
  type = .map
  value {
    type = .record
    field "order" { type = .int, required = true }
  }
}
root {
  field "spec" {
    type = .record
    field "replicas" { type = .int }
  }
}
`,
			wantSub: `def/unused-map: map value declares field "order" without unique-by = ["order"]`,
		},
		{
			name: "ordered set invalid under status",
			src: `@lkal "0.1"
lkal-schema = "0.2"
root {
  field "spec" {
    type = .record
    field "replicas" { type = .int }
  }
  field "status" {
    type = .record
    field "steps" {
      type = .map
      value {
        type = .record
        field "order" { type = .int, required = true }
      }
    }
  }
}
`,
			wantSub: `status/steps: map value declares field "order" without unique-by = ["order"]`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ks, err := resource.CompileKindSchema("bad.schema.lkal", []byte(tc.src))
			if err == nil {
				t.Fatalf("CompileKindSchema succeeded (%+v), want error containing %q", ks, tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestCompileKindSchemaOrderedSetValid(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{
			name: "composite unique-by with order and optional port",
			src: `@lkal "0.1"
lkal-schema = "0.2"
root {
  field "spec" {
    type = .record
    field "listeners" {
      type = .map
      unique-by = ["order", "port"]
      value {
        type = .record
        field "order" { type = .int, required = true }
        field "port" { type = .int }
      }
    }
  }
}
`,
		},
		{
			name: "non-order unique-by remains unconstrained",
			src: `@lkal "0.1"
lkal-schema = "0.2"
root {
  field "spec" {
    type = .record
    field "members" {
      type = .map
      unique-by = ["port"]
      value {
        type = .record
        field "port" { type = .string }
      }
    }
  }
}
`,
		},
		{
			name: "open value record with explicit required int order",
			src: `@lkal "0.1"
lkal-schema = "0.2"
root {
  field "spec" {
    type = .record
    field "plugins" {
      type = .map
      unique-by = ["order"]
      value {
        type = .record
        open = true
        field "order" { type = .int, required = true }
      }
    }
  }
}
`,
		},
		{
			name: "def indirection for map value and order type",
			src: `@lkal "0.1"
lkal-schema = "0.2"
def "ord" {
  type = .int
}
def "plugin-item" {
  type = .record
  field "order" { use = &def/ord, required = true }
  field "type" { type = .string, required = true }
}
def "plugin-map" {
  type = .map
  unique-by = ["order"]
  value { use = &def/plugin-item }
}
root {
  field "spec" {
    type = .record
    field "plugins" { use = &def/plugin-map }
  }
}
`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ks, err := resource.CompileKindSchema("valid.schema.lkal", []byte(tc.src))
			if err != nil {
				t.Fatalf("CompileKindSchema: %v", err)
			}
			if ks.Schema() == nil {
				t.Fatal("Schema() = nil")
			}
		})
	}
}

func TestKindSchemaDerivesRefRules(t *testing.T) {
	ks, err := resource.CompileKindSchema("workload.schema.lkal", []byte(`@lkal "0.1"
lkal-schema = "0.2"

def "env-var" {
  type = .record
  field "value" { type = .string }
  field "from" {
    types = [.string, .ref]
    targets = [&secret, &configmap, &secret]
  }
}

root {
  field "spec" {
    type = .record
    field "serviceAccount" {
      type = .ref
      targets = [&serviceaccount]
    }
    field "volumes" {
      type = .list
      items {
        type = .ref
        targets = [&volume.storage.example.com]
      }
    }
    field "container" {
      type = .map
      value {
        type = .record
        field "image" { type = .string, required = true }
        field "env" {
          type = .map
          value { use = &def/env-var }
        }
      }
    }
  }
}
`))
	if err != nil {
		t.Fatalf("CompileKindSchema: %v", err)
	}
	if ks.Schema() == nil {
		t.Fatal("Schema() = nil, want non-nil")
	}

	want := []refs.Rule{
		{Path: []string{"spec", "serviceAccount"}, Targets: []string{"serviceaccount"}},
		{Path: []string{"spec", "volumes"}, Targets: []string{"volume.storage.example.com"}},
		{Path: []string{"spec", "container", "*", "env", "*", "from"}, Targets: []string{"secret", "configmap"}},
	}
	got := ks.RefRules()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RefRules() =\n%#v\nwant:\n%#v", got, want)
	}

	// Mutating returned slice must not mutate internal state.
	got[0].Path[0] = "mutated"
	got[0].Targets[0] = "mutated"
	if !reflect.DeepEqual(ks.RefRules(), want) {
		t.Fatal("RefRules() returned aliased slices")
	}
}

func TestObjectsKindSchemaCompletionValidationAndOrderedSets(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	auth := &fakeAuthorizer{}

	workloadKS, err := resource.CompileKindSchema("workload.schema.lkal", []byte(`@lkal "0.1"
lkal-schema = "0.2"

root {
  field "spec" {
    type = .record
    default {}
    field "replicas" {
      type = .int
      min = 0
      max = 100
      default = 2
    }
    field "creds" {
      type = .ref
      targets = [&secret]
      default = &secret/default-creds
    }
    field "init" {
      type = .map
      unique-by = ["order"]
      value {
        type = .record
        field "order" { type = .int, required = true }
        field "image" { type = .string, required = true }
        field "timeout" { type = .quantity, default = 30s }
      }
    }
    field "extra" {
      type = .record
      open = true
    }
  }
  field "status" {
    type = .record
    field "readyReplicas" { type = .int, min = 0 }
  }
}
`))
	if err != nil {
		t.Fatalf("CompileKindSchema(workload): %v", err)
	}
	secretKS, err := resource.CompileKindSchema("secret.schema.lkal", []byte(`@lkal "0.1"
lkal-schema = "0.2"
root {
  field "spec" {
    type = .record
    field "data" { type = .string }
  }
}
`))
	if err != nil {
		t.Fatalf("CompileKindSchema(secret): %v", err)
	}

	cfg, _ := testObjectsConfig()
	cfg.Tenant = "acme"
	cfg.Schemas = resource.Schemas{
		"workload": workloadKS,
		"secret":   secretKS,
	}
	cfg.Authorizer = auth
	objs := resource.NewObjects(st, cfg)

	// Unregistered kind is rejected fail-closed on Create.
	unknownCand := rec(
		f("meta", rec(f("kind", lkal.String("daemon")), f("name", lkal.String("d1")))),
		f("spec", rec()),
	)
	if _, err := objs.Create(ctx, testPrincipal, unknownCand); !errors.Is(err, envelope.ErrMalformed) {
		t.Fatalf("Create(unregistered kind) = %v, want envelope.ErrMalformed", err)
	}

	// Defaulted Ref still undergoes existence check (fails with ErrNotFound before default-creds exists).
	minCand := rec(
		f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("app")))),
		f("spec", rec(
			f("init", rec(
				f("migrate", rec(f("order", lkal.Int(10)), f("image", lkal.String("acme/migrate:1")))),
				f("seed", rec(f("order", lkal.Int(20)), f("image", lkal.String("acme/seed:1")))),
			)),
		)),
	)
	_, err = objs.Create(ctx, testPrincipal, minCand)
	_ = assertRefsErr(t, err, refs.ErrNotFound)

	// Seed default-creds secret; Create now completes replicas=2, creds=&secret/default-creds, and init/*/timeout=30s.
	if _, err := objs.Create(ctx, testPrincipal, secretCand("default-creds")); err != nil {
		t.Fatalf("Create(default-creds): %v", err)
	}
	auth.Reset()
	created, err := objs.Create(ctx, testPrincipal, minCand)
	if err != nil {
		t.Fatalf("Create(app): %v", err)
	}

	// Verify defaulted Ref was authorized and indexed.
	if len(auth.Requests()) != 1 || auth.Requests()[0].SourcePath != "spec/creds" {
		t.Fatalf("authorizer requests = %+v, want 1 request for spec/creds", auth.Requests())
	}
	assertBackRefPresent(t, ctx, st,
		keyspace.Object{Group: "core", Kind: "secret", Name: "default-creds"},
		keyspace.Object{Group: "core", Kind: "workload", Name: "app"},
	)

	spec, _ := getRecordField(created, "spec")
	if got := getIntField(spec, "replicas"); got != 2 {
		t.Fatalf("completed replicas = %d, want 2", got)
	}
	initMap, _ := getRecordField(spec, "init")
	migrateRec, _ := getRecordField(initMap, "migrate")
	if gotQty, ok := getField(migrateRec, "timeout"); !ok || !lkal.Equal(gotQty, lkal.Quantity{Number: "30", Unit: "s"}) {
		t.Fatalf("completed init/migrate/timeout = %#v, want 30s", gotQty)
	}

	// Re-sending the un-defaulted candidate on Update completes to the identical spec:
	// generation stays 1 and store revision does not advance.
	revBefore, err := st.CurrentRev(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reapplied, err := objs.Update(ctx, testPrincipal, minCand)
	if err != nil {
		t.Fatalf("Update(idempotent un-defaulted): %v", err)
	}
	revAfter, err := st.CurrentRev(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if revAfter != revBefore {
		t.Fatalf("idempotent Update moved store revision: %d -> %d", revBefore, revAfter)
	}
	reMeta, _ := getRecordField(reapplied, "meta")
	if getIntField(reMeta, "generation") != 1 {
		t.Fatalf("idempotent Update generation = %d, want 1", getIntField(reMeta, "generation"))
	}

	// Omitting spec completely also materializes spec default {} and nested defaults.
	noSpecCand := rec(f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("bare")))))
	bareCreated, err := objs.Create(ctx, testPrincipal, noSpecCand)
	if err != nil {
		t.Fatalf("Create(bare): %v", err)
	}
	bareSpec, ok := getRecordField(bareCreated, "spec")
	if !ok || getIntField(bareSpec, "replicas") != 2 {
		t.Fatalf("bare spec = %#v, want replicas=2", bareSpec)
	}

	// ADR 0011 duplicate order leaf in ordered record set is rejected with L509 -> ErrMalformed.
	dupOrderCand := rec(
		f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("dup-order")))),
		f("spec", rec(
			f("init", rec(
				f("a", rec(f("order", lkal.Int(10)), f("image", lkal.String("x")))),
				f("b", rec(f("order", lkal.Int(10)), f("image", lkal.String("y")))),
			)),
		)),
	)
	_, err = objs.Create(ctx, testPrincipal, dupOrderCand)
	var envErr *envelope.Error
	if !errors.As(err, &envErr) || envErr.Kind != envelope.ErrMalformed || !strings.Contains(envErr.Detail, "L509") {
		t.Fatalf("duplicate order Create = %v, want envelope.ErrMalformed with L509", err)
	}

	// Ref smuggled inside open=true record under spec is still rejected fail-closed by refs.Prepare (ErrKind).
	openRefCand := rec(
		f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("open-ref")))),
		f("spec", rec(
			f("extra", rec(f("sneaky", lkal.Ref{"secret", "default-creds"}))),
		)),
	)
	_, err = objs.Create(ctx, testPrincipal, openRefCand)
	_ = assertRefsErr(t, err, refs.ErrKind)

	// Closed status automatically allows observedGeneration and condition alongside readyReplicas,
	// while rejecting unknown status fields.
	validStatus := rec(
		f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("app")))),
		f("status", rec(
			f("observedGeneration", lkal.Int(1)),
			f("readyReplicas", lkal.Int(2)),
			f("condition", rec(
				f("Ready", rec(f("state", lkal.Enum("true")), f("reason", lkal.String("Available")))),
			)),
		)),
	)
	if _, err := objs.UpdateStatus(ctx, validStatus); err != nil {
		t.Fatalf("UpdateStatus(valid augmented): %v", err)
	}
	unknownStatus := rec(
		f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("app")))),
		f("status", rec(
			f("observedGeneration", lkal.Int(1)),
			f("undeclared", lkal.String("nope")),
		)),
	)
	if _, err := objs.UpdateStatus(ctx, unknownStatus); !errors.Is(err, envelope.ErrMalformed) {
		t.Fatalf("UpdateStatus(unknown field) = %v, want envelope.ErrMalformed", err)
	}
}

func TestObjectsNarrowedRefTargetsDoNotBreakUnchangedRefsOrMaskErrors(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	auth := &fakeAuthorizer{}

	const v1Src = `@lkal "0.1"
lkal-schema = "0.2"
root {
  field "spec" {
    type = .record
    field "items" {
      type = .list
      items {
        type = .ref
        targets = [&secret, &configmap]
      }
    }
    field "replicas" { type = .int }
  }
}
`
	const v2NarrowedSrc = `@lkal "0.1"
lkal-schema = "0.2"
root {
  field "spec" {
    type = .record
    field "items" {
      type = .list
      items {
        type = .ref
        targets = [&secret]
      }
    }
    field "replicas" { type = .int }
  }
}
`
	v1KS, err := resource.CompileKindSchema("workload-v1.schema.lkal", []byte(v1Src))
	if err != nil {
		t.Fatal(err)
	}
	v2KS, err := resource.CompileKindSchema("workload-v2.schema.lkal", []byte(v2NarrowedSrc))
	if err != nil {
		t.Fatal(err)
	}
	cmKS, err := resource.CompileKindSchema("cm.schema.lkal", []byte(`@lkal "0.1"
lkal-schema = "0.2"
root { field "spec" { type = .record, field "key" { type = .string } } }
`))
	if err != nil {
		t.Fatal(err)
	}
	secKS, err := resource.CompileKindSchema("sec.schema.lkal", []byte(`@lkal "0.1"
lkal-schema = "0.2"
root { field "spec" { type = .record, field "data" { type = .string } } }
`))
	if err != nil {
		t.Fatal(err)
	}

	cfgV1, _ := testObjectsConfig()
	cfgV1.Tenant = "acme"
	cfgV1.Schemas = resource.Schemas{"workload": v1KS, "configmap": cmKS, "secret": secKS}
	cfgV1.Authorizer = auth
	objsV1 := resource.NewObjects(st, cfgV1)

	// Create 25 configmaps and reference all 25 in a single workload under v1 schema (>20 maxErrors cap).
	refList := make(lkal.List, 25)
	for i := range 25 {
		name := fmt.Sprintf("cm-%02d", i)
		if _, err := objsV1.Create(ctx, testPrincipal, configMapCand(name)); err != nil {
			t.Fatal(err)
		}
		refList[i] = lkal.Ref{"configmap", name}
	}
	if _, err := objsV1.Create(ctx, testPrincipal, rec(
		f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("legacy")))),
		f("spec", rec(f("items", refList), f("replicas", lkal.Int(1)))),
	)); err != nil {
		t.Fatalf("v1 Create: %v", err)
	}

	// Switch to v2 schema where items targets narrowed to [&secret].
	cfgV2 := cfgV1
	cfgV2.Schemas = resource.Schemas{"workload": v2KS, "configmap": cmKS, "secret": secKS}
	objsV2 := resource.NewObjects(st, cfgV2)

	// 1) Updating replicas while keeping the 25 existing configmap refs unchanged MUST succeed (ADR 0026).
	if _, err := objsV2.Update(ctx, testPrincipal, rec(
		f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("legacy")))),
		f("spec", rec(f("items", refList), f("replicas", lkal.Int(3)))),
	)); err != nil {
		t.Fatalf("v2 Update with unchanged narrowed refs failed: %v", err)
	}

	// 2) A real type error in replicas ("bad" instead of int) MUST NOT be masked by 25 narrowed refs hitting maxErrors=20.
	_, err = objsV2.Update(ctx, testPrincipal, rec(
		f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("legacy")))),
		f("spec", rec(f("items", refList), f("replicas", lkal.String("not-an-int")))),
	))
	var envErr *envelope.Error
	if !errors.As(err, &envErr) || envErr.Kind != envelope.ErrMalformed || envErr.Path != "spec/replicas" {
		t.Fatalf("v2 Update with bad replicas = %v, want envelope.ErrMalformed at spec/replicas", err)
	}

	// 3) Adding a NEW configmap ref under v2 MUST still fail with refs.ErrKind.
	if _, err := objsV2.Create(ctx, testPrincipal, configMapCand("cm-new")); err != nil {
		t.Fatal(err)
	}
	withAdded := append(refList, lkal.Ref{"configmap", "cm-new"})
	_, err = objsV2.Update(ctx, testPrincipal, rec(
		f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("legacy")))),
		f("spec", rec(f("items", withAdded), f("replicas", lkal.Int(3)))),
	))
	_ = assertRefsErr(t, err, refs.ErrKind)
}

func TestObjectsOrderedSetStoreRoundTrip(t *testing.T) {
	checkObjectsOrderedSetRoundTrip(t, memstore.New())
}

func checkObjectsOrderedSetRoundTrip(t *testing.T, st tenant.Store) {
	t.Helper()
	ctx := context.Background()

	netKS, err := resource.CompileKindSchema("network.schema.lkal", []byte(`@lkal "0.1"
lkal-schema = "0.2"
root {
  field "spec" {
    type = .record
    field "plugins" {
      type = .map
      unique-by = ["order"]
      value {
        type = .record
        open = true
        field "order" { type = .int, required = true }
        field "type" { type = .string, required = true }
      }
    }
    field "init" {
      type = .map
      unique-by = ["order"]
      value {
        type = .record
        field "order" { type = .int, required = true }
        field "image" { type = .string, required = true }
        field "timeout" { type = .quantity, default = 30s }
      }
    }
  }
}
`))
	if err != nil {
		t.Fatalf("CompileKindSchema: %v", err)
	}

	cfg, _ := testObjectsConfig()
	cfg.Tenant = "acme"
	cfg.Schemas = resource.Schemas{"network": netKS}
	objs := resource.NewObjects(st, cfg)

	cniItems := []orderedset.Item{
		{Key: "fw-pre", Value: rec(f("type", lkal.String("firewall")), f("policy", lkal.String("allow")))},
		{Key: "bridge", Value: rec(f("type", lkal.String("bridge")), f("bridge", lkal.String("lk0")))},
		{Key: "fw-post", Value: rec(f("type", lkal.String("firewall")), f("policy", lkal.String("drop")))},
	}
	pluginsSet, err := orderedset.FromItems(cniItems)
	if err != nil {
		t.Fatalf("FromItems: %v", err)
	}

	initElems := []lkal.Record{
		rec(f("name", lkal.String("migrate")), f("image", lkal.String("acme/migrate:1"))),
		rec(f("name", lkal.String("seed")), f("image", lkal.String("acme/seed:1"))),
	}
	initSet, err := orderedset.FromKeyedSlice(initElems, "name")
	if err != nil {
		t.Fatalf("FromKeyedSlice: %v", err)
	}

	cand := rec(
		f("meta", rec(f("kind", lkal.String("network")), f("name", lkal.String("podnet")))),
		f("spec", rec(f("plugins", pluginsSet), f("init", initSet))),
	)
	created, err := objs.Create(ctx, testPrincipal, cand)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := objs.Get(ctx, keyspace.Object{Group: "core", Kind: "network", Name: "podnet"})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !lkal.Equal(got, created) {
		t.Fatalf("Get != Create")
	}

	specRec, _ := getRecordField(got, "spec")
	storedPlugins, _ := getRecordField(specRec, "plugins")
	entries, err := orderedset.Entries(storedPlugins)
	if err != nil {
		t.Fatalf("Entries(stored plugins): %v", err)
	}
	if len(entries) != len(cniItems) {
		t.Fatalf("len(entries) = %d, want %d", len(entries), len(cniItems))
	}
	for i, e := range entries {
		if e.Key != cniItems[i].Key || e.Order != orderedset.Step*int64(i+1) || !lkal.Equal(e.Value, cniItems[i].Value) {
			t.Fatalf("plugin[%d] = %+v, want key=%q order=%d val=%#v", i, e, cniItems[i].Key, orderedset.Step*int64(i+1), cniItems[i].Value)
		}
	}

	revEntries, err := orderedset.EntriesReverse(storedPlugins)
	if err != nil {
		t.Fatalf("EntriesReverse: %v", err)
	}
	if revEntries[0].Key != "fw-post" || revEntries[2].Key != "fw-pre" {
		t.Fatalf("EntriesReverse order = [%s, %s, %s]", revEntries[0].Key, revEntries[1].Key, revEntries[2].Key)
	}

	storedInit, _ := getRecordField(specRec, "init")
	projectedInit, err := orderedset.ToKeyedSlice(storedInit, "name")
	if err != nil {
		t.Fatalf("ToKeyedSlice(stored init): %v", err)
	}
	if len(projectedInit) != 2 || getStringField(projectedInit[0], "name") != "migrate" || getStringField(projectedInit[1], "name") != "seed" {
		t.Fatalf("ToKeyedSlice = %#v", projectedInit)
	}
	if qty, ok := getField(projectedInit[0], "timeout"); !ok || !lkal.Equal(qty, lkal.Quantity{Number: "30", Unit: "s"}) {
		t.Fatalf("completed timeout = %#v, want 30s", qty)
	}

	// Permuting map fields without changing order leaves is an idempotent no-op (no generation or rev bump).
	revBefore, err := st.CurrentRev(ctx)
	if err != nil {
		t.Fatal(err)
	}
	permutedPlugins := rec(pluginsSet.Fields[2], pluginsSet.Fields[0], pluginsSet.Fields[1])
	permutedCand := rec(
		f("meta", rec(f("kind", lkal.String("network")), f("name", lkal.String("podnet")))),
		f("spec", rec(f("plugins", permutedPlugins), f("init", initSet))),
	)
	afterPerm, err := objs.Update(ctx, testPrincipal, permutedCand)
	if err != nil {
		t.Fatalf("Update(permuted): %v", err)
	}
	revAfter, err := st.CurrentRev(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if revAfter != revBefore {
		t.Fatalf("permuted Update moved revision: %d -> %d", revBefore, revAfter)
	}
	permMeta, _ := getRecordField(afterPerm, "meta")
	if getIntField(permMeta, "generation") != 1 {
		t.Fatalf("permuted Update generation = %d, want 1", getIntField(permMeta, "generation"))
	}

	// Prepend before 10 (order 0) and insert midpoint between 10 and 20 (order 15) -> bumps generation to 2.
	beforeOrder, err := orderedset.OrderBefore(10)
	if err != nil || beforeOrder != 0 {
		t.Fatalf("OrderBefore(10) = (%d, %v), want (0, nil)", beforeOrder, err)
	}
	midOrder, err := orderedset.OrderBetween(10, 20)
	if err != nil || midOrder != 15 {
		t.Fatalf("OrderBetween(10, 20) = (%d, %v), want (15, nil)", midOrder, err)
	}
	expandedPlugins := rec(
		pluginsSet.Fields[0],
		pluginsSet.Fields[1],
		pluginsSet.Fields[2],
		f("pre-hook", rec(f("order", lkal.Int(beforeOrder)), f("type", lkal.String("tuning")))),
		f("mid-hook", rec(f("order", lkal.Int(midOrder)), f("type", lkal.String("portmap")))),
	)
	updated, err := objs.Update(ctx, testPrincipal, rec(
		f("meta", rec(f("kind", lkal.String("network")), f("name", lkal.String("podnet")))),
		f("spec", rec(f("plugins", expandedPlugins), f("init", initSet))),
	))
	if err != nil {
		t.Fatalf("Update(expanded): %v", err)
	}
	updMeta, _ := getRecordField(updated, "meta")
	if getIntField(updMeta, "generation") != 2 {
		t.Fatalf("expanded Update generation = %d, want 2", getIntField(updMeta, "generation"))
	}
	updSpec, _ := getRecordField(updated, "spec")
	updPlugins, _ := getRecordField(updSpec, "plugins")
	updEntries, err := orderedset.Entries(updPlugins)
	if err != nil {
		t.Fatalf("Entries(expanded): %v", err)
	}
	wantExpanded := []string{"pre-hook", "fw-pre", "mid-hook", "bridge", "fw-post"}
	for i, e := range updEntries {
		if e.Key != wantExpanded[i] {
			t.Fatalf("expanded[%d].Key = %q, want %q", i, e.Key, wantExpanded[i])
		}
	}

	// Renumber persists clean 10, 20, 30, 40, 50 while preserving exact element order.
	renPlugins, err := orderedset.Renumber(updPlugins)
	if err != nil {
		t.Fatalf("Renumber: %v", err)
	}
	afterRen, err := objs.Update(ctx, testPrincipal, rec(
		f("meta", rec(f("kind", lkal.String("network")), f("name", lkal.String("podnet")))),
		f("spec", rec(f("plugins", renPlugins), f("init", initSet))),
	))
	if err != nil {
		t.Fatalf("Update(renumbered): %v", err)
	}
	renSpec, _ := getRecordField(afterRen, "spec")
	renStoredPlugins, _ := getRecordField(renSpec, "plugins")
	renEntries, err := orderedset.Entries(renStoredPlugins)
	if err != nil {
		t.Fatalf("Entries(renumbered): %v", err)
	}
	for i, e := range renEntries {
		if e.Key != wantExpanded[i] || e.Order != orderedset.Step*int64(i+1) {
			t.Fatalf("renEntries[%d] = (%q, %d), want (%q, %d)", i, e.Key, e.Order, wantExpanded[i], orderedset.Step*int64(i+1))
		}
	}

	// Missing required order field in an open value record is rejected with L502 -> ErrMalformed.
	missingOrder := rec(
		f("meta", rec(f("kind", lkal.String("network")), f("name", lkal.String("bad-missing")))),
		f("spec", rec(f("plugins", rec(f("bridge", rec(f("type", lkal.String("bridge")))))))),
	)
	_, err = objs.Create(ctx, testPrincipal, missingOrder)
	var envErr *envelope.Error
	if !errors.As(err, &envErr) || envErr.Kind != envelope.ErrMalformed || !strings.Contains(envErr.Detail, "L502") {
		t.Fatalf("Create(missing order) = %v, want envelope.ErrMalformed with L502", err)
	}
}
