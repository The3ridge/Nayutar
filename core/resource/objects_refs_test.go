package resource_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/resource"
	"github.com/Kaikei-e/Nayutar/core/resource/envelope"
	"github.com/Kaikei-e/Nayutar/core/resource/refcedar"
	"github.com/Kaikei-e/Nayutar/core/resource/refs"
	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
	"github.com/Kaikei-e/Nayutar/core/store/memstore"
	"github.com/Kaikei-e/Nayutar/core/store/tenant"
	"github.com/cedar-policy/cedar-go"
)

var (
	testPrincipal  = refs.Principal{Type: "User", ID: "alice"}
	cedarAlice     = refs.Principal{Type: "Nayutar::User", ID: "alice"}
	cedarBob       = refs.Principal{Type: "Nayutar::User", ID: "bob"}
	defaultCedarPS = `
permit(
  principal == Nayutar::User::"alice",
  action == Nayutar::Action::"Reference",
  resource
) when {
  resource has labels && resource.labels has "share"
};
forbid(
  principal,
  action == Nayutar::Action::"Reference",
  resource
) when {
  context.sourcePath like "spec/forbidden*"
};
`
)

type fakeAuthorizer struct {
	mu       sync.Mutex
	requests []refs.Request
	allow    func(refs.Request) bool
}

func (f *fakeAuthorizer) Authorize(req refs.Request) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	if f.allow == nil {
		return true
	}
	return f.allow(req)
}

func (f *fakeAuthorizer) Requests() []refs.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

func (f *fakeAuthorizer) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = nil
}

func mustCedarAuthorizer(t *testing.T, policySrc string) *refcedar.Authorizer {
	t.Helper()
	ps, err := cedar.NewPolicySetFromBytes("test.cedar", []byte(policySrc))
	if err != nil {
		t.Fatalf("parse cedar policies: %v", err)
	}
	auth, err := refcedar.New(ps, nil)
	if err != nil {
		t.Fatalf("refcedar.New: %v", err)
	}
	return auth
}

var refSuiteSchemas = func() resource.Schemas {
	mustCompile := func(name, src string) *resource.KindSchema {
		ks, err := resource.CompileKindSchema(name, []byte(src))
		if err != nil {
			panic(fmt.Sprintf("compile %s: %v", name, err))
		}
		return ks
	}
	var b strings.Builder
	b.WriteString(`@lkal "0.1"
lkal-schema = "0.2"

def "any-ref" {
  type = .ref
  targets = [&secret, &configmap, &workload, &widget.example.com]
}

root {
  field "spec" {
    type = .record
    field "primary" { use = &def/any-ref }
    field "secondary" { use = &def/any-ref }
    field "config" { use = &def/any-ref }
    field "priv" { use = &def/any-ref }
    field "forbiddenSecret" { use = &def/any-ref }
    field "r" { use = &def/any-ref }
    field "db" { use = &def/any-ref }
    field "s" { use = &def/any-ref }
    field "self" { use = &def/any-ref }
    field "b" { use = &def/any-ref }
    field "c" { use = &def/any-ref }
    field "list" {
      type = .list
      items { use = &def/any-ref }
    }
    field "a" {
      type = .list
      items { use = &def/any-ref }
    }
    field "secrets" {
      type = .map
      value {
        type = .ref
        targets = [&secret]
      }
    }
    field "count" { type = .int }
    field "n" { type = .int }
`)
	for i := range 10 {
		fmt.Fprintf(&b, "    field \"f%02d\" { use = &def/any-ref }\n", i)
	}
	b.WriteString(`  }
  field "status" {
    type = .record
    open = true
  }
}
`)
	return resource.Schemas{
		"workload": mustCompile("workload.schema.lkal", b.String()),
		"secret": mustCompile("secret.schema.lkal", `@lkal "0.1"
lkal-schema = "0.2"
root {
  field "spec" {
    type = .record
    field "data" { type = .string }
  }
}
`),
		"configmap": mustCompile("configmap.schema.lkal", `@lkal "0.1"
lkal-schema = "0.2"
root {
  field "spec" {
    type = .record
    field "key" { type = .string }
  }
}
`),
		"widget.example.com": mustCompile("widget.schema.lkal", `@lkal "0.1"
lkal-schema = "0.2"
root {
  field "spec" { type = .record }
}
`),
	}
}()

func testRefSchemas() resource.Schemas {
	return maps.Clone(refSuiteSchemas)
}

func secretCand(name string, labels ...lkal.Field) lkal.Record {
	metaFields := []lkal.Field{
		f("kind", lkal.String("secret")),
		f("name", lkal.String(name)),
	}
	if len(labels) > 0 {
		metaFields = append(metaFields, f("labels", rec(labels...)))
	}
	return rec(
		f("meta", rec(metaFields...)),
		f("spec", rec(f("data", lkal.String("val")))),
	)
}

func configMapCand(name string, labels ...lkal.Field) lkal.Record {
	metaFields := []lkal.Field{
		f("kind", lkal.String("configmap")),
		f("name", lkal.String(name)),
	}
	if len(labels) > 0 {
		metaFields = append(metaFields, f("labels", rec(labels...)))
	}
	return rec(
		f("meta", rec(metaFields...)),
		f("spec", rec(f("key", lkal.String("v")))),
	)
}

func workloadRefCand(name string, specFields ...lkal.Field) lkal.Record {
	return rec(
		f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String(name)))),
		f("spec", rec(specFields...)),
	)
}

func objectRelKey(t *testing.T, obj keyspace.Object) string {
	t.Helper()
	k, err := (keyspace.Key{
		Family: keyspace.FamilyObject,
		Tenant: "t",
		Object: obj,
	}).Rel()
	if err != nil {
		t.Fatalf("object key(%v): %v", obj, err)
	}
	return k
}

func backRefRelKey(t *testing.T, target, source keyspace.Object) string {
	t.Helper()
	k, err := (keyspace.Key{
		Family: keyspace.FamilyBackRef,
		Tenant: "t",
		Object: target,
		Source: source,
	}).Rel()
	if err != nil {
		t.Fatalf("backref key(%v, %v): %v", target, source, err)
	}
	return k
}

func assertBackRefPresent(t *testing.T, ctx context.Context, st tenant.Store, target, source keyspace.Object) {
	t.Helper()
	k := backRefRelKey(t, target, source)
	kv, _, err := st.Get(ctx, k, 0)
	if err != nil {
		t.Fatalf("expected back-ref %s to exist: %v", k, err)
	}
	if len(kv.Value) != 0 {
		t.Fatalf("back-ref %s value = %q, want empty", k, kv.Value)
	}
}

func assertBackRefAbsent(t *testing.T, ctx context.Context, st tenant.Store, target, source keyspace.Object) {
	t.Helper()
	k := backRefRelKey(t, target, source)
	if kv, _, err := st.Get(ctx, k, 0); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected back-ref %s to be absent, got (%+v, %v)", k, kv, err)
	}
}

