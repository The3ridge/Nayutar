package ownership_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/Kaikei-e/LKAL"
	"github.com/Kaikei-e/Nayutar/core/resource"
	"github.com/Kaikei-e/Nayutar/core/resource/ownership"
	"github.com/Kaikei-e/Nayutar/core/store/keyspace"
)

func fld(k string, v lkal.Value) lkal.Field {
	return lkal.Field{Key: k, Value: v}
}

func rec(fields ...lkal.Field) lkal.Record {
	return lkal.Record{Fields: fields}
}

func objDoc(name string, labels *lkal.Record, spec *lkal.Record) lkal.Record {
	metaFields := []lkal.Field{
		fld("kind", lkal.String("workload")),
		fld("name", lkal.String(name)),
	}
	if labels != nil {
		metaFields = append(metaFields, fld("labels", *labels))
	}
	fields := []lkal.Field{fld("meta", lkal.Record{Fields: metaFields})}
	if spec != nil {
		fields = append(fields, fld("spec", *spec))
	}
	return lkal.Record{Fields: fields}
}

var testObj = keyspace.Object{Group: "core", Kind: "workload", Name: "web"}

func TestActorIDAndPathValidation(t *testing.T) {
	for _, valid := range []ownership.ActorID{"User:alice", "svc/hpa", "system:anonymous", "日本語-actor"} {
		if err := ownership.ValidateActorID(valid); err != nil {
			t.Fatalf("ValidateActorID(%q) unexpected err: %v", valid, err)
		}
	}
	for _, bad := range []ownership.ActorID{"", "   ", "a\x00b", "a\nb", "a\tb", "a\x1fb", "a\x7fb", ownership.ActorID([]byte{0xff, 0xfe})} {
		err := ownership.ValidateActorID(bad)
		if !errors.Is(err, ownership.ErrInvalidInput) {
			t.Fatalf("ValidateActorID(%q) = %v, want K101", bad, err)
		}
	}

	// Path segment parsing & quoting (including '/' inside label keys).
	pLabel := ownership.Path{"meta", "labels", "app.example.com/name"}
	if err := ownership.ValidateEntranceSpecPath(pLabel); err != nil {
		t.Fatalf("ValidateEntranceSpecPath(label with slash): %v", err)
	}
	s := pLabel.String()
	if s != `meta/labels/"app.example.com/name"` {
		t.Fatalf("pLabel.String() = %q", s)
	}
	parsed, err := ownership.ParsePath(s)
	if err != nil || !parsed.Equal(pLabel) {
		t.Fatalf("ParsePath(%q) = (%v, %v), want %v", s, parsed, err, pLabel)
	}
	// Leading slash accepted.
	parsedLead, err := ownership.ParsePath("/spec/replicas")
	if err != nil || !parsedLead.Equal(ownership.Path{"spec", "replicas"}) {
		t.Fatalf("ParsePath(/spec/replicas) = (%v, %v)", parsedLead, err)
	}

	// Outside EntranceSpec rejected.
	for _, out := range []ownership.Path{
		{"meta", "uid"},
		{"meta", "name"},
		{"meta", "labels"},
		{"meta", "labels", "a", "b"},
		{"spec"},
		{"status", "phase"},
		{"spec", "$bad"},
		{"spec", "bad\nkey"},
	} {
		if err := ownership.ValidateEntranceSpecPath(out); !errors.Is(err, ownership.ErrInvalidInput) {
			t.Fatalf("ValidateEntranceSpecPath(%v) = %v, want K101", out, err)
		}
	}
}

func TestScopeSegmentMatchingAndResolveIsolation(t *testing.T) {
	// Empty scope fails closed.
	var empty ownership.Scope
	if empty.Allows(ownership.Path{"spec", "replicas"}, ownership.PermOwn) {
		t.Fatal("empty scope unexpectedly allowed PermOwn")
	}

	// PermRegular never implies PermResolve.
	regScope := ownership.RootScope(ownership.PermRegular)
	if regScope.Allows(ownership.Path{"spec", "replicas"}, ownership.PermResolve) {
		t.Fatal("PermRegular must not imply PermResolve")
	}
	if !regScope.Allows(ownership.Path{"spec", "replicas"}, ownership.PermSet|ownership.PermOwn) {
		t.Fatal("PermRegular should allow Set+Own")
	}

	// Subtree matching operates on segments only, never raw string prefixes.
	subRule, err := ownership.NewSubtreeRule(ownership.Path{"spec", "app"}, ownership.PermSet|ownership.PermOwn)
	if err != nil {
		t.Fatal(err)
	}
	exactRule, err := ownership.NewExactRule(ownership.Path{"meta", "labels", "env"}, ownership.PermOwn|ownership.PermResolve)
	if err != nil {
		t.Fatal(err)
	}
	sc, err := ownership.NewScope(subRule, exactRule)
	if err != nil {
		t.Fatal(err)
	}

	if !sc.Allows(ownership.Path{"spec", "app"}, ownership.PermSet|ownership.PermOwn) {
		t.Fatal("subtree rule should match prefix itself")
	}
	if !sc.Allows(ownership.Path{"spec", "app", "image"}, ownership.PermSet|ownership.PermOwn) {
		t.Fatal("subtree rule should match descendant")
	}
	if sc.Allows(ownership.Path{"spec", "application", "image"}, ownership.PermOwn) {
		t.Fatal("subtree rule must not match raw string prefix sibling 'application'")
	}
	if !sc.Allows(ownership.Path{"meta", "labels", "env"}, ownership.PermOwn|ownership.PermResolve) {
		t.Fatal("exact rule should match exact label")
	}
	if sc.Allows(ownership.Path{"meta", "labels", "env2"}, ownership.PermOwn) {
		t.Fatal("exact rule must not match sibling")
	}
}

