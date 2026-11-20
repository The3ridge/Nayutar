import Nayutar.Refs

set_option autoImplicit false

namespace Nayutar.Refs

open Nayutar.Envelope (Value)

private theorem minOcc_eq_none (l : List Occ) : minOcc l = none ↔ l = [] := by
  cases l with
  | nil => simp [minOcc]
  | cons _ _ => simp [minOcc]

private theorem filter_nil_all {α : Type} {p : α → Bool} {xs : List α}
    (h : xs.filter p = []) : ∀ x ∈ xs, p x = false := by
  intro x hx
  cases hp : p x with
  | false => rfl
  | true =>
    have hmem : x ∈ xs.filter p := List.mem_filter.mpr ⟨hx, hp⟩
    rw [h] at hmem
    cases hmem

private theorem mem_dedup {α : Type} [DecidableEq α] (xs : List α) (x : α) :
    x ∈ dedup xs ↔ x ∈ xs := by
  induction xs with
  | nil => simp [dedup]
  | cons y ys ih =>
    simp only [dedup]
    split
    · rename_i hc
      rw [List.contains_eq_mem, decide_eq_true_eq] at hc
      constructor
      · intro hx
        exact List.mem_cons_of_mem y (ih.mp hx)
      · intro hx
        rcases List.mem_cons.mp hx with rfl | hmem
        · exact hc
        · exact ih.mpr hmem
    · constructor
      · intro hx
        rcases List.mem_cons.mp hx with rfl | hmem
        · simp
        · exact List.mem_cons_of_mem y (ih.mp hmem)
      · intro hx
        rcases List.mem_cons.mp hx with rfl | hmem
        · simp
        · exact List.mem_cons_of_mem y (ih.mpr hmem)

private theorem nodup_dedup {α : Type} [DecidableEq α] (xs : List α) : (dedup xs).Nodup := by
  induction xs with
  | nil => simp [dedup]
  | cons y ys ih =>
    simp only [dedup]
    split
    · exact ih
    · rename_i hnot
      simp only [List.nodup_cons]
      refine ⟨?_, ih⟩
      intro hmem
      apply hnot
      rw [List.contains_eq_mem, decide_eq_true_eq]
      exact hmem

private theorem mem_minus {α : Type} [DecidableEq α] (xs ys : List α) (x : α) :
    x ∈ minus xs ys ↔ x ∈ xs ∧ x ∉ ys := by
  simp only [minus, List.mem_filter, Bool.not_eq_true', List.contains_eq_mem, decide_eq_false_iff_not]

private theorem nodup_minus {α : Type} [DecidableEq α] {xs ys : List α} (h : xs.Nodup) :
    (minus xs ys).Nodup :=
  h.sublist List.filter_sublist

private theorem mem_filterMap_iff {α β : Type} (f : α → Option β) (xs : List α) (b : β) :
    b ∈ xs.filterMap f ↔ ∃ a ∈ xs, f a = some b := by
  induction xs with
  | nil => simp [List.filterMap]
  | cons a rest ih =>
    cases ha : f a with
    | none =>
      simp [List.filterMap, ha, ih]
    | some b' =>
      simp only [List.filterMap, ha, List.mem_cons, ih]
      constructor
      · rintro (rfl | ⟨a', ha', hf⟩)
        · exact ⟨a, Or.inl rfl, ha⟩
        · exact ⟨a', Or.inr ha', hf⟩
      · rintro ⟨a', (rfl | ha'), hf⟩
        · rw [ha] at hf
          cases hf
          exact Or.inl rfl
        · exact Or.inr ⟨a', ha', hf⟩

private theorem parse_validObj {segs : List String} {p : Parsed} (h : parse segs = some p) :
    validObj p.obj = true := by
  cases segs with
  | nil => simp [parse] at h
  | cons qkind rest =>
    cases rest with
    | nil => simp [parse] at h
    | cons name leaf =>
      cases hk : Nayutar.Keyspace.parseRel "t" s!"ref/{qkind}/{name}/k/n" with
      | none => simp [parse, hk] at h
      | some k =>
        simp only [parse, hk] at h
        split at h
        · rename_i hcond
          cases h
          simp only [Bool.and_eq_true] at hcond
          exact hcond.2
        · cases h