func assertRefsErr(t *testing.T, err error, wantKind refs.ErrorKind) *refs.Error {
	t.Helper()
	if err == nil {
		t.Fatalf("got nil error, want *refs.Error(%s)", wantKind)
	}
	var re *refs.Error
	if !errors.As(err, &re) {
		t.Fatalf("error %T (%v) is not *refs.Error", err, err)
	}
	if re.Kind != wantKind {
		t.Fatalf("got *refs.Error kind %s, want %s (%v)", re.Kind, wantKind, re)
	}
	return re
}

func openRegisteredTenant(t *testing.T, ctx context.Context, root store.Store, name string) tenant.Store {
	t.Helper()
	reg := tenant.NewRegistry(root)
	if err := reg.Register(ctx, name); err != nil {
		t.Fatalf("Register(%q): %v", name, err)
	}
	v, err := reg.View(ctx, name)
	if err != nil {
		t.Fatalf("View(%q): %v", name, err)
	}
	return v
}

func seedBackRefsInChunks(t *testing.T, ctx context.Context, st tenant.Store, ops []store.Op) {
	t.Helper()
	const chunk = 50
	for len(ops) > 0 {
		n := min(chunk, len(ops))
		ok, _, err := st.Txn(ctx, nil, ops[:n])
		if err != nil || !ok {
			t.Fatalf("seed chunk failed: ok=%v err=%v", ok, err)
		}
		ops = ops[n:]
	}
}