func TestDiagnosticPriorityAndPipelineStages(t *testing.T) {
	// Exact priority: K301 > K201 > K204 > K202/K302 > K206 > K203 > K401.
	ordered := []ownership.Code{
		ownership.CodeObservationUnavailable,
		ownership.CodeOwnershipConflict,
		ownership.CodeMigrationRequired,
		ownership.CodeStructureConflict,
		ownership.CodeUnownedValueConflict,
		ownership.CodeExternalDrift,
		ownership.CodePolicyDenied,
	}
	for i := range len(ordered) - 1 {
		if ownership.DiagnosticPriority(ordered[i]) >= ownership.DiagnosticPriority(ordered[i+1]) {
			t.Fatalf("priority %s (%d) not strictly higher than %s (%d)",
				ordered[i], ownership.DiagnosticPriority(ordered[i]),
				ordered[i+1], ownership.DiagnosticPriority(ordered[i+1]))
		}
	}
	if ownership.DiagnosticPriority(ownership.CodeStructureConflict) != ownership.DiagnosticPriority(ownership.CodeUnrepresentable) {
		t.Fatal("K202 and K302 must share the same priority tier")
	}

	// K301 beats K201 and K204.
	cpUnknown := ownership.ClassifyPath(ownership.PathInput{
		Path:               ownership.Path{"spec", "x"},
		Actor:              "User:alice",
		Declared:           true,
		Desired:            lkal.Int(1),
		Observed:           ownership.Observed{Kind: ownership.ObsUnknown, Reason: "unavailable"},
		ForeignOverlaps:    []ownership.Path{{"spec", "x"}},
		SelfRetiredCreated: []ownership.Path{{"spec", "x", "y"}},
	})
	if cpUnknown.PrimaryCode != ownership.CodeObservationUnavailable {
		t.Fatalf("PrimaryCode = %s, want K301", cpUnknown.PrimaryCode)
	}

	// K302 on opaque target.
	cpOpaque := ownership.ClassifyPath(ownership.PathInput{
		Path:     ownership.Path{"spec", "x"},
		Actor:    "User:alice",
		Declared: true,
		Desired:  lkal.Int(1),
		Observed: ownership.Observed{Kind: ownership.ObsOpaque, Reason: "unrepresentable"},
	})
	if cpOpaque.PrimaryCode != ownership.CodeUnrepresentable {
		t.Fatalf("PrimaryCode = %s, want K302", cpOpaque.PrimaryCode)
	}

	// SelectNoOp, SelectDeferred, and SelectBlocker must never trigger K401 under empty Scope.
	var emptyScope ownership.Scope
	for _, pp := range []ownership.PathPlan{
		ownership.SelectModeForPath(ownership.PathPlan{Path: ownership.Path{"spec", "a"}, StateClass: ownership.ClassConverged}, ownership.ModeApply),
		ownership.SelectModeForPath(ownership.PathPlan{Path: ownership.Path{"spec", "b"}, StateClass: ownership.ClassRetiredCreatedPrunable}, ownership.ModeApply),
		ownership.SelectModeForPath(ownership.PathPlan{Path: ownership.Path{"spec", "c"}, StateClass: ownership.ClassDeclaredConflict, PrimaryCode: ownership.CodeOwnershipConflict}, ownership.ModeApply),
	} {
		ap := ownership.AuthorizePath(pp, emptyScope)
		if ap.Decision == ownership.DecisionPolicyDenied || ap.PrimaryCode == ownership.CodePolicyDenied {
			t.Fatalf("non-candidate %+v unexpectedly triggered K401: %+v", pp.Selection, ap)
		}
	}
}

