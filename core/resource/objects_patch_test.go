package resource_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/resource"
	"github.com/Kaikei-e/Nayutar/core/resource/envelope"
	"github.com/Kaikei-e/Nayutar/core/resource/orderedset"
	"github.com/Kaikei-e/Nayutar/core/resource/patch"
	"github.com/Kaikei-e/Nayutar/core/resource/refs"
	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
	"github.com/Kaikei-e/Nayutar/core/store/memstore"
	"github.com/Kaikei-e/Nayutar/core/store/tenant"
)

var patchSuiteSchemas = func() resource.Schemas {
	workload, err := resource.CompileKindSchema("workload-patch.schema.lkal", []byte(`@lkal "0.1"
lkal-schema = "0.2"

root {
  field "spec" {
    type = .record
    field "replicas" { type = .int, default = 3 }
    field "ratio" { type = .decimal }
    field "mem" { type = .quantity }
    field "probe" {
      type = .record
      field "timeoutSeconds" { type = .int, default = 5 }
      field "path" { type = .string }
    }
    field "plugin" {
      type = .map
      unique-by = ["order"]
      value {
        type = .record
        field "order" { type = .int, required = true }
        field "enabled" { type = .bool, default = true }
      }
    }
  }
  field "status" {
    type = .record
    field "phase" { type = .string }
    field "readyReplicas" { type = .int }
  }
}
`))
	if err != nil {
		panic(err)
	}
	return resource.Schemas{"workload": workload}
}()

func patchObjectsConfig() (resource.Config, *fixedClock) {
	clk := &fixedClock{t: time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)}
	return resource.Config{
		Now:     clk.Now,
		Rand:    &seqRand{},
		Schemas: patchSuiteSchemas,
	}, clk
}

