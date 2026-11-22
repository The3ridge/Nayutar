import Nayutar.OrderedSet

set_option autoImplicit false

namespace Nayutar.OrderedSet

open Nayutar.Envelope (Value Values Fields)

private theorem unique_record (fs : Fields) :
    (Value.record fs).unique = (fs.keysDistinct && Fields.unique fs) := by
  simp only [Value.unique]

private theorem fields_unique_get : ∀ (fs : Fields) (k : String) (v : Value),
    Fields.unique fs = true → fs.get k = some v → v.unique = true
  | .nil, _, _, _, hg => by simp [Fields.get] at hg
  | .cons k' v' rest, k, v, h, hg => by
    simp only [Fields.unique, Bool.and_eq_true] at h
    simp only [Fields.get] at hg
    split at hg
    · cases hg
      exact h.1
    · exact fields_unique_get rest k v h.2 hg

private theorem stripField_get_self : ∀ (fs : Fields) (target : String),
    (stripField fs target).get target = none
  | .nil, _ => rfl
  | .cons k v rest, target => by
    simp only [stripField]
    split
    · exact stripField_get_self rest target
    · rename_i hne
      simp only [Fields.get, hne, ite_false]
      exact stripField_get_self rest target

private theorem stripField_get_ne : ∀ (fs : Fields) {target k : String},
    k ≠ target → (stripField fs target).get k = fs.get k
  | .nil, _, _, _ => rfl
  | .cons k' v rest, target, k, hne => by
    simp only [stripField]
    split
    · rename_i heq
      subst heq
      simp only [Fields.get, Ne.symm hne, ite_false]
      exact stripField_get_ne rest hne
    · simp only [Fields.get]
      split
      · rfl
      · exact stripField_get_ne rest hne

private theorem stripField_of_get_none : ∀ (fs : Fields) (target : String),
    fs.get target = none → stripField fs target = fs
  | .nil, _, _ => rfl
  | .cons k v rest, target, hg => by
    simp only [Fields.get] at hg
    split at hg
    · cases hg
    · rename_i hne
      simp only [stripField, hne, ite_false]
      rw [stripField_of_get_none rest target hg]

private theorem stripField_keysDistinct : ∀ (fs : Fields) (target : String),
    fs.keysDistinct = true → (stripField fs target).keysDistinct = true
  | .nil, _, _ => rfl
  | .cons k v rest, target, h => by
    simp only [Fields.keysDistinct, Bool.and_eq_true] at h
    simp only [stripField]
    split
    · exact stripField_keysDistinct rest target h.2
    · rename_i hne
      simp only [Fields.keysDistinct, Bool.and_eq_true]
      exact ⟨by rw [stripField_get_ne rest hne]; exact h.1, stripField_keysDistinct rest target h.2⟩

private theorem stripField_fieldsUnique : ∀ (fs : Fields) (target : String),
    Fields.unique fs = true → Fields.unique (stripField fs target) = true
  | .nil, _, _ => rfl
  | .cons k v rest, target, h => by
    simp only [Fields.unique, Bool.and_eq_true] at h
    simp only [stripField]
    split
    · exact stripField_fieldsUnique rest target h.2
    · simp only [Fields.unique, Bool.and_eq_true]
      exact ⟨h.1, stripField_fieldsUnique rest target h.2⟩

private theorem stripField_unique (fs : Fields) (target : String)
    (h : (Value.record fs).unique = true) :
    (Value.record (stripField fs target)).unique = true := by
  simp only [unique_record, Bool.and_eq_true] at h ⊢
  exact ⟨stripField_keysDistinct fs target h.1, stripField_fieldsUnique fs target h.2⟩

private theorem stripField_length_some : ∀ (fs : Fields) (target : String),
    fs.keysDistinct = true → (fs.get target).isSome = true →
    (stripField fs target).length + 1 = fs.length
  | .nil, _, _, hs => by simp [Fields.get] at hs
  | .cons k v rest, target, hd, hs => by
    simp only [Fields.keysDistinct, Bool.and_eq_true] at hd
    simp only [Fields.get] at hs
    split at hs
    · rename_i heq
      subst heq
      simp only [stripField, ite_true, Fields.length]
      have hnone : rest.get k = none := by
        cases hrg : rest.get k with
        | none => rfl
        | some _ =>
          rw [hrg] at hd
          simp at hd
      rw [stripField_of_get_none rest k hnone]
    · rename_i hne
      simp only [stripField, hne, ite_false, Fields.length]
      have ih := stripField_length_some rest target hd.2 hs
      omega

mutual
  private theorem value_eqv_refl : ∀ (v : Value), v.unique = true → Value.eqv v v = true
    | .str _, _ => by simp [Value.eqv]
    | .int _, _ => by simp [Value.eqv]
    | .dec _, _ => by simp [Value.eqv]
    | .bool _, _ => by simp [Value.eqv]
    | .enum _, _ => by simp [Value.eqv]
    | .qty _ _, _ => by simp [Value.eqv]
    | .ref _, _ => by simp [Value.eqv]
    | .list vs, hu => by
      simp only [Value.unique] at hu
      simp only [Value.eqv]
      exact values_eqv_refl vs hu
    | .record fs, hu => by
      simp only [Value.unique, Bool.and_eq_true] at hu
      simp only [Value.eqv, beq_self_eq_true, Bool.true_and]
      exact fields_within_refl fs hu.2 hu.1 fs (fun _ _ h => h)

  private theorem values_eqv_refl : ∀ (vs : Values), vs.unique = true → Values.eqv vs vs = true
    | .nil, _ => rfl
    | .cons v vs, hu => by
      simp only [Values.unique, Bool.and_eq_true] at hu
      simp only [Values.eqv, Bool.and_eq_true]
      exact ⟨value_eqv_refl v hu.1, values_eqv_refl vs hu.2⟩

  private theorem fields_within_refl : ∀ (fs : Fields),
      Fields.unique fs = true → fs.keysDistinct = true →
      ∀ (bs : Fields), (∀ k v, fs.get k = some v → bs.get k = some v) →
      Fields.within fs bs = true
    | .nil, _, _, _, _ => rfl
    | .cons k v rest, hu, hd, bs, hget => by
      simp only [Fields.unique, Bool.and_eq_true] at hu
      simp only [Fields.keysDistinct, Bool.and_eq_true] at hd
      simp only [Fields.within, Bool.and_eq_true]
      have hk : bs.get k = some v := hget k v (by simp [Fields.get])
      refine ⟨by rw [hk]; exact value_eqv_refl v hu.1, ?_⟩
      refine fields_within_refl rest hu.2 hd.2 bs ?_
      intro k' v' hg
      have hne : k ≠ k' := by
        intro heq
        subst heq
        rw [hg] at hd
        simp at hd
      exact hget k' v' (by simp [Fields.get, hne, hg])
end

private theorem within_get_some : ∀ (a b : Fields),
    Fields.within a b = true →
    ∀ k v, a.get k = some v → ∃ w, b.get k = some w ∧ Value.eqv v w = true
  | .nil, _, _, _, _, hg => by simp [Fields.get] at hg
  | .cons k' v' rest, b, hw, k, v, hg => by
    simp only [Fields.within, Bool.and_eq_true] at hw
    simp only [Fields.get] at hg
    split at hg
    · rename_i heq
      subst heq
      cases hg
      cases hbg : b.get k' with
      | none =>
        rw [hbg] at hw
        simp at hw
      | some w =>
        rw [hbg] at hw
        exact ⟨w, rfl, hw.1⟩
    · exact within_get_some rest b hw.2 k v hg

private def fieldsKeys : Fields → List String
  | .nil => []
  | .cons k _ rest => k :: fieldsKeys rest

private theorem fieldsKeys_length : ∀ (fs : Fields), (fieldsKeys fs).length = fs.length
  | .nil => rfl
  | .cons _ _ rest => by simp [fieldsKeys, Fields.length, fieldsKeys_length rest]

private theorem mem_fieldsKeys_iff : ∀ (fs : Fields) (k : String),
    k ∈ fieldsKeys fs ↔ (fs.get k).isSome = true
  | .nil, _ => by simp [fieldsKeys, Fields.get]
  | .cons k' _ rest, k => by
    simp only [fieldsKeys, List.mem_cons, Fields.get]
    by_cases heq : k' = k
    · subst heq
      simp
    · have hne : k ≠ k' := Ne.symm heq
      simp [heq, hne, mem_fieldsKeys_iff rest k]