func TestApplyPruneDefaultedAndBottomUpPruning(t *testing.T) {
	alice := ownership.ActorID("User:alice")
	bob := ownership.ActorID("User:bob")
	allScope := ownership.RootScope(ownership.PermAll)

	// Empty section roots valid, nested {} rejected with K102.
	emptyRoots := objDoc("web", &lkal.Record{}, &lkal.Record{})
	if _, err := ownership.ProjectDeclaration(emptyRoots, false); err != nil {
		t.Fatalf("empty section roots failed: %v", err)
	}
	nestedEmpty := objDoc("web", nil, &lkal.Record{Fields: []lkal.Field{fld("nested", rec())}})
	if _, err := ownership.ProjectDeclaration(nestedEmpty, false); !errors.Is(err, ownership.ErrEmptyRecord) {
		t.Fatalf("nested {} = %v, want K102", err)
	}

	// Initial creation by Alice with schema-completed default on spec/replicas and spec/plugin/a/enabled.
	rawCreate := objDoc("web",
		&lkal.Record{Fields: []lkal.Field{fld("env", lkal.String("prod"))}},
		&lkal.Record{Fields: []lkal.Field{
			fld("image", lkal.String("nginx:1")),
			fld("plugin", rec(
				fld("a", rec(fld("order", lkal.Int(10)))),
			)),
		}},
	)
	completedCreate := objDoc("web",
		&lkal.Record{Fields: []lkal.Field{fld("env", lkal.String("prod"))}},
		&lkal.Record{Fields: []lkal.Field{
			fld("image", lkal.String("nginx:1")),
			fld("replicas", lkal.Int(3)),
			fld("plugin", rec(
				fld("a", rec(fld("order", lkal.Int(10)), fld("enabled", lkal.Bool(true)))),
			)),
		}},
	)
	st, err := ownership.Create(alice, allScope, rawCreate)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	st = ownership.MarkCompletedDefaults(st, rawCreate, completedCreate)
	if e, ok := st.Get(ownership.Path{"spec", "replicas"}); !ok || e.State != ownership.StateDefaulted {
		t.Fatalf("spec/replicas entry = (%+v, %v), want defaulted", e, ok)
	}

	// Bob can claim .defaulted spec/replicas with a different value (no K206!) -> OriginCreated.
	bobDecl := objDoc("web", nil, &lkal.Record{Fields: []lkal.Field{fld("replicas", lkal.Int(5))}})
	resBob, err := ownership.PlanApply(ownership.PlanInput{
		Object:          testObj,
		Actor:           bob,
		Scope:           allScope,
		CurrentDoc:      completedCreate,
		CurrentState:    st,
		CurrentOwnerRev: 1,
		Declaration:     bobDecl,
	})
	if err != nil || resBob.Blocked {
		t.Fatalf("Bob apply defaulted: err=%v blocked=%v resErr=%v", err, resBob.Blocked, resBob.Err())
	}
	if !resBob.EnvChanged || !resBob.OwnChanged {
		t.Fatalf("Bob apply defaulted expected EnvChanged & OwnChanged, got env=%v own=%v", resBob.EnvChanged, resBob.OwnChanged)
	}
	eRep, _ := resBob.NextState.Get(ownership.Path{"spec", "replicas"})
	if eRep.State != ownership.StateOwned || eRep.Owner != bob || eRep.Origin != ownership.OriginCreated {
		t.Fatalf("spec/replicas after Bob claim = %+v, want (bob, created)", eRep)
	}
	// Alice's leaves untouched!
	eImg, _ := resBob.NextState.Get(ownership.Path{"spec", "image"})
	if eImg.Owner != alice || eImg.Origin != ownership.OriginCreated {
		t.Fatalf("Alice spec/image disturbed: %+v", eImg)
	}

	// Bob declaring Alice's spec/image triggers K201 and suppresses Bob's other changes (All-or-Nothing).
	bobConflictDecl := objDoc("web", nil, &lkal.Record{Fields: []lkal.Field{
		fld("replicas", lkal.Int(6)),
		fld("image", lkal.String("nginx:2")),
	}})
	resConflict, err := ownership.PlanApply(ownership.PlanInput{
		Object:          testObj,
		Actor:           bob,
		Scope:           allScope,
		CurrentDoc:      resBob.NextDoc,
		CurrentState:    resBob.NextState,
		CurrentOwnerRev: 2,
		Declaration:     bobConflictDecl,
	})
	if err != nil || !resConflict.Blocked || !errors.Is(resConflict.Err(), ownership.ErrOwnershipConflict) {
		t.Fatalf("expected K201 blocked, got err=%v blocked=%v resErr=%v", err, resConflict.Blocked, resConflict.Err())
	}
	if !lkal.Equal(resConflict.NextDoc, resBob.NextDoc) || !resConflict.NextState.Equal(resBob.NextState) {
		t.Fatal("blocked plan mutated NextDoc or NextState")
	}

	// Alice retires spec/plugin/a/order and meta/labels/env: Apply emits K604 and does NOT delete!
	aliceApplyRetire := objDoc("web", nil, &lkal.Record{Fields: []lkal.Field{
		fld("image", lkal.String("nginx:1")),
	}})
	resApplyRetire, err := ownership.PlanApply(ownership.PlanInput{
		Object:          testObj,
		Actor:           alice,
		Scope:           allScope,
		CurrentDoc:      resBob.NextDoc,
		CurrentState:    resBob.NextState,
		CurrentOwnerRev: 2,
		Declaration:     aliceApplyRetire,
	})
	if err != nil || resApplyRetire.Blocked || resApplyRetire.EnvChanged || resApplyRetire.OwnChanged {
		t.Fatalf("Apply with retired created should be no-op + K604, got err=%v blocked=%v env=%v own=%v",
			err, resApplyRetire.Blocked, resApplyRetire.EnvChanged, resApplyRetire.OwnChanged)
	}
	hasK604 := false
	for _, d := range resApplyRetire.Diagnostics {
		if d.Code == ownership.CodePruneRequired {
			hasK604 = true
		}
	}
	if !hasK604 {
		t.Fatalf("expected K604 diagnostic on Apply, got %+v", resApplyRetire.Diagnostics)
	}

	// Alice runs Prune: deletes spec/plugin/a/order (and bottom-up removes defaulted-only spec/plugin/a & spec/plugin!)
	// and deletes meta/labels/env (removing emptied meta/labels!). Bob's spec/replicas survives!
	resPrune, err := ownership.PlanPrune(ownership.PlanInput{
		Object:          testObj,
		Actor:           alice,
		Scope:           allScope,
		CurrentDoc:      resBob.NextDoc,
		CurrentState:    resBob.NextState,
		CurrentOwnerRev: 2,
		Declaration:     aliceApplyRetire,
	})
	if err != nil || resPrune.Blocked || !resPrune.EnvChanged || !resPrune.OwnChanged {
		t.Fatalf("Prune failed: err=%v blocked=%v resErr=%v env=%v own=%v", err, resPrune.Blocked, resPrune.Err(), resPrune.EnvChanged, resPrune.OwnChanged)
	}
	obsPlugin, _ := ownership.InspectDocPath(resPrune.NextDoc, ownership.Path{"spec", "plugin"})
	if obsPlugin.Kind != ownership.ObsAbsent {
		t.Fatalf("spec/plugin not bottom-up pruned: %+v", obsPlugin)
	}
	obsLabels, _ := ownership.InspectDocPath(resPrune.NextDoc, ownership.Path{"meta", "labels"})
	if obsLabels.Kind != ownership.ObsAbsent {
		t.Fatalf("meta/labels not removed when emptied: %+v", obsLabels)
	}
	obsRep, _ := ownership.InspectDocPath(resPrune.NextDoc, ownership.Path{"spec", "replicas"})
	if obsRep.Kind != ownership.ObsLeaf || !lkal.Equal(obsRep.Value, lkal.Int(5)) {
		t.Fatalf("Bob's spec/replicas damaged by Alice prune: %+v", obsRep)
	}
	if _, stillHasDef := resPrune.NextState.Get(ownership.Path{"spec", "plugin", "a", "enabled"}); stillHasDef {
		t.Fatal("orphaned defaulted entry under pruned intermediate record was not removed")
	}
}