func checkObjectsPatchAndList(t *testing.T, newStore func(*testing.T) tenant.Store) {
	t.Helper()

	t.Run("ListPaginationAndSnapshot", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		st := newStore(t)
		cfg, _ := patchObjectsConfig()
		objs := resource.NewObjects(st, cfg)

		empty, err := objs.List(ctx, "core", "workload", resource.ListOptions{})
		if err != nil {
			t.Fatalf("List(empty): %v", err)
		}
		if len(empty.Items) != 0 || empty.Continue != "" {
			t.Fatalf("List(empty) = (%d items, cont=%q), want empty", len(empty.Items), empty.Continue)
		}

		names := []string{"alpha", "beta", "gamma", "delta"}
		for _, n := range names {
			if _, err := objs.Create(ctx, refs.Principal{}, workloadCand(n, 2)); err != nil {
				t.Fatalf("Create(%s): %v", n, err)
			}
		}

		// Full list returns sorted items with revisions attached.
		all, err := objs.List(ctx, "core", "workload", resource.ListOptions{})
		if err != nil {
			t.Fatalf("List(all): %v", err)
		}
		if len(all.Items) != 4 || all.Continue != "" || all.Revision < 1 {
			t.Fatalf("List(all) = (%d items, rev=%d, cont=%q)", len(all.Items), all.Revision, all.Continue)
		}
		wantOrder := []string{"alpha", "beta", "delta", "gamma"}
		for i, doc := range all.Items {
			meta, _ := getRecordField(doc, "meta")
			if got := getStringField(meta, "name"); got != wantOrder[i] {
				t.Fatalf("all.Items[%d].name = %q, want %q", i, got, wantOrder[i])
			}
			if rev := getIntField(meta, "revision"); rev < 1 {
				t.Fatalf("all.Items[%d].revision = %d, want >= 1", i, rev)
			}
		}

		// Paginated list pinned at snapshot rev ignores later creations.
		p1, err := objs.List(ctx, "core", "workload", resource.ListOptions{Limit: 2})
		if err != nil {
			t.Fatalf("List(page1): %v", err)
		}
		if len(p1.Items) != 2 || p1.Continue == "" {
			t.Fatalf("page1 = (%d items, cont=%q)", len(p1.Items), p1.Continue)
		}
		if _, err := objs.Create(ctx, refs.Principal{}, workloadCand("epsilon", 1)); err != nil {
			t.Fatalf("Create(epsilon): %v", err)
		}
		p2, err := objs.List(ctx, "core", "workload", resource.ListOptions{
			AtRev:    p1.Revision,
			Limit:    2,
			Continue: p1.Continue,
		})
		if err != nil {
			t.Fatalf("List(page2): %v", err)
		}
		if len(p2.Items) != 2 || p2.Continue != "" || p2.Revision != p1.Revision {
			t.Fatalf("page2 = (%d items, rev=%d, cont=%q), want 2 items at rev=%d with empty cont", len(p2.Items), p2.Revision, p2.Continue, p1.Revision)
		}
		var pagedNames []string
		for _, doc := range append(p1.Items, p2.Items...) {
			meta, _ := getRecordField(doc, "meta")
			pagedNames = append(pagedNames, getStringField(meta, "name"))
		}
		for i := range wantOrder {
			if pagedNames[i] != wantOrder[i] {
				t.Fatalf("paged names = %v, want %v", pagedNames, wantOrder)
			}
		}

		// Invalid group/kind -> envelope.ErrMalformed; invalid continue -> store.ErrInvalidArgument.
		if _, err := objs.List(ctx, "BAD", "workload", resource.ListOptions{}); !errors.Is(err, envelope.ErrMalformed) {
			t.Fatalf("List(bad group) = %v, want envelope.ErrMalformed", err)
		}
		if _, err := objs.List(ctx, "core", "BadKind", resource.ListOptions{}); !errors.Is(err, envelope.ErrMalformed) {
			t.Fatalf("List(bad kind) = %v, want envelope.ErrMalformed", err)
		}
		if _, err := objs.List(ctx, "core", "workload", resource.ListOptions{Continue: "o/core/other/x"}); !errors.Is(err, store.ErrInvalidArgument) {
			t.Fatalf("List(bad continue) = %v, want store.ErrInvalidArgument", err)
		}
	})

	t.Run("PatchAndPatchStatusLifecycleDefaultsAndOrderedSet", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		st := newStore(t)
		cfg, clk := patchObjectsConfig()
		objs := resource.NewObjects(st, cfg)

		obj := keyspace.Object{Group: "core", Kind: "workload", Name: "web"}
		cand := rec(
			f("meta", rec(
				f("kind", lkal.String("workload")),
				f("name", lkal.String("web")),
				f("labels", rec(f("env", lkal.String("dev")))),
			)),
			f("spec", rec(
				f("replicas", lkal.Int(5)),
				f("probe", rec(f("path", lkal.String("/healthz")))),
				f("plugin", rec(
					f("a", rec(f("order", lkal.Int(10)))),
					f("b", rec(f("order", lkal.Int(20)))),
				)),
			)),
		)
		created, err := objs.Create(ctx, refs.Principal{}, cand)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		meta1, _ := getRecordField(created, "meta")
		rev1 := getIntField(meta1, "revision")

		// Patch with meta/revision test + labels with slash key + spec update.
		patched1, err := objs.Patch(ctx, refs.Principal{}, obj, []patch.Op{
			{Kind: patch.OpTest, Path: "meta/revision", Value: lkal.Int(rev1)},
			{Kind: patch.OpSet, Path: "meta/labels", Value: rec(f("app.example.com/name", lkal.String("web")))},
			{Kind: patch.OpSet, Path: "spec/ratio", Value: lkal.Decimal("1.50")},
			{Kind: patch.OpSet, Path: "spec/mem", Value: lkal.Quantity{Number: "1.50", Unit: "Mi"}},
		})
		if err != nil {
			t.Fatalf("Patch(1): %v", err)
		}
		meta2, _ := getRecordField(patched1, "meta")
		rev2 := getIntField(meta2, "revision")
		if rev2 <= rev1 || getIntField(meta2, "generation") != 2 {
			t.Fatalf("Patch(1) rev=%d gen=%d, want rev>%d gen=2", rev2, getIntField(meta2, "generation"), rev1)
		}

		// No-op Patch with unnormalized decimal does not move store revision or generation.
		curBefore, err := st.CurrentRev(ctx)
		if err != nil {
			t.Fatal(err)
		}
		noop, err := objs.Patch(ctx, refs.Principal{}, obj, []patch.Op{
			{Kind: patch.OpTest, Path: "spec/ratio", Value: lkal.Decimal("1.50")},
			{Kind: patch.OpSet, Path: "spec/ratio", Value: lkal.Decimal("1.500")},
		})
		if err != nil {
			t.Fatalf("Patch(noop): %v", err)
		}
		curAfter, err := st.CurrentRev(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if curAfter != curBefore || !lkal.Equal(noop, patched1) {
			t.Fatalf("no-op Patch moved store rev %d -> %d", curBefore, curAfter)
		}

		// Deleting spec/replicas re-completes default = 3 (changing 5 -> 3 and bumping generation).
		afterDelDefault, err := objs.Patch(ctx, refs.Principal{}, obj, []patch.Op{
			{Kind: patch.OpDel, Path: "spec/replicas"},
		})
		if err != nil {
			t.Fatalf("Patch(del replicas): %v", err)
		}
		specDel, _ := getRecordField(afterDelDefault, "spec")
		if got := getIntField(specDel, "replicas"); got != 3 {
			t.Fatalf("replicas after del = %d, want default 3", got)
		}
		metaDel, _ := getRecordField(afterDelDefault, "meta")
		if got := getIntField(metaDel, "generation"); got != 3 {
			t.Fatalf("generation after del replicas = %d, want 3", got)
		}

		// Deleting spec/replicas again when already at default 3 is a no-op (no Txn, no gen bump).
		curBefore, _ = st.CurrentRev(ctx)
		if _, err := objs.Patch(ctx, refs.Principal{}, obj, []patch.Op{{Kind: patch.OpDel, Path: "spec/replicas"}}); err != nil {
			t.Fatalf("Patch(del default replicas noop): %v", err)
		}
		if curAfter, _ = st.CurrentRev(ctx); curAfter != curBefore {
			t.Fatalf("re-deleting defaulted field moved store rev %d -> %d", curBefore, curAfter)
		}

		// Deleting parent record spec/probe removes probe without recreating its defaulted child.
		afterDelProbe, err := objs.Patch(ctx, refs.Principal{}, obj, []patch.Op{
			{Kind: patch.OpDel, Path: "spec/probe"},
		})
		if err != nil {
			t.Fatalf("Patch(del probe): %v", err)
		}
		specNoProbe, _ := getRecordField(afterDelProbe, "spec")
		if _, exists := getField(specNoProbe, "probe"); exists {
			t.Fatalf("spec/probe unexpectedly recreated by completion")
		}

		// Atomic ordered-set order swap in one Patch without intermediate duplicate-order collision.
		afterSwap, err := objs.Patch(ctx, refs.Principal{}, obj, []patch.Op{
			{Kind: patch.OpSet, Path: "spec/plugin/a/order", Value: lkal.Int(20)},
			{Kind: patch.OpSet, Path: "spec/plugin/b/order", Value: lkal.Int(10)},
		})
		if err != nil {
			t.Fatalf("Patch(swap orderedset orders): %v", err)
		}
		specSwap, _ := getRecordField(afterSwap, "spec")
		plugSwap, _ := getRecordField(specSwap, "plugin")
		entries, err := orderedset.Entries(plugSwap)
		if err != nil {
			t.Fatalf("orderedset.Entries: %v", err)
		}
		if len(entries) != 2 || entries[0].Key != "b" || entries[1].Key != "a" {
			t.Fatalf("swapped entries = %#v, want [b, a]", entries)
		}

		// Duplicate order after patch completion -> envelope.ErrMalformed.
		if _, err := objs.Patch(ctx, refs.Principal{}, obj, []patch.Op{
			{Kind: patch.OpSet, Path: "spec/plugin/a/order", Value: lkal.Int(10)},
		}); !errors.Is(err, envelope.ErrMalformed) {
			t.Fatalf("Patch(duplicate order) = %v, want envelope.ErrMalformed", err)
		}

		// Stale revision test -> patch.ErrConflict.
		if _, err := objs.Patch(ctx, refs.Principal{}, obj, []patch.Op{
			{Kind: patch.OpTest, Path: "meta/revision", Value: lkal.Int(rev1)},
			{Kind: patch.OpSet, Path: "spec/replicas", Value: lkal.Int(9)},
		}); !errors.Is(err, patch.ErrConflict) {
			t.Fatalf("Patch(stale rev test) = %v, want patch.ErrConflict", err)
		}

		// PatchStatus lifecycle: sets status, observedGeneration, condition with server since.
		st1, err := objs.PatchStatus(ctx, obj, []patch.Op{
			{Kind: patch.OpSet, Path: "status/phase", Value: lkal.String("Running")},
			{Kind: patch.OpSet, Path: "status/observedGeneration", Value: lkal.Int(2)},
			{Kind: patch.OpSet, Path: "status/condition/Ready", Value: rec(
				f("state", lkal.Enum("true")),
				f("reason", lkal.String("Available")),
			)},
		})
		if err != nil {
			t.Fatalf("PatchStatus(1): %v", err)
		}
		st1Status, _ := getRecordField(st1, "status")
		st1Cond, _ := getRecordField(st1Status, "condition")
		st1Ready, _ := getRecordField(st1Cond, "Ready")
		if got := getStringField(st1Ready, "since"); got != "2026-11-22T10:00:00Z" {
			t.Fatalf("initial since = %q, want 2026-11-22T10:00:00Z", got)
		}

		// Advance clock, patch condition reason only -> since preserved.
		clk.Set(time.Date(2026, 11, 22, 11, 0, 0, 0, time.UTC))
		st2, err := objs.PatchStatus(ctx, obj, []patch.Op{
			{Kind: patch.OpSet, Path: "status/condition/Ready/reason", Value: lkal.String("Updated")},
		})
		if err != nil {
			t.Fatalf("PatchStatus(2): %v", err)
		}
		st2Status, _ := getRecordField(st2, "status")
		st2Cond, _ := getRecordField(st2Status, "condition")
		st2Ready, _ := getRecordField(st2Cond, "Ready")
		if got := getStringField(st2Ready, "since"); got != "2026-11-22T10:00:00Z" {
			t.Fatalf("same-state since = %q, want 2026-11-22T10:00:00Z", got)
		}

		// Direct since mutation -> patch.ErrForbidden; observedGeneration regression -> envelope.ErrObserved.
		if _, err := objs.PatchStatus(ctx, obj, []patch.Op{
			{Kind: patch.OpSet, Path: "status/condition/Ready/since", Value: lkal.String("2026-11-22T11:00:00Z")},
		}); !errors.Is(err, patch.ErrForbidden) {
			t.Fatalf("PatchStatus(set since) = %v, want patch.ErrForbidden", err)
		}
		if _, err := objs.PatchStatus(ctx, obj, []patch.Op{
			{Kind: patch.OpSet, Path: "status/observedGeneration", Value: lkal.Int(1)},
		}); !errors.Is(err, envelope.ErrObserved) {
			t.Fatalf("PatchStatus(regress obsGen) = %v, want envelope.ErrObserved", err)
		}

		// PatchStatus no-op does not move store revision.
		curBefore, _ = st.CurrentRev(ctx)
		if _, err := objs.PatchStatus(ctx, obj, []patch.Op{
			{Kind: patch.OpSet, Path: "status/phase", Value: lkal.String("Running")},
		}); err != nil {
			t.Fatalf("PatchStatus(noop): %v", err)
		}
		if curAfter, _ = st.CurrentRev(ctx); curAfter != curBefore {
			t.Fatalf("no-op PatchStatus moved store rev %d -> %d", curBefore, curAfter)
		}

		// Cross-entrance rejection.
		if _, err := objs.Patch(ctx, refs.Principal{}, obj, []patch.Op{{Kind: patch.OpDel, Path: "status"}}); !errors.Is(err, patch.ErrForbidden) {
			t.Fatalf("Patch(del status) = %v, want patch.ErrForbidden", err)
		}
		if _, err := objs.PatchStatus(ctx, obj, []patch.Op{{Kind: patch.OpSet, Path: "spec/replicas", Value: lkal.Int(1)}}); !errors.Is(err, patch.ErrForbidden) {
			t.Fatalf("PatchStatus(set spec) = %v, want patch.ErrForbidden", err)
		}
	})
}