func checkObjectsRefsSuite(t *testing.T, newRoot func(*testing.T) store.Store) {
	t.Helper()

	t.Run("CedarCreateUpdateDeleteAndDeniedRollback", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		st := openRegisteredTenant(t, ctx, newRoot(t), "acme")
		cfg, _ := testObjectsConfig()
		cfg.Tenant = "acme"
		cfg.Schemas = testRefSchemas()
		cfg.Authorizer = mustCedarAuthorizer(t, defaultCedarPS)
		objs := resource.NewObjects(st, cfg)

		share := f("share", lkal.String("yes"))
		if _, err := objs.Create(ctx, cedarAlice, secretCand("db", share)); err != nil {
			t.Fatal(err)
		}
		if _, err := objs.Create(ctx, cedarAlice, secretCand("cache", share)); err != nil {
			t.Fatal(err)
		}
		if _, err := objs.Create(ctx, cedarAlice, configMapCand("cfg", share)); err != nil {
			t.Fatal(err)
		}
		if _, err := objs.Create(ctx, cedarAlice, secretCand("private")); err != nil {
			t.Fatal(err)
		}

		srcObj := keyspace.Object{Group: "core", Kind: "workload", Name: "web"}
		dbObj := keyspace.Object{Group: "core", Kind: "secret", Name: "db"}
		cacheObj := keyspace.Object{Group: "core", Kind: "secret", Name: "cache"}
		cfgObj := keyspace.Object{Group: "core", Kind: "configmap", Name: "cfg"}
		privObj := keyspace.Object{Group: "core", Kind: "secret", Name: "private"}

		created, err := objs.Create(ctx, cedarAlice, workloadRefCand("web",
			f("primary", lkal.Ref{"secret", "db"}),
			f("config", lkal.Ref{"configmap", "cfg", "spec", "key"}),
		))
		if err != nil {
			t.Fatalf("Create with Cedar allow: %v", err)
		}
		assertBackRefPresent(t, ctx, st, dbObj, srcObj)
		assertBackRefPresent(t, ctx, st, cfgObj, srcObj)

		meta, _ := getRecordField(created, "meta")
		rev1 := getIntField(meta, "revision")

		updCand := workloadRefCand("web",
			f("primary", lkal.Ref{"secret", "db"}),
			f("secondary", lkal.Ref{"secret", "cache"}),
		)
		updCand = replaceMetaField(updCand, "revision", lkal.Int(rev1))
		updated, err := objs.Update(ctx, cedarAlice, updCand)
		if err != nil {
			t.Fatalf("Update with refs delta: %v", err)
		}
		assertBackRefPresent(t, ctx, st, dbObj, srcObj)
		assertBackRefPresent(t, ctx, st, cacheObj, srcObj)
		assertBackRefAbsent(t, ctx, st, cfgObj, srcObj)

		// Denied by missing "share" label on Create and Update.
		blockedObj := keyspace.Object{Group: "core", Kind: "workload", Name: "blocked"}
		revBefore, err := st.CurrentRev(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, err = objs.Create(ctx, cedarAlice, workloadRefCand("blocked",
			f("primary", lkal.Ref{"secret", "db"}),
			f("priv", lkal.Ref{"secret", "private"}),
		))
		assertRefsErr(t, err, refs.ErrDenied)
		if revAfter, err := st.CurrentRev(ctx); err != nil || revAfter != revBefore {
			t.Fatalf("denied Create moved revision: %d -> %d (err=%v)", revBefore, revAfter, err)
		}
		if _, err := objs.Get(ctx, blockedObj); !errors.Is(err, resource.ErrNotFound) {
			t.Fatalf("denied Create wrote body: %v", err)
		}
		assertBackRefAbsent(t, ctx, st, dbObj, blockedObj)
		assertBackRefAbsent(t, ctx, st, privObj, blockedObj)

		_, err = objs.Update(ctx, cedarAlice, workloadRefCand("web",
			f("primary", lkal.Ref{"secret", "db"}),
			f("priv", lkal.Ref{"secret", "private"}),
		))
		assertRefsErr(t, err, refs.ErrDenied)
		if revAfter, err := st.CurrentRev(ctx); err != nil || revAfter != revBefore {
			t.Fatalf("denied Update moved revision: %d -> %d (err=%v)", revBefore, revAfter, err)
		}
		assertBackRefPresent(t, ctx, st, dbObj, srcObj)
		assertBackRefPresent(t, ctx, st, cacheObj, srcObj)
		assertBackRefAbsent(t, ctx, st, privObj, srcObj)

		// Denied by forbid on context.sourcePath like "spec/forbidden*".
		_, err = objs.Create(ctx, cedarAlice, workloadRefCand("blocked-path",
			f("forbiddenSecret", lkal.Ref{"secret", "db"}),
		))
		assertRefsErr(t, err, refs.ErrDenied)
		if revAfter, err := st.CurrentRev(ctx); err != nil || revAfter != revBefore {
			t.Fatalf("forbid sourcePath Create moved revision: %d -> %d (err=%v)", revBefore, revAfter, err)
		}

		// Denied for wrong principal.
		_, err = objs.Create(ctx, cedarBob, workloadRefCand("blocked-bob",
			f("primary", lkal.Ref{"secret", "db"}),
		))
		assertRefsErr(t, err, refs.ErrDenied)
		if revAfter, err := st.CurrentRev(ctx); err != nil || revAfter != revBefore {
			t.Fatalf("wrong principal Create moved revision: %d -> %d (err=%v)", revBefore, revAfter, err)
		}

		// Policy that errors at evaluation denies even when a permit matches.
		errPolicyCfg := cfg
		errPolicyCfg.Authorizer = mustCedarAuthorizer(t, `
permit(principal == Nayutar::User::"alice", action == Nayutar::Action::"Reference", resource);
permit(principal, action == Nayutar::Action::"Reference", resource) when { resource.missingAttr == "x" };
`)
		errPolicyObjs := resource.NewObjects(st, errPolicyCfg)
		_, err = errPolicyObjs.Create(ctx, cedarAlice, workloadRefCand("blocked-eval",
			f("primary", lkal.Ref{"secret", "db"}),
		))
		assertRefsErr(t, err, refs.ErrDenied)
		if revAfter, err := st.CurrentRev(ctx); err != nil || revAfter != revBefore {
			t.Fatalf("eval-error policy Create moved revision: %d -> %d (err=%v)", revBefore, revAfter, err)
		}

		updMeta, _ := getRecordField(updated, "meta")
		if err := objs.Delete(ctx, srcObj, getIntField(updMeta, "revision")); err != nil {
			t.Fatalf("Delete with refs: %v", err)
		}
		if _, err := objs.Get(ctx, srcObj); !errors.Is(err, resource.ErrNotFound) {
			t.Fatalf("Get after Delete = %v, want ErrNotFound", err)
		}
		assertBackRefAbsent(t, ctx, st, dbObj, srcObj)
		assertBackRefAbsent(t, ctx, st, cacheObj, srcObj)
	})

	t.Run("G0NegativeCrossTenantAndDisallowedKind", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		st := openRegisteredTenant(t, ctx, newRoot(t), "acme")
		cfg, _ := testObjectsConfig()
		cfg.Tenant = "acme"
		cfg.Schemas = testRefSchemas()
		cfg.Authorizer = mustCedarAuthorizer(t, defaultCedarPS)
		objs := resource.NewObjects(st, cfg)

		revBefore, err := st.CurrentRev(ctx)
		if err != nil {
			t.Fatal(err)
		}

		malformed := []struct {
			name string
			ref  lkal.Ref
		}{
			{"key-imitating", lkal.Ref{"t", "other", "o", "core", "secret", "x"}},
			{"segment /t/other in kind", lkal.Ref{"/t/other", "x"}},
			{"segment /t/other in leaf", lkal.Ref{"secret", "x", "spec", "/t/other"}},
			{"dotdot", lkal.Ref{"..", "x"}},
			{"secret.core", lkal.Ref{"secret.core", "x"}},
		}
		for _, tc := range malformed {
			t.Run(tc.name, func(t *testing.T) {
				_, err := objs.Create(ctx, cedarAlice, workloadRefCand("bad-notation", f("r", tc.ref)))
				assertRefsErr(t, err, refs.ErrMalformed)
				if revAfter, err := st.CurrentRev(ctx); err != nil || revAfter != revBefore {
					t.Fatalf("malformed Create moved revision: %d -> %d (err=%v)", revBefore, revAfter, err)
				}
			})
		}

		_, err = objs.Create(ctx, cedarAlice, workloadRefCand("bad-kind", f("secrets", rec(f("item", lkal.Ref{"configmap", "cm"})))))
		assertRefsErr(t, err, refs.ErrKind)
		if revAfter, err := st.CurrentRev(ctx); err != nil || revAfter != revBefore {
			t.Fatalf("disallowed kind Create moved revision: %d -> %d (err=%v)", revBefore, revAfter, err)
		}
	})

	t.Run("G0TargetCompareConflict", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		st := openRegisteredTenant(t, ctx, newRoot(t), "acme")
		cfg, _ := testObjectsConfig()
		cfg.Tenant = "acme"
		cfg.Schemas = testRefSchemas()
		cfg.Authorizer = mustCedarAuthorizer(t, defaultCedarPS)
		baseObjs := resource.NewObjects(st, cfg)

		if _, err := baseObjs.Create(ctx, cedarAlice, secretCand("db", f("share", lkal.String("yes")))); err != nil {
			t.Fatal(err)
		}
		if _, err := baseObjs.Create(ctx, cedarAlice, workloadRefCand("existing")); err != nil {
			t.Fatal(err)
		}
		dbObj := keyspace.Object{Group: "core", Kind: "secret", Name: "db"}
		dbKey := objectRelKey(t, dbObj)

		raceCreateObjs := resource.NewObjects(&targetMutatingBeforeTxnStore{Store: st, targetKey: dbKey}, cfg)
		newObj := keyspace.Object{Group: "core", Kind: "workload", Name: "raced-create"}
		if _, err := raceCreateObjs.Create(ctx, cedarAlice, workloadRefCand("raced-create", f("db", lkal.Ref{"secret", "db"}))); !errors.Is(err, envelope.ErrConflict) {
			t.Fatalf("Create with target race = %v, want ErrConflict", err)
		}
		if _, err := baseObjs.Get(ctx, newObj); !errors.Is(err, resource.ErrNotFound) {
			t.Fatalf("raced Create wrote body: %v", err)
		}
		assertBackRefAbsent(t, ctx, st, dbObj, newObj)

		raceUpdateObjs := resource.NewObjects(&targetMutatingBeforeTxnStore{Store: st, targetKey: dbKey}, cfg)
		existingObj := keyspace.Object{Group: "core", Kind: "workload", Name: "existing"}
		if _, err := raceUpdateObjs.Update(ctx, cedarAlice, workloadRefCand("existing", f("db", lkal.Ref{"secret", "db"}))); !errors.Is(err, envelope.ErrConflict) {
			t.Fatalf("Update with target race = %v, want ErrConflict", err)
		}
		assertBackRefAbsent(t, ctx, st, dbObj, existingObj)
	})

	t.Run("G0ScanDeleteMoreThan64BackRefs", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		st := openRegisteredTenant(t, ctx, newRoot(t), "acme")
		cfg, _ := testObjectsConfig()
		cfg.Tenant = "acme"
		cfg.Schemas = testRefSchemas()
		cfg.Authorizer = mustCedarAuthorizer(t, defaultCedarPS)
		objs := resource.NewObjects(st, cfg)

		corruptObj := keyspace.Object{Group: "core", Kind: "workload", Name: "corrupt"}
		corruptKey := objectRelKey(t, corruptObj)

		const numRefs = 135
		ops := make([]store.Op, 0, numRefs+2)
		ops = append(ops, store.Op{Type: store.OpPut, Key: corruptKey, Value: []byte("not-lkal")})
		for i := range numRefs {
			tgt := keyspace.Object{Group: "core", Kind: "secret", Name: fmt.Sprintf("s%03d", i)}
			ops = append(ops, store.Op{Type: store.OpPut, Key: backRefRelKey(t, tgt, corruptObj), Value: []byte{}})
		}
		otherObj := keyspace.Object{Group: "core", Kind: "workload", Name: "other"}
		otherTgt := keyspace.Object{Group: "core", Kind: "secret", Name: "s000"}
		ops = append(ops, store.Op{Type: store.OpPut, Key: backRefRelKey(t, otherTgt, otherObj), Value: []byte{}})
		seedBackRefsInChunks(t, ctx, st, ops)

		if err := objs.Delete(ctx, corruptObj, 0); err != nil {
			t.Fatalf("Delete(corrupt): %v", err)
		}
		if _, err := objs.Get(ctx, corruptObj); !errors.Is(err, resource.ErrNotFound) {
			t.Fatalf("Get after corrupt Delete = %v, want ErrNotFound", err)
		}
		for i := range numRefs {
			tgt := keyspace.Object{Group: "core", Kind: "secret", Name: fmt.Sprintf("s%03d", i)}
			assertBackRefAbsent(t, ctx, st, tgt, corruptObj)
		}
		assertBackRefPresent(t, ctx, st, otherTgt, otherObj)
	})

	t.Run("TenantIsolation", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		root := newRoot(t)
		vAlpha := openRegisteredTenant(t, ctx, root, "alpha")
		vBeta := openRegisteredTenant(t, ctx, root, "beta")

		// 1) Cross-tenant missing target gets notFound and writes nothing in either tenant.
		allowAllAuth := mustCedarAuthorizer(t, `permit(principal == Nayutar::User::"alice", action == Nayutar::Action::"Reference", resource);`)
		alphaCfg, _ := testObjectsConfig()
		alphaCfg.Tenant = "alpha"
		alphaCfg.Schemas = testRefSchemas()
		alphaCfg.Authorizer = allowAllAuth
		betaCfg := alphaCfg
		betaCfg.Tenant = "beta"

		alphaObjs := resource.NewObjects(vAlpha, alphaCfg)
		betaObjs := resource.NewObjects(vBeta, betaCfg)

		if _, err := betaObjs.Create(ctx, cedarAlice, secretCand("only-in-beta")); err != nil {
			t.Fatal(err)
		}
		revBefore, err := root.CurrentRev(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, err = alphaObjs.Create(ctx, cedarAlice, workloadRefCand("w-missing", f("s", lkal.Ref{"secret", "only-in-beta"})))
		assertRefsErr(t, err, refs.ErrNotFound)
		if revAfter, err := root.CurrentRev(ctx); err != nil || revAfter != revBefore {
			t.Fatalf("cross-tenant notFound moved revision: %d -> %d (err=%v)", revBefore, revAfter, err)
		}
		wMissingObj := keyspace.Object{Group: "core", Kind: "workload", Name: "w-missing"}
		onlyBetaObj := keyspace.Object{Group: "core", Kind: "secret", Name: "only-in-beta"}
		assertBackRefAbsent(t, ctx, vAlpha, onlyBetaObj, wMissingObj)
		assertBackRefAbsent(t, ctx, vBeta, onlyBetaObj, wMissingObj)

		// 2) Same name in both tenants resolves to the caller's own tenant copy (checked via label-based Cedar policy).
		labelAuth := mustCedarAuthorizer(t, `
permit(
  principal == Nayutar::User::"alice",
  action == Nayutar::Action::"Reference",
  resource
) when {
  resource has labels && resource.labels has "alphaOnly" && resource.labels["alphaOnly"] == "yes"
};
`)
		alphaCfg.Authorizer = labelAuth
		betaCfg.Authorizer = labelAuth
		alphaLabelObjs := resource.NewObjects(vAlpha, alphaCfg)
		betaLabelObjs := resource.NewObjects(vBeta, betaCfg)

		if _, err := alphaLabelObjs.Create(ctx, cedarAlice, secretCand("shared", f("alphaOnly", lkal.String("yes")))); err != nil {
			t.Fatal(err)
		}
		if _, err := betaLabelObjs.Create(ctx, cedarAlice, secretCand("shared", f("betaOnly", lkal.String("yes")))); err != nil {
			t.Fatal(err)
		}

		sharedObj := keyspace.Object{Group: "core", Kind: "secret", Name: "shared"}
		w1Obj := keyspace.Object{Group: "core", Kind: "workload", Name: "w1"}
		if _, err := alphaLabelObjs.Create(ctx, cedarAlice, workloadRefCand("w1", f("s", lkal.Ref{"secret", "shared"}))); err != nil {
			t.Fatalf("alpha Create(w1): %v", err)
		}
		assertBackRefPresent(t, ctx, vAlpha, sharedObj, w1Obj)
		assertBackRefAbsent(t, ctx, vBeta, sharedObj, w1Obj)

		if _, err := betaLabelObjs.Create(ctx, cedarAlice, workloadRefCand("w2", f("s", lkal.Ref{"secret", "shared"}))); err == nil {
			t.Fatal("beta Create(w2) succeeded, want ErrDenied")
		} else {
			assertRefsErr(t, err, refs.ErrDenied)
		}
	})
}