func TestExplainAdoptReleaseAndUnownedK206(t *testing.T) {
	alice := ownership.ActorID("User:alice")
	bob := ownership.ActorID("User:bob")
	allScope := ownership.RootScope(ownership.PermAll)
	noResolveScope := ownership.RootScope(ownership.PermRegular)

	doc := objDoc("web", nil, &lkal.Record{Fields: []lkal.Field{
		fld("image", lkal.String("nginx:1")),
		fld("replicas", lkal.Int(2)),
	}})
	st, err := ownership.Create(alice, allScope, doc)
	if err != nil {
		t.Fatal(err)
	}

	exp1, err := ownership.Explain(testObj, doc, st, 5, 5)
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if !strings.HasPrefix(exp1.Expect, "ksg-doc-v1:") {
		t.Fatalf("doc expect token = %q", exp1.Expect)
	}
	var repTok string
	for _, lf := range exp1.Leaves {
		if lf.Path.Equal(ownership.Path{"spec", "replicas"}) {
			repTok = lf.Expect
		}
	}
	if !strings.HasPrefix(repTok, "ksg-tok-v1:") {
		t.Fatalf("path expect token = %q", repTok)
	}

	// Release without PermResolve fails with K401.
	_, err = ownership.Release(ownership.ResolveInput{
		Object: testObj, Actor: alice, Scope: noResolveScope,
		CurrentDoc: doc, CurrentState: st, CurrentOwnerRev: 5,
		Path: ownership.Path{"spec", "replicas"}, Expect: repTok, ExpectedOwnerRev: 5,
	})
	if !errors.Is(err, ownership.ErrPolicyDenied) {
		t.Fatalf("Release without Resolve = %v, want K401", err)
	}

	// Release with wrong ownerRev fails with K201; wrong expect fails with K203.
	_, err = ownership.Release(ownership.ResolveInput{
		Object: testObj, Actor: alice, Scope: allScope,
		CurrentDoc: doc, CurrentState: st, CurrentOwnerRev: 5,
		Path: ownership.Path{"spec", "replicas"}, Expect: repTok, ExpectedOwnerRev: 4,
	})
	if !errors.Is(err, ownership.ErrOwnershipConflict) {
		t.Fatalf("Release stale ownerRev = %v, want K201", err)
	}
	_, err = ownership.Release(ownership.ResolveInput{
		Object: testObj, Actor: alice, Scope: allScope,
		CurrentDoc: doc, CurrentState: st, CurrentOwnerRev: 5,
		Path: ownership.Path{"spec", "replicas"}, Expect: "ksg-tok-v1:0000", ExpectedOwnerRev: 5,
	})
	if !errors.Is(err, ownership.ErrExternalDrift) {
		t.Fatalf("Release bad expect = %v, want K203", err)
	}

	// Alice releases spec/replicas -> becomes StateUnowned, emits K602.
	relRes, err := ownership.Release(ownership.ResolveInput{
		Object: testObj, Actor: alice, Scope: allScope,
		CurrentDoc: doc, CurrentState: st, CurrentOwnerRev: 5,
		Path: ownership.Path{"spec", "replicas"}, Expect: repTok, ExpectedOwnerRev: 5,
	})
	if err != nil || !relRes.OwnChanged {
		t.Fatalf("Release: err=%v changed=%v", err, relRes.OwnChanged)
	}
	if len(relRes.Diagnostics) != 1 || relRes.Diagnostics[0].Code != ownership.CodeExplicitDeletionRightWaived {
		t.Fatalf("Release diags = %+v, want K602", relRes.Diagnostics)
	}
	if e, _ := relRes.NextState.Get(ownership.Path{"spec", "replicas"}); e.State != ownership.StateUnowned {
		t.Fatalf("after release state = %+v, want unowned", e)
	}

	// Bob applying a DIFFERENT value to unowned spec/replicas fails with K206!
	resK206, err := ownership.PlanApply(ownership.PlanInput{
		Object: testObj, Actor: bob, Scope: allScope,
		CurrentDoc: doc, CurrentState: relRes.NextState, CurrentOwnerRev: 6,
		Declaration: objDoc("web", nil, &lkal.Record{Fields: []lkal.Field{fld("replicas", lkal.Int(4))}}),
	})
	if err != nil || !resK206.Blocked || !errors.Is(resK206.Err(), ownership.ErrUnownedValueConflict) {
		t.Fatalf("unowned mismatch expected K206, got err=%v resErr=%v", err, resK206.Err())
	}

	// Bob explicitly adopts spec/replicas -> becomes (bob, OriginModified), o/ unchanged.
	adoptRes, err := ownership.Adopt(ownership.ResolveInput{
		Object: testObj, Actor: bob, Scope: allScope,
		CurrentDoc: doc, CurrentState: relRes.NextState, CurrentOwnerRev: 6,
		Path: ownership.Path{"spec", "replicas"}, Expect: repTok, ExpectedOwnerRev: 6,
	})
	if err != nil || !adoptRes.OwnChanged {
		t.Fatalf("Adopt: err=%v changed=%v", err, adoptRes.OwnChanged)
	}
	eMod, _ := adoptRes.NextState.Get(ownership.Path{"spec", "replicas"})
	if eMod.State != ownership.StateOwned || eMod.Owner != bob || eMod.Origin != ownership.OriginModified {
		t.Fatalf("after adopt = %+v, want (bob, modified)", eMod)
	}

	// Bob retires spec/replicas: Prune must NOT delete OriginModified; Apply unmanages it back to StateUnowned!
	pruneMod, err := ownership.PlanPrune(ownership.PlanInput{
		Object: testObj, Actor: bob, Scope: allScope,
		CurrentDoc: doc, CurrentState: adoptRes.NextState, CurrentOwnerRev: 7,
		Declaration: objDoc("web", nil, &lkal.Record{}),
	})
	if err != nil || pruneMod.Blocked || pruneMod.EnvChanged || pruneMod.OwnChanged {
		t.Fatalf("Prune must never delete OriginModified: err=%v blocked=%v env=%v own=%v", err, pruneMod.Blocked, pruneMod.EnvChanged, pruneMod.OwnChanged)
	}
	unmanageRes, err := ownership.PlanApply(ownership.PlanInput{
		Object: testObj, Actor: bob, Scope: allScope,
		CurrentDoc: doc, CurrentState: adoptRes.NextState, CurrentOwnerRev: 7,
		Declaration: objDoc("web", nil, &lkal.Record{}),
	})
	if err != nil || unmanageRes.Blocked || unmanageRes.EnvChanged || !unmanageRes.OwnChanged {
		t.Fatalf("Apply should unmanage retired OriginModified: err=%v blocked=%v env=%v own=%v", err, unmanageRes.Blocked, unmanageRes.EnvChanged, unmanageRes.OwnChanged)
	}
	if e, _ := unmanageRes.NextState.Get(ownership.Path{"spec", "replicas"}); e.State != ownership.StateUnowned {
		t.Fatalf("after unmanage = %+v, want unowned", e)
	}
}