func checkObjectsPatchRefs(t *testing.T, newRoot func(*testing.T) store.Store) {
	t.Helper()

	t.Run("PatchRefsAuthorizeAndBackRefIndex", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		reg := tenant.NewRegistry(newRoot(t))
		if err := reg.Register(ctx, "acme"); err != nil {
			t.Fatalf("Register: %v", err)
		}
		v, err := reg.View(ctx, "acme")
		if err != nil {
			t.Fatalf("View: %v", err)
		}

		cfg := resource.Config{
			Tenant:     "acme",
			Schemas:    refSuiteSchemas,
			Authorizer: mustCedarAuthorizer(t, defaultCedarPS),
			Rand:       &seqRand{},
		}
		objs := resource.NewObjects(v, cfg)

		secA := rec(
			f("meta", rec(
				f("kind", lkal.String("secret")),
				f("name", lkal.String("a")),
				f("labels", rec(f("share", lkal.String("yes")))),
			)),
		)
		secB := rec(
			f("meta", rec(
				f("kind", lkal.String("secret")),
				f("name", lkal.String("b")),
				f("labels", rec(f("share", lkal.String("yes")))),
			)),
		)
		if _, err := objs.Create(ctx, cedarAlice, secA); err != nil {
			t.Fatalf("Create(secret a): %v", err)
		}
		if _, err := objs.Create(ctx, cedarAlice, secB); err != nil {
			t.Fatalf("Create(secret b): %v", err)
		}
		wl := rec(
			f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("app")))),
			f("spec", rec(f("primary", lkal.Ref{"secret", "a"}))),
		)
		if _, err := objs.Create(ctx, cedarAlice, wl); err != nil {
			t.Fatalf("Create(workload): %v", err)
		}

		wlObj := keyspace.Object{Group: "core", Kind: "workload", Name: "app"}
		// Bob is denied referencing secret b.
		var re *refs.Error
		if _, err := objs.Patch(ctx, cedarBob, wlObj, []patch.Op{
			{Kind: patch.OpSet, Path: "spec/primary", Value: lkal.Ref{"secret", "b"}},
		}); !errors.As(err, &re) || re.Kind != refs.ErrDenied {
			t.Fatalf("Patch(bob ref b) = %v, want refs.ErrDenied", err)
		}
		// Alice referencing absent secret without labels is denied by Cedar (no existence leak).
		re = nil
		if _, err := objs.Patch(ctx, cedarAlice, wlObj, []patch.Op{
			{Kind: patch.OpSet, Path: "spec/primary", Value: lkal.Ref{"secret", "missing"}},
		}); !errors.As(err, &re) || re.Kind != refs.ErrDenied {
			t.Fatalf("Patch(alice ref missing under label policy) = %v, want refs.ErrDenied", err)
		}
		// Under an allow-all authorizer, referencing absent secret returns refs.ErrNotFound.
		allowAllObjs := resource.NewObjects(v, resource.Config{
			Tenant:     "acme",
			Schemas:    refSuiteSchemas,
			Authorizer: &fakeAuthorizer{},
			Rand:       &seqRand{},
		})
		re = nil
		if _, err := allowAllObjs.Patch(ctx, testPrincipal, wlObj, []patch.Op{
			{Kind: patch.OpSet, Path: "spec/primary", Value: lkal.Ref{"secret", "missing"}},
		}); !errors.As(err, &re) || re.Kind != refs.ErrNotFound {
			t.Fatalf("Patch(allow-all ref missing) = %v, want refs.ErrNotFound", err)
		}
		// Alice switches ref from secret a -> secret b; backref index updates atomically.
		if _, err := objs.Patch(ctx, cedarAlice, wlObj, []patch.Op{
			{Kind: patch.OpSet, Path: "spec/primary", Value: lkal.Ref{"secret", "b"}},
		}); err != nil {
			t.Fatalf("Patch(alice ref b): %v", err)
		}
		brA := "ref/secret/a/workload/app"
		brB := "ref/secret/b/workload/app"
		if _, _, err := v.Get(ctx, brA, 0); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("old backref %s still present: %v", brA, err)
		}
		if _, _, err := v.Get(ctx, brB, 0); err != nil {
			t.Fatalf("new backref %s missing: %v", brB, err)
		}
	})
}