type targetMutatingBeforeTxnStore struct {
	tenant.Store
	targetKey string
	mutated   bool
}

func (w *targetMutatingBeforeTxnStore) Txn(ctx context.Context, cmps []store.Cmp, ops []store.Op) (bool, store.Rev, error) {
	if !w.mutated {
		w.mutated = true
		kv, _, err := w.Store.Get(ctx, w.targetKey, 0)
		if err != nil {
			return false, 0, err
		}
		ok, _, err := w.Store.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: w.targetKey, Value: kv.Value}})
		if err != nil || !ok {
			return false, 0, fmt.Errorf("mutate target before txn: ok=%v err=%w", ok, err)
		}
	}
	return w.Store.Txn(ctx, cmps, ops)
}

type spyCmpStore struct {
	tenant.Store
	lastCmps []store.Cmp
}

func (s *spyCmpStore) Txn(ctx context.Context, cmps []store.Cmp, ops []store.Op) (bool, store.Rev, error) {
	s.lastCmps = slices.Clone(cmps)
	return s.Store.Txn(ctx, cmps, ops)
}

type tooLargeStore struct {
	tenant.Store
	maxDeleteOps int
}

func (t *tooLargeStore) Txn(ctx context.Context, cmps []store.Cmp, ops []store.Op) (bool, store.Rev, error) {
	deleteOps := 0
	for _, op := range ops {
		if op.Type == store.OpDelete {
			deleteOps++
		}
	}
	if deleteOps > t.maxDeleteOps {
		return false, 0, store.ErrTooLarge
	}
	return t.Store.Txn(ctx, cmps, ops)
}

type mutateOnDeleteBatchStore struct {
	tenant.Store
	bodyKey  string
	mutated  bool
	txnCalls int
}

func (m *mutateOnDeleteBatchStore) Txn(ctx context.Context, cmps []store.Cmp, ops []store.Op) (bool, store.Rev, error) {
	m.txnCalls++
	if !m.mutated && m.txnCalls == 1 {
		m.mutated = true
		_, _, _ = m.Store.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: m.bodyKey, Value: []byte("changed")}})
	}
	return m.Store.Txn(ctx, cmps, ops)
}