private theorem targets_validObj {doc : Value} {x : Obj} (hx : x ∈ targets doc) :
    validObj x = true := by
  rw [targets, mem_dedup, mem_filterMap_iff] at hx
  obtain ⟨o, _, hmap⟩ := hx
  cases hp : parse o.ref with
  | none => simp [hp] at hmap
  | some t =>
    simp only [hp, Option.map_some, Option.some.injEq] at hmap
    subst hmap
    exact parse_validObj hp

private theorem storedTargets_validObj {i : Input} {x : Obj} (hx : x ∈ storedTargets i) :
    validObj x = true := by
  cases hs : i.stored with
  | none => simp [storedTargets, hs] at hx
  | some s =>
    simp only [storedTargets, hs] at hx
    exact targets_validObj hx

private theorem nodup_targets (doc : Value) : (targets doc).Nodup :=
  nodup_dedup _

private theorem nodup_storedTargets (i : Input) : (storedTargets i).Nodup := by
  cases hs : i.stored with
  | none => simp [storedTargets, hs]
  | some s =>
    simp only [storedTargets, hs]
    exact nodup_targets s

private theorem check_ok {i : Input} {d : Delta} (h : check i = .ok d) :
    phase1 i = none ∧ (storedTargets i).length ≤ maxTargets ∧
    phase2 i = none ∧ phase3 i = none ∧
    (targets i.next).length ≤ maxTargets ∧
    phase5 i = none ∧ phase6 i = none ∧ phase7 i = none ∧
    d = {
      put := minus (targets i.next) (storedTargets i)
      delete := minus (storedTargets i) (targets i.next)
      compare := dedup ((added i).filterMap (fun o => (parse o.ref).map Parsed.obj))
    } := by
  simp only [check] at h
  split at h
  · cases h
  · rename_i h1
    split at h
    · cases h
    · rename_i h1b
      split at h
      · cases h
      · rename_i h2
        split at h
        · cases h
        · rename_i h3
          split at h
          · cases h
          · rename_i h4
            split at h
            · cases h
            · rename_i h5
              split at h
              · cases h
              · rename_i h6
                split at h
                · cases h
                · rename_i h7
                  cases h
                  exact ⟨h1, by omega, h2, h3, by omega, h5, h6, h7, rfl⟩

theorem accepted_refs_well_placed (i : Input) (d : Delta) (h : check i = .ok d) :
    ∀ o ∈ occurrences i.next, inSpec o = true ∧ validKeys o.keys = true := by
  obtain ⟨_, _, h2, -⟩ := check_ok h
  simp only [phase2, minOcc_eq_none] at h2
  intro o ho
  have hbad := filter_nil_all h2 o ho
  cases ha : inSpec o <;> cases hb : validKeys o.keys <;> simp [ha, hb] at hbad ⊢