func TestMigrationK204AndDeferredReleaseK603(t *testing.T) {
	alice := ownership.ActorID("User:alice")
	allScope := ownership.RootScope(ownership.PermAll)

	// Retired self-Created at spec/a overlaps declared spec/a/b -> K204 blocker in BOTH Apply and Prune.
	doc := objDoc("web", nil, &lkal.Record{Fields: []lkal.Field{fld("a", lkal.String("v"))}})
	stCreated, _ := ownership.Create(alice, allScope, doc)
	declNested := objDoc("web", nil, &lkal.Record{Fields: []lkal.Field{fld("a", rec(fld("b", lkal.Int(1))))}})

	for _, mode := range []string{"apply", "prune"} {
		in := ownership.PlanInput{
			Object: testObj, Actor: alice, Scope: allScope,
			CurrentDoc: doc, CurrentState: stCreated, CurrentOwnerRev: 1, Declaration: declNested,
		}
		var res ownership.PlanResult
		var err error
		if mode == "apply" {
			res, err = ownership.PlanApply(in)
		} else {
			res, err = ownership.PlanPrune(in)
		}
		if err != nil || !res.Blocked || !errors.Is(res.Err(), ownership.ErrMigrationRequired) {
			t.Fatalf("%s expected K204 MigrationRequired, got err=%v resErr=%v", mode, err, res.Err())
		}
	}

	// Retired self-Modified at spec/a overlaps declared spec/a/b -> pass 1 unmanages spec/a and emits K603 (not blocked!).
	stMod := ownership.NewState(alice)
	stMod.Set(ownership.Path{"spec", "a"}, ownership.Entry{State: ownership.StateOwned, Owner: alice, Origin: ownership.OriginModified})
	resMod, err := ownership.PlanApply(ownership.PlanInput{
		Object: testObj, Actor: alice, Scope: allScope,
		CurrentDoc: doc, CurrentState: stMod, CurrentOwnerRev: 1, Declaration: declNested,
	})
	if err != nil || resMod.Blocked || resMod.EnvChanged || !resMod.OwnChanged {
		t.Fatalf("retired modified overlap pass 1: err=%v blocked=%v env=%v own=%v", err, resMod.Blocked, resMod.EnvChanged, resMod.OwnChanged)
	}
	hasK603 := false
	for _, d := range resMod.Diagnostics {
		if d.Code == ownership.CodeDeferredUntilRelease {
			hasK603 = true
		}
	}
	if !hasK603 {
		t.Fatalf("expected K603 DeferredUntilRelease, got %+v", resMod.Diagnostics)
	}
	if e, _ := resMod.NextState.Get(ownership.Path{"spec", "a"}); e.State != ownership.StateUnowned {
		t.Fatalf("spec/a should be unmanaged on pass 1, got %+v", e)
	}
}

