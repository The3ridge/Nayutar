import Nayutar.Ownership

set_option autoImplicit false

namespace Nayutar.Ownership

open Nayutar.Envelope (Value Fields)
open Nayutar.Store (Store Rev)
open Kisage (Origin PermSet Mode permDeleteOwn permOwnResolve)

theorem plan_scope_and_frame
    (inp : LeafInput) (mode : Mode)
    (hWork : (planLeaf inp mode).docOp.isSome = true ∨
             (planLeaf inp mode).ownerOp.isSome = true) :
    (planLeaf inp mode).decision = .authorized ∧
    PermSet.allows inp.granted (planLeaf inp mode).requiredPerms = true ∧
    (planLeaf inp mode).inMA = true := by
  sorry

theorem plan_no_structure_destruction
    (inp : LeafInput) (mode : Mode)
    (hProt : isProtectedStructure inp.obs inp.anc = true) :
    (planLeaf inp mode).docOp = none := by
  sorry

theorem prune_conservative_deletion
    (inp : LeafInput) (mode : Mode) (p : Path)
    (hDel : (planLeaf inp mode).docOp = some (.delete p)) :
    mode = .prune ∧
    inp.declared = none ∧
    PermSet.allows inp.granted permDeleteOwn = true ∧
    inp.status = some (.owned inp.actor .created) ∧
    inp.anc = .clear ∧
    p = inp.path ∧
    ∃ actual, inp.obs = .leaf actual ∧
      (∀ la, inp.lastApplied = some la → Value.eqv actual la = true) := by
  sorry

theorem plan_no_silent_overwrite (inp : LeafInput) (mode : Mode) :
    ((∃ m o, inp.status = some (.owned m o) ∧ m ≠ inp.actor) ∨
     (inp.declared.isSome = true ∧ inp.hasForeignOverlap = true) →
       (planLeaf inp mode).docOp = none ∧ (planLeaf inp mode).ownerOp = none) ∧
    ((∃ o actual la, inp.status = some (.owned inp.actor o) ∧ inp.obs = .leaf actual ∧
       inp.lastApplied = some la ∧ Value.eqv actual la = false) →
       (planLeaf inp mode).docOp = none ∧
       ((inp.declared.isSome = true ∨ inp.status = some (.owned inp.actor .created)) →
         (planLeaf inp mode).ownerOp = none)) ∧
    ((∃ desired o, inp.declared = some desired ∧ inp.status = some (.owned inp.actor o) ∧
       inp.obs = .absent) →
       (planLeaf inp mode).docOp = none ∧ (planLeaf inp mode).ownerOp = none) ∧
    ((∃ desired actual, inp.declared = some desired ∧
       (inp.status = none ∨ inp.status = some .unowned) ∧
       inp.obs = .leaf actual ∧ Value.eqv actual desired = false) →
       (planLeaf inp mode).docOp = none ∧ (planLeaf inp mode).ownerOp = none) := by
  sorry

theorem apply_fixed_point
    (path : Path) (actor : ActorID) (val actual : Value) (orig : Origin)
    (granted : PermSet) (mode : Mode)
    (hEq : Value.eqv actual val = true) :
    let inp : LeafInput := {
      path := path
      actor := actor
      declared := some val
      obs := .leaf actual
      anc := .clear
      status := some (.owned actor orig)
      lastApplied := none
      hasForeignOverlap := false
      selfOverlap := .none
      granted := granted
    }
    (planLeaf inp mode).decision = .noOp ∧
    (planLeaf inp mode).docOp = none ∧
    (planLeaf inp mode).ownerOp = none := by
  sorry

theorem plan_deterministic
    (doc₁ doc₂ decl₁ decl₂ : Value) (ow₁ ow₂ : OwnerState)
    (a₁ a₂ : ActorID) (s₁ s₂ : Scope) (m₁ m₂ : Mode)
    (e₁ e₂ r₁ r₂ : Bool)
    (hDoc : doc₁ = doc₂) (hOw : ow₁ = ow₂) (hDecl : decl₁ = decl₂)
    (hA : a₁ = a₂) (hS : s₁ = s₂) (hM : m₁ = m₂) (hE : e₁ = e₂) (hR : r₁ = r₂) :
    planMulti doc₁ ow₁ decl₁ a₁ s₁ m₁ e₁ r₁ = planMulti doc₂ ow₂ decl₂ a₂ s₂ m₂ e₂ r₂ := by
  sorry

