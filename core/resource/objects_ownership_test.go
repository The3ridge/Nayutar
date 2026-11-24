package resource_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/resource"
	"github.com/Kaikei-e/Nayutar/core/resource/envelope"
	"github.com/Kaikei-e/Nayutar/core/resource/ownership"
	"github.com/Kaikei-e/Nayutar/core/resource/patch"
	"github.com/Kaikei-e/Nayutar/core/resource/refs"
	"github.com/Kaikei-e/Nayutar/core/store"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
	"github.com/Kaikei-e/Nayutar/core/store/memstore"
	"github.com/Kaikei-e/Nayutar/core/store/tenant"
)

func checkObjectsOwnership(t *testing.T, newStore func(*testing.T) tenant.Store) {
	t.Helper()

	alice := refs.Principal{Type: "User", ID: "alice"}
	bob := refs.Principal{Type: "User", ID: "bob"}
	hpa := refs.Principal{Type: "ServiceAccount", ID: "hpa"}

	resolver := func(p refs.Principal, _ keyspace.Object) (ownership.ActorID, ownership.Scope, error) {
		switch p {
		case alice:
			return "User:alice", ownership.RootScope(ownership.PermAll), nil
		case bob:
			return "User:bob", ownership.RootScope(ownership.PermAll), nil
		case hpa:
			r, err := ownership.NewExactRule(ownership.Path{"spec", "replicas"}, ownership.PermSet|ownership.PermOwn|ownership.PermResolve)
			if err != nil {
				return "", ownership.Scope{}, err
			}
			return "ServiceAccount:hpa", ownership.MustNewScope(r), nil
		default:
			return "", ownership.Scope{}, errors.New("unknown principal")
		}
	}

	t.Run("ApplyPruneDefaultsOrderedSetAndMetadataOnlyCAS", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		st := newStore(t)
		cfg, _ := patchObjectsConfig()
		cfg.ScopeResolver = resolver
		objs := resource.NewObjects(st, cfg)
		obj := keyspace.Object{Group: "core", Kind: "workload", Name: "api"}

		// Unknown principal fails closed with K401.
		if _, err := objs.Apply(ctx, refs.Principal{}, workloadCand("api", 2), resource.ApplyOptions{}); !errors.Is(err, ownership.ErrPolicyDenied) {
			t.Fatalf("Apply(unknown principal) = %v, want K401", err)
		}

		// Nested empty record in Apply declaration fails with K102.
		badNested := rec(
			f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("api")))),
			f("spec", rec(f("probe", rec()))),
		)
		if _, err := objs.Apply(ctx, alice, badNested, resource.ApplyOptions{}); !errors.Is(err, ownership.ErrEmptyRecord) {
			t.Fatalf("Apply(nested empty) = %v, want K102", err)
		}

		// Alice creates via Apply omitting defaulted spec/replicas and spec/plugin/a/enabled.
		aliceDecl1 := rec(
			f("meta", rec(
				f("kind", lkal.String("workload")),
				f("name", lkal.String("api")),
				f("labels", rec(f("env", lkal.String("prod")))),
			)),
			f("spec", rec(
				f("probe", rec(f("path", lkal.String("/healthz")))),
				f("plugin", rec(
					f("a", rec(f("order", lkal.Int(10)))),
				)),
			)),
		)
		res1, err := objs.Apply(ctx, alice, aliceDecl1, resource.ApplyOptions{})
		if err != nil {
			t.Fatalf("Apply(create): %v", err)
		}
		if !res1.Created || res1.OwnerRevision < 1 {
			t.Fatalf("Apply(create) = (created=%v ownerRev=%d)", res1.Created, res1.OwnerRevision)
		}
		spec1, _ := getRecordField(res1.Object, "spec")
		if got := getIntField(spec1, "replicas"); got != 3 {
			t.Fatalf("default replicas = %d, want 3", got)
		}

		// Explain shows defaulted fields as StateDefaulted and Alice's leaves as StateOwned/Created.
		exp1, err := objs.Explain(ctx, obj, 0)
		if err != nil {
			t.Fatalf("Explain(1): %v", err)
		}
		findLeaf := func(exp ownership.Explanation, path string) ownership.LeafExplanation {
			for _, lf := range exp.Leaves {
				if lf.Path.String() == path {
					return lf
				}
			}
			t.Fatalf("leaf %s missing from explain %+v", path, exp.Leaves)
			return ownership.LeafExplanation{}
		}
		if lf := findLeaf(exp1, "spec/replicas"); lf.State != ownership.StateDefaulted {
			t.Fatalf("spec/replicas state = %s, want defaulted", lf.State)
		}
		if lf := findLeaf(exp1, "spec/plugin/a/enabled"); lf.State != ownership.StateDefaulted {
			t.Fatalf("spec/plugin/a/enabled state = %s, want defaulted", lf.State)
		}
		if lf := findLeaf(exp1, "spec/plugin/a/order"); lf.State != ownership.StateOwned || lf.Owner != "User:alice" || lf.Origin != ownership.OriginCreated {
			t.Fatalf("spec/plugin/a/order = %+v, want (alice, created)", lf)
		}

		// HPA (scoped strictly to spec/replicas) claims defaulted spec/replicas = 5 without K206.
		hpaDecl := rec(
			f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("api")))),
			f("spec", rec(f("replicas", lkal.Int(5)))),
		)
		resHPA, err := objs.Apply(ctx, hpa, hpaDecl, resource.ApplyOptions{
			Expect:           exp1.Expect,
			ExpectedOwnerRev: res1.OwnerRevision,
		})
		if err != nil {
			t.Fatalf("Apply(hpa): %v", err)
		}
		if resHPA.Created {
			t.Fatal("HPA apply reported Created=true on existing object")
		}
		// HPA attempting to write spec/probe/path fails with K201 (Alice owns it; K201 > K401).
		hpaBad := rec(
			f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("api")))),
			f("spec", rec(f("replicas", lkal.Int(6)), f("probe", rec(f("path", lkal.String("/bad")))))),
		)
		if _, err := objs.Apply(ctx, hpa, hpaBad, resource.ApplyOptions{}); !errors.Is(err, ownership.ErrOwnershipConflict) {
			t.Fatalf("HPA foreign overlap = %v, want K201", err)
		}
		// HPA attempting to write unowned spec/ratio outside its scope fails with K401.
		hpaOutOfScope := rec(
			f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("api")))),
			f("spec", rec(f("replicas", lkal.Int(5)), f("ratio", lkal.Decimal("1.5")))),
		)
		if _, err := objs.Apply(ctx, hpa, hpaOutOfScope, resource.ApplyOptions{}); !errors.Is(err, ownership.ErrPolicyDenied) {
			t.Fatalf("HPA out of scope = %v, want K401", err)
		}

		// Alice retires spec/plugin/a and meta/labels/env: Apply emits K604 and does not delete.
		aliceDecl2 := rec(
			f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("api")))),
			f("spec", rec(f("probe", rec(f("path", lkal.String("/healthz")))))),
		)
		resApplyRetire, err := objs.Apply(ctx, alice, aliceDecl2, resource.ApplyOptions{})
		if err != nil {
			t.Fatalf("Apply(retire): %v", err)
		}
		hasK604 := false
		for _, d := range resApplyRetire.Diagnostics {
			if d.Code == ownership.CodePruneRequired {
				hasK604 = true
			}
		}
		if !hasK604 {
			t.Fatalf("expected K604 notice on Apply, got %+v", resApplyRetire.Diagnostics)
		}

		// Alice runs Prune: removes ordered-set element spec/plugin/a (with defaulted enabled) and meta/labels/env!
		resPrune, err := objs.Prune(ctx, alice, aliceDecl2, resource.ApplyOptions{})
		if err != nil {
			t.Fatalf("Prune(alice): %v", err)
		}
		specPruned, _ := getRecordField(resPrune.Object, "spec")
		if _, exists := getField(specPruned, "plugin"); exists {
			t.Fatal("spec/plugin was not bottom-up pruned")
		}
		if got := getIntField(specPruned, "replicas"); got != 5 {
			t.Fatalf("HPA replicas after Alice prune = %d, want 5", got)
		}

		// Explicit Release by Alice on spec/probe/path must NOT modify o/ ModRev (metadata-only CAS)!
		expBeforeRel, err := objs.Explain(ctx, obj, 0)
		if err != nil {
			t.Fatalf("Explain before release: %v", err)
		}
		pathLeaf := findLeaf(expBeforeRel, "spec/probe/path")
		relRes, err := objs.Release(ctx, alice, obj, resource.ResolveOptions{
			Path:             "spec/probe/path",
			Expect:           pathLeaf.Expect,
			ExpectedOwnerRev: store.Rev(expBeforeRel.OwnerRevision),
		})
		if err != nil {
			t.Fatalf("Release: %v", err)
		}
		if relRes.Explanation.Revision != expBeforeRel.Revision {
			t.Fatalf("Release mutated o/ ModRev %d -> %d, want unchanged", expBeforeRel.Revision, relRes.Explanation.Revision)
		}
		if relRes.Explanation.OwnerRevision <= expBeforeRel.OwnerRevision {
			t.Fatalf("Release did not advance ownerRevision: %d -> %d", expBeforeRel.OwnerRevision, relRes.Explanation.OwnerRevision)
		}

		// Bob Patching unowned spec/probe/path to new value fails with K206!
		if _, err := objs.Patch(ctx, bob, obj, []patch.Op{
			{Kind: patch.OpSet, Path: "spec/probe/path", Value: lkal.String("/readyz")},
		}); !errors.Is(err, ownership.ErrUnownedValueConflict) {
			t.Fatalf("Patch unowned mismatch = %v, want K206", err)
		}

		// Bob Adopts spec/probe/path (metadata-only CAS, o/ ModRev stays unchanged!), then Updates it.
		unownedLeaf := findLeaf(relRes.Explanation, "spec/probe/path")
		adoptRes, err := objs.Adopt(ctx, bob, obj, resource.ResolveOptions{
			Path:             "spec/probe/path",
			Expect:           unownedLeaf.Expect,
			ExpectedOwnerRev: store.Rev(relRes.Explanation.OwnerRevision),
		})
		if err != nil {
			t.Fatalf("Adopt: %v", err)
		}
		if adoptRes.Explanation.Revision != expBeforeRel.Revision {
			t.Fatalf("Adopt mutated o/ ModRev %d -> %d, want unchanged", expBeforeRel.Revision, adoptRes.Explanation.Revision)
		}
		if lf := findLeaf(adoptRes.Explanation, "spec/probe/path"); lf.Owner != "User:bob" || lf.Origin != ownership.OriginModified {
			t.Fatalf("adopted leaf = %+v, want (bob, modified)", lf)
		}

		// Bob updates spec/probe/path via Apply -> keeps OriginModified, now succeeds.
		bobApply := rec(
			f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("api")))),
			f("spec", rec(f("probe", rec(f("path", lkal.String("/readyz")))))),
		)
		if _, err := objs.Apply(ctx, bob, bobApply, resource.ApplyOptions{}); err != nil {
			t.Fatalf("Apply(bob after adopt): %v", err)
		}

		// HPA Prune of its own OriginCreated spec/replicas restores schema default = 3 and marks it .defaulted!
		hpaEmpty := rec(
			f("meta", rec(f("kind", lkal.String("workload")), f("name", lkal.String("api")))),
			f("spec", rec()),
		)
		hpaPruneScope := func(p refs.Principal, _ keyspace.Object) (ownership.ActorID, ownership.Scope, error) {
			if p == hpa {
				r, _ := ownership.NewExactRule(ownership.Path{"spec", "replicas"}, ownership.PermAll)
				return "ServiceAccount:hpa", ownership.MustNewScope(r), nil
			}
			return resolver(p, obj)
		}
		cfgPrune := cfg
		cfgPrune.ScopeResolver = hpaPruneScope
		pruneObjs := resource.NewObjects(st, cfgPrune)
		prunedHPA, err := pruneObjs.Prune(ctx, hpa, hpaEmpty, resource.ApplyOptions{})
		if err != nil {
			t.Fatalf("Prune(hpa): %v", err)
		}
		specRestored, _ := getRecordField(prunedHPA.Object, "spec")
		if got := getIntField(specRestored, "replicas"); got != 3 {
			t.Fatalf("replicas after HPA prune = %d, want default 3", got)
		}
		expRestored, err := pruneObjs.Explain(ctx, obj, 0)
		if err != nil {
			t.Fatalf("Explain after HPA prune: %v", err)
		}
		if lf := findLeaf(expRestored, "spec/replicas"); lf.State != ownership.StateDefaulted {
			t.Fatalf("spec/replicas after HPA prune = %+v, want defaulted", lf)
		}

		// Delete removes both o/ and own/ atomically.
		if err := objs.Delete(ctx, obj, 0); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		ownKey, _ := (keyspace.Key{Family: keyspace.FamilyOwnership, Tenant: "t", Object: obj}).Rel()
		if _, _, err := st.Get(ctx, ownKey, 0); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("own/ key still present after Delete: %v", err)
		}
	})
}