func TestCompactTrieCodecRoundtripCompactionAndSizeBudget(t *testing.T) {
	alice := ownership.ActorID("User:alice")
	hpa := ownership.ActorID("User:hpa")
	mesh := ownership.ActorID("User:mesh")
	retired := ownership.ActorID("User:retired")

	// Representative multi-actor stored workload envelope (~1,350 bytes object body).
	workload := rec(
		fld("meta", rec(
			fld("kind", lkal.String("workload")),
			fld("name", lkal.String("payments-api")),
			fld("labels", rec(
				fld("app.example.com/name", lkal.String("payments-api")),
				fld("app.example.com/component", lkal.String("backend")),
				fld("app.example.com/tier", lkal.String("critical")),
				fld("env", lkal.String("production")),
			)),
			fld("uid", lkal.String("01234567-89ab-4def-8123-456789abcdef")),
			fld("generation", lkal.Int(3)),
		)),
		fld("spec", rec(
			fld("replicas", lkal.Int(6)),
			fld("serviceAccount", lkal.String("payments-api-sa")),
			fld("image", lkal.String("registry.internal.example.com/payments/api:v2.14.3-sha256-abcdef123456")),
			fld("command", lkal.List{lkal.String("/usr/local/bin/payments-api"), lkal.String("--listen=:8443"), lkal.String("--metrics=:9090")}),
			fld("resources", rec(
				fld("cpuRequest", lkal.Quantity{Number: "500", Unit: "m"}),
				fld("cpuLimit", lkal.Quantity{Number: "2000", Unit: "m"}),
				fld("memoryRequest", lkal.Quantity{Number: "512", Unit: "Mi"}),
				fld("memoryLimit", lkal.Quantity{Number: "2048", Unit: "Mi"}),
			)),
			fld("probe", rec(
				fld("path", lkal.String("/healthz")),
				fld("port", lkal.Int(8443)),
				fld("timeoutSeconds", lkal.Int(5)),
				fld("periodSeconds", lkal.Int(10)),
			)),
			fld("env", rec(
				fld("LOG_LEVEL", lkal.String("info")),
				fld("REGION", lkal.String("ap-northeast-1")),
				fld("CLUSTER", lkal.String("prod-tokyo-01")),
				fld("OTEL_EXPORTER_ENDPOINT", lkal.String("https://otel.observability.svc.cluster.local:4317")),
			)),
			fld("mesh", rec(
				fld("enabled", lkal.Bool(true)),
				fld("mode", lkal.Enum("strict")),
				fld("mtlsPort", lkal.Int(15001)),
				fld("proxyImage", lkal.String("registry.internal.example.com/mesh/proxy:v1.24.0")),
			)),
			fld("note", lkal.String("released-field")),
			fld("creatorMod", lkal.String("downgraded-to-modified")),
		)),
		fld("status", rec(
			fld("observedGeneration", lkal.Int(3)),
			fld("readyReplicas", lkal.Int(6)),
			fld("condition", rec(
				fld("Available", rec(
					fld("state", lkal.Enum("true")),
					fld("reason", lkal.String("MinimumReplicasAvailable")),
					fld("since", lkal.String("2026-11-23T00:00:00Z")),
				)),
				fld("Progressing", rec(
					fld("state", lkal.Enum("true")),
					fld("reason", lkal.String("NewReplicaSetAvailable")),
					fld("since", lkal.String("2026-11-23T00:00:00Z")),
				)),
			)),
		)),
	)

	// 1. Single-owner workload: compact to ~38 bytes.
	stSingle := ownership.NewState(alice)
	leaves, err := ownership.ProjectEntranceSpec(workload)
	if err != nil {
		t.Fatal(err)
	}
	for _, lf := range leaves {
		stSingle.Set(lf.Path, ownership.Entry{State: ownership.StateOwned, Owner: alice, Origin: ownership.OriginCreated})
	}
	encSingle, err := ownership.Encode(stSingle, workload)
	if err != nil {
		t.Fatalf("Encode(single): %v", err)
	}
	if bytes.ContainsRune(encSingle, '\n') {
		t.Fatalf("Encode emitted newline: %s", encSingle)
	}
	if string(encSingle) != `{"$lkal":"0.1","actors":["User:alice"]}` {
		t.Fatalf("single-owner encoding = %s, want minimal actors-only payload", encSingle)
	}
	decSingle, err := ownership.Decode(encSingle, workload)
	if err != nil || !decSingle.Equal(stSingle) {
		t.Fatalf("single-owner roundtrip mismatch: err=%v", err)
	}

	// 2. Multi-actor workload exercising:
	//    - default creator (alice, created) omitted
	//    - creator downgraded to modified (alice, modified) preserved explicitly (no escalation!)
	//    - HPA owning spec/replicas (modified)
	//    - mesh owning entire spec/mesh/** subtree -> compressed with w = true!
	//    - released .unowned leaf (spec/note) preserved explicitly
	//    - .defaulted leaf (spec/probe/timeoutSeconds) preserved explicitly
	stMulti := stSingle.Clone()
	stMulti.Set(ownership.Path{"spec", "replicas"}, ownership.Entry{State: ownership.StateOwned, Owner: hpa, Origin: ownership.OriginModified})
	for _, k := range []string{"enabled", "mode", "mtlsPort", "proxyImage"} {
		stMulti.Set(ownership.Path{"spec", "mesh", k}, ownership.Entry{State: ownership.StateOwned, Owner: mesh, Origin: ownership.OriginCreated})
	}
	stMulti.Set(ownership.Path{"spec", "note"}, ownership.Entry{State: ownership.StateUnowned})
	stMulti.Set(ownership.Path{"spec", "probe", "timeoutSeconds"}, ownership.Entry{State: ownership.StateDefaulted})
	stMulti.Set(ownership.Path{"spec", "creatorMod"}, ownership.Entry{State: ownership.StateOwned, Owner: alice, Origin: ownership.OriginModified})
	// Briefly add & remove retired actor to confirm unused actors[1..] compaction.
	stMulti.Set(ownership.Path{"spec", "tmp"}, ownership.Entry{State: ownership.StateOwned, Owner: retired, Origin: ownership.OriginCreated})
	stMulti.Delete(ownership.Path{"spec", "tmp"})

	encMulti, err := ownership.Encode(stMulti, workload)
	if err != nil {
		t.Fatalf("Encode(multi): %v", err)
	}
	if bytes.Contains(encMulti, []byte("User:retired")) {
		t.Fatalf("unused foreign actor not compacted: %s", encMulti)
	}
	if !bytes.Contains(encMulti, []byte(`"w":true`)) {
		t.Fatalf("uniform foreign subtree not compressed with w=true: %s", encMulti)
	}
	decMulti, err := ownership.Decode(encMulti, workload)
	if err != nil {
		t.Fatalf("Decode(multi): %v", err)
	}
	if !decMulti.Equal(stMulti) {
		t.Fatalf("multi-actor roundtrip mismatch:\ngot:  %+v\nwant: %+v", decMulti, stMulti)
	}
	// Verify creator's modified leaf did NOT escalate to created.
	if e, _ := decMulti.Get(ownership.Path{"spec", "creatorMod"}); e.Owner != alice || e.Origin != ownership.OriginModified {
		t.Fatalf("creator modified leaf escalated: %+v", e)
	}

	// Verify <= 20% size budget on representative multi-actor workload (creator + HPA + mesh subtree).
	stBudget := stSingle.Clone()
	stBudget.Set(ownership.Path{"spec", "replicas"}, ownership.Entry{State: ownership.StateOwned, Owner: hpa, Origin: ownership.OriginModified})
	for _, k := range []string{"enabled", "mode", "mtlsPort", "proxyImage"} {
		stBudget.Set(ownership.Path{"spec", "mesh", k}, ownership.Entry{State: ownership.StateOwned, Owner: mesh, Origin: ownership.OriginCreated})
	}
	encBudget, err := ownership.Encode(stBudget, workload)
	if err != nil {
		t.Fatalf("Encode(budget): %v", err)
	}
	objBytes, err := resource.Encode(workload)
	if err != nil {
		t.Fatalf("resource.Encode: %v", err)
	}
	ratio := float64(len(encBudget)) / float64(len(objBytes))
	if ratio > 0.20 {
		t.Fatalf("ownership trie size %d B exceeds 20%% of workload %d B (ratio=%.3f): %s",
			len(encBudget), len(objBytes), ratio, encBudget)
	}
}

