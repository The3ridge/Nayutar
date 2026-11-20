import Nayutar.Refs

namespace Nayutar.Refs

/-- An admitted write leaves every reference of the result inside spec, on a path whose keys are
non-empty and contain no '/'. -/
theorem accepted_refs_well_placed (i : Input) (d : Delta) (h : check i = .ok d) :
    ∀ o ∈ occurrences i.next, inSpec o = true ∧ validKeys o.keys = true := by
  sorry

/-- Every reference of an admitted result was already stored, or it parses, the schema table
allows its target kind at its path, the authorization oracle allowed it and its target exists. -/
theorem accepted_new_refs_checked (i : Input) (d : Delta) (h : check i = .ok d) :
    ∀ o ∈ occurrences i.next, o ∈ storedOccs i ∨
      ∃ t, parse o.ref = some t ∧ t.qkind ∈ allowed i.schema i.source.qkind o.path ∧
        i.allow o = true ∧ i.present t.obj = true := by
  sorry

/-- An admitted result references at most `maxTargets` distinct objects from its spec. -/
theorem accepted_targets_bounded (i : Input) (d : Delta) (h : check i = .ok d) :
    (targets i.next).length ≤ maxTargets := by
  sorry

/-- The index delta of an admitted write puts exactly the new targets and deletes exactly the
dropped ones, each without duplicates. -/
theorem index_delta_exact (i : Input) (d : Delta) (h : check i = .ok d) :
    d.put.Nodup ∧ d.delete.Nodup ∧
      ∀ x, (x ∈ d.put ↔ x ∈ targets i.next ∧ x ∉ storedTargets i) ∧
           (x ∈ d.delete ↔ x ∈ storedTargets i ∧ x ∉ targets i.next) := by
  sorry

/-- Every target whose back-reference is added is also compared in the same transaction. -/
theorem accepted_put_compared (i : Input) (d : Delta) (h : check i = .ok d) :
    ∀ x ∈ d.put, x ∈ d.compare := by
  sorry

/-- When an added reference is not authorized, the outcome does not depend on which targets
exist. -/
theorem denied_ignores_presence (i : Input) (h : ∃ o ∈ added i, i.allow o = false) :
    ∀ p : Obj → Bool, check { i with present := p } = check i := by
  sorry

/-- Every object in the delta of an admitted write satisfies the key grammar. -/
theorem accepted_delta_valid (i : Input) (d : Delta) (h : check i = .ok d) :
    ∀ x ∈ d.put ++ d.delete ++ d.compare, validObj x = true := by
  sorry

/-- An admitted delete adds nothing to the index and removes exactly the stored targets. -/
theorem delete_clears_index (s : Nayutar.Envelope.Value) (d : Delta)
    (h : deleteDelta s = .ok d) :
    d.put = [] ∧ d.delete.Nodup ∧ ∀ x, x ∈ d.delete ↔ x ∈ targets s := by
  sorry

end Nayutar.Refs