func TestMemObjectsOwnership(t *testing.T) {
	checkObjectsOwnership(t, func(t *testing.T) tenant.Store {
		t.Helper()
		return memstore.New()
	})
}

func TestObjectsOwnershipDefaultModeAndConflictErrors(t *testing.T) {
	ctx := context.Background()
	st := memstore.New()
	cfg, _ := patchObjectsConfig()
	objs := resource.NewObjects(st, cfg)
	obj := keyspace.Object{Group: "core", Kind: "workload", Name: "def"}

	// Default library mode (ScopeResolver == nil): complete principal maps to Type:ID with PermRegular (no PermResolve).
	alice := refs.Principal{Type: "User", ID: "alice"}
	bob := refs.Principal{Type: "User", ID: "bob"}
	if _, err := objs.Create(ctx, alice, workloadCand("def", 4)); err != nil {
		t.Fatalf("Create: %v", err)
	}
	exp, err := objs.Explain(ctx, obj, 0)
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if exp.DefaultOwner != "User:alice" {
		t.Fatalf("DefaultOwner = %q, want User:alice", exp.DefaultOwner)
	}
	// Release requires PermResolve -> denied in default mode.
	if _, err := objs.Release(ctx, alice, obj, resource.ResolveOptions{
		Path:             "spec/replicas",
		Expect:           exp.Leaves[0].Expect,
		ExpectedOwnerRev: store.Rev(exp.OwnerRevision),
	}); !errors.Is(err, ownership.ErrPolicyDenied) {
		t.Fatalf("Release in default mode = %v, want K401", err)
	}
	// Bob Update modifying Alice's leaf fails with K201.
	if _, err := objs.Update(ctx, bob, workloadCand("def", 9)); !errors.Is(err, ownership.ErrOwnershipConflict) {
		t.Fatalf("Bob Update foreign leaf = %v, want K201", err)
	}
	// CAS failure on Apply/Adopt returns envelope.ErrConflict.
	casObjs := resource.NewObjects(&conflictOnceStore{Store: st}, cfg)
	if _, err := casObjs.Apply(ctx, alice, workloadCand("def", 7), resource.ApplyOptions{}); !errors.Is(err, envelope.ErrConflict) {
		t.Fatalf("Apply CAS conflict = %v, want envelope.ErrConflict", err)
	}
}