func TestImperativeCheckUpdateAndCheckPatch(t *testing.T) {
	alice := ownership.ActorID("User:alice")
	bob := ownership.ActorID("User:bob")
	allScope := ownership.RootScope(ownership.PermRegular)

	oldDoc := objDoc("web", nil, &lkal.Record{Fields: []lkal.Field{
		fld("a", lkal.Int(1)),
		fld("b", lkal.Int(2)),
		fld("def", lkal.Int(3)),
		fld("un", lkal.Int(4)),
	}})
	st := ownership.NewState(alice)
	st.Set(ownership.Path{"spec", "a"}, ownership.Entry{State: ownership.StateOwned, Owner: alice, Origin: ownership.OriginCreated})
	st.Set(ownership.Path{"spec", "b"}, ownership.Entry{State: ownership.StateOwned, Owner: bob, Origin: ownership.OriginCreated})
	st.Set(ownership.Path{"spec", "def"}, ownership.Entry{State: ownership.StateDefaulted})
	st.Set(ownership.Path{"spec", "un"}, ownership.Entry{State: ownership.StateUnowned})

	// Echoing unchanged doc preserves all states without error.
	stEcho, err := ownership.CheckUpdate(bob, allScope, oldDoc, st, oldDoc)
	if err != nil || !stEcho.Equal(st) {
		t.Fatalf("echo CheckUpdate = (%+v, %v), want unchanged", stEcho, err)
	}

	// Bob modifying Alice's leaf -> K201.
	badForeign := objDoc("web", nil, &lkal.Record{Fields: []lkal.Field{
		fld("a", lkal.Int(99)), fld("b", lkal.Int(2)), fld("def", lkal.Int(3)), fld("un", lkal.Int(4)),
	}})
	if _, err := ownership.CheckPatch(bob, allScope, oldDoc, st, badForeign); !errors.Is(err, ownership.ErrOwnershipConflict) {
		t.Fatalf("modify foreign = %v, want K201", err)
	}

	// Bob modifying .unowned leaf -> K206.
	badUnowned := objDoc("web", nil, &lkal.Record{Fields: []lkal.Field{
		fld("a", lkal.Int(1)), fld("b", lkal.Int(2)), fld("def", lkal.Int(3)), fld("un", lkal.Int(99)),
	}})
	if _, err := ownership.CheckPatch(bob, allScope, oldDoc, st, badUnowned); !errors.Is(err, ownership.ErrUnownedValueConflict) {
		t.Fatalf("modify unowned = %v, want K206", err)
	}

	// Bob modifying .defaulted leaf -> claimed as (bob, OriginCreated) without K206!
	claimDef := objDoc("web", nil, &lkal.Record{Fields: []lkal.Field{
		fld("a", lkal.Int(1)), fld("b", lkal.Int(2)), fld("def", lkal.Int(10)), fld("un", lkal.Int(4)),
	}})
	stClaimed, err := ownership.CheckPatch(bob, allScope, oldDoc, st, claimDef)
	if err != nil {
		t.Fatalf("claim defaulted = %v", err)
	}
	if e, _ := stClaimed.Get(ownership.Path{"spec", "def"}); e.Owner != bob || e.Origin != ownership.OriginCreated {
		t.Fatalf("claimed defaulted = %+v, want (bob, created)", e)
	}
}