theorem accepted_new_refs_checked (i : Input) (d : Delta) (h : check i = .ok d) :
    ∀ o ∈ occurrences i.next, o ∈ storedOccs i ∨
      ∃ t, parse o.ref = some t ∧ t.qkind ∈ allowed i.schema i.source.qkind o.path ∧
        i.allow o = true ∧ i.present t.obj = true := by
  obtain ⟨_, _, h2, h3, _, h5, h6, h7, -⟩ := check_ok h
  simp only [phase2, minOcc_eq_none] at h2
  simp only [phase3, minOcc_eq_none] at h3
  simp only [phase5, minOcc_eq_none] at h5
  simp only [phase6, minOcc_eq_none] at h6
  simp only [phase7, minOcc_eq_none] at h7
  intro o ho
  by_cases hprev : o ∈ storedOccs i
  · exact Or.inl hprev
  · right
    have hin : inSpec o = true := by
      have hbad := filter_nil_all h2 o ho
      cases hs : inSpec o
      · simp [hs] at hbad
      · rfl
    have hadd : o ∈ added i := by
      rw [added, List.mem_filter]
      refine ⟨ho, ?_⟩
      simp only [Bool.and_eq_true, Bool.not_eq_true', List.contains_eq_mem, decide_eq_false_iff_not]
      exact ⟨hin, hprev⟩
    have hsome := filter_nil_all h3 o hadd
    cases hp : parse o.ref with
    | none => simp [hp] at hsome
    | some t =>
      refine ⟨t, rfl, ?_, ?_, ?_⟩
      · have hbad := filter_nil_all h5 o hadd
        rw [hp] at hbad
        cases hc : (allowed i.schema i.source.qkind o.path).contains t.qkind with
        | false =>
          dsimp only at hbad
          rw [hc] at hbad
          cases hbad
        | true => rwa [List.contains_eq_mem, decide_eq_true_eq] at hc
      · have hbad := filter_nil_all h6 o hadd
        cases ha : i.allow o with
        | true => rfl
        | false =>
          rw [ha] at hbad
          cases hbad
      · have hbad := filter_nil_all h7 o hadd
        rw [hp] at hbad
        cases hpr : i.present t.obj with
        | true => rfl
        | false =>
          dsimp only at hbad
          rw [hpr] at hbad
          cases hbad

theorem accepted_targets_bounded (i : Input) (d : Delta) (h : check i = .ok d) :
    (targets i.next).length ≤ maxTargets := by
  obtain ⟨_, _, _, _, h4, -⟩ := check_ok h
  exact h4

theorem index_delta_exact (i : Input) (d : Delta) (h : check i = .ok d) :
    d.put.Nodup ∧ d.delete.Nodup ∧
      ∀ x, (x ∈ d.put ↔ x ∈ targets i.next ∧ x ∉ storedTargets i) ∧
           (x ∈ d.delete ↔ x ∈ storedTargets i ∧ x ∉ targets i.next) := by
  obtain ⟨_, _, _, _, _, _, _, _, rfl⟩ := check_ok h
  refine ⟨nodup_minus (nodup_targets i.next), nodup_minus (nodup_storedTargets i), ?_⟩
  intro x
  exact ⟨mem_minus _ _ _, mem_minus _ _ _⟩

theorem accepted_put_compared (i : Input) (d : Delta) (h : check i = .ok d) :
    ∀ x ∈ d.put, x ∈ d.compare := by
  obtain ⟨_, _, _, _, _, _, _, _, rfl⟩ := check_ok h
  intro x hx
  rw [mem_minus] at hx
  obtain ⟨hnxt, hnotold⟩ := hx
  rw [targets, mem_dedup, mem_filterMap_iff] at hnxt
  obtain ⟨o, ho_filt, hmap⟩ := hnxt
  rw [List.mem_filter] at ho_filt
  obtain ⟨ho_next, hin⟩ := ho_filt
  cases hp : parse o.ref with
  | none => simp [hp] at hmap
  | some t =>
    simp only [hp, Option.map_some, Option.some.injEq] at hmap
    have ho_not_stored : o ∉ storedOccs i := by
      intro ho_stored
      apply hnotold
      cases hs : i.stored with
      | none => simp [storedOccs, hs] at ho_stored
      | some s =>
        simp only [storedOccs, hs] at ho_stored
        simp only [storedTargets, hs, targets, mem_dedup, mem_filterMap_iff]
        refine ⟨o, List.mem_filter.mpr ⟨ho_stored, hin⟩, ?_⟩
        simp [hp, hmap]
    have hadd : o ∈ added i := by
      rw [added, List.mem_filter]
      refine ⟨ho_next, ?_⟩
      simp only [Bool.and_eq_true, Bool.not_eq_true', List.contains_eq_mem, decide_eq_false_iff_not]
      exact ⟨hin, ho_not_stored⟩
    rw [mem_dedup, mem_filterMap_iff]
    refine ⟨o, hadd, ?_⟩
    simp [hp, hmap]

private theorem storedTargets_with_present (i : Input) (p : Obj → Bool) :
    storedTargets { i with present := p } = storedTargets i := rfl

private theorem phase1_with_present (i : Input) (p : Obj → Bool) :
    phase1 { i with present := p } = phase1 i := rfl

private theorem phase2_with_present (i : Input) (p : Obj → Bool) :
    phase2 { i with present := p } = phase2 i := rfl

private theorem phase3_with_present (i : Input) (p : Obj → Bool) :
    phase3 { i with present := p } = phase3 i := rfl

private theorem phase5_with_present (i : Input) (p : Obj → Bool) :
    phase5 { i with present := p } = phase5 i := rfl

private theorem phase6_with_present (i : Input) (p : Obj → Bool) :
    phase6 { i with present := p } = phase6 i := rfl

theorem denied_ignores_presence (i : Input) (h : ∃ o ∈ added i, i.allow o = false) :
    ∀ p : Obj → Bool, check { i with present := p } = check i := by
  intro p
  obtain ⟨o, ho, hallow⟩ := h
  have hmem : o ∈ (added i).filter (fun o => !(i.allow o)) :=
    List.mem_filter.mpr ⟨ho, by simp [hallow]⟩
  have hne : (added i).filter (fun o => !(i.allow o)) ≠ [] := by
    intro heq
    rw [heq] at hmem
    cases hmem
  have hp6_some : ∃ occ, phase6 i = some occ := by
    cases h6 : phase6 i with
    | none =>
      simp only [phase6, minOcc_eq_none] at h6
      exact absurd h6 hne
    | some occ => exact ⟨occ, rfl⟩
  obtain ⟨occ, hocc⟩ := hp6_some
  simp only [check, storedTargets_with_present, phase1_with_present, phase2_with_present,
    phase3_with_present, phase5_with_present, phase6_with_present]
  cases phase1 i with
  | some _ => rfl
  | none =>
    by_cases h1b : (storedTargets i).length > maxTargets
    · simp [h1b]
    · simp [h1b]
      cases phase2 i with
      | some _ => rfl
      | none =>
        cases phase3 i with
        | some _ => rfl
        | none =>
          by_cases h4 : (targets i.next).length > maxTargets
          · simp [h4]
          · simp [h4]
            cases phase5 i with
            | some _ => rfl
            | none => simp [hocc]

theorem accepted_delta_valid (i : Input) (d : Delta) (h : check i = .ok d) :
    ∀ x ∈ d.put ++ d.delete ++ d.compare, validObj x = true := by
  obtain ⟨_, _, _, _, _, _, _, _, rfl⟩ := check_ok h
  intro x hx
  rcases List.mem_append.mp hx with hx12 | hx3
  · rcases List.mem_append.mp hx12 with hx1 | hx2
    · rw [mem_minus] at hx1
      exact targets_validObj hx1.1
    · rw [mem_minus] at hx2
      exact storedTargets_validObj hx2.1
  · rw [mem_dedup, mem_filterMap_iff] at hx3
    obtain ⟨o, _, hmap⟩ := hx3
    cases hp : parse o.ref with
    | none => simp [hp] at hmap
    | some t =>
      simp only [hp, Option.map_some, Option.some.injEq] at hmap
      subst hmap
      exact parse_validObj hp

theorem delete_clears_index (s : Nayutar.Envelope.Value) (d : Delta)
    (h : deleteDelta s = .ok d) :
    d.put = [] ∧ d.delete.Nodup ∧ ∀ x, x ∈ d.delete ↔ x ∈ targets s := by
  simp only [deleteDelta] at h
  split at h
  · cases h
  · split at h
    · cases h
    · cases h
      exact ⟨rfl, nodup_targets s, fun _ => Iff.rfl⟩

end Nayutar.Refs