func TestMemObjectsPatchAndList(t *testing.T) {
	checkObjectsPatchAndList(t, func(t *testing.T) tenant.Store {
		t.Helper()
		return memstore.New()
	})
	checkObjectsPatchRefs(t, func(t *testing.T) store.Store {
		t.Helper()
		return memstore.New()
	})
}

func TestDecodeObjectAndListMultiPage(t *testing.T) {
	ctx := context.Background()
	obj := keyspace.Object{Group: "core", Kind: "workload", Name: "web"}
	stored, err := envelope.Create(workloadCand("web", 2), "01234567-89ab-4def-8123-456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := resource.Encode(stored)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := resource.DecodeObject(obj, raw, 7)
	if err != nil {
		t.Fatalf("DecodeObject: %v", err)
	}
	meta, _ := getRecordField(decoded, "meta")
	if got := getIntField(meta, "revision"); got != 7 {
		t.Fatalf("DecodeObject revision = %d, want 7", got)
	}

	// Corrupt cases: invalid modRev, wrong identity, bad bytes, invalid object.
	if _, err := resource.DecodeObject(obj, raw, 0); !errors.Is(err, resource.ErrCorrupt) {
		t.Fatalf("DecodeObject(rev=0) = %v, want ErrCorrupt", err)
	}
	if _, err := resource.DecodeObject(keyspace.Object{Group: "core", Kind: "workload", Name: "other"}, raw, 7); !errors.Is(err, resource.ErrCorrupt) {
		t.Fatalf("DecodeObject(wrong name) = %v, want ErrCorrupt", err)
	}
	if _, err := resource.DecodeObject(obj, []byte("{bad"), 7); !errors.Is(err, resource.ErrCorrupt) {
		t.Fatalf("DecodeObject(bad json) = %v, want ErrCorrupt", err)
	}
	if _, err := resource.DecodeObject(keyspace.Object{Group: "core", Kind: "Bad", Name: "x"}, raw, 7); !errors.Is(err, keyspace.ErrInvalid) {
		t.Fatalf("DecodeObject(invalid obj) = %v, want keyspace.ErrInvalid", err)
	}

	// Multi-page List (Limit == 0) spans > defaultListPageSize (512) objects cleanly.
	st := memstore.New()
	const total = 520
	for i := range total {
		name := fmt.Sprintf("w-%04d", i)
		doc, err := envelope.Create(workloadCand(name, 1), fmt.Sprintf("00000000-0000-4000-8000-%012d", i))
		if err != nil {
			t.Fatal(err)
		}
		b, err := resource.Encode(doc)
		if err != nil {
			t.Fatal(err)
		}
		key := "o/core/workload/" + name
		if _, _, err := st.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: key, Value: b}}); err != nil {
			t.Fatal(err)
		}
	}
	cfg, _ := patchObjectsConfig()
	objs := resource.NewObjects(st, cfg)
	res, err := objs.List(ctx, "core", "workload", resource.ListOptions{})
	if err != nil {
		t.Fatalf("List(520): %v", err)
	}
	if len(res.Items) != total || res.Continue != "" {
		t.Fatalf("List(520) = (%d items, cont=%q), want %d", len(res.Items), res.Continue, total)
	}
}