private theorem nodup_fieldsKeys : ∀ (fs : Fields),
    fs.keysDistinct = true → (fieldsKeys fs).Nodup
  | .nil, _ => by simp [fieldsKeys]
  | .cons k _ rest, hd => by
    simp only [Fields.keysDistinct, Bool.and_eq_true] at hd
    simp only [fieldsKeys, List.nodup_cons]
    refine ⟨?_, nodup_fieldsKeys rest hd.2⟩
    intro hmem
    rw [mem_fieldsKeys_iff] at hmem
    cases hrg : rest.get k with
    | none => simp [hrg] at hmem
    | some _ =>
      rw [hrg] at hd
      simp at hd

private def removeFirst (x : String) : List String → List String
  | [] => []
  | y :: ys => if x = y then ys else y :: removeFirst x ys

private theorem mem_removeFirst_of_ne {x k : String} : ∀ (ys : List String),
    k ∈ ys → k ≠ x → k ∈ removeFirst x ys
  | [], hk, _ => by cases hk
  | y :: ys, hk, hne => by
    simp only [removeFirst]
    split
    · rename_i heq
      subst heq
      rcases List.mem_cons.mp hk with rfl | hmem
      · exact absurd rfl hne
      · exact hmem
    · rcases List.mem_cons.mp hk with rfl | hmem
      · exact List.mem_cons_self
      · exact List.mem_cons_of_mem y (mem_removeFirst_of_ne ys hmem hne)

private theorem mem_of_mem_removeFirst {x k : String} : ∀ (ys : List String),
    k ∈ removeFirst x ys → k ∈ ys
  | [], hk => by cases hk
  | y :: ys, hk => by
    simp only [removeFirst] at hk
    split at hk
    · exact List.mem_cons_of_mem y hk
    · rcases List.mem_cons.mp hk with rfl | hmem
      · exact List.mem_cons_self
      · exact List.mem_cons_of_mem y (mem_of_mem_removeFirst ys hmem)

private theorem removeFirst_length {x : String} : ∀ (ys : List String),
    x ∈ ys → (removeFirst x ys).length + 1 = ys.length
  | [], hx => by cases hx
  | y :: ys, hx => by
    simp only [removeFirst]
    split
    · rfl
    · rename_i hne
      rcases List.mem_cons.mp hx with rfl | hmem
      · exact absurd rfl hne
      · simp only [List.length_cons]
        have ih := removeFirst_length ys hmem
        omega

private theorem removeFirst_nodup {x : String} : ∀ (ys : List String),
    ys.Nodup → (removeFirst x ys).Nodup
  | [], _ => by simp [removeFirst]
  | y :: ys, hnd => by
    simp only [List.nodup_cons] at hnd
    simp only [removeFirst]
    split
    · exact hnd.2
    · simp only [List.nodup_cons]
      refine ⟨fun hmem => hnd.1 (mem_of_mem_removeFirst ys hmem), removeFirst_nodup ys hnd.2⟩

private theorem nodup_subset_eq_length : ∀ (xs ys : List String),
    xs.Nodup → ys.Nodup → xs.length = ys.length →
    (∀ k ∈ xs, k ∈ ys) → ∀ k ∈ ys, k ∈ xs
  | [], ys, _, _, hlen, _, k, hk => by
    cases ys with
    | nil => cases hk
    | cons _ _ => simp at hlen
  | x :: xs, ys, hnx, hny, hlen, hsub, k, hk => by
    simp only [List.nodup_cons] at hnx
    have hxys : x ∈ ys := hsub x List.mem_cons_self
    by_cases hkx : k = x
    · subst hkx
      exact List.mem_cons_self
    · have hk' : k ∈ removeFirst x ys := mem_removeFirst_of_ne ys hk hkx
      have hlen' : xs.length = (removeFirst x ys).length := by
        have hr := removeFirst_length ys hxys
        simp only [List.length_cons] at hlen
        omega
      have hsub' : ∀ z ∈ xs, z ∈ removeFirst x ys := by
        intro z hz
        have hzne : z ≠ x := fun heq => hnx.1 (heq ▸ hz)
        exact mem_removeFirst_of_ne ys (hsub z (List.mem_cons_of_mem x hz)) hzne
      have hmem := nodup_subset_eq_length xs (removeFirst x ys) hnx.2 (removeFirst_nodup ys hny)
        hlen' hsub' k hk'
      exact List.mem_cons_of_mem x hmem

