import Nayutar.Envelope

namespace Nayutar.Envelope

/-- Closure: what `create` admits is a well-formed stored document. -/
theorem create_is_stored (cand : Value) (uid : String) (d : Value)
    (h : create cand uid = .ok d) : stored d = true := by
  sorry

/-- A created document starts at generation 1 and carries the uid the server supplied. -/
theorem create_starts_at_generation_one (cand : Value) (uid : String) (d : Value)
    (h : create cand uid = .ok d) :
    d.at ["meta", "generation"] = some (.int 1) ∧ d.at ["meta", "uid"] = some (.str uid) := by
  sorry

/-- Closure: what `update` admits is a well-formed stored document. -/
theorem update_is_stored (old : Value) (rev : Int) (cand d : Value)
    (h : update old rev cand = .ok d) : stored d = true := by
  sorry

/-- The meta-and-spec entrance never changes the uid or anything under status. -/
theorem update_keeps_uid_and_status (old : Value) (rev : Int) (cand d : Value)
    (h : update old rev cand = .ok d) :
    d.at ["meta", "uid"] = old.at ["meta", "uid"] ∧ d.at ["status"] = old.at ["status"] := by
  sorry

/-- The generation grows by exactly one when the spec changes and stays put otherwise; the written
spec is the candidate's. -/
theorem update_bumps_generation_iff_spec_changes (old : Value) (rev : Int) (cand d : Value)
    (h : update old rev cand = .ok d) :
    ∃ g, old.at ["meta", "generation"] = some (.int g) ∧
      d.at ["meta", "generation"]
        = some (.int (if optEqv (old.at ["spec"]) (cand.at ["spec"]) then g else g + 1))
      ∧ d.at ["spec"] = cand.at ["spec"] := by
  sorry

/-- Closure: what `updateStatus` admits is a well-formed stored document, so observedGeneration
never passes the generation. -/
theorem update_status_is_stored (old : Value) (rev : Int) (cand : Value) (now : String)
    (d : Value) (h : updateStatus old rev cand now = .ok d) : stored d = true := by
  sorry

/-- The status entrance never changes meta (hence the generation and the uid) or the spec. -/
theorem update_status_keeps_meta_and_spec (old : Value) (rev : Int) (cand : Value) (now : String)
    (d : Value) (h : updateStatus old rev cand now = .ok d) :
    d.at ["meta"] = old.at ["meta"] ∧ d.at ["spec"] = old.at ["spec"] := by
  sorry

/-- Once written, observedGeneration never falls and never vanishes. -/
theorem update_status_observed_is_monotone (old : Value) (rev : Int) (cand : Value)
    (now : String) (d : Value) (h : updateStatus old rev cand now = .ok d) (m : Int)
    (hm : old.at ["status", "observedGeneration"] = some (.int m)) :
    ∃ n, d.at ["status", "observedGeneration"] = some (.int n) ∧ m ≤ n := by
  sorry

/-- A written condition keeps the stored `since` while its state is unchanged and takes `now`
otherwise. -/
theorem update_status_since_follows_state (old : Value) (rev : Int) (cand : Value)
    (now : String) (d : Value) (h : updateStatus old rev cand now = .ok d) (t : String)
    (c : Value) (hc : d.at ["status", "condition", t] = some c) :
    c.at ["since"] =
      (match old.at ["status", "condition", t] with
        | some oc =>
          if optEqv (oc.at ["state"]) (c.at ["state"]) then oc.at ["since"] else some (.str now)
        | none => some (.str now)) := by
  sorry

/-- A candidate that names a revision other than the current one is never admitted by either
entrance. -/
theorem stale_revision_is_rejected (old : Value) (rev : Int) (cand : Value) (now : String)
    (r : Int) (hr : cand.at ["meta", "revision"] = some (.int r)) (hne : r ≠ rev) (d : Value) :
    update old rev cand ≠ .ok d ∧ updateStatus old rev cand now ≠ .ok d := by
  sorry

end Nayutar.Envelope