func TestCheckImperativeRetiredSelfOwnedAndCorruptTrieDecode(t *testing.T) {
	alice := ownership.ActorID("User:alice")
	allScope := ownership.RootScope(ownership.PermRegular)

	// 1. Re-adding an exact retired self-owned Modified leaf preserves OriginModified (no laundering to Created).
	emptyDoc := objDoc("web", nil, &lkal.Record{})
	stRetMod := ownership.NewState(alice)
	stRetMod.Set(ownership.Path{"spec", "x"}, ownership.Entry{State: ownership.StateOwned, Owner: alice, Origin: ownership.OriginModified})
	addExact := objDoc("web", nil, &lkal.Record{Fields: []lkal.Field{fld("x", lkal.Int(2))}})
	stReAdded, err := ownership.CheckUpdate(alice, allScope, emptyDoc, stRetMod, addExact)
	if err != nil {
		t.Fatalf("CheckUpdate re-add exact retired modified: %v", err)
	}
	if e, _ := stReAdded.Get(ownership.Path{"spec", "x"}); e.State != ownership.StateOwned || e.Owner != alice || e.Origin != ownership.OriginModified {
		t.Fatalf("exact retired modified became %+v, want (alice, modified)", e)
	}

	// 2. Adding a child under retired self-owned Modified ancestor fails with K201 (preserves non-overlap).
	addChild := objDoc("web", nil, &lkal.Record{Fields: []lkal.Field{fld("x", rec(fld("y", lkal.Int(2))))}})
	if _, err := ownership.CheckUpdate(alice, allScope, emptyDoc, stRetMod, addChild); !errors.Is(err, ownership.ErrOwnershipConflict) {
		t.Fatalf("CheckUpdate child under retired modified = %v, want K201", err)
	}

	// 3. Adding a child under retired self-owned Created ancestor fails with K204 MigrationRequired.
	stRetCreated := ownership.NewState(alice)
	stRetCreated.Set(ownership.Path{"spec", "x"}, ownership.Entry{State: ownership.StateOwned, Owner: alice, Origin: ownership.OriginCreated})
	if _, err := ownership.CheckPatch(alice, allScope, emptyDoc, stRetCreated, addChild); !errors.Is(err, ownership.ErrMigrationRequired) {
		t.Fatalf("CheckPatch child under retired created = %v, want K204", err)
	}

	// 4. Decode rejects w=true combined with children c, empty c, and overlapping StateOwned entries with K101.
	docNested := objDoc("web", nil, &lkal.Record{Fields: []lkal.Field{
		fld("x", rec(fld("y", lkal.Int(1)))),
	}})
	corruptCases := []struct {
		name string
		raw  string
	}{
		{
			name: "whole_with_children",
			raw:  `{"$lkal":"0.1","actors":["User:alice","User:bob"],"trie":{"spec":{"s":{"$enum":"owned"},"a":1,"o":{"$enum":"created"},"w":true,"c":{"x":{"s":{"$enum":"owned"},"a":0,"o":{"$enum":"created"}}}}}}`,
		},
		{
			name: "empty_children_map",
			raw:  `{"$lkal":"0.1","actors":["User:alice"],"trie":{"spec":{"s":{"$enum":"owned"},"a":0,"o":{"$enum":"modified"},"c":{}}}}`,
		},
		{
			name: "overlapping_owned_ancestor_and_doc_leaf",
			raw:  `{"$lkal":"0.1","actors":["User:alice","User:bob"],"trie":{"spec":{"c":{"x":{"s":{"$enum":"owned"},"a":1,"o":{"$enum":"created"}}}}}}`,
		},
	}
	for _, tc := range corruptCases {
		if _, err := ownership.Decode([]byte(tc.raw), docNested); !errors.Is(err, ownership.ErrInvalidInput) {
			t.Fatalf("Decode(%s) = %v, want K101", tc.name, err)
		}
	}
}
