import Nayutar.Patch

set_option autoImplicit false

namespace Nayutar.Patch

open Nayutar.Envelope (Value Fields optEqv)

theorem apply_empty (doc : Fields) (h : (Value.record doc).unique = true) :
    apply doc [] = .ok doc := by
  sorry

theorem apply_preserves_unique (doc : Fields) (ops : List Op) (res : Fields)
    (h : apply doc ops = .ok res) :
    (Value.record res).unique = true := by
  sorry

theorem apply_append (doc : Fields) (ops₁ ops₂ : List Op)
    (h₁ : opsOk ops₁ = true) (h₂ : opsOk ops₂ = true) :
    apply doc (ops₁ ++ ops₂) = (apply doc ops₁).bind (fun mid => apply mid ops₂) := by
  sorry

theorem apply_eqv_invariant (a b : Fields) (ops : List Op)
    (ha : (Value.record a).unique = true)
    (hb : (Value.record b).unique = true)
    (heqv : Value.eqv (.record a) (.record b) = true) :
    (∀ err, apply a ops = .error err ↔ apply b ops = .error err) ∧
    (∀ ra rb, apply a ops = .ok ra → apply b ops = .ok rb →
      Value.eqv (.record ra) (.record rb) = true) := by
  sorry

theorem diff_refl (doc : Fields)
    (hu : (Value.record doc).unique = true)
    (htop : topSegsOk doc = true) :
    diff doc doc = .ok [] := by
  sorry

theorem diff_apply_roundtrip (old next : Fields) (ops : List Op)
    (h : diff old next = .ok ops) :
    ∃ res, apply old ops = .ok res ∧
      (Value.record res).unique = true ∧
      Value.eqv (.record res) (.record next) = true := by
  sorry

theorem toRecord_fromRecord_roundtrip (ops : List Op) (rec : Fields)
    (h : toRecord ops = .ok rec) :
    (Value.record rec).unique = true ∧ fromRecord rec = .ok ops := by
  sorry

theorem patch_is_stored (old : Fields) (rev : Int) (ops : List Op) (next : Value)
    (h : patch old rev ops = .ok next) :
    Nayutar.Envelope.stored next = true ∧
    next.at ["meta", "uid"] = (Value.record old).at ["meta", "uid"] ∧
    next.at ["status"] = (Value.record old).at ["status"] := by
  sorry

theorem patch_bumps_generation_iff_spec_changes (old : Fields) (rev : Int) (ops : List Op)
    (next : Value) (h : patch old rev ops = .ok next) :
    ∃ g, (Value.record old).at ["meta", "generation"] = some (.int g) ∧
      next.at ["meta", "generation"] =
        some (.int (if optEqv ((Value.record old).at ["spec"]) (next.at ["spec"]) then g else g + 1)) := by
  sorry

theorem patchStatus_is_stored (old : Fields) (rev : Int) (ops : List Op) (now : String)
    (next : Value) (h : patchStatus old rev ops now = .ok next) :
    Nayutar.Envelope.stored next = true ∧
    next.at ["meta"] = (Value.record old).at ["meta"] ∧
    next.at ["spec"] = (Value.record old).at ["spec"] := by
  sorry

end Nayutar.Patch