func TestObjectsPatchErrorsFencingAndCAS(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	cfg, _ := patchObjectsConfig()
	objs := resource.NewObjects(st, cfg)

	obj := keyspace.Object{Group: "core", Kind: "workload", Name: "item"}
	if _, err := objs.Create(ctx, refs.Principal{}, workloadCand("item", 2)); err != nil {
		t.Fatal(err)
	}

	// Invalid object -> envelope.ErrMalformed.
	badObj := keyspace.Object{Group: "core", Kind: "BadKind", Name: "item"}
	if _, err := objs.Patch(ctx, refs.Principal{}, badObj, nil); !errors.Is(err, envelope.ErrMalformed) {
		t.Fatalf("Patch(bad obj) = %v, want envelope.ErrMalformed", err)
	}
	// Malformed ops -> patch.ErrMalformed.
	if _, err := objs.Patch(ctx, refs.Principal{}, obj, []patch.Op{{Kind: patch.OpDel, Path: "spec/a", Value: lkal.Int(1)}}); !errors.Is(err, patch.ErrMalformed) {
		t.Fatalf("Patch(bad ops) = %v, want patch.ErrMalformed", err)
	}
	// Invalid LKAL value inside op -> patch.ErrMalformed.
	if _, err := objs.Patch(ctx, refs.Principal{}, obj, []patch.Op{{Kind: patch.OpSet, Path: "spec/bad", Value: lkal.List{lkal.Int(1), lkal.String("a")}}}); !errors.Is(err, patch.ErrMalformed) {
		t.Fatalf("Patch(mixed list) = %v, want patch.ErrMalformed", err)
	}
	// Unknown kind -> envelope.ErrMalformed.
	unknownObj := keyspace.Object{Group: "core", Kind: "unknown", Name: "item"}
	if _, err := objs.Patch(ctx, refs.Principal{}, unknownObj, nil); !errors.Is(err, envelope.ErrMalformed) {
		t.Fatalf("Patch(unknown kind) = %v, want envelope.ErrMalformed", err)
	}
	// Missing object -> resource.ErrNotFound.
	missingObj := keyspace.Object{Group: "core", Kind: "workload", Name: "missing"}
	if _, err := objs.Patch(ctx, refs.Principal{}, missingObj, nil); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("Patch(missing) = %v, want ErrNotFound", err)
	}
	// Missing field for del -> patch.ErrNotFound.
	if _, err := objs.Patch(ctx, refs.Principal{}, obj, []patch.Op{{Kind: patch.OpDel, Path: "spec/nope"}}); !errors.Is(err, patch.ErrNotFound) {
		t.Fatalf("Patch(del missing) = %v, want patch.ErrNotFound", err)
	}

	// Concurrent modification during CAS -> envelope.ErrConflict.
	casStore := &conflictOnceStore{Store: st}
	casObjs := resource.NewObjects(casStore, cfg)
	if _, err := casObjs.Patch(ctx, refs.Principal{}, obj, []patch.Op{{Kind: patch.OpSet, Path: "spec/replicas", Value: lkal.Int(8)}}); !errors.Is(err, envelope.ErrConflict) {
		t.Fatalf("Patch(CAS conflict) = %v, want envelope.ErrConflict", err)
	}

	// Fenced tenant view returns tenant.ErrFenced on Patch and PatchStatus.
	root := memstore.New()
	reg := tenant.NewRegistry(root)
	if err := reg.Register(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	vOld, err := reg.View(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	oldObjs := resource.NewObjects(vOld, cfg)
	if _, err := oldObjs.Create(ctx, refs.Principal{}, workloadCand("fenced", 1)); err != nil {
		t.Fatal(err)
	}
	if err := reg.Delete(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(ctx, "alpha"); err != nil {
		t.Fatal(err)
	}
	vNew, err := reg.View(ctx, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	newObjs := resource.NewObjects(vNew, cfg)
	if _, err := newObjs.Create(ctx, refs.Principal{}, workloadCand("fenced", 1)); err != nil {
		t.Fatal(err)
	}
	fencedObj := keyspace.Object{Group: "core", Kind: "workload", Name: "fenced"}
	if _, err := oldObjs.Patch(ctx, refs.Principal{}, fencedObj, []patch.Op{{Kind: patch.OpSet, Path: "spec/replicas", Value: lkal.Int(9)}}); !errors.Is(err, tenant.ErrFenced) {
		t.Fatalf("Patch on fenced view = %v, want tenant.ErrFenced", err)
	}
	if _, err := oldObjs.PatchStatus(ctx, fencedObj, []patch.Op{{Kind: patch.OpSet, Path: "status/phase", Value: lkal.String("Ready")}}); !errors.Is(err, tenant.ErrFenced) {
		t.Fatalf("PatchStatus on fenced view = %v, want tenant.ErrFenced", err)
	}

	// Corrupt stored entry in List -> ErrCorrupt.
	corruptStore := memstore.New()
	if _, _, err := corruptStore.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: "o/core/workload/bad", Value: []byte("corrupt")}}); err != nil {
		t.Fatal(err)
	}
	corruptObjs := resource.NewObjects(corruptStore, cfg)
	if _, err := corruptObjs.List(ctx, "core", "workload", resource.ListOptions{}); !errors.Is(err, resource.ErrCorrupt) {
		t.Fatalf("List(corrupt) = %v, want ErrCorrupt", err)
	}

	// Unexpected key format under prefix -> ErrCorrupt; compacted/future rev -> store errors.
	badKeyStore := memstore.New()
	if _, _, err := badKeyStore.Txn(ctx, nil, []store.Op{{Type: store.OpPut, Key: "o/core/workload/a/extra", Value: []byte("{}")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := resource.NewObjects(badKeyStore, cfg).List(ctx, "core", "workload", resource.ListOptions{}); !errors.Is(err, resource.ErrCorrupt) {
		t.Fatalf("List(bad key) = %v, want ErrCorrupt", err)
	}
	if _, err := objs.List(ctx, "core", "workload", resource.ListOptions{AtRev: 999999}); !errors.Is(err, store.ErrFutureRev) {
		t.Fatalf("List(future rev) = %v, want store.ErrFutureRev", err)
	}
}

type conflictOnceStore struct {
	tenant.Store
}

func (c *conflictOnceStore) Txn(_ context.Context, _ []store.Cmp, _ []store.Op) (bool, store.Rev, error) {
	return false, 1, nil
}