type afterFirstIndexDeleteTxnStore struct {
	tenant.Store
	afterFirst func()
	fired      bool
}

func (a *afterFirstIndexDeleteTxnStore) Txn(ctx context.Context, cmps []store.Cmp, ops []store.Op) (bool, store.Rev, error) {
	ok, rev, err := a.Store.Txn(ctx, cmps, ops)
	if !a.fired && err == nil && ok && len(ops) > 0 && ops[0].Type == store.OpDelete && strings.HasPrefix(ops[0].Key, keyspace.BackRefsPrefix) {
		a.fired = true
		a.afterFirst()
	}
	return ok, rev, err
}

func seedRawStoredWithTargets(t *testing.T, ctx context.Context, st tenant.Store, name string, numTargets int) (keyspace.Object, string, []keyspace.Object) {
	t.Helper()
	srcObj := keyspace.Object{Group: "core", Kind: "workload", Name: name}
	srcKey := objectRelKey(t, srcObj)
	fields := make([]lkal.Field, numTargets)
	targets := make([]keyspace.Object, numTargets)
	ops := make([]store.Op, 0, numTargets+1)
	for i := range numTargets {
		sName := fmt.Sprintf("s%02d", i)
		fields[i] = f(fmt.Sprintf("f%02d", i), lkal.Ref{"secret", sName})
		tgt := keyspace.Object{Group: "core", Kind: "secret", Name: sName}
		targets[i] = tgt
		ops = append(ops, store.Op{Type: store.OpPut, Key: backRefRelKey(t, tgt, srcObj), Value: []byte{}})
	}
	storedDoc, err := envelope.Create(workloadRefCand(name, fields...), "01234567-89ab-4def-8123-456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := resource.Encode(storedDoc)
	if err != nil {
		t.Fatal(err)
	}
	ops = append(ops, store.Op{Type: store.OpPut, Key: srcKey, Value: raw})
	seedBackRefsInChunks(t, ctx, st, ops)
	return srcObj, srcKey, targets
}

func TestObjectsReferences(t *testing.T) {
	checkObjectsRefsSuite(t, func(*testing.T) store.Store {
		return memstore.New()
	})

	t.Run("StoredExceeding32TargetsInternalAndScanDeleteRace", func(t *testing.T) {
		ctx := context.Background()
		st := memstore.New()
		cfg, _ := testObjectsConfig()
		cfg.Tenant = "acme"
		cfg.Schemas = testRefSchemas()
		cfg.Authorizer = &fakeAuthorizer{}
		objs := resource.NewObjects(st, cfg)

		// Seed secrets s00..s09 so reducing to 10 refs would pass target existence if phase 1 didn't reject first.
		for i := range 10 {
			if _, err := objs.Create(ctx, testPrincipal, secretCand(fmt.Sprintf("s%02d", i))); err != nil {
				t.Fatal(err)
			}
		}

		srcObj, _, targets40 := seedRawStoredWithTargets(t, ctx, st, "over40", 40)
		fields10 := make([]lkal.Field, 10)
		for i := range 10 {
			fields10[i] = f(fmt.Sprintf("f%02d", i), lkal.Ref{"secret", fmt.Sprintf("s%02d", i)})
		}
		reduceCand := workloadRefCand("over40", fields10...)

		revBefore, err := st.CurrentRev(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, err = objs.Update(ctx, testPrincipal, reduceCand)
		re := assertRefsErr(t, err, refs.ErrInternal)
		if re.Occurrence.Path != "" || len(re.Occurrence.Ref) != 0 {
			t.Fatalf("expected zero occurrence on >32 stored targets, got %+v", re.Occurrence)
		}
		if revAfter, err := st.CurrentRev(ctx); err != nil || revAfter != revBefore {
			t.Fatalf("Update(>32 stored) moved revision: %d -> %d (err=%v)", revBefore, revAfter, err)
		}
		for _, tgt := range targets40 {
			assertBackRefPresent(t, ctx, st, tgt, srcObj)
		}

		_, err = objs.UpdateStatus(ctx, rec(
			f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("over40")))),
			f("status", rec(f("phase", lkal.String("Ready")))),
		))
		assertRefsErr(t, err, refs.ErrInternal)
		if revAfter, err := st.CurrentRev(ctx); err != nil || revAfter != revBefore {
			t.Fatalf("UpdateStatus(>32 stored) moved revision: %d -> %d (err=%v)", revBefore, revAfter, err)
		}

		if err := objs.Delete(ctx, srcObj, 0); err != nil {
			t.Fatalf("Delete(over40): %v", err)
		}
		if _, err := objs.Get(ctx, srcObj); !errors.Is(err, resource.ErrNotFound) {
			t.Fatalf("Get after Delete(over40) = %v, want ErrNotFound", err)
		}
		for _, tgt := range targets40 {
			assertBackRefAbsent(t, ctx, st, tgt, srcObj)
		}

		// Race: 70 stored targets so scan Delete runs 2 index-deleting Txns (64 + 6).
		// After the first index-deleting Txn, run Update on the same object; Update must fail with ErrInternal
		// and Delete must finish leaving neither body nor any back-reference key.
		raceObj, _, targets70 := seedRawStoredWithTargets(t, ctx, st, "race70", 70)
		var updateErr error
		var updateCalled bool
		raceWrapper := &afterFirstIndexDeleteTxnStore{
			Store: st,
			afterFirst: func() {
				updateCalled = true
				_, updateErr = objs.Update(ctx, testPrincipal, workloadRefCand("race70", fields10...))
			},
		}
		raceObjs := resource.NewObjects(raceWrapper, cfg)
		if err := raceObjs.Delete(ctx, raceObj, 0); err != nil {
			t.Fatalf("Delete(race70): %v", err)
		}
		if !updateCalled {
			t.Fatal("afterFirst hook was not called during scan Delete")
		}
		assertRefsErr(t, updateErr, refs.ErrInternal)
		if _, err := objs.Get(ctx, raceObj); !errors.Is(err, resource.ErrNotFound) {
			t.Fatalf("Get after race Delete = %v, want ErrNotFound", err)
		}
		for _, tgt := range targets70 {
			assertBackRefAbsent(t, ctx, st, tgt, raceObj)
		}
		kvs, _, _, err := st.Range(ctx, keyspace.BackRefsPrefix, 0, 0, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, kv := range kvs {
			parsed, err := keyspace.ParseRel("t", kv.Key)
			if err == nil && parsed.Family == keyspace.FamilyBackRef && parsed.Source == raceObj {
				t.Fatalf("leftover back-ref key %s after race Delete", kv.Key)
			}
		}
	})

	t.Run("UpdateStatusAndNoOpLeaveIndexAndAuthAlone", func(t *testing.T) {
		ctx := context.Background()
		st := memstore.New()
		auth := &fakeAuthorizer{}
		cfg, _ := testObjectsConfig()
		cfg.Tenant = "acme"
		cfg.Schemas = testRefSchemas()
		cfg.Authorizer = auth
		objs := resource.NewObjects(st, cfg)

		if _, err := objs.Create(ctx, testPrincipal, secretCand("db")); err != nil {
			t.Fatal(err)
		}
		created, err := objs.Create(ctx, testPrincipal, workloadRefCand("web",
			f("primary", lkal.Ref{"secret", "db"}),
		))
		if err != nil {
			t.Fatal(err)
		}
		srcObj := keyspace.Object{Group: "core", Kind: "workload", Name: "web"}
		dbObj := keyspace.Object{Group: "core", Kind: "secret", Name: "db"}

		auth.Reset()
		revBeforeNoOp, err := st.CurrentRev(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := objs.Update(ctx, testPrincipal, created); err != nil {
			t.Fatalf("no-op Update: %v", err)
		}
		if revAfter, err := st.CurrentRev(ctx); err != nil || revAfter != revBeforeNoOp {
			t.Fatalf("no-op Update moved revision: %d -> %d (err=%v)", revBeforeNoOp, revAfter, err)
		}
		if len(auth.Requests()) != 0 {
			t.Fatalf("no-op Update called authorizer %d times, want 0", len(auth.Requests()))
		}

		statusCand := rec(
			f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("web")))),
			f("status", rec(f("phase", lkal.String("Running")))),
		)
		if _, err := objs.UpdateStatus(ctx, statusCand); err != nil {
			t.Fatalf("UpdateStatus: %v", err)
		}
		if len(auth.Requests()) != 0 {
			t.Fatalf("UpdateStatus called authorizer %d times, want 0", len(auth.Requests()))
		}
		assertBackRefPresent(t, ctx, st, dbObj, srcObj)

		badStatusCand := rec(
			f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("web")))),
			f("status", rec(f("leak", lkal.Ref{"secret", "db"}))),
		)
		re := assertRefsErr(t, func() error {
			_, err := objs.UpdateStatus(ctx, badStatusCand)
			return err
		}(), refs.ErrMalformed)
		if re.Occurrence.Path != "status/leak" {
			t.Fatalf("occurrence path = %q, want status/leak", re.Occurrence.Path)
		}
	})

	t.Run("DeleteScanFallbackOnErrTooLargeAndConflict", func(t *testing.T) {
		ctx := context.Background()

		st := memstore.New()
		wrapped := &tooLargeStore{Store: st, maxDeleteOps: 10}
		cfg, _ := testObjectsConfig()
		objs := resource.NewObjects(wrapped, cfg)

		srcObj, srcKey, targets := seedRawStoredWithTargets(t, ctx, st, "big", 33)
		if err := objs.Delete(ctx, srcObj, 0); err != nil {
			t.Fatalf("Delete with ErrTooLarge halving: %v", err)
		}
		for _, tgt := range targets {
			assertBackRefAbsent(t, ctx, st, tgt, srcObj)
		}
		if _, _, err := st.Get(ctx, srcKey, 0); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("body still exists after Delete: %v", err)
		}

		st2 := memstore.New()
		obj2 := keyspace.Object{Group: "core", Kind: "workload", Name: "tiny"}
		if ok, _, err := st2.Txn(ctx, nil, []store.Op{
			{Type: store.OpPut, Key: objectRelKey(t, obj2), Value: []byte("bad")},
			{Type: store.OpPut, Key: backRefRelKey(t, keyspace.Object{Group: "core", Kind: "secret", Name: "s1"}, obj2), Value: []byte{}},
		}); err != nil || !ok {
			t.Fatal(err)
		}
		objsTooSmall := resource.NewObjects(&tooLargeStore{Store: st2, maxDeleteOps: 0}, cfg)
		if err := objsTooSmall.Delete(ctx, obj2, 0); !errors.Is(err, store.ErrTooLarge) {
			t.Fatalf("Delete(maxDeleteOps=0) = %v, want store.ErrTooLarge", err)
		}

		st3 := memstore.New()
		obj3 := keyspace.Object{Group: "core", Kind: "workload", Name: "race"}
		key3 := objectRelKey(t, obj3)
		if ok, _, err := st3.Txn(ctx, nil, []store.Op{
			{Type: store.OpPut, Key: key3, Value: []byte("bad")},
			{Type: store.OpPut, Key: backRefRelKey(t, keyspace.Object{Group: "core", Kind: "secret", Name: "s1"}, obj3), Value: []byte{}},
		}); err != nil || !ok {
			t.Fatal(err)
		}
		objsRace := resource.NewObjects(&mutateOnDeleteBatchStore{Store: st3, bodyKey: key3}, cfg)
		if err := objsRace.Delete(ctx, obj3, 0); !errors.Is(err, envelope.ErrConflict) {
			t.Fatalf("Delete(mid-scan body change) = %v, want ErrConflict", err)
		}
	})

	t.Run("AllErrorPhasesAndBoundaries", func(t *testing.T) {
		ctx := context.Background()
		st := memstore.New()
		auth := &fakeAuthorizer{}
		cfg, _ := testObjectsConfig()
		cfg.Tenant = "acme"
		cfg.Schemas = testRefSchemas()
		cfg.Authorizer = auth
		objs := resource.NewObjects(st, cfg)

		if _, err := objs.Create(ctx, testPrincipal, secretCand("present", f("tier", lkal.String("db")))); err != nil {
			t.Fatal(err)
		}

		unreadableObj := keyspace.Object{Group: "core", Kind: "secret", Name: "garbage"}
		if ok, _, err := st.Txn(ctx, nil, []store.Op{
			{Type: store.OpPut, Key: objectRelKey(t, unreadableObj), Value: []byte("corrupt-bytes")},
		}); err != nil || !ok {
			t.Fatal(err)
		}
		mismatchDoc, err := envelope.Create(secretCand("other-name"), "01234567-89ab-4def-8123-456789abcdef")
		if err != nil {
			t.Fatal(err)
		}
		mismatchRaw, err := resource.Encode(mismatchDoc)
		if err != nil {
			t.Fatal(err)
		}
		mismatchObj := keyspace.Object{Group: "core", Kind: "secret", Name: "mismatch"}
		if ok, _, err := st.Txn(ctx, nil, []store.Op{
			{Type: store.OpPut, Key: objectRelKey(t, mismatchObj), Value: mismatchRaw},
		}); err != nil || !ok {
			t.Fatal(err)
		}

		// Phase 1 ErrInternal on Update and UpdateStatus when stored doc has a Ref in status.
		phase1Obj := keyspace.Object{Group: "core", Kind: "workload", Name: "phase1"}
		phase1Doc, err := envelope.Create(workloadRefCand("phase1"), "01234567-89ab-4def-8123-456789abcdef")
		if err != nil {
			t.Fatal(err)
		}
		phase1Doc.Fields = append(phase1Doc.Fields, f("status", rec(f("bad", lkal.Ref{"secret", "present"}))))
		phase1Raw, err := resource.Encode(phase1Doc)
		if err != nil {
			t.Fatal(err)
		}
		if ok, _, err := st.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: objectRelKey(t, phase1Obj), Value: phase1Raw}}); err != nil || !ok {
			t.Fatal(err)
		}
		if _, err := objs.Update(ctx, testPrincipal, workloadRefCand("phase1")); err == nil {
			t.Fatal("Update(phase1) succeeded, want ErrInternal")
		} else {
			assertRefsErr(t, err, refs.ErrInternal)
		}
		if _, err := objs.UpdateStatus(ctx, rec(
			f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("phase1")))),
			f("status", rec(f("ok", lkal.String("done")))),
		)); err == nil {
			t.Fatal("UpdateStatus(phase1) succeeded, want ErrInternal")
		} else {
			assertRefsErr(t, err, refs.ErrInternal)
		}

		// Missing target with allow -> ErrNotFound; with deny -> ErrDenied.
		auth.allow = func(refs.Request) bool { return true }
		_, err = objs.Create(ctx, testPrincipal, workloadRefCand("missing-allow", f("r", lkal.Ref{"secret", "missing"})))
		assertRefsErr(t, err, refs.ErrNotFound)

		auth.allow = func(refs.Request) bool { return false }
		_, err = objs.Create(ctx, testPrincipal, workloadRefCand("missing-deny", f("r", lkal.Ref{"secret", "missing"})))
		assertRefsErr(t, err, refs.ErrDenied)

		// Unreadable target with deny -> ErrDenied; with allow -> ErrInternal naming target.
		auth.Reset()
		auth.allow = func(refs.Request) bool { return false }
		_, err = objs.Create(ctx, testPrincipal, workloadRefCand("unreadable-deny", f("r", lkal.Ref{"secret", "garbage"})))
		assertRefsErr(t, err, refs.ErrDenied)
		reqs := auth.Requests()
		if len(reqs) != 1 || reqs[0].TargetExists || reqs[0].TargetLabels != nil {
			t.Fatalf("authorizer saw %+v for unreadable target, want TargetExists=false, TargetLabels=nil", reqs)
		}

		auth.allow = func(refs.Request) bool { return true }
		for _, badTarget := range []string{"garbage", "mismatch"} {
			_, err = objs.Create(ctx, testPrincipal, workloadRefCand("unreadable-allow-"+badTarget, f("r", lkal.Ref{"secret", badTarget})))
			re := assertRefsErr(t, err, refs.ErrInternal)
			if !strings.Contains(re.Error(), badTarget) {
				t.Fatalf("unreadable target ErrInternal = %q, want it to name %q", re.Error(), badTarget)
			}
		}

		// Empty principal -> ErrDenied without calling authorizer.
		auth.Reset()
		for _, p := range []refs.Principal{{Type: "", ID: "alice"}, {Type: "User", ID: ""}} {
			_, err = objs.Create(ctx, p, workloadRefCand("empty-principal", f("r", lkal.Ref{"secret", "present"})))
			assertRefsErr(t, err, refs.ErrDenied)
		}
		if len(auth.Requests()) != 0 {
			t.Fatalf("empty principal called authorizer %d times, want 0", len(auth.Requests()))
		}

		// Nil authorizer -> ErrDenied.
		nilAuthCfg := cfg
		nilAuthCfg.Authorizer = nil
		nilAuthObjs := resource.NewObjects(st, nilAuthCfg)
		_, err = nilAuthObjs.Create(ctx, testPrincipal, workloadRefCand("nil-auth", f("r", lkal.Ref{"secret", "present"})))
		assertRefsErr(t, err, refs.ErrDenied)

		// 32 targets ok, 33 malformed.
		auth.allow = func(refs.Request) bool { return true }
		list32 := make(lkal.List, 32)
		for i := range 32 {
			name := fmt.Sprintf("lim%02d", i)
			if _, err := objs.Create(ctx, testPrincipal, secretCand(name)); err != nil {
				t.Fatal(err)
			}
			list32[i] = lkal.Ref{"secret", name}
		}
		if _, err := objs.Create(ctx, testPrincipal, workloadRefCand("ok32", f("list", list32))); err != nil {
			t.Fatalf("32 targets Create failed: %v", err)
		}
		list33 := append(slices.Clone(list32), lkal.Ref{"secret", "lim32"})
		_, err = objs.Create(ctx, testPrincipal, workloadRefCand("bad33", f("list", list33)))
		assertRefsErr(t, err, refs.ErrMalformed)

		// Self reference: Create -> ErrNotFound; Update -> ok and no duplicate target cmp.
		_, err = objs.Create(ctx, testPrincipal, workloadRefCand("self", f("self", lkal.Ref{"workload", "self"})))
		assertRefsErr(t, err, refs.ErrNotFound)

		spy := &spyCmpStore{Store: st}
		spyObjs := resource.NewObjects(spy, cfg)
		createdSelf, err := spyObjs.Create(ctx, testPrincipal, workloadRefCand("self"))
		if err != nil {
			t.Fatal(err)
		}
		selfMeta, _ := getRecordField(createdSelf, "meta")
		selfUpd := workloadRefCand("self", f("self", lkal.Ref{"workload", "self"}))
		selfUpd = replaceMetaField(selfUpd, "revision", lkal.Int(getIntField(selfMeta, "revision")))
		if _, err := spyObjs.Update(ctx, testPrincipal, selfUpd); err != nil {
			t.Fatalf("self-reference Update failed: %v", err)
		}
		selfObj := keyspace.Object{Group: "core", Kind: "workload", Name: "self"}
		assertBackRefPresent(t, ctx, st, selfObj, selfObj)
		selfKey := objectRelKey(t, selfObj)
		seenKeys := make(map[string]int)
		for _, c := range spy.lastCmps {
			seenKeys[c.Key]++
		}
		if seenKeys[selfKey] != 1 {
			t.Fatalf("self key cmp count = %d, want 1 (cmps=%+v)", seenKeys[selfKey], spy.lastCmps)
		}
	})

	t.Run("RequestFieldsAndDecisionCache", func(t *testing.T) {
		ctx := context.Background()
		st := memstore.New()
		auth := &fakeAuthorizer{}
		cfg, _ := testObjectsConfig()
		cfg.Tenant = "tenant-x"
		cfg.Schemas = testRefSchemas()
		cfg.Authorizer = auth
		objs := resource.NewObjects(st, cfg)

		if _, err := objs.Create(ctx, testPrincipal, secretCand("labeled", f("env", lkal.String("prod")), f("team", lkal.String("core")))); err != nil {
			t.Fatal(err)
		}
		if _, err := objs.Create(ctx, testPrincipal, secretCand("unlabeled")); err != nil {
			t.Fatal(err)
		}

		auth.Reset()
		_, err := objs.Create(ctx, testPrincipal, workloadRefCand("consumer",
			f("a", lkal.List{
				lkal.Ref{"secret", "labeled"},
				lkal.Ref{"secret", "labeled", "spec", "data"},
			}),
			f("b", lkal.Ref{"secret", "labeled", "meta", "uid"}),
			f("c", lkal.Ref{"secret", "unlabeled"}),
		))
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		reqs := auth.Requests()
		if len(reqs) != 3 {
			t.Fatalf("authorizer call count = %d, want 3 (reqs=%+v)", len(reqs), reqs)
		}
		wantSrc := keyspace.Object{Group: "core", Kind: "workload", Name: "consumer"}
		wantLabeled := keyspace.Object{Group: "core", Kind: "secret", Name: "labeled"}
		wantUnlabeled := keyspace.Object{Group: "core", Kind: "secret", Name: "unlabeled"}

		wantReqs := []refs.Request{
			{
				Tenant:       "tenant-x",
				Principal:    testPrincipal,
				Source:       wantSrc,
				SourcePath:   "spec/a",
				Target:       wantLabeled,
				TargetLabels: map[string]string{"env": "prod", "team": "core"},
				TargetExists: true,
			},
			{
				Tenant:       "tenant-x",
				Principal:    testPrincipal,
				Source:       wantSrc,
				SourcePath:   "spec/b",
				Target:       wantLabeled,
				TargetLabels: map[string]string{"env": "prod", "team": "core"},
				TargetExists: true,
			},
			{
				Tenant:       "tenant-x",
				Principal:    testPrincipal,
				Source:       wantSrc,
				SourcePath:   "spec/c",
				Target:       wantUnlabeled,
				TargetLabels: map[string]string{},
				TargetExists: true,
			},
		}
		if !reflect.DeepEqual(reqs, wantReqs) {
			t.Fatalf("requests mismatch:\ngot:  %#v\nwant: %#v", reqs, wantReqs)
		}
		if reqs[2].TargetLabels == nil {
			t.Fatal("unlabeled target TargetLabels = nil, want empty non-nil map")
		}
	})

	t.Run("ConfigValidation", func(t *testing.T) {
		ctx := context.Background()

		t.Run("InvalidReferencesSchema", func(t *testing.T) {
			st := memstore.New()
			validCfg, _ := testObjectsConfig()
			validCfg.Tenant = "acme"
			validObjs := resource.NewObjects(st, validCfg)
			if _, err := validObjs.Create(ctx, testPrincipal, workloadRefCand("existing")); err != nil {
				t.Fatal(err)
			}

			badCfg, _ := testObjectsConfig()
			badCfg.Tenant = "acme"
			badCfg.Schemas = resource.Schemas{
				"secret.core": testRefSchemas()["secret"],
				"workload":    testRefSchemas()["workload"],
			}
			badObjs := resource.NewObjects(st, badCfg)

			revBefore, err := st.CurrentRev(ctx)
			if err != nil {
				t.Fatal(err)
			}
			_, err = badObjs.Create(ctx, testPrincipal, workloadRefCand("fresh"))
			re := assertRefsErr(t, err, refs.ErrInternal)
			if !strings.Contains(re.Detail, "invalid schema") {
				t.Fatalf("Create error detail = %q, want invalid schema message", re.Detail)
			}
			_, err = badObjs.Update(ctx, testPrincipal, workloadRefCand("existing", f("n", lkal.Int(1))))
			assertRefsErr(t, err, refs.ErrInternal)
			if revAfter, err := st.CurrentRev(ctx); err != nil || revAfter != revBefore {
				t.Fatalf("invalid schema moved revision: %d -> %d (err=%v)", revBefore, revAfter, err)
			}
		})

		t.Run("InvalidTenantWithAuthorizer", func(t *testing.T) {
			st := memstore.New()
			auth := &fakeAuthorizer{}
			seedCfg, _ := testObjectsConfig()
			seedCfg.Tenant = "acme"
			seedCfg.Schemas = testRefSchemas()
			seedCfg.Authorizer = auth
			seedObjs := resource.NewObjects(st, seedCfg)
			if _, err := seedObjs.Create(ctx, testPrincipal, secretCand("db")); err != nil {
				t.Fatal(err)
			}
			if _, err := seedObjs.Create(ctx, testPrincipal, workloadRefCand("existing")); err != nil {
				t.Fatal(err)
			}

			for i, badTenant := range []string{"", "ACME", "bad/tenant"} {
				badCfg, _ := testObjectsConfig()
				badCfg.Tenant = badTenant
				badCfg.Schemas = testRefSchemas()
				badCfg.Authorizer = auth
				badObjs := resource.NewObjects(st, badCfg)

				auth.Reset()
				revBefore, err := st.CurrentRev(ctx)
				if err != nil {
					t.Fatal(err)
				}

				_, err = badObjs.Create(ctx, testPrincipal, workloadRefCand("new-with-ref", f("s", lkal.Ref{"secret", "db"})))
				assertRefsErr(t, err, refs.ErrInternal)

				_, err = badObjs.Update(ctx, testPrincipal, workloadRefCand("existing", f("s", lkal.Ref{"secret", "db"})))
				assertRefsErr(t, err, refs.ErrInternal)

				if len(auth.Requests()) != 0 {
					t.Fatalf("invalid tenant %q called authorizer %d times, want 0", badTenant, len(auth.Requests()))
				}
				if revAfter, err := st.CurrentRev(ctx); err != nil || revAfter != revBefore {
					t.Fatalf("invalid tenant %q moved revision: %d -> %d (err=%v)", badTenant, revBefore, revAfter, err)
				}

				// Writes with no added occurrences still succeed even with invalid tenant.
				noRefName := fmt.Sprintf("no-refs-%d", i)
				if _, err := badObjs.Create(ctx, testPrincipal, workloadRefCand(noRefName)); err != nil {
					t.Fatalf("Create without added occurrences for tenant %q failed: %v", badTenant, err)
				}
				if _, err := badObjs.Update(ctx, testPrincipal, workloadRefCand(noRefName, f("count", lkal.Int(1)))); err != nil {
					t.Fatalf("Update without added occurrences for tenant %q failed: %v", badTenant, err)
				}
			}

			// When Authorizer == nil, added occurrences fail with ErrDenied, not ErrInternal.
			nilAuthCfg, _ := testObjectsConfig()
			nilAuthCfg.Tenant = ""
			nilAuthCfg.Schemas = testRefSchemas()
			nilAuthObjs := resource.NewObjects(st, nilAuthCfg)
			_, err := nilAuthObjs.Create(ctx, testPrincipal, workloadRefCand("nil-auth-bad-tenant", f("s", lkal.Ref{"secret", "db"})))
			assertRefsErr(t, err, refs.ErrDenied)
		})
	})
}