private theorem record_eqv_get_iff {a b : Fields}
    (ha : a.keysDistinct = true) (hb : b.keysDistinct = true)
    (heqv : Value.eqv (.record a) (.record b) = true) :
    (∀ k v, a.get k = some v → ∃ w, b.get k = some w ∧ Value.eqv v w = true) ∧
    (∀ k w, b.get k = some w → ∃ v, a.get k = some v ∧ Value.eqv v w = true) := by
  simp only [Value.eqv, Bool.and_eq_true, beq_iff_eq] at heqv
  obtain ⟨hlen, hw⟩ := heqv
  have hfwd : ∀ k v, a.get k = some v → ∃ w, b.get k = some w ∧ Value.eqv v w = true :=
    within_get_some a b hw
  refine ⟨hfwd, ?_⟩
  intro k w hbg
  have hsub : ∀ z ∈ fieldsKeys a, z ∈ fieldsKeys b := by
    intro z hz
    rw [mem_fieldsKeys_iff] at hz ⊢
    cases hag : a.get z with
    | none => simp [hag] at hz
    | some v =>
      obtain ⟨w', hbg', _⟩ := hfwd z v hag
      simp [hbg']
  have hk_in_b : k ∈ fieldsKeys b := (mem_fieldsKeys_iff b k).mpr (by simp [hbg])
  have hlen_keys : (fieldsKeys a).length = (fieldsKeys b).length := by
    rw [fieldsKeys_length, fieldsKeys_length, hlen]
  have hk_in_a : k ∈ fieldsKeys a :=
    nodup_subset_eq_length (fieldsKeys a) (fieldsKeys b)
      (nodup_fieldsKeys a ha) (nodup_fieldsKeys b hb) hlen_keys hsub k hk_in_b
  have hisSome : (a.get k).isSome = true := (mem_fieldsKeys_iff a k).mp hk_in_a
  cases hag : a.get k with
  | none => simp [hag] at hisSome
  | some v =>
    obtain ⟨w', hbg', hvw⟩ := hfwd k v hag
    rw [hbg] at hbg'
    cases hbg'
    exact ⟨v, rfl, hvw⟩

private theorem within_stripField : ∀ (a b : Fields) (target : String),
    Fields.within a b = true → Fields.within (stripField a target) (stripField b target) = true
  | .nil, _, _, _ => rfl
  | .cons k v rest, b, target, hw => by
    simp only [Fields.within, Bool.and_eq_true] at hw
    simp only [stripField]
    split
    · exact within_stripField rest b target hw.2
    · rename_i hne
      simp only [Fields.within, Bool.and_eq_true]
      refine ⟨?_, within_stripField rest b target hw.2⟩
      rw [stripField_get_ne b hne]
      exact hw.1

private theorem stripField_eqv {a b : Fields} (target : String)
    (ha : a.keysDistinct = true) (hb : b.keysDistinct = true)
    (heqv : Value.eqv (.record a) (.record b) = true) :
    Value.eqv (.record (stripField a target)) (.record (stripField b target)) = true := by
  obtain ⟨hfwd, hbwd⟩ := record_eqv_get_iff ha hb heqv
  simp only [Value.eqv, Bool.and_eq_true, beq_iff_eq] at heqv ⊢
  refine ⟨?_, within_stripField a b target heqv.2⟩
  cases hag : a.get target with
  | none =>
    have hbg : b.get target = none := by
      cases hbg' : b.get target with
      | none => rfl
      | some w =>
        obtain ⟨v, hav, _⟩ := hbwd target w hbg'
        rw [hag] at hav
        cases hav
    rw [stripField_of_get_none a target hag, stripField_of_get_none b target hbg, heqv.1]
  | some v =>
    obtain ⟨w, hbg, _⟩ := hfwd target v hag
    have hla := stripField_length_some a target ha (by simp [hag])
    have hlb := stripField_length_some b target hb (by simp [hbg])
    omega

private theorem cons_stripField_eqv {el : Fields} {kf k : String}
    (hu : (Value.record el).unique = true) (hg : el.get kf = some (.str k)) :
    Value.eqv (.record (.cons kf (.str k) (stripField el kf))) (.record el) = true := by
  simp only [unique_record, Bool.and_eq_true] at hu
  simp only [Value.eqv, Bool.and_eq_true, beq_iff_eq, Fields.length]
  refine ⟨stripField_length_some el kf hu.1 (by simp [hg]), ?_⟩
  simp only [Fields.within, hg, Value.eqv, beq_self_eq_true, Bool.true_and]
  refine fields_within_refl (stripField el kf)
    (stripField_fieldsUnique el kf hu.2) (stripField_keysDistinct el kf hu.1) el ?_
  intro k' v' hsg
  by_cases heq : k' = kf
  · subst heq
    rw [stripField_get_self] at hsg
    cases hsg
  · rwa [stripField_get_ne el heq] at hsg

private theorem hasDup_eq_false_iff {α : Type} [DecidableEq α] : ∀ (xs : List α),
    hasDup xs = false ↔ xs.Nodup
  | [] => by simp [hasDup]
  | x :: xs => by
    simp only [hasDup, Bool.or_eq_false_iff, List.nodup_cons, List.contains_eq_mem,
      decide_eq_false_iff_not, hasDup_eq_false_iff xs]

private theorem mem_insertEntry (e y : Entry) : ∀ (xs : List Entry),
    y ∈ insertEntry e xs ↔ y = e ∨ y ∈ xs
  | [] => by simp [insertEntry]
  | x :: xs => by
    simp only [insertEntry]
    split
    · simp [List.mem_cons]
    · simp only [List.mem_cons, mem_insertEntry e y xs]
      constructor
      · rintro (rfl | rfl | hmem)
        · exact Or.inr (Or.inl rfl)
        · exact Or.inl rfl
        · exact Or.inr (Or.inr hmem)
      · rintro (rfl | rfl | hmem)
        · exact Or.inr (Or.inl rfl)
        · exact Or.inl rfl
        · exact Or.inr (Or.inr hmem)

private theorem length_insertEntry (e : Entry) : ∀ (xs : List Entry),
    (insertEntry e xs).length = xs.length + 1
  | [] => rfl
  | x :: xs => by
    simp only [insertEntry]
    split
    · rfl
    · simp [length_insertEntry e xs]

private theorem insertEntry_pairwise_lt (e : Entry) : ∀ (xs : List Entry),
    List.Pairwise (fun a b => a.order < b.order) xs →
    (∀ x ∈ xs, e.order ≠ x.order) →
    List.Pairwise (fun a b => a.order < b.order) (insertEntry e xs)
  | [], _, _ => List.pairwise_singleton _ _
  | x :: xs, hp, hne => by
    simp only [List.pairwise_cons] at hp
    simp only [insertEntry]
    split
    · rename_i hle
      have hlt : e.order < x.order := by
        have hnx := hne x List.mem_cons_self
        omega
      refine List.pairwise_cons.mpr ⟨?_, List.pairwise_cons.mpr hp⟩
      intro y hy
      rcases List.mem_cons.mp hy with rfl | hmem
      · exact hlt
      · have hxy := hp.1 y hmem
        omega
    · rename_i hnle
      have hlt : x.order < e.order := by omega
      have ih := insertEntry_pairwise_lt e xs hp.2 (fun z hz => hne z (List.mem_cons_of_mem x hz))
      refine List.pairwise_cons.mpr ⟨?_, ih⟩
      intro y hy
      rcases (mem_insertEntry e y xs).mp hy with rfl | hmem
      · exact hlt
      · exact hp.1 y hmem

private theorem insertEntry_pairwise_key (e : Entry) : ∀ (xs : List Entry),
    List.Pairwise (fun a b => a.key ≠ b.key) xs →
    (∀ x ∈ xs, e.key ≠ x.key) →
    List.Pairwise (fun a b => a.key ≠ b.key) (insertEntry e xs)
  | [], _, _ => List.pairwise_singleton _ _
  | x :: xs, hp, hne => by
    simp only [List.pairwise_cons] at hp
    simp only [insertEntry]
    split
    · exact List.pairwise_cons.mpr ⟨hne, List.pairwise_cons.mpr hp⟩
    · have ih := insertEntry_pairwise_key e xs hp.2 (fun z hz => hne z (List.mem_cons_of_mem x hz))
      refine List.pairwise_cons.mpr ⟨?_, ih⟩
      intro y hy
      rcases (mem_insertEntry e y xs).mp hy with rfl | hmem
      · exact Ne.symm (hne x List.mem_cons_self)
      · exact hp.1 y hmem

private theorem mem_sortEntries (y : Entry) : ∀ (xs : List Entry),
    y ∈ sortEntries xs ↔ y ∈ xs
  | [] => by simp [sortEntries]
  | x :: xs => by
    simp only [sortEntries, mem_insertEntry, List.mem_cons, mem_sortEntries y xs]

private theorem length_sortEntries : ∀ (xs : List Entry),
    (sortEntries xs).length = xs.length
  | [] => rfl
  | x :: xs => by simp [sortEntries, length_insertEntry, length_sortEntries xs]

private theorem sortEntries_pairwise_lt : ∀ (xs : List Entry),
    (xs.map Entry.order).Nodup →
    List.Pairwise (fun a b => a.order < b.order) (sortEntries xs)
  | [], _ => List.Pairwise.nil
  | e :: xs, hnd => by
    simp only [List.map_cons, List.nodup_cons] at hnd
    refine insertEntry_pairwise_lt e (sortEntries xs) (sortEntries_pairwise_lt xs hnd.2) ?_
    intro x hx heq
    have hx' : x ∈ xs := (mem_sortEntries x xs).mp hx
    exact hnd.1 (List.mem_map.mpr ⟨x, hx', heq.symm⟩)

private theorem sortEntries_pairwise_key : ∀ (xs : List Entry),
    List.Pairwise (fun a b => a.key ≠ b.key) xs →
    List.Pairwise (fun a b => a.key ≠ b.key) (sortEntries xs)
  | [], _ => List.Pairwise.nil
  | e :: xs, hp => by
    simp only [List.pairwise_cons] at hp
    refine insertEntry_pairwise_key e (sortEntries xs) (sortEntries_pairwise_key xs hp.2) ?_
    intro x hx
    exact hp.1 x ((mem_sortEntries x xs).mp hx)

private theorem sortEntries_of_pairwise_lt : ∀ (xs : List Entry),
    List.Pairwise (fun a b => a.order < b.order) xs → sortEntries xs = xs
  | [], _ => rfl
  | e :: xs, hp => by
    simp only [List.pairwise_cons] at hp
    simp only [sortEntries, sortEntries_of_pairwise_lt xs hp.2]
    cases xs with
    | nil => rfl
    | cons x rest =>
      have hlt : e.order < x.order := hp.1 x List.mem_cons_self
      have hle : e.order ≤ x.order := by omega
      simp [insertEntry, hle]

private theorem nodup_orders_of_pairwise_lt : ∀ (xs : List Entry),
    List.Pairwise (fun a b => a.order < b.order) xs → (xs.map Entry.order).Nodup
  | [], _ => List.Pairwise.nil
  | e :: xs, hp => by
    simp only [List.pairwise_cons] at hp
    simp only [List.map_cons, List.nodup_cons]
    refine ⟨?_, nodup_orders_of_pairwise_lt xs hp.2⟩
    intro hmem
    rcases List.mem_map.mp hmem with ⟨x, hx, heq⟩
    have hlt := hp.1 x hx
    omega

private theorem extractEntries_length : ∀ (set : Fields) (raw : List Entry),
    extractEntries set = some raw → raw.length = set.length
  | .nil, raw, h => by
    simp only [extractEntries, Option.some.injEq] at h
    subst h
    rfl
  | .cons k v rest, raw, h => by
    simp only [extractEntries] at h
    split at h
    · cases h
    · split at h
      · split at h
        · split at h
          · cases h
          · split at h
            · rename_i tail htail
              cases h
              simp only [List.length_cons, Fields.length]
              have ih := extractEntries_length rest tail htail
              omega
            · cases h
        · cases h
      · cases h

private theorem extractEntries_sound : ∀ (set : Fields) (raw : List Entry),
    set.keysDistinct = true → extractEntries set = some raw →
    List.Pairwise (fun a b => a.key ≠ b.key) raw ∧
    ∀ e ∈ raw, validKey e.key = true ∧ inInt64 e.order = true ∧
      ∃ rec, set.get e.key = some (.record rec) ∧
        rec.get orderField = some (.int e.order) ∧
        e.value = stripField rec orderField
  | .nil, raw, _, h => by
    simp only [extractEntries, Option.some.injEq] at h
    subst h
    exact ⟨List.Pairwise.nil, fun _ he => by cases he⟩
  | .cons k v rest, raw, hd, h => by
    simp only [Fields.keysDistinct, Bool.and_eq_true] at hd
    simp only [extractEntries] at h
    split at h
    · cases h
    · rename_i hvk
      have hvk' : validKey k = true := by simpa using hvk
      split at h
      · rename_i rec
        split at h
        · rename_i ord hord
          split at h
          · cases h
          · rename_i hin
            have hin' : inInt64 ord = true := by simpa using hin
            split at h
            · rename_i tail htail
              cases h
              obtain ⟨ihpw, ihmem⟩ := extractEntries_sound rest tail hd.2 htail
              refine ⟨List.pairwise_cons.mpr ⟨?_, ihpw⟩, ?_⟩
              · intro x hx heq
                obtain ⟨_, _, rec', hget', _, _⟩ := ihmem x hx
                rw [← heq] at hget'
                rw [hget'] at hd
                simp at hd
              · intro e he
                rcases List.mem_cons.mp he with rfl | hmem
                · exact ⟨hvk', hin', rec, by simp [Fields.get], hord, rfl⟩
                · obtain ⟨hve, hine, rec', hget', hord', hval'⟩ := ihmem e hmem
                  have hne : k ≠ e.key := by
                    intro heq
                    rw [heq] at hd
                    rw [hget'] at hd
                    simp at hd
                  exact ⟨hve, hine, rec', by simp [Fields.get, hne, hget'], hord', hval'⟩
            · cases h
        · cases h
      · cases h

private theorem extractEntries_get_mem : ∀ (set : Fields) (raw : List Entry),
    set.keysDistinct = true → extractEntries set = some raw →
    ∀ k v, set.get k = some v →
      ∃ e ∈ raw, ∃ rec, e.key = k ∧ v = .record rec ∧
        rec.get orderField = some (.int e.order) ∧ e.value = stripField rec orderField
  | .nil, _, _, _, _, _, hg => by simp [Fields.get] at hg
  | .cons k' v' rest, raw, hd, h, k, v, hg => by
    simp only [Fields.keysDistinct, Bool.and_eq_true] at hd
    simp only [extractEntries] at h
    split at h
    · cases h
    · split at h
      · rename_i rec
        split at h
        · rename_i ord hord
          split at h
          · cases h
          · split at h
            · rename_i tail htail
              cases h
              simp only [Fields.get] at hg
              split at hg
              · rename_i heq
                subst heq
                cases hg
                exact ⟨{ key := k', order := ord, value := stripField rec orderField },
                  List.mem_cons_self, rec, rfl, rfl, hord, rfl⟩
              · obtain ⟨e, he, rec', hke, hve, horde, hvale⟩ :=
                  extractEntries_get_mem rest tail hd.2 htail k v hg
                exact ⟨e, List.mem_cons_of_mem _ he, rec', hke, hve, horde, hvale⟩
            · cases h
        · cases h
      · cases h

private theorem extractEntries_isSome_iff : ∀ (set : Fields),
    set.keysDistinct = true →
    ((extractEntries set).isSome = true ↔
      ∀ k v, set.get k = some v →
        validKey k = true ∧ ∃ rec ord, v = .record rec ∧
          rec.get orderField = some (.int ord) ∧ inInt64 ord = true)
  | .nil, _ => by simp [extractEntries, Fields.get]
  | .cons k v rest, hd => by
    simp only [Fields.keysDistinct, Bool.and_eq_true] at hd
    constructor
    · intro hs
      cases hx : extractEntries (.cons k v rest) with
      | none => simp [hx] at hs
      | some raw =>
        intro k' v' hg
        obtain ⟨_, hmem⟩ := extractEntries_sound (.cons k v rest) raw (by simp [Fields.keysDistinct, hd]) hx
        obtain ⟨e, he, rec, rfl, rfl, hord, _⟩ :=
          extractEntries_get_mem (.cons k v rest) raw (by simp [Fields.keysDistinct, hd]) hx k' v' hg
        obtain ⟨hvk, hin, _⟩ := hmem e he
        exact ⟨hvk, rec, e.order, rfl, hord, hin⟩
    · intro hall
      have hhead := hall k v (by simp [Fields.get])
      obtain ⟨hvk, rec, ord, rfl, hord, hin⟩ := hhead
      have hrest_all : ∀ k' v', rest.get k' = some v' →
          validKey k' = true ∧ ∃ rec' ord', v' = .record rec' ∧
            rec'.get orderField = some (.int ord') ∧ inInt64 ord' = true := by
        intro k' v' hg
        have hne : k ≠ k' := by
          intro heq
          subst heq
          rw [hg] at hd
          simp at hd
        exact hall k' v' (by simp [Fields.get, hne, hg])
      have hrest := (extractEntries_isSome_iff rest hd.2).mpr hrest_all
      cases hx : extractEntries rest with
      | none => simp [hx] at hrest
      | some tail =>
        simp [extractEntries, hvk, hord, hin, hx]

private theorem entries_ok {set : Fields} {es : List Entry} (h : entries set = .ok es) :
    (Value.record set).unique = true ∧
    ∃ raw, extractEntries set = some raw ∧ hasDup (raw.map Entry.order) = false ∧
      es = sortEntries raw := by
  unfold entries at h
  split at h
  · cases h
  · rename_i hu
    cases hx : extractEntries set with
    | none =>
      rw [hx] at h
      cases h
    | some raw =>
      rw [hx] at h
      simp only at h
      split at h
      · cases h
      · rename_i hnd
        cases h
        exact ⟨by simpa using hu, raw, rfl, by simpa using hnd, rfl⟩

theorem entries_strictly_sorted (set : Fields) (es : List Entry) (h : entries set = .ok es) :
    List.Pairwise (fun a b => a.order < b.order) es ∧
    List.Pairwise (fun a b => a.key ≠ b.key) es ∧
    ∀ e ∈ es, validKey e.key = true ∧ inInt64 e.order = true := by
  obtain ⟨hu, raw, hx, hnd, rfl⟩ := entries_ok h
  simp only [unique_record, Bool.and_eq_true] at hu
  obtain ⟨hpw_key, hsound⟩ := extractEntries_sound set raw hu.1 hx
  have hnodup : (raw.map Entry.order).Nodup := (hasDup_eq_false_iff _).mp hnd
  refine ⟨sortEntries_pairwise_lt raw hnodup, sortEntries_pairwise_key raw hpw_key, ?_⟩
  intro e he
  obtain ⟨hvk, hin, _⟩ := hsound e ((mem_sortEntries e raw).mp he)
  exact ⟨hvk, hin⟩

theorem entries_strips_order (set : Fields) (es : List Entry) (h : entries set = .ok es) :
    ∀ e ∈ es, (e.value.get orderField).isNone = true ∧ (Value.record e.value).unique = true := by
  obtain ⟨hu, raw, hx, _, rfl⟩ := entries_ok h
  simp only [unique_record, Bool.and_eq_true] at hu
  obtain ⟨_, hsound⟩ := extractEntries_sound set raw hu.1 hx
  intro e he
  obtain ⟨_, _, rec, hget, _, hval⟩ := hsound e ((mem_sortEntries e raw).mp he)
  rw [hval]
  refine ⟨by rw [stripField_get_self]; rfl, ?_⟩
  have hrecU : (Value.record rec).unique = true := fields_unique_get set e.key (.record rec) hu.2 hget
  exact stripField_unique rec orderField hrecU

theorem entries_complete_and_sound (set : Fields) (es : List Entry) (h : entries set = .ok es) :
    es.length = set.length ∧
    ∀ e ∈ es, ∃ rec, set.get e.key = some (.record rec) ∧
      rec.get orderField = some (.int e.order) ∧ e.value = stripField rec orderField := by
  obtain ⟨hu, raw, hx, _, rfl⟩ := entries_ok h
  simp only [unique_record, Bool.and_eq_true] at hu
  obtain ⟨_, hsound⟩ := extractEntries_sound set raw hu.1 hx
  refine ⟨by rw [length_sortEntries, extractEntries_length set raw hx], ?_⟩
  intro e he
  obtain ⟨_, _, hrec⟩ := hsound e ((mem_sortEntries e raw).mp he)
  exact hrec

theorem entries_reverse_eq_reverse (set : Fields) :
    entriesReverse set = (entries set).map List.reverse := rfl

private theorem value_eqv_record_elim {fs : Fields} {w : Value}
    (h : Value.eqv (.record fs) w = true) :
    ∃ gs, w = .record gs ∧ Value.eqv (.record fs) (.record gs) = true := by
  cases w <;> try (simp [Value.eqv] at h; done)
  exact ⟨_, rfl, h⟩

private theorem value_eqv_record_elim_right {v : Value} {gs : Fields}
    (h : Value.eqv v (.record gs) = true) :
    ∃ fs, v = .record fs ∧ Value.eqv (.record fs) (.record gs) = true := by
  cases v <;> try (simp [Value.eqv] at h; done)
  exact ⟨_, rfl, h⟩

private theorem value_eqv_int_elim {n : Int} {w : Value}
    (h : Value.eqv (.int n) w = true) : w = .int n := by
  cases w <;> try (simp [Value.eqv] at h; done)
  simp only [Value.eqv, beq_iff_eq] at h
  subst h
  rfl

private theorem value_eqv_int_elim_right {v : Value} {n : Int}
    (h : Value.eqv v (.int n) = true) : v = .int n := by
  cases v <;> try (simp [Value.eqv] at h; done)
  simp only [Value.eqv, beq_iff_eq] at h
  subst h
  rfl

private theorem hasDup_order_iff_exists_distinct_key : ∀ (xs : List Entry),
    List.Pairwise (fun a b => a.key ≠ b.key) xs →
    (hasDup (xs.map Entry.order) = true ↔
      ∃ e1 ∈ xs, ∃ e2 ∈ xs, e1.key ≠ e2.key ∧ e1.order = e2.order)
  | [], _ => by simp [hasDup]
  | e :: xs, hpw => by
    simp only [List.pairwise_cons] at hpw
    simp only [List.map_cons, hasDup, Bool.or_eq_true, List.contains_eq_mem, decide_eq_true_eq,
      hasDup_order_iff_exists_distinct_key xs hpw.2]
    constructor
    · rintro (hmem | ⟨e1, he1, e2, he2, hkne, ho⟩)
      · rcases List.mem_map.mp hmem with ⟨x, hx, hxo⟩
        exact ⟨e, List.mem_cons_self, x, List.mem_cons_of_mem e hx, hpw.1 x hx, hxo.symm⟩
      · exact ⟨e1, List.mem_cons_of_mem e he1, e2, List.mem_cons_of_mem e he2, hkne, ho⟩
    · rintro ⟨e1, he1, e2, he2, hkne, ho⟩
      rcases List.mem_cons.mp he1 with rfl | h1
      · rcases List.mem_cons.mp he2 with rfl | h2
        · exact absurd rfl hkne
        · exact Or.inl (List.mem_map.mpr ⟨e2, h2, ho.symm⟩)
      · rcases List.mem_cons.mp he2 with rfl | h2
        · exact Or.inl (List.mem_map.mpr ⟨e1, h1, ho⟩)
        · exact Or.inr ⟨e1, h1, e2, h2, hkne, ho⟩

private theorem sorted_rel_eq : ∀ (esa esb : List Entry),
    List.Pairwise (fun x y => x.order < y.order) esa →
    List.Pairwise (fun x y => x.order < y.order) esb →
    (∀ x ∈ esa, ∃ y ∈ esb, x.key = y.key ∧ x.order = y.order ∧
      Value.eqv (.record x.value) (.record y.value) = true) →
    (∀ y ∈ esb, ∃ x ∈ esa, x.key = y.key ∧ x.order = y.order ∧
      Value.eqv (.record x.value) (.record y.value) = true) →
    esa.length = esb.length ∧
    ∀ (i : Nat) (hia : i < esa.length) (hib : i < esb.length),
      (esa[i]'hia).key = (esb[i]'hib).key ∧
      (esa[i]'hia).order = (esb[i]'hib).order ∧
      Value.eqv (.record (esa[i]'hia).value) (.record (esb[i]'hib).value) = true
  | [], [], _, _, _, _ => ⟨rfl, fun _ hia _ => by cases hia⟩
  | [], y :: _, _, _, _, hbwd => by
    obtain ⟨x, hx, _⟩ := hbwd y List.mem_cons_self
    cases hx
  | x :: _, [], _, _, hfwd, _ => by
    obtain ⟨y, hy, _⟩ := hfwd x List.mem_cons_self
    cases hy
  | x :: xs, y :: ys, hpa, hpb, hfwd, hbwd => by
    simp only [List.pairwise_cons] at hpa hpb
    obtain ⟨y', hy', hxyk, hxyo, hxyv⟩ := hfwd x List.mem_cons_self
    obtain ⟨x', hx', hyxk, hyxo, _⟩ := hbwd y List.mem_cons_self
    have hylex : y.order ≤ x.order := by
      rcases List.mem_cons.mp hy' with rfl | hmem
      · omega
      · have := hpb.1 y' hmem
        omega
    have hxley : x.order ≤ y.order := by
      rcases List.mem_cons.mp hx' with rfl | hmem
      · omega
      · have := hpa.1 x' hmem
        omega
    have hxy_ord : x.order = y.order := by omega
    have hy'_eq : y' = y := by
      rcases List.mem_cons.mp hy' with rfl | hmem
      · rfl
      · have := hpb.1 y' hmem
        omega
    rw [hy'_eq] at hxyk hxyo hxyv
    have hfwd' : ∀ a ∈ xs, ∃ b ∈ ys, a.key = b.key ∧ a.order = b.order ∧
        Value.eqv (.record a.value) (.record b.value) = true := by
      intro a ha
      obtain ⟨b, hb, hkab, hoab, hvab⟩ := hfwd a (List.mem_cons_of_mem x ha)
      rcases List.mem_cons.mp hb with rfl | hmem
      · have := hpa.1 a ha
        omega
      · exact ⟨b, hmem, hkab, hoab, hvab⟩
    have hbwd' : ∀ b ∈ ys, ∃ a ∈ xs, a.key = b.key ∧ a.order = b.order ∧
        Value.eqv (.record a.value) (.record b.value) = true := by
      intro b hb
      obtain ⟨a, ha, hkab, hoab, hvab⟩ := hbwd b (List.mem_cons_of_mem y hb)
      rcases List.mem_cons.mp ha with rfl | hmem
      · have := hpb.1 b hb
        omega
      · exact ⟨a, hmem, hkab, hoab, hvab⟩
    obtain ⟨ihlen, ihat⟩ := sorted_rel_eq xs ys hpa.2 hpb.2 hfwd' hbwd'
    refine ⟨by simp [ihlen], ?_⟩
    intro i hia hib
    cases i with
    | zero => exact ⟨hxyk, hxyo, hxyv⟩
    | succ k => exact ihat k (Nat.lt_of_succ_lt_succ hia) (Nat.lt_of_succ_lt_succ hib)

theorem entries_eqv_invariant (a b : Fields)
    (ha : (Value.record a).unique = true)
    (hb : (Value.record b).unique = true)
    (heqv : Value.eqv (.record a) (.record b) = true) :
    (∀ ea, entries a = .error ea ↔ entries b = .error ea) ∧
    (∀ esa esb, entries a = .ok esa → entries b = .ok esb →
      esa.length = esb.length ∧
      ∀ (i : Nat) (hia : i < esa.length) (hib : i < esb.length),
        (esa[i]'hia).key = (esb[i]'hib).key ∧
        (esa[i]'hia).order = (esb[i]'hib).order ∧
        Value.eqv (.record (esa[i]'hia).value) (.record (esb[i]'hib).value) = true) := by
  have haU := ha
  have hbU := hb
  simp only [unique_record, Bool.and_eq_true] at ha hb
  obtain ⟨hfwd_ab, hbwd_ab⟩ := record_eqv_get_iff ha.1 hb.1 heqv
  have hisSome_iff : (extractEntries a).isSome = true ↔ (extractEntries b).isSome = true := by
    rw [extractEntries_isSome_iff a ha.1, extractEntries_isSome_iff b hb.1]
    constructor
    · intro halla k w hbg
      obtain ⟨v, hag, hvw⟩ := hbwd_ab k w hbg
      obtain ⟨hvk, r1, ord, rfl, hord1, hin⟩ := halla k v hag
      obtain ⟨r2, rfl, hr_eqv⟩ := value_eqv_record_elim hvw
      have hr1U : (Value.record r1).unique = true := fields_unique_get a k (.record r1) ha.2 hag
      have hr2U : (Value.record r2).unique = true := fields_unique_get b k (.record r2) hb.2 hbg
      simp only [unique_record, Bool.and_eq_true] at hr1U hr2U
      obtain ⟨hfwd_r, _⟩ := record_eqv_get_iff hr1U.1 hr2U.1 hr_eqv
      obtain ⟨w', hord2, hord_eqv⟩ := hfwd_r orderField (.int ord) hord1
      have hw' : w' = .int ord := value_eqv_int_elim hord_eqv
      subst hw'
      exact ⟨hvk, r2, ord, rfl, hord2, hin⟩
    · intro hallb k v hag
      obtain ⟨w, hbg, hvw⟩ := hfwd_ab k v hag
      obtain ⟨hvk, r2, ord, rfl, hord2, hin⟩ := hallb k w hbg
      obtain ⟨r1, rfl, hr_eqv⟩ := value_eqv_record_elim_right hvw
      have hr1U : (Value.record r1).unique = true := fields_unique_get a k (.record r1) ha.2 hag
      have hr2U : (Value.record r2).unique = true := fields_unique_get b k (.record r2) hb.2 hbg
      simp only [unique_record, Bool.and_eq_true] at hr1U hr2U
      obtain ⟨_, hbwd_r⟩ := record_eqv_get_iff hr1U.1 hr2U.1 hr_eqv
      obtain ⟨v', hord1, hord_eqv⟩ := hbwd_r orderField (.int ord) hord2
      have hv' : v' = .int ord := value_eqv_int_elim_right hord_eqv
      subst hv'
      exact ⟨hvk, r1, ord, rfl, hord1, hin⟩
  cases hxa : extractEntries a with
  | none =>
    have hxb : extractEntries b = none := by
      cases hxb' : extractEntries b with
      | none => rfl
      | some _ =>
        have h := hisSome_iff.mpr (by simp [hxb'])
        simp [hxa] at h
    have hea : entries a = .error .malformed := by simp [entries, haU, hxa]
    have heb : entries b = .error .malformed := by simp [entries, hbU, hxb]
    refine ⟨fun ea => by simp [hea, heb], fun _ _ h1 _ => by rw [hea] at h1; cases h1⟩
  | some rawa =>
    cases hxb : extractEntries b with
    | none =>
      have h := hisSome_iff.mp (by simp [hxa])
      simp [hxb] at h
    | some rawb =>
      obtain ⟨hpwa, hsounda⟩ := extractEntries_sound a rawa ha.1 hxa
      obtain ⟨hpwb, hsoundb⟩ := extractEntries_sound b rawb hb.1 hxb
      have hmatch_fwd : ∀ x ∈ rawa, ∃ y ∈ rawb, x.key = y.key ∧ x.order = y.order ∧
          Value.eqv (.record x.value) (.record y.value) = true := by
        intro x hx
        obtain ⟨_, _, r1, hag, hord1, hval1⟩ := hsounda x hx
        obtain ⟨w, hbg, hvw⟩ := hfwd_ab x.key (.record r1) hag
        obtain ⟨r2, rfl, hr_eqv⟩ := value_eqv_record_elim hvw
        obtain ⟨y, hy, r2', hyk, hreq, hord2, hval2⟩ :=
          extractEntries_get_mem b rawb hb.1 hxb x.key (.record r2) hbg
        cases hreq
        have hr1U : (Value.record r1).unique = true := fields_unique_get a x.key (.record r1) ha.2 hag
        have hr2U : (Value.record r2).unique = true := fields_unique_get b x.key (.record r2) hb.2 hbg
        simp only [unique_record, Bool.and_eq_true] at hr1U hr2U
        obtain ⟨hfwd_r, _⟩ := record_eqv_get_iff hr1U.1 hr2U.1 hr_eqv
        obtain ⟨w', hord2', hord_eqv⟩ := hfwd_r orderField (.int x.order) hord1
        rw [hord2] at hord2'
        cases hord2'
        have hord_eq : y.order = x.order := Value.int.inj (value_eqv_int_elim hord_eqv)
        refine ⟨y, hy, hyk.symm, hord_eq.symm, ?_⟩
        rw [hval1, hval2]
        exact stripField_eqv orderField hr1U.1 hr2U.1 hr_eqv
      have hmatch_bwd : ∀ y ∈ rawb, ∃ x ∈ rawa, x.key = y.key ∧ x.order = y.order ∧
          Value.eqv (.record x.value) (.record y.value) = true := by
        intro y hy
        obtain ⟨_, _, r2, hbg, hord2, hval2⟩ := hsoundb y hy
        obtain ⟨v, hag, hvw⟩ := hbwd_ab y.key (.record r2) hbg
        obtain ⟨r1, rfl, hr_eqv⟩ := value_eqv_record_elim_right hvw
        obtain ⟨x, hx, r1', hxk, hreq, hord1, hval1⟩ :=
          extractEntries_get_mem a rawa ha.1 hxa y.key (.record r1) hag
        cases hreq
        have hr1U : (Value.record r1).unique = true := fields_unique_get a y.key (.record r1) ha.2 hag
        have hr2U : (Value.record r2).unique = true := fields_unique_get b y.key (.record r2) hb.2 hbg
        simp only [unique_record, Bool.and_eq_true] at hr1U hr2U
        obtain ⟨_, hbwd_r⟩ := record_eqv_get_iff hr1U.1 hr2U.1 hr_eqv
        obtain ⟨v', hord1', hord_eqv⟩ := hbwd_r orderField (.int y.order) hord2
        rw [hord1] at hord1'
        cases hord1'
        have hord_eq : x.order = y.order := Value.int.inj (value_eqv_int_elim_right hord_eqv)
        refine ⟨x, hx, hxk, hord_eq, ?_⟩
        rw [hval1, hval2]
        exact stripField_eqv orderField hr1U.1 hr2U.1 hr_eqv
      have hdup_iff : hasDup (rawa.map Entry.order) = true ↔ hasDup (rawb.map Entry.order) = true := by
        rw [hasDup_order_iff_exists_distinct_key rawa hpwa,
          hasDup_order_iff_exists_distinct_key rawb hpwb]
        constructor
        · rintro ⟨x1, hx1, x2, hx2, hkne, ho⟩
          obtain ⟨y1, hy1, hk1, ho1, _⟩ := hmatch_fwd x1 hx1
          obtain ⟨y2, hy2, hk2, ho2, _⟩ := hmatch_fwd x2 hx2
          exact ⟨y1, hy1, y2, hy2, by rw [← hk1, ← hk2]; exact hkne, by rw [← ho1, ← ho2]; exact ho⟩
        · rintro ⟨y1, hy1, y2, hy2, hkne, ho⟩
          obtain ⟨x1, hx1, hk1, ho1, _⟩ := hmatch_bwd y1 hy1
          obtain ⟨x2, hx2, hk2, ho2, _⟩ := hmatch_bwd y2 hy2
          exact ⟨x1, hx1, x2, hx2, by rw [hk1, hk2]; exact hkne, by rw [ho1, ho2]; exact ho⟩
      cases hda : hasDup (rawa.map Entry.order) with
      | true =>
        have hdb : hasDup (rawb.map Entry.order) = true := hdup_iff.mp hda
        have hea : entries a = .error .duplicateOrder := by simp [entries, haU, hxa, hda]
        have heb : entries b = .error .duplicateOrder := by simp [entries, hbU, hxb, hdb]
        refine ⟨fun ea => by simp [hea, heb], fun _ _ h1 _ => by rw [hea] at h1; cases h1⟩
      | false =>
        have hdb : hasDup (rawb.map Entry.order) = false := by
          cases hdb' : hasDup (rawb.map Entry.order) with
          | false => rfl
          | true =>
            have h := hdup_iff.mpr hdb'
            rw [hda] at h
            cases h
        have hea : entries a = .ok (sortEntries rawa) := by simp [entries, haU, hxa, hda]
        have heb : entries b = .ok (sortEntries rawb) := by simp [entries, hbU, hxb, hdb]
        refine ⟨fun ea => by simp [hea, heb], ?_⟩
        intro esa esb h1 h2
        rw [hea] at h1
        rw [heb] at h2
        cases h1
        cases h2
        have hpa_lt := sortEntries_pairwise_lt rawa ((hasDup_eq_false_iff _).mp hda)
        have hpb_lt := sortEntries_pairwise_lt rawb ((hasDup_eq_false_iff _).mp hdb)
        refine sorted_rel_eq (sortEntries rawa) (sortEntries rawb) hpa_lt hpb_lt ?_ ?_
        · intro x hx
          obtain ⟨y, hy, hxy⟩ := hmatch_fwd x ((mem_sortEntries x rawa).mp hx)
          exact ⟨y, (mem_sortEntries y rawb).mpr hy, hxy⟩
        · intro y hy
          obtain ⟨x, hx, hxy⟩ := hmatch_bwd y ((mem_sortEntries y rawb).mp hy)
          exact ⟨x, (mem_sortEntries x rawa).mpr hx, hxy⟩

private def expectedEntries (idx : Nat) : List Item → List Entry
  | [] => []
  | it :: rest =>
    { key := it.key, order := step * (Int.ofNat idx + 1), value := it.value } ::
      expectedEntries (idx + 1) rest

private theorem expectedEntries_length (idx : Nat) : ∀ (items : List Item),
    (expectedEntries idx items).length = items.length
  | [] => rfl
  | _ :: rest => by simp [expectedEntries, expectedEntries_length (idx + 1) rest]

private theorem expectedEntries_getElem (idx : Nat) : ∀ (items : List Item)
    (i : Nat) (hi : i < items.length) (he : i < (expectedEntries idx items).length),
    ((expectedEntries idx items)[i]'he).key = (items[i]'hi).key ∧
    ((expectedEntries idx items)[i]'he).order = step * (Int.ofNat (idx + i) + 1) ∧
    ((expectedEntries idx items)[i]'he).value = (items[i]'hi).value
  | [], _, hi, _ => by cases hi
  | it :: rest, 0, _, _ => by simp [expectedEntries]
  | it :: rest, i + 1, hi, he => by
    have ih := expectedEntries_getElem (idx + 1) rest i
      (Nat.lt_of_succ_lt_succ hi) (Nat.lt_of_succ_lt_succ he)
    simp only [expectedEntries, List.getElem_cons_succ] at ih ⊢
    refine ⟨ih.1, ?_, ih.2.2⟩
    rw [ih.2.1]
    have hidx : idx + 1 + i = idx + (i + 1) := by omega
    rw [hidx]

private theorem inInt64_eq_true (n : Int) :
    inInt64 n = true ↔ minInt64 ≤ n ∧ n ≤ maxInt64 := by
  simp [inInt64]

private theorem expectedEntries_mem_order_gt (idx : Nat) : ∀ (items : List Item) (e : Entry),
    e ∈ expectedEntries (idx + 1) items → step * (Int.ofNat idx + 1) < e.order
  | [], _, he => by cases he
  | _ :: rest, e, he => by
    simp only [expectedEntries, List.mem_cons] at he
    rcases he with rfl | hmem
    · simp only [step, Int.ofNat_eq_natCast, Int.natCast_add, Int.natCast_one]
      omega
    · have ih := expectedEntries_mem_order_gt (idx + 1) rest e hmem
      simp only [step, Int.ofNat_eq_natCast, Int.natCast_add, Int.natCast_one] at ih ⊢
      omega

private theorem expectedEntries_pairwise_lt (idx : Nat) : ∀ (items : List Item),
    List.Pairwise (fun a b => a.order < b.order) (expectedEntries idx items)
  | [] => List.Pairwise.nil
  | _ :: rest =>
    List.pairwise_cons.mpr
      ⟨expectedEntries_mem_order_gt idx rest, expectedEntries_pairwise_lt (idx + 1) rest⟩

private theorem buildFromItems_get_none (idx : Nat) : ∀ (items : List Item) (k : String),
    k ∉ items.map Item.key → (buildFromItems idx items).get k = none
  | [], _, _ => rfl
  | it :: rest, k, hnot => by
    simp only [List.map_cons, List.mem_cons, not_or] at hnot
    simp only [buildFromItems, Fields.get, Ne.symm hnot.1, ite_false]
    exact buildFromItems_get_none (idx + 1) rest k hnot.2

private theorem buildFromItems_unique (idx : Nat) : ∀ (items : List Item),
    (∀ it ∈ items, (Value.record it.value).unique = true) →
    (∀ it ∈ items, (it.value.get orderField).isNone = true) →
    (items.map Item.key).Nodup →
    (Value.record (buildFromItems idx items)).unique = true
  | [], _, _, _ => rfl
  | it :: rest, hu, hno, hnd => by
    simp only [List.map_cons, List.nodup_cons] at hnd
    have ih := buildFromItems_unique (idx + 1) rest
      (fun x hx => hu x (List.mem_cons_of_mem it hx))
      (fun x hx => hno x (List.mem_cons_of_mem it hx))
      hnd.2
    simp only [unique_record, Bool.and_eq_true] at ih ⊢
    have hitU := hu it List.mem_cons_self
    have hitNo := hno it List.mem_cons_self
    simp only [unique_record, Bool.and_eq_true] at hitU
    simp only [buildFromItems, Fields.keysDistinct, Fields.unique, Bool.and_eq_true, Value.unique]
    refine ⟨⟨?_, ih.1⟩, ⟨⟨hitNo, hitU.1⟩, ⟨trivial, hitU.2⟩⟩, ih.2⟩
    rw [buildFromItems_get_none (idx + 1) rest it.key hnd.1]
    rfl

private theorem extractEntries_buildFromItems (idx : Nat) : ∀ (items : List Item),
    (∀ it ∈ items, validKey it.key = true) →
    (∀ it ∈ items, (it.value.get orderField).isNone = true) →
    step * Int.ofNat (idx + items.length) ≤ maxInt64 →
    extractEntries (buildFromItems idx items) = some (expectedEntries idx items)
  | [], _, _, _ => rfl
  | it :: rest, hvk, hno, hbound => by
    have hvk_it := hvk it List.mem_cons_self
    have hno_it := hno it List.mem_cons_self
    have hnone_it : it.value.get orderField = none := by
      cases hg : it.value.get orderField with
      | none => rfl
      | some _ =>
        rw [hg] at hno_it
        simp at hno_it
    have hbound_rest : step * Int.ofNat (idx + 1 + rest.length) ≤ maxInt64 := by
      simp only [List.length_cons] at hbound
      have heq : idx + 1 + rest.length = idx + (rest.length + 1) := by omega
      rw [heq]
      exact hbound
    have ih := extractEntries_buildFromItems (idx + 1) rest
      (fun x hx => hvk x (List.mem_cons_of_mem it hx))
      (fun x hx => hno x (List.mem_cons_of_mem it hx))
      hbound_rest
    have hin : inInt64 (step * (Int.ofNat idx + 1)) = true := by
      rw [inInt64_eq_true]
      simp only [minInt64, maxInt64, step, List.length_cons, Int.ofNat_eq_natCast,
        Int.natCast_add, Int.natCast_one] at hbound ⊢
      exact ⟨by omega, by omega⟩
    simp only [buildFromItems, extractEntries, hvk_it, Bool.not_true, Bool.false_eq_true, ite_false,
      Fields.get, ite_true, hin, ih, stripField, stripField_of_get_none it.value orderField hnone_it,
      expectedEntries]

theorem fromItems_entries_roundtrip (items : List Item) (set : Fields)
    (h : fromItems items = .ok set) :
    (Value.record set).unique = true ∧
    ∃ es, entries set = .ok es ∧ es.length = items.length ∧
      ∀ (i : Nat) (hi : i < items.length) (he : i < es.length),
        (es[i]'he).key = (items[i]'hi).key ∧
        (es[i]'he).order = step * (Int.ofNat i + 1) ∧
        (es[i]'he).value = (items[i]'hi).value := by
  unfold fromItems at h
  split at h
  · cases h
  · rename_i h1
    split at h
    · cases h
    · rename_i h2
      split at h
      · cases h
      · rename_i h3
        split at h
        · cases h
        · rename_i h4
          cases h
          have hall1 : ∀ it ∈ items, validKey it.key = true ∧ (Value.record it.value).unique = true := by
            have hall : items.all (fun it => validKey it.key && (Value.record it.value).unique) = true := by
              simpa using h1
            intro it hit
            have := List.all_eq_true.mp hall it hit
            simpa [Bool.and_eq_true] using this
          have hall2 : ∀ it ∈ items, (it.value.get orderField).isNone = true := by
            have hall : items.all (fun it => (it.value.get orderField).isNone) = true := by
              simpa using h2
            exact List.all_eq_true.mp hall
          have hnd : (items.map Item.key).Nodup :=
            (hasDup_eq_false_iff _).mp (by simpa using h3)
          have hbound : step * Int.ofNat (0 + items.length) ≤ maxInt64 := by
            simp only [Nat.zero_add]
            simpa using h4
          have huniq : (Value.record (buildFromItems 0 items)).unique = true :=
            buildFromItems_unique 0 items (fun it hit => (hall1 it hit).2) hall2 hnd
          have hextract := extractEntries_buildFromItems 0 items
            (fun it hit => (hall1 it hit).1) hall2 hbound
          have hpw := expectedEntries_pairwise_lt 0 items
          have hnoDupOrd : hasDup ((expectedEntries 0 items).map Entry.order) = false :=
            (hasDup_eq_false_iff _).mpr (nodup_orders_of_pairwise_lt _ hpw)
          have hsort := sortEntries_of_pairwise_lt (expectedEntries 0 items) hpw
          have hentries : entries (buildFromItems 0 items) = .ok (expectedEntries 0 items) := by
            simp [entries, huniq, hextract, hnoDupOrd, hsort]
          refine ⟨huniq, expectedEntries 0 items, hentries, expectedEntries_length 0 items, ?_⟩
          intro i hi he
          simpa using expectedEntries_getElem 0 items i hi he

private theorem extractKeyedItems_props (keyField : String) :
    ∀ (elems : List Fields) (items : List Item),
    extractKeyedItems keyField elems = some items →
    items.length = elems.length ∧
    ∀ (i : Nat) (hi : i < elems.length) (hit : i < items.length),
      (Value.record (elems[i]'hi)).unique = true ∧
      (elems[i]'hi).get keyField = some (.str (items[i]'hit).key) ∧
      (items[i]'hit).value = stripField (elems[i]'hi) keyField
  | [], items, h => by
    simp only [extractKeyedItems, Option.some.injEq] at h
    subst h
    exact ⟨rfl, fun _ hi _ => by cases hi⟩
  | el :: rest, items, h => by
    simp only [extractKeyedItems] at h
    split at h
    · cases h
    · rename_i hu
      have hu' : (Value.record el).unique = true := by simpa using hu
      split at h
      · rename_i k hk
        split at h
        · cases h
        · split at h
          · rename_i tail htail
            cases h
            obtain ⟨ihlen, ihat⟩ := extractKeyedItems_props keyField rest tail htail
            refine ⟨by simp [ihlen], ?_⟩
            intro i hi hit
            cases i with
            | zero => exact ⟨hu', hk, rfl⟩
            | succ j =>
              exact ihat j (Nat.lt_of_succ_lt_succ hi) (Nat.lt_of_succ_lt_succ hit)
          · cases h
      · cases h

theorem keyed_slice_roundtrip (elems : List Fields) (keyField : String) (set : Fields)
    (h : fromKeyedSlice elems keyField = .ok set) :
    (Value.record set).unique = true ∧
    ∃ out, toKeyedSlice set keyField = .ok out ∧ out.length = elems.length ∧
      ∀ (i : Nat) (hi : i < elems.length) (ho : i < out.length),
        Value.eqv (.record (out[i]'ho)) (.record (elems[i]'hi)) = true := by
  unfold fromKeyedSlice at h
  split at h
  · cases h
  · rename_i hvkf
    have hvkf' : validKeyField keyField = true := by simpa using hvkf
    cases hx : extractKeyedItems keyField elems with
    | none =>
      rw [hx] at h
      cases h
    | some items =>
      rw [hx] at h
      obtain ⟨huniq, es, hentries, hes_len, hes_at⟩ := fromItems_entries_roundtrip items set h
      obtain ⟨hitems_len, hitems_at⟩ := extractKeyedItems_props keyField elems items hx
      have hnoKey : es.all (fun e => (e.value.get keyField).isNone) = true := by
        apply List.all_eq_true.mpr
        intro e he
        obtain ⟨i, hi_es, rfl⟩ := List.getElem_of_mem he
        have hi_it : i < items.length := hes_len ▸ hi_es
        have hi_el : i < elems.length := hitems_len ▸ hi_it
        have hval_eq := (hes_at i hi_it hi_es).2.2
        have hit_val := (hitems_at i hi_el hi_it).2.2
        rw [hval_eq, hit_val, stripField_get_self]
        rfl
      let out : List Fields := es.map (fun e => Fields.cons keyField (.str e.key) e.value)
      have hto : toKeyedSlice set keyField = .ok out := by
        simp [toKeyedSlice, hvkf', hentries, toKeyedSliceFromEntries, hnoKey, out]
      refine ⟨huniq, out, hto, by simp [out, hes_len, hitems_len], ?_⟩
      intro i hi ho
      have hi_es : i < es.length := by simpa [out] using ho
      have hi_it : i < items.length := hes_len ▸ hi_es
      obtain ⟨helU, helGet, hitVal⟩ := hitems_at i hi hi_it
      obtain ⟨hesKey, _, hesVal⟩ := hes_at i hi_it hi_es
      have hout_i : out[i]'ho = .cons keyField (.str (items[i]'hi_it).key)
          (stripField (elems[i]'hi) keyField) := by
        simp only [out, List.getElem_map, hesKey, hesVal, hitVal]
      rw [hout_i]
      exact cons_stripField_eqv helU helGet

theorem orderBetween_strict (lo hi : Int) :
    (∀ mid, orderBetween lo hi = .ok mid → lo < mid ∧ mid < hi ∧ inInt64 mid = true) ∧
    (inInt64 lo = true → inInt64 hi = true → lo < hi → hi - lo = 1 →
      orderBetween lo hi = .error .gapExhausted) := by
  constructor
  · intro mid h
    unfold orderBetween at h
    split at h
    · cases h
    · rename_i hin
      split at h
      · cases h
      · rename_i hge
        split at h
        · cases h
        · rename_i hgap
          cases h
          simp only [Bool.or_eq_true, Bool.not_eq_true', not_or] at hin
          have hlo : inInt64 lo = true := by simpa using hin.1
          have hhi : inInt64 hi = true := by simpa using hin.2
          simp only [inInt64_eq_true, minInt64, maxInt64] at hlo hhi ⊢
          simp only [decide_eq_true_eq] at hge hgap
          exact ⟨by omega, by omega, by omega, by omega⟩
  · intro hlo hhi hlt hdiff
    unfold orderBetween
    have h1 : (!inInt64 lo || !inInt64 hi) = false := by simp [hlo, hhi]
    have h2 : decide (lo ≥ hi) = false := by
      simp only [decide_eq_false_iff_not]
      omega
    have h3 : decide (lo = hi - 1) = true := by
      simp only [decide_eq_true_eq]
      omega
    simp [h1, h2, h3]

theorem renumber_preserves_order (set set' : Fields) (h : renumber set = .ok set') :
    (Value.record set').unique = true ∧
    ∃ es es', entries set = .ok es ∧ entries set' = .ok es' ∧ es'.length = es.length ∧
      ∀ (i : Nat) (he : i < es.length) (he' : i < es'.length),
        (es'[i]'he').key = (es[i]'he).key ∧
        (es'[i]'he').order = step * (Int.ofNat i + 1) ∧
        (es'[i]'he').value = (es[i]'he).value := by
  unfold renumber at h
  cases hes : entries set with
  | error err =>
    rw [hes] at h
    cases h
  | ok es =>
    rw [hes] at h
    simp only [bind, Except.bind] at h
    obtain ⟨huniq, es', hes', hlen, hat⟩ := fromItems_entries_roundtrip _ set' h
    refine ⟨huniq, es, es', rfl, hes', by simpa using hlen, ?_⟩
    intro i he he'
    have hit : i < (es.map (fun e => ({ key := e.key, value := e.value } : Item))).length := by
      simpa using he
    simpa using hat i hit he'

end Nayutar.OrderedSet