theorem plan_all_or_nothing
    (plans : List LeafPlan) (expectMatch ownerRevMatch : Bool)
    (hBlock : ownerRevMatch = false ∨ expectMatch = false ∨
      ∃ p ∈ plans, p.decision = .blocker ∨ p.decision = .policyDenied) :
    let g := gatePlan plans expectMatch ownerRevMatch
    g.blocked = true ∧ g.docOps = [] ∧ g.ownerOps = [] := by
  sorry

theorem resolve_safety (req : ResolveRequest) :
    let res := planResolve req
    res.nextDoc = req.doc ∧
    (res.accepted = false → res.nextOwner = req.owner) ∧
    (res.accepted = true →
      req.scope.allows req.path permOwnResolve = true ∧
      req.expectMatch = true ∧
      req.ownerRevMatch = true ∧
      (∀ m o, req.owner.get req.path = some (.owned m o) → m = req.actor) ∧
      (req.kind = .adopt → res.nextOwner.get req.path = some (.owned req.actor .modified)) ∧
      (req.kind = .release → ∀ m o, res.nextOwner.get req.path ≠ some (.owned m o))) := by
  sorry

theorem apply_never_deletes_leaves (inp : LeafInput) (p : Path) :
    (planLeaf inp .apply).docOp ≠ some (.delete p) := by
  sorry

theorem planMulti_foreign_frame
    (doc declDoc : Value) (ow : OwnerState) (actor : ActorID) (scope : Scope)
    (mode : Mode) (expectMatch ownerRevMatch : Bool) (out : PlanOutput)
    (hPlan : planMulti doc ow declDoc actor scope mode expectMatch ownerRevMatch = .ok out)
    (p : Path) (m : ActorID) (orig : Origin)
    (hForeign : ow.get p = some (.owned m orig)) (hNe : m ≠ actor) :
    out.nextOwner.get p = some (.owned m orig) ∧
    ∀ op ∈ out.docOps, op.path ≠ p := by
  sorry

theorem trie_encode_decode_roundtrip
    (creator : ActorID) (leaves : List (Path × LeafStatus))
    (hCreator : validActor creator = true)
    (hWF : TrieLeavesWF leaves) :
    ∀ p ls, (p, ls) ∈ leaves →
      decodeLeaf (encodeTrie creator leaves) p = .ok ls := by
  sorry

theorem cas_init_invariant : CASInvariant CASState.empty := by
  sorry

theorem cas_step_preserves_invariant (st st' : CASState) (act : CASAction) (out : CASOutcome)
    (hInv : CASInvariant st) (hStep : stepCAS st act = (out, st')) :
    CASInvariant st' := by
  sorry

theorem cas_trace_invariant (st : CASState) (acts : List CASAction)
    (hInv : CASInvariant st) :
    CASInvariant (runCAS st acts) := by
  sorry

theorem cas_conflict_unchanged (st st' : CASState) (act : CASAction)
    (hStep : stepCAS st act = (.casConflict, st')) :
    st' = st := by
  sorry

theorem cas_atomic_pair (st st' : CASState) (act : CASAction) (out : CASOutcome)
    (hInv : CASInvariant st) (hStep : stepCAS st act = (out, st')) :
    StorePairingWF st' ∧
    (∀ addr actor scope path oRev ownRev exp,
      (act = .adopt addr actor scope path oRev ownRev exp ∨
       act = .release addr actor scope path oRev ownRev exp) →
      st'.store.modRev addr.oKey = st.store.modRev addr.oKey ∧
      ∀ rec rec', st.objects addr = some rec → st'.objects addr = some rec' →
        rec'.doc = rec.doc) := by
  sorry

end Nayutar.Ownership
