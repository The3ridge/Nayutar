import Nayutar.Ownership

set_option autoImplicit false

namespace Nayutar.Ownership

open Nayutar.Envelope (Value Values Fields optEqv)
open Nayutar.Store (Store Rev Cmp Write KV)
open Kisage (Origin PermSet Mode SelectionKind ActionKind DecisionKind
  permNone permOwn permSetOwn permDeleteOwn permOwnResolve permRegular permAll
  requiredPermissions authorizeSelection)

/-! ### List, Path, Sorting, and Lookup Lemmas -/

private theorem any_eq_true_iff {α : Type} (p : α → Bool) : ∀ (l : List α),
    l.any p = true ↔ ∃ x ∈ l, p x = true
  | [] => by simp
  | x :: xs => by
    simp only [List.any_cons, Bool.or_eq_true, List.mem_cons]
    constructor
    · rintro (hx | hxs)
      · exact ⟨x, Or.inl rfl, hx⟩
      · obtain ⟨y, hy, hp⟩ := (any_eq_true_iff p xs).mp hxs
        exact ⟨y, Or.inr hy, hp⟩
    · rintro ⟨y, (rfl | hy), hp⟩
      · exact Or.inl hp
      · exact Or.inr ((any_eq_true_iff p xs).mpr ⟨y, hy, hp⟩)

private theorem all_eq_true_iff {α : Type} (p : α → Bool) : ∀ (l : List α),
    l.all p = true ↔ ∀ x ∈ l, p x = true
  | [] => by simp
  | x :: xs => by
    simp only [List.all_cons, Bool.and_eq_true, List.mem_cons]
    constructor
    · intro ⟨hx, hxs⟩ y hy
      rcases hy with rfl | hmem
      · exact hx
      · exact (all_eq_true_iff p xs).mp hxs y hmem
    · intro h
      exact ⟨h x (Or.inl rfl), (all_eq_true_iff p xs).mpr (fun y hy => h y (Or.inr hy))⟩

private theorem mem_filterMap_iff {α β : Type} (f : α → Option β) : ∀ (l : List α) (b : β),
    b ∈ l.filterMap f ↔ ∃ a ∈ l, f a = some b
  | [], _ => by simp [List.filterMap]
  | a :: rest, b => by
    cases ha : f a with
    | none => simp [List.filterMap, ha, mem_filterMap_iff f rest b]
    | some b' =>
      simp only [List.filterMap, ha, List.mem_cons, mem_filterMap_iff f rest b]
      constructor
      · rintro (rfl | ⟨a', ha', hf⟩)
        · exact ⟨a, Or.inl rfl, ha⟩
        · exact ⟨a', Or.inr ha', hf⟩
      · rintro ⟨a', (rfl | ha'), hf⟩
        · rw [ha] at hf
          cases hf
          exact Or.inl rfl
        · exact Or.inr ⟨a', ha', hf⟩

private theorem pairwise_map_of {α β : Type} {R : α → α → Prop} {S : β → β → Prop}
    (f : α → β) (hImpl : ∀ a b, R a b → S (f a) (f b)) :
    ∀ (l : List α), l.Pairwise R → (l.map f).Pairwise S
  | [], _ => by simp
  | x :: xs, hpw => by
    simp only [List.pairwise_cons] at hpw
    simp only [List.map_cons, List.pairwise_cons]
    refine ⟨?_, pairwise_map_of f hImpl xs hpw.2⟩
    intro y hy
    obtain ⟨a, ha, rfl⟩ := List.mem_map.mp hy
    exact hImpl x a (hpw.1 a ha)

private theorem pairwise_filterMap_of {α β : Type} {R : α → α → Prop} {S : β → β → Prop}
    (f : α → Option β) (hImpl : ∀ a₁ a₂ b₁ b₂, R a₁ a₂ → f a₁ = some b₁ → f a₂ = some b₂ → S b₁ b₂) :
    ∀ (l : List α), l.Pairwise R → (l.filterMap f).Pairwise S
  | [], _ => by simp [List.filterMap]
  | x :: xs, hpw => by
    simp only [List.pairwise_cons] at hpw
    cases hx : f x with
    | none =>
      simp only [List.filterMap, hx]
      exact pairwise_filterMap_of f hImpl xs hpw.2
    | some b =>
      simp only [List.filterMap, hx, List.pairwise_cons]
      refine ⟨?_, pairwise_filterMap_of f hImpl xs hpw.2⟩
      intro b' hb'
      obtain ⟨a, ha, hfa⟩ := (mem_filterMap_iff f xs b').mp hb'
      exact hImpl x a b b' (hpw.1 a ha) hx hfa

private theorem isPrefixOf_refl : ∀ (p : Path), isPrefixOf p p = true
  | [] => rfl
  | _ :: rest => by simp [isPrefixOf, isPrefixOf_refl rest]

private theorem isProperAncestor_irrefl (p : Path) : isProperAncestor p p = false := by
  unfold isProperAncestor
  simp

private theorem overlaps_refl (p : Path) : overlaps p p = true := by
  simp [overlaps]

private theorem overlaps_symm (a b : Path) : overlaps a b = overlaps b a := by
  unfold overlaps
  by_cases h : a = b
  · subst h; simp
  · have h1 : (a == b) = false := beq_eq_false_iff_ne.mpr h
    have h2 : (b == a) = false := beq_eq_false_iff_ne.mpr (Ne.symm h)
    simp [h1, h2, Bool.or_comm]

private theorem overlaps_false_implies_ne {a b : Path} (h : overlaps a b = false) : a ≠ b := by
  intro heq
  subst heq
  rw [overlaps_refl] at h
  cases h

private theorem isPrefixOf_append_key_eq : ∀ (pfx : Path) (k₁ k₂ : String) (s₁ s₂ : Path),
    isPrefixOf (pfx ++ [k₁] ++ s₁) (pfx ++ [k₂] ++ s₂) = true → k₁ = k₂
  | [], k₁, k₂, _, _, h => by
    simp only [List.nil_append, List.cons_append, isPrefixOf, Bool.and_eq_true, beq_iff_eq] at h
    exact h.1
  | _ :: rest, k₁, k₂, s₁, s₂, h => by
    simp only [List.cons_append, isPrefixOf, Bool.and_eq_true] at h
    exact isPrefixOf_append_key_eq rest k₁ k₂ s₁ s₂ h.2

private theorem overlaps_append_key_ne (pfx : Path) {k₁ k₂ : String} (s₁ s₂ : Path) (hne : k₁ ≠ k₂) :
    overlaps (pfx ++ [k₁] ++ s₁) (pfx ++ [k₂] ++ s₂) = false := by
  unfold overlaps isProperAncestor
  have h1 : isPrefixOf (pfx ++ [k₁] ++ s₁) (pfx ++ [k₂] ++ s₂) = false := by
    cases h : isPrefixOf (pfx ++ [k₁] ++ s₁) (pfx ++ [k₂] ++ s₂) with
    | false => rfl
    | true => exact absurd (isPrefixOf_append_key_eq pfx k₁ k₂ s₁ s₂ h) hne
  have h2 : isPrefixOf (pfx ++ [k₂] ++ s₂) (pfx ++ [k₁] ++ s₁) = false := by
    cases h : isPrefixOf (pfx ++ [k₂] ++ s₂) (pfx ++ [k₁] ++ s₁) with
    | false => rfl
    | true => exact absurd (isPrefixOf_append_key_eq pfx k₂ k₁ s₂ s₁ h) (Ne.symm hne)
  have heq : (pfx ++ [k₁] ++ s₁ == pfx ++ [k₂] ++ s₂) = false := by
    apply beq_eq_false_iff_ne.mpr
    intro he
    have hr := isPrefixOf_refl (pfx ++ [k₂] ++ s₂)
    rw [he] at h1
    rw [hr] at h1
    cases h1
  simp only [heq, h1, h2, Bool.false_or, Bool.and_false]

private theorem mem_insertSortedPath (p x : Path) : ∀ (l : List Path),
    x ∈ insertSortedPath p l ↔ x = p ∨ x ∈ l
  | [] => by simp [insertSortedPath]
  | q :: qs => by
    simp only [insertSortedPath]
    split
    · rename_i heq
      simp only [beq_iff_eq] at heq
      subst heq
      simp only [List.mem_cons]
      constructor
      · intro h; exact Or.inr h
      · rintro (rfl | h); exact Or.inl rfl; exact h
    · split
      · simp only [List.mem_cons]
      · simp only [List.mem_cons, mem_insertSortedPath p x qs]
        constructor
        · rintro (rfl | rfl | h)
          · exact Or.inr (Or.inl rfl)
          · exact Or.inl rfl
          · exact Or.inr (Or.inr h)
        · rintro (rfl | rfl | h)
          · exact Or.inr (Or.inl rfl)
          · exact Or.inl rfl
          · exact Or.inr (Or.inr h)

private theorem pairwise_ne_insertSortedPath (p : Path) : ∀ (l : List Path),
    p ∉ l → l.Pairwise (· ≠ ·) → (insertSortedPath p l).Pairwise (· ≠ ·)
  | [], _, _ => by simp [insertSortedPath]
  | q :: qs, hnot, hpw => by
    simp only [List.mem_cons, not_or] at hnot
    simp only [List.pairwise_cons] at hpw
    have hne : (p == q) = false := beq_eq_false_iff_ne.mpr hnot.1
    simp only [insertSortedPath, hne, Bool.false_eq_true, ↓reduceIte]
    split
    · simp only [List.pairwise_cons]
      refine ⟨?_, hpw.1, hpw.2⟩
      intro z hz
      rcases List.mem_cons.mp hz with rfl | hmem
      · exact hnot.1
      · intro heq; subst heq; exact hnot.2 hmem
    · simp only [List.pairwise_cons]
      refine ⟨?_, pairwise_ne_insertSortedPath p qs hnot.2 hpw.2⟩
      intro z hz
      rcases (mem_insertSortedPath p z qs).mp hz with rfl | hmem
      · exact Ne.symm hnot.1
      · exact hpw.1 z hmem

private theorem pairwise_ne_sortDedupPaths : ∀ (l : List Path),
    (sortDedupPaths l).Pairwise (· ≠ ·)
  | [] => by simp [sortDedupPaths]
  | p :: ps => by
    simp only [sortDedupPaths]
    apply pairwise_ne_insertSortedPath
    · intro hmem
      rw [List.mem_filter] at hmem
      simp at hmem
    · exact (pairwise_ne_sortDedupPaths ps).sublist List.filter_sublist

private theorem mem_sortDedupPaths (x : Path) : ∀ (l : List Path),
    x ∈ sortDedupPaths l ↔ x ∈ l
  | [] => by simp [sortDedupPaths]
  | p :: ps => by
    simp only [sortDedupPaths, mem_insertSortedPath, List.mem_filter, bne_iff_ne,
      mem_sortDedupPaths x ps, List.mem_cons]
    constructor
    · rintro (rfl | ⟨hx, _⟩)
      · exact Or.inl rfl
      · exact Or.inr hx
    · rintro (rfl | hx)
      · exact Or.inl rfl
      · by_cases heq : x = p
        · exact Or.inl heq
        · exact Or.inr ⟨hx, heq⟩

private theorem mem_removePath {α : Type} (p : Path) (x : Path × α) : ∀ (l : List (Path × α)),
    x ∈ removePath l p ↔ x ∈ l ∧ x.1 ≠ p
  | [] => by simp [removePath]
  | (q, v) :: rest => by
    simp only [removePath]
    split
    · rename_i heq
      simp only [beq_iff_eq] at heq
      subst heq
      simp only [mem_removePath q x rest, List.mem_cons]
      constructor
      · rintro ⟨hm, hne⟩; exact ⟨Or.inr hm, hne⟩
      · rintro ⟨rfl | hm, hne⟩
        · exact absurd rfl hne
        · exact ⟨hm, hne⟩
    · rename_i hne
      simp only [beq_iff_eq] at hne
      simp only [List.mem_cons, mem_removePath p x rest]
      constructor
      · rintro (rfl | ⟨hm, hxp⟩)
        · exact ⟨Or.inl rfl, hne⟩
        · exact ⟨Or.inr hm, hxp⟩
      · rintro ⟨rfl | hm, hxp⟩
        · exact Or.inl rfl
        · exact Or.inr ⟨hm, hxp⟩

private theorem pairwise_removePath {α : Type} {R : (Path × α) → (Path × α) → Prop} (p : Path) :
    ∀ (l : List (Path × α)), l.Pairwise R → (removePath l p).Pairwise R
  | [], _ => by simp [removePath]
  | (q, v) :: rest, hpw => by
    simp only [List.pairwise_cons] at hpw
    simp only [removePath]
    split
    · exact pairwise_removePath p rest hpw.2
    · simp only [List.pairwise_cons]
      refine ⟨fun x hx => hpw.1 x ((mem_removePath p x rest).mp hx).1,
        pairwise_removePath p rest hpw.2⟩

private theorem mem_insertSortedPair {α : Type} (p : Path) (v : α) (x : Path × α) :
    ∀ (l : List (Path × α)), (∀ w, (p, w) ∉ l) →
    (x ∈ insertSortedPair p v l ↔ x = (p, v) ∨ x ∈ l)
  | [], _ => by simp [insertSortedPair]
  | (q, w) :: rest, hnot => by
    have hpq : p ≠ q := fun heq => hnot w (heq ▸ List.mem_cons_self)
    have hbeq : (p == q) = false := beq_eq_false_iff_ne.mpr hpq
    have hnot' : ∀ u, (p, u) ∉ rest := fun u hu => hnot u (List.mem_cons_of_mem _ hu)
    simp only [insertSortedPair, hbeq, Bool.false_eq_true, ↓reduceIte]
    split
    · simp only [List.mem_cons]
    · simp only [List.mem_cons, mem_insertSortedPair p v x rest hnot']
      constructor
      · rintro (rfl | rfl | hm)
        · exact Or.inr (Or.inl rfl)
        · exact Or.inl rfl
        · exact Or.inr (Or.inr hm)
      · rintro (rfl | rfl | hm)
        · exact Or.inr (Or.inl rfl)
        · exact Or.inl rfl
        · exact Or.inr (Or.inr hm)

private theorem pairwise_insertSortedPair {α : Type} {R : (Path × α) → (Path × α) → Prop}
    (hSym : ∀ a b, R a b → R b a) (p : Path) (v : α) :
    ∀ (l : List (Path × α)),
    (∀ w, (p, w) ∉ l) →
    (∀ x ∈ l, R (p, v) x) →
    l.Pairwise R →
    (insertSortedPair p v l).Pairwise R
  | [], _, _, _ => by simp [insertSortedPair]
  | (q, w) :: rest, hnot, hall, hpw => by
    have hpq : p ≠ q := fun heq => hnot w (heq ▸ List.mem_cons_self)
    have hbeq : (p == q) = false := beq_eq_false_iff_ne.mpr hpq
    have hnot' : ∀ u, (p, u) ∉ rest := fun u hu => hnot u (List.mem_cons_of_mem _ hu)
    simp only [List.pairwise_cons] at hpw
    simp only [insertSortedPair, hbeq, Bool.false_eq_true, ↓reduceIte]
    split
    · simp only [List.pairwise_cons]
      exact ⟨hall, hpw.1, hpw.2⟩
    · simp only [List.pairwise_cons]
      refine ⟨?_, pairwise_insertSortedPair hSym p v rest hnot'
        (fun x hx => hall x (List.mem_cons_of_mem _ hx)) hpw.2⟩
      intro x hx
      rcases (mem_insertSortedPair p v x rest hnot').mp hx with rfl | hmem
      · exact hSym _ _ (hall (q, w) List.mem_cons_self)
      · exact hpw.1 x hmem

private theorem mem_sortPairs_of_distinct {α : Type} : ∀ (l : List (Path × α)) (x : Path × α),
    l.Pairwise (fun a b => a.1 ≠ b.1) → (x ∈ sortPairs l ↔ x ∈ l)
  | [], _, _ => by simp [sortPairs]
  | (p, v) :: rest, x, hpw => by
    simp only [List.pairwise_cons] at hpw
    simp only [sortPairs]
    have hnot : ∀ w, (p, w) ∉ removePath (sortPairs rest) p := fun w hw =>
      ((mem_removePath p (p, w) (sortPairs rest)).mp hw).2 rfl
    rw [mem_insertSortedPair p v x _ hnot, mem_removePath, mem_sortPairs_of_distinct rest x hpw.2]
    simp only [List.mem_cons]
    constructor
    · rintro (rfl | ⟨hm, _⟩)
      · exact Or.inl rfl
      · exact Or.inr hm
    · rintro (rfl | hm)
      · exact Or.inl rfl
      · exact Or.inr ⟨hm, Ne.symm (hpw.1 x hm)⟩

private theorem pairwise_sortPairs_of_no_overlap {α : Type} : ∀ (l : List (Path × α)),
    l.Pairwise (fun a b => overlaps a.1 b.1 = false) →
    (sortPairs l).Pairwise (fun a b => overlaps a.1 b.1 = false)
  | [], _ => by simp [sortPairs]
  | (p, v) :: rest, hpw => by
    simp only [List.pairwise_cons] at hpw
    have hDist : rest.Pairwise (fun a b => a.1 ≠ b.1) :=
      hpw.2.imp (fun h => overlaps_false_implies_ne h)
    simp only [sortPairs]
    apply pairwise_insertSortedPair (fun a b h => by rw [overlaps_symm]; exact h) p v
    · intro w hw; exact ((mem_removePath p (p, w) (sortPairs rest)).mp hw).2 rfl
    · intro x hx
      have hmem := (mem_sortPairs_of_distinct rest x hDist).mp ((mem_removePath p x _).mp hx).1
      exact hpw.1 x hmem
    · exact pairwise_removePath p _ (pairwise_sortPairs_of_no_overlap rest hpw.2)

private theorem lookupPath_mem {α : Type} : ∀ (l : List (Path × α)) (p : Path) (v : α),
    lookupPath l p = some v → (p, v) ∈ l
  | [], _, _, h => by simp [lookupPath] at h
  | (q, w) :: rest, p, v, h => by
    simp only [lookupPath] at h
    split at h
    · rename_i heq
      simp only [beq_iff_eq] at heq
      cases h
      subst heq
      exact List.mem_cons_self
    · exact List.mem_cons_of_mem _ (lookupPath_mem rest p v h)

private theorem mem_lookupPath_of_pairwise {α : Type} : ∀ (l : List (Path × α)) (p : Path) (v : α),
    l.Pairwise (fun a b => a.1 ≠ b.1) → (p, v) ∈ l → lookupPath l p = some v
  | [], _, _, _, h => by cases h
  | (q, w) :: rest, p, v, hpw, hm => by
    simp only [List.pairwise_cons] at hpw
    rcases List.mem_cons.mp hm with heq | hmem
    · cases heq
      simp [lookupPath]
    · have hne : q ≠ p := hpw.1 (p, v) hmem
      have hbeq : (q == p) = false := beq_eq_false_iff_ne.mpr hne
      simp only [lookupPath, hbeq, Bool.false_eq_true, ↓reduceIte]
      exact mem_lookupPath_of_pairwise rest p v hpw.2 hmem

private theorem lookupPath_removePath_self {α : Type} : ∀ (l : List (Path × α)) (p : Path),
    lookupPath (removePath l p) p = none
  | [], _ => rfl
  | (q, v) :: rest, p => by
    simp only [removePath]
    split
    · exact lookupPath_removePath_self rest p
    · rename_i hne
      simp only [lookupPath, hne, Bool.false_eq_true, ↓reduceIte]
      exact lookupPath_removePath_self rest p

private theorem lookupPath_removePath_ne {α : Type} : ∀ (l : List (Path × α)) {p k : Path},
    k ≠ p → lookupPath (removePath l p) k = lookupPath l k
  | [], _, _, _ => rfl
  | (q, v) :: rest, p, k, hne => by
    simp only [removePath]
    split
    · rename_i hqp
      simp only [beq_iff_eq] at hqp
      subst hqp
      have hqk : (q == k) = false := beq_eq_false_iff_ne.mpr (Ne.symm hne)
      simp only [lookupPath, hqk, Bool.false_eq_true, ↓reduceIte]
      exact lookupPath_removePath_ne rest hne
    · simp only [lookupPath]
      split
      · rfl
      · exact lookupPath_removePath_ne rest hne

private theorem lookupPath_insertSortedPair_self {α : Type} : ∀ (l : List (Path × α)) (p : Path) (v : α),
    lookupPath (insertSortedPair p v l) p = some v
  | [], p, v => by simp [insertSortedPair, lookupPath]
  | (q, w) :: rest, p, v => by
    simp only [insertSortedPair]
    split
    · simp [lookupPath]
    · rename_i hne
      simp only [beq_iff_eq] at hne
      split
      · simp [lookupPath]
      · have hqp : (q == p) = false := beq_eq_false_iff_ne.mpr (Ne.symm hne)
        simp only [lookupPath, hqp, Bool.false_eq_true, ↓reduceIte]
        exact lookupPath_insertSortedPair_self rest p v

private theorem lookupPath_insertSortedPair_ne {α : Type} : ∀ (l : List (Path × α)) {p k : Path} (v : α),
    k ≠ p → lookupPath (insertSortedPair p v l) k = lookupPath l k
  | [], p, k, v, hne => by
    have hpk : (p == k) = false := beq_eq_false_iff_ne.mpr (Ne.symm hne)
    simp [insertSortedPair, lookupPath, hpk]
  | (q, w) :: rest, p, k, v, hne => by
    have hpk : (p == k) = false := beq_eq_false_iff_ne.mpr (Ne.symm hne)
    simp only [insertSortedPair]
    split
    · rename_i hpq
      simp only [beq_iff_eq] at hpq
      subst hpq
      simp [lookupPath, hpk]
    · split
      · simp [lookupPath, hpk]
      · simp only [lookupPath]
        split
        · rfl
        · exact lookupPath_insertSortedPair_ne rest v hne

private theorem ownerState_get_set_self (ow : OwnerState) (p : Path) (st : LeafStatus) :
    (ow.set p st).get p = some st :=
  lookupPath_insertSortedPair_self _ p st

private theorem ownerState_get_set_ne (ow : OwnerState) {p k : Path} (st : LeafStatus) (hne : k ≠ p) :
    (ow.set p st).get k = ow.get k := by
  unfold OwnerState.set OwnerState.get
  rw [lookupPath_insertSortedPair_ne _ st hne, lookupPath_removePath_ne _ hne]

private theorem ownerState_get_remove_self (ow : OwnerState) (p : Path) :
    (ow.remove p).get p = none :=
  lookupPath_removePath_self _ p

private theorem ownerState_get_remove_ne (ow : OwnerState) {p k : Path} (hne : k ≠ p) :
    (ow.remove p).get k = ow.get k :=
  lookupPath_removePath_ne _ hne

private theorem mem_ownerState_set (ow : OwnerState) (p : Path) (st : LeafStatus) (x : Path × LeafStatus) :
    x ∈ (ow.set p st).entries ↔ x = (p, st) ∨ (x ∈ ow.entries ∧ x.1 ≠ p) := by
  unfold OwnerState.set
  have hnot : ∀ w, (p, w) ∉ removePath ow.entries p := fun w hw =>
    ((mem_removePath p (p, w) ow.entries).mp hw).2 rfl
  rw [mem_insertSortedPair p st x _ hnot, mem_removePath]

private theorem mem_ownerState_remove (ow : OwnerState) (p : Path) (x : Path × LeafStatus) :
    x ∈ (ow.remove p).entries ↔ x ∈ ow.entries ∧ x.1 ≠ p :=
  mem_removePath p x ow.entries

private theorem pairwise_ownerState_set (ow : OwnerState) (p : Path) (st : LeafStatus)
    (hpw : ow.entries.Pairwise (fun a b => a.1 ≠ b.1)) :
    (ow.set p st).entries.Pairwise (fun a b => a.1 ≠ b.1) := by
  unfold OwnerState.set
  apply pairwise_insertSortedPair (fun _ _ h => Ne.symm h) p st
  · intro w hw; exact ((mem_removePath p (p, w) ow.entries).mp hw).2 rfl
  · intro x hx; exact Ne.symm ((mem_removePath p x ow.entries).mp hx).2
  · exact pairwise_removePath p ow.entries hpw

private theorem pairwise_ownerState_remove (ow : OwnerState) (p : Path)
    (hpw : ow.entries.Pairwise (fun a b => a.1 ≠ b.1)) :
    (ow.remove p).entries.Pairwise (fun a b => a.1 ≠ b.1) :=
  pairwise_removePath p ow.entries hpw

/-! ### Part 1: Plan Safety Theorems (Theorems 1–9) -/

private theorem authorizeSelection_eq_authorized
    (sel : SelectionKind) (reqPerms granted : PermSet)
    (hAuth : authorizeSelection sel reqPerms granted = .authorized) :
    sel = .candidate ∧ PermSet.allows granted reqPerms = true := by
  unfold authorizeSelection at hAuth
  cases sel with
  | noOp | deferred | blocker => contradiction
  | candidate =>
    by_cases hp : PermSet.allows granted reqPerms = true
    · exact ⟨rfl, hp⟩
    · simp [hp] at hAuth

private theorem buildDocOp_not_authorized (p : Path) (dec : DecisionKind) (act : ActionKind)
    (decl : Option Value) (obs : Obs) (h : dec ≠ .authorized) :
    buildDocOp p dec act decl obs = none := by
  cases dec <;> simp_all [buildDocOp]

private theorem buildOwnerOp_not_authorized (p : Path) (a : ActorID) (dec : DecisionKind)
    (act : ActionKind) (st : Option LeafStatus) (obs : Obs) (h : dec ≠ .authorized) :
    buildOwnerOp p a dec act st obs = none := by
  cases dec <;> simp_all [buildOwnerOp]

private theorem buildOps_some_implies_authorized (p : Path) (a : ActorID) (dec : DecisionKind)
    (act : ActionKind) (decl : Option Value) (st : Option LeafStatus) (obs : Obs)
    (h : (buildDocOp p dec act decl obs).isSome = true ∨
         (buildOwnerOp p a dec act st obs).isSome = true) :
    dec = .authorized := by
  cases dec <;> simp_all [buildDocOp, buildOwnerOp]

/-- **Theorem 1a (Scope & Active Domain Bound)**:
    Whenever `planLeaf` emits any document or ownership operation, the path is authorized,
    `inp.granted` satisfies `requiredPerms`, and the path lies in `M_A(d, σ)`. -/
theorem plan_scope_and_frame
    (inp : LeafInput) (mode : Mode)
    (hWork : (planLeaf inp mode).docOp.isSome = true ∨
             (planLeaf inp mode).ownerOp.isSome = true) :
    (planLeaf inp mode).decision = .authorized ∧
    PermSet.allows inp.granted (planLeaf inp mode).requiredPerms = true ∧
    (planLeaf inp mode).inMA = true := by
  have hDec : (planLeaf inp mode).decision = .authorized :=
    buildOps_some_implies_authorized inp.path inp.actor (planLeaf inp mode).decision
      (planLeaf inp mode).action inp.declared inp.status inp.obs hWork
  have hAuthSel : authorizeSelection (planLeaf inp mode).selection (planLeaf inp mode).requiredPerms inp.granted = .authorized :=
    hDec
  have ⟨hSelCand, hPerm⟩ := authorizeSelection_eq_authorized _ _ _ hAuthSel
  have hInMA : (planLeaf inp mode).inMA = true := by
    have hNotOut : (planLeaf inp mode).stateClass ≠ .outsideDomain := by
      intro hOut
      change selectModeKind (planLeaf inp mode).stateClass mode = .candidate at hSelCand
      rw [hOut] at hSelCand
      cases mode <;> contradiction
    unfold planLeaf at hNotOut ⊢
    dsimp only at hNotOut ⊢
    cases hDecl : inp.declared with
    | some _ => simp [inActiveDomain]
    | none =>
      cases hSt : inp.status with
      | none => simp [classifyLeaf, hDecl, hSt] at hNotOut
      | some ls =>
        cases ls with
        | unowned | defaulted => simp [classifyLeaf, hDecl, hSt] at hNotOut
        | owned m orig =>
          by_cases hm : m = inp.actor
          · subst hm; simp [inActiveDomain]
          · have hne : (m != inp.actor) = true := bne_iff_ne.mpr hm
            simp [classifyLeaf, hDecl, hSt, hne] at hNotOut
  exact ⟨hDec, hPerm, hInMA⟩

private theorem classifyLeaf_protected_not_doc_action
    (actor : ActorID) (decl : Option Value) (obs : Obs) (anc : AncObs)
    (st : Option LeafStatus) (la : Option Value) (hF : Bool) (so : SelfOverlap)
    (hProt : isProtectedStructure obs anc = true) :
    let act := selectModeAction (classifyLeaf actor decl obs anc st la hF so).1
    act ≠ .setLeaf ∧ act ≠ .deleteLeaf := by
  cases decl with
  | some desired =>
    cases so with
    | retiredCreated => simp [classifyLeaf, selectModeAction]
    | retiredModified =>
      cases st with
      | none =>
        cases hF <;> simp [classifyLeaf, selectModeAction]
      | some ls =>
        cases ls with
        | unowned | defaulted =>
          cases hF <;> simp [classifyLeaf, selectModeAction]
        | owned m orig =>
          by_cases hm : (m != actor) = true <;> cases hF <;> simp [classifyLeaf, hm, selectModeAction]
    | none =>
      cases st with
      | none =>
        cases hF
        · simp [classifyLeaf, hProt, selectModeAction]
        · simp [classifyLeaf, selectModeAction]
      | some ls =>
        cases ls with
        | unowned | defaulted =>
          cases hF
          · simp [classifyLeaf, hProt, selectModeAction]
          · simp [classifyLeaf, selectModeAction]
        | owned m orig =>
          by_cases hm : (m != actor) = true
          · simp [classifyLeaf, hm, selectModeAction]
          · cases hF
            · simp [classifyLeaf, hm, hProt, selectModeAction]
            · simp [classifyLeaf, hm, selectModeAction]
  | none =>
    cases st with
    | none => simp [classifyLeaf, selectModeAction]
    | some ls =>
      cases ls with
      | unowned | defaulted => simp [classifyLeaf, selectModeAction]
      | owned m orig =>
        dsimp only [classifyLeaf]
        split
        · simp [selectModeAction]
        · cases orig with
          | modified =>
            dsimp only
            split <;> simp [selectModeAction]
          | created =>
            dsimp only
            split
            · simp [selectModeAction]
            · split
              · simp [selectModeAction]
              · simp [hProt, selectModeAction]

/-- **Theorem 2 (Structure Non-Destruction)**:
    When the target or any proper ancestor is a protected structure (`record`, `opaque`,
    `nonRecord`, or `opaqueAnc`), `planLeaf` never emits a document operation. -/
theorem plan_no_structure_destruction
    (inp : LeafInput) (mode : Mode)
    (hProt : isProtectedStructure inp.obs inp.anc = true) :
    (planLeaf inp mode).docOp = none := by
  have hNot := classifyLeaf_protected_not_doc_action
    inp.actor inp.declared inp.obs inp.anc inp.status inp.lastApplied inp.hasForeignOverlap inp.selfOverlap hProt
  have hNotSet : (planLeaf inp mode).action ≠ .setLeaf := hNot.1
  have hNotDel : (planLeaf inp mode).action ≠ .deleteLeaf := hNot.2
  change buildDocOp inp.path (planLeaf inp mode).decision (planLeaf inp mode).action inp.declared inp.obs = none
  cases (planLeaf inp mode).decision <;>
    cases hAct : (planLeaf inp mode).action <;>
    cases inp.declared <;>
    cases inp.obs <;>
    simp_all [buildDocOp]

/-- **Theorem 3 (Conservative Deletion in `Prune`)**:
    A leaf deletion `DocOp.delete p` is emitted ONLY when `mode = .prune`, the path is
    undeclared, `Delete + Own` is granted, the leaf is self-owned with `Origin = .created`,
    all ancestors are clear, and the observed leaf matches `lastApplied` (when provided). -/
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
  have hBuild : (planLeaf inp mode).decision = .authorized ∧
      (planLeaf inp mode).action = .deleteLeaf ∧
      inp.declared = none ∧
      p = inp.path ∧
      ∃ actual, inp.obs = .leaf actual := by
    change buildDocOp inp.path (planLeaf inp mode).decision (planLeaf inp mode).action inp.declared inp.obs = some (.delete p) at hDel
    cases hDec : (planLeaf inp mode).decision <;>
      cases hAct : (planLeaf inp mode).action <;>
      cases hDecl : inp.declared <;>
      cases hObs : inp.obs <;>
      simp_all [buildDocOp]
  obtain ⟨hDec, hAct, hDecl, hpEq, actual, hObs⟩ := hBuild
  have ⟨hSelCand, hPerm⟩ := authorizeSelection_eq_authorized
    (planLeaf inp mode).selection (planLeaf inp mode).requiredPerms inp.granted hDec
  have hSc : (planLeaf inp mode).stateClass = .retiredCreatedPrunable := by
    change selectModeAction (planLeaf inp mode).stateClass = .deleteLeaf at hAct
    cases hs : (planLeaf inp mode).stateClass <;> simp_all [selectModeAction]
  have hMode : mode = .prune := by
    change selectModeKind (planLeaf inp mode).stateClass mode = .candidate at hSelCand
    rw [hSc] at hSelCand
    cases mode <;> simp_all [selectModeKind]
  subst hMode
  have hReq : (planLeaf inp .prune).requiredPerms = permDeleteOwn := by
    change selectModePerms (planLeaf inp .prune).stateClass .prune = permDeleteOwn
    rw [hSc]; rfl
  rw [hReq] at hPerm
  change (classifyLeaf inp.actor inp.declared inp.obs inp.anc inp.status inp.lastApplied inp.hasForeignOverlap inp.selfOverlap).1 = .retiredCreatedPrunable at hSc
  rw [hDecl, hObs] at hSc
  unfold classifyLeaf at hSc
  cases hSt : inp.status with
  | none => rw [hSt] at hSc; cases hSc
  | some ls =>
    rw [hSt] at hSc
    cases ls with
    | unowned | defaulted => cases hSc
    | owned m orig =>
      by_cases hm : (m != inp.actor) = true
      · simp [hm] at hSc
      · simp only [Bool.not_eq_true, bne_eq_false_iff_eq] at hm
        subst hm
        simp only [bne_self_eq_false, Bool.false_eq_true, ↓reduceIte] at hSc
        cases orig with
        | modified =>
          by_cases hu : isObsUnknown (.leaf actual) inp.anc = true <;> simp [hu] at hSc
        | created =>
          by_cases hso : (inp.selfOverlap == .retiredCreated) = true
          · simp [hso] at hSc
          · simp only [hso, Bool.false_eq_true, ↓reduceIte] at hSc
            by_cases hfo : inp.hasForeignOverlap = true
            · simp [hfo] at hSc
            · simp only [hfo, Bool.false_eq_true, ↓reduceIte] at hSc
              by_cases hbad : (isObsUnknown (.leaf actual) inp.anc || isProtectedStructure (.leaf actual) inp.anc) = true
              · simp [hbad] at hSc
              · simp only [hbad, Bool.false_eq_true, ↓reduceIte] at hSc
                have hAnc : inp.anc = .clear := by
                  cases ha : inp.anc with
                  | clear => rfl
                  | nonRecord | opaqueAnc | unknownAnc =>
                    simp [ha, isObsUnknown, isProtectedStructure] at hbad
                have hNoDrift : ∀ la, inp.lastApplied = some la → Value.eqv actual la = true := by
                  intro la hla
                  rw [hla] at hSc
                  cases heq : Value.eqv actual la with
                  | true => rfl
                  | false => simp [heq] at hSc
                exact ⟨rfl, hDecl, hPerm, rfl, hAnc, hpEq, actual, hObs, hNoDrift⟩

private theorem not_candidate_implies_no_ops (inp : LeafInput) (mode : Mode)
    (hNotCand : (planLeaf inp mode).selection ≠ .candidate) :
    (planLeaf inp mode).docOp = none ∧ (planLeaf inp mode).ownerOp = none := by
  have hNotAuth : (planLeaf inp mode).decision ≠ .authorized := by
    intro hDec
    have ⟨hc, _⟩ := authorizeSelection_eq_authorized _ _ _ hDec
    exact hNotCand hc
  exact ⟨buildDocOp_not_authorized _ _ _ _ _ hNotAuth,
         buildOwnerOp_not_authorized _ _ _ _ _ _ hNotAuth⟩

/-- **Theorem 4 (No Silent Overwrite)**:
    `planLeaf` never silently overwrites (1) foreign-owned or foreign-overlapping paths,
    (2) drifted self-owned leaves, (3) drift-deleted declared self-owned leaves, or
    (4) conflicting unowned leaves (`K206`). -/
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
  refine ⟨?_, ?_, ?_, ?_⟩
  · intro hF
    apply not_candidate_implies_no_ops
    change selectModeKind (classifyLeaf inp.actor inp.declared inp.obs inp.anc inp.status inp.lastApplied inp.hasForeignOverlap inp.selfOverlap).1 mode ≠ .candidate
    rcases hF with ⟨m, o, hSt, hNe⟩ | ⟨hDeclSome, hFor⟩
    · have hBne : (m != inp.actor) = true := bne_iff_ne.mpr hNe
      cases hD : inp.declared with
      | some desired =>
        cases inp.selfOverlap <;> cases mode <;> simp [classifyLeaf, hSt, hBne, selectModeKind]
      | none =>
        cases mode <;> simp [classifyLeaf, hSt, hBne, selectModeKind]
    · cases hD : inp.declared with
      | none => simp [hD] at hDeclSome
      | some desired =>
        cases inp.selfOverlap <;> cases mode <;> simp [classifyLeaf, hFor, selectModeKind]
  · rintro ⟨o, actual, la, hSt, hObs, hLa, hDiff⟩
    have hCondNotCand : (inp.declared.isSome = true ∨ inp.status = some (.owned inp.actor .created)) →
        (planLeaf inp mode).selection ≠ .candidate := by
      intro hCond
      change selectModeKind (classifyLeaf inp.actor inp.declared inp.obs inp.anc inp.status inp.lastApplied inp.hasForeignOverlap inp.selfOverlap).1 mode ≠ .candidate
      rw [hSt, hObs, hLa]
      cases hD : inp.declared with
      | some desired =>
        cases inp.selfOverlap with
        | retiredCreated => cases mode <;> simp [classifyLeaf, selectModeKind]
        | retiredModified =>
          by_cases hf : inp.hasForeignOverlap = true <;> cases mode <;> simp [classifyLeaf, hf, selectModeKind]
        | none =>
          by_cases hf : inp.hasForeignOverlap = true
          · cases mode <;> simp [classifyLeaf, hf, selectModeKind]
          · by_cases hb : (isObsUnknown (.leaf actual) inp.anc || isProtectedStructure (.leaf actual) inp.anc) = true <;>
              cases mode <;> simp [classifyLeaf, hf, hb, hDiff, selectModeKind]
      | none =>
        rw [hD, hSt] at hCond
        rcases hCond with hAbs | hOrig
        · simp at hAbs
        · injection hOrig with hEq
          injection hEq with _ hO
          subst hO
          by_cases hso : (inp.selfOverlap == .retiredCreated) = true
          · cases mode <;> simp [classifyLeaf, hso, selectModeKind]
          · by_cases hf : inp.hasForeignOverlap = true
            · cases mode <;> simp [classifyLeaf, hso, hf, selectModeKind]
            · by_cases hb : (isObsUnknown (.leaf actual) inp.anc || isProtectedStructure (.leaf actual) inp.anc) = true <;>
                cases mode <;> simp [classifyLeaf, hso, hf, hb, hDiff, selectModeKind]
    refine ⟨?_, fun hCond => (not_candidate_implies_no_ops inp mode (hCondNotCand hCond)).2⟩
    by_cases hCond : inp.declared.isSome = true ∨ inp.status = some (.owned inp.actor .created)
    · exact (not_candidate_implies_no_ops inp mode (hCondNotCand hCond)).1
    · have hDeclNone : inp.declared = none := by
        cases hD : inp.declared with
        | none => rfl
        | some _ => exact absurd (Or.inl (by simp [hD])) hCond
      have hOrigMod : o = .modified := by
        cases o with
        | modified => rfl
        | created => exact absurd (Or.inr hSt) hCond
      subst hOrigMod
      have hNotDel : (planLeaf inp mode).action ≠ .deleteLeaf := by
        change selectModeAction (classifyLeaf inp.actor inp.declared inp.obs inp.anc inp.status inp.lastApplied inp.hasForeignOverlap inp.selfOverlap).1 ≠ .deleteLeaf
        rw [hDeclNone, hSt, hObs]
        by_cases hu : isObsUnknown (.leaf actual) inp.anc = true <;>
          simp [classifyLeaf, hu, selectModeAction]
      change buildDocOp inp.path (planLeaf inp mode).decision (planLeaf inp mode).action inp.declared inp.obs = none
      rw [hDeclNone]
      cases (planLeaf inp mode).decision <;>
        cases hAct : (planLeaf inp mode).action <;>
        cases inp.obs <;>
        simp_all [buildDocOp]
  · rintro ⟨desired, o, hDecl, hSt, hObs⟩
    apply not_candidate_implies_no_ops
    change selectModeKind (classifyLeaf inp.actor inp.declared inp.obs inp.anc inp.status inp.lastApplied inp.hasForeignOverlap inp.selfOverlap).1 mode ≠ .candidate
    rw [hDecl, hSt, hObs]
    cases inp.selfOverlap with
    | retiredCreated => cases mode <;> simp [classifyLeaf, selectModeKind]
    | retiredModified =>
      by_cases hf : inp.hasForeignOverlap = true <;> cases mode <;> simp [classifyLeaf, hf, selectModeKind]
    | none =>
      by_cases hf : inp.hasForeignOverlap = true
      · cases mode <;> simp [classifyLeaf, hf, selectModeKind]
      · by_cases hb : (isObsUnknown .absent inp.anc || isProtectedStructure .absent inp.anc) = true <;>
          cases mode <;> simp [classifyLeaf, hf, hb, selectModeKind]
  · rintro ⟨desired, actual, hDecl, hSt, hObs, hMismatch⟩
    apply not_candidate_implies_no_ops
    change selectModeKind (classifyLeaf inp.actor inp.declared inp.obs inp.anc inp.status inp.lastApplied inp.hasForeignOverlap inp.selfOverlap).1 mode ≠ .candidate
    rw [hDecl, hObs]
    rcases hSt with hNone | hUn
    · rw [hNone]
      cases inp.selfOverlap with
      | retiredCreated => cases mode <;> simp [classifyLeaf, selectModeKind]
      | retiredModified =>
        by_cases hf : inp.hasForeignOverlap = true <;> cases mode <;> simp [classifyLeaf, hf, selectModeKind]
      | none =>
        by_cases hf : inp.hasForeignOverlap = true
        · cases mode <;> simp [classifyLeaf, hf, selectModeKind]
        · by_cases hb : (isObsUnknown (.leaf actual) inp.anc || isProtectedStructure (.leaf actual) inp.anc) = true <;>
            cases mode <;> simp [classifyLeaf, hf, hb, hMismatch, selectModeKind]
    · rw [hUn]
      cases inp.selfOverlap with
      | retiredCreated => cases mode <;> simp [classifyLeaf, selectModeKind]
      | retiredModified =>
        by_cases hf : inp.hasForeignOverlap = true <;> cases mode <;> simp [classifyLeaf, hf, selectModeKind]
      | none =>
        by_cases hf : inp.hasForeignOverlap = true
        · cases mode <;> simp [classifyLeaf, hf, selectModeKind]
        · by_cases hb : (isObsUnknown (.leaf actual) inp.anc || isProtectedStructure (.leaf actual) inp.anc) = true <;>
            cases mode <;> simp [classifyLeaf, hf, hb, hMismatch, selectModeKind]

/-- **Theorem 5 (Convergence Fixed Point)**:
    When a declared leaf matches the observed self-owned state without drift or overlaps,
    both `Apply` and `Prune` emit `decision = .noOp` and zero document or ownership changes. -/
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
  intro inp
  cases mode <;>
    simp [inp, planLeaf, classifyLeaf, selectModeKind, selectModeAction, selectModePerms,
      authorizeSelection, buildDocOp, buildOwnerOp, isObsUnknown, isProtectedStructure, hEq]

/-- **Theorem 6 (Plan Determinism)**:
    Identical inputs always produce identical multi-path plans and outcomes. -/
theorem plan_deterministic
    (doc₁ doc₂ decl₁ decl₂ : Value) (ow₁ ow₂ : OwnerState)
    (a₁ a₂ : ActorID) (s₁ s₂ : Scope) (m₁ m₂ : Mode)
    (e₁ e₂ r₁ r₂ : Bool)
    (hDoc : doc₁ = doc₂) (hOw : ow₁ = ow₂) (hDecl : decl₁ = decl₂)
    (hA : a₁ = a₂) (hS : s₁ = s₂) (hM : m₁ = m₂) (hE : e₁ = e₂) (hR : r₁ = r₂) :
    planMulti doc₁ ow₁ decl₁ a₁ s₁ m₁ e₁ r₁ = planMulti doc₂ ow₂ decl₂ a₂ s₂ m₂ e₂ r₂ := by
  subst hDoc hOw hDecl hA hS hM hE hR
  rfl

/-- **Theorem 7 (Intra-Mode All-or-Nothing Gate)**:
    If `ownerRevMatch` or `expectMatch` fails, or if any planned path has decision
    `.blocker` or `.policyDenied`, `gatePlan` suppresses every document and ownership op. -/
theorem plan_all_or_nothing
    (plans : List LeafPlan) (expectMatch ownerRevMatch : Bool)
    (hBlock : ownerRevMatch = false ∨ expectMatch = false ∨
      ∃ p ∈ plans, p.decision = .blocker ∨ p.decision = .policyDenied) :
    let g := gatePlan plans expectMatch ownerRevMatch
    g.blocked = true ∧ g.docOps = [] ∧ g.ownerOps = [] := by
  have hCond : (!ownerRevMatch || !expectMatch || plans.any (fun p => isBlockingDecision p.decision)) = true := by
    rcases hBlock with hr | he | ⟨p, hp, hdec⟩
    · simp [hr]
    · simp [he]
    · have hany : plans.any (fun q => isBlockingDecision q.decision) = true := by
        apply (any_eq_true_iff _ plans).mpr
        refine ⟨p, hp, ?_⟩
        rcases hdec with hd | hd <;> simp [isBlockingDecision, hd]
      simp [hany]
  unfold gatePlan
  simp [hCond]

/-- **Theorem 8 (Explicit `Adopt` / `Release` Safety)**:
    `planResolve` never mutates `o/` (`nextDoc = req.doc`), leaves `owner` unchanged when
    rejected, and when accepted: requires `Own + Resolve`, matching `expect` & `ownerRevision`,
    never hijacks foreign ownership, sets `.modified` (never `.created`) on `Adopt`, and
    removes ownership on `Release`. -/
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
  dsimp only
  refine ⟨?_, ?_, ?_⟩
  · unfold planResolve
    dsimp only
    repeat (first | split | rfl)
  · unfold planResolve
    dsimp only
    repeat (first | split | (intro _; rfl) | (intro h; cases h))
  · intro hAcc
    unfold planResolve at hAcc ⊢
    dsimp only at hAcc ⊢
    split at hAcc
    · cases hAcc
    · rename_i hPre
      simp only [hPre]
      split at hAcc
      · cases hAcc
      · rename_i hPerm
        simp only [hPerm]
        have hAllows : req.scope.allows req.path permOwnResolve = true := by
          cases hA : req.scope.allows req.path permOwnResolve with
          | true => rfl
          | false => simp [hA] at hPerm
        cases hKind : req.kind with
        | adopt =>
          simp only [hKind] at hAcc ⊢
          split at hAcc
          · cases hAcc
          · rename_i hNoOv
            simp only [hNoOv]
            split at hAcc
            · cases hAcc
            · rename_i hRev
              simp only [hRev]
              have hRevT : req.ownerRevMatch = true := by
                cases hr : req.ownerRevMatch <;> simp [hr] at hRev ⊢
              cases hStruct : primaryCodeOfCodes (obsDiags (observeDoc req.doc req.path).1 (observeDoc req.doc req.path).2) with
              | some _ => rw [hStruct] at hAcc; cases hAcc
              | none =>
                rw [hStruct] at hAcc
                dsimp only at hAcc ⊢
                cases hObs : (observeDoc req.doc req.path).1 with
                | leaf _ =>
                  rw [hObs] at hAcc
                  dsimp only at hAcc ⊢
                  split at hAcc
                  · cases hAcc
                  · rename_i hExp
                    simp only [hExp]
                    have hExpT : req.expectMatch = true := by
                      cases he : req.expectMatch <;> simp [he] at hExp ⊢
                    refine ⟨hAllows, hExpT, hRevT, ?_, ?_, ?_⟩
                    · intro m o hGet
                      by_cases hm : m = req.actor
                      · exact hm
                      · have hMem : (req.path, LeafStatus.owned m o) ∈ req.owner.entries :=
                          lookupPath_mem req.owner.entries req.path (.owned m o) hGet
                        have hOv : hasAdoptOverlap req.owner req.actor req.path = true := by
                          unfold hasAdoptOverlap
                          apply (any_eq_true_iff _ req.owner.entries).mpr
                          refine ⟨(req.path, .owned m o), hMem, ?_⟩
                          simp [bne_iff_ne.mpr hm, overlaps_refl]
                        exact absurd hOv hNoOv
                    · intro _
                      exact ownerState_get_set_self req.owner req.path (.owned req.actor .modified)
                    · intro hAbs; cases hAbs
                | _ => rw [hObs] at hAcc; cases hAcc
        | release =>
          simp only [hKind] at hAcc ⊢
          cases hGet : req.owner.get req.path with
          | none => rw [hGet] at hAcc; cases hAcc
          | some ls =>
            rw [hGet] at hAcc
            cases ls with
            | unowned | defaulted => cases hAcc
            | owned m orig =>
              dsimp only at hAcc ⊢
              split at hAcc
              · cases hAcc
              · rename_i hSelf
                simp only [hSelf]
                have hMeq : m = req.actor := by
                  cases hm : (m != req.actor) with
                  | true => simp [hm] at hSelf
                  | false => exact bne_eq_false_iff_eq.mp hm
                split at hAcc
                · cases hAcc
                · rename_i hRev
                  simp only [hRev]
                  have hRevT : req.ownerRevMatch = true := by
                    cases hr : req.ownerRevMatch <;> simp [hr] at hRev ⊢
                  split at hAcc
                  · cases hAcc
                  · rename_i hKnown
                    simp only [hKnown]
                    split at hAcc
                    · cases hAcc
                    · rename_i hExp
                      simp only [hExp]
                      have hExpT : req.expectMatch = true := by
                        cases he : req.expectMatch <;> simp [he] at hExp ⊢
                      refine ⟨hAllows, hExpT, hRevT, ?_, ?_, ?_⟩
                      · intro m' o' hGet'
                        cases hGet'
                        exact hMeq
                      · intro hAbs; cases hAbs
                      · intro _ m' o' hAfter
                        simp only [Bool.false_eq_true, ↓reduceIte] at hAfter
                        cases hObs : (observeDoc req.doc req.path).1 with
                        | absent =>
                          simp only [hObs] at hAfter
                          rw [ownerState_get_remove_self] at hAfter
                          cases hAfter
                        | _ =>
                          simp only [hObs] at hAfter
                          rw [ownerState_get_set_self] at hAfter
                          cases hAfter

/-- **Theorem 9 (Cross-Mode Isolation)**:
    `Apply` mode never emits a leaf deletion (`DocOp.delete`). -/
theorem apply_never_deletes_leaves (inp : LeafInput) (p : Path) :
    (planLeaf inp .apply).docOp ≠ some (.delete p) := by
  intro hDel
  have ⟨hPrune, _⟩ := prune_conservative_deletion inp .apply p hDel
  cases hPrune

/-! ### Multi-Path Foreign Frame Preservation -/

mutual
  private theorem removeDefaultedValue_preserves_owned :
      ∀ (v : Value) (pfx : Path) (ow0 ow : OwnerState) (p : Path) (m : ActorID) (orig : Origin),
      onlyDefaultedValue pfx ow0 v = true →
      ow0.get p = some (.owned m orig) →
      ow.get p = some (.owned m orig) →
      (removeDefaultedValue pfx ow v).get p = some (.owned m orig)
    | .record fs, pfx, ow0, ow, p, m, orig, hDef, h0, hGet =>
      removeDefaultedFields_preserves_owned fs pfx ow0 ow p m orig hDef h0 hGet
    | .str _, pfx, ow0, ow, p, m, orig, hDef, h0, hGet
    | .int _, pfx, ow0, ow, p, m, orig, hDef, h0, hGet
    | .dec _, pfx, ow0, ow, p, m, orig, hDef, h0, hGet
    | .bool _, pfx, ow0, ow, p, m, orig, hDef, h0, hGet
    | .enum _, pfx, ow0, ow, p, m, orig, hDef, h0, hGet
    | .qty _ _, pfx, ow0, ow, p, m, orig, hDef, h0, hGet
    | .ref _, pfx, ow0, ow, p, m, orig, hDef, h0, hGet
    | .list _, pfx, ow0, ow, p, m, orig, hDef, h0, hGet => by
      simp only [onlyDefaultedValue, beq_iff_eq] at hDef
      have hne : p ≠ pfx := by intro heq; subst heq; rw [h0] at hDef; cases hDef
      simp only [removeDefaultedValue]
      rw [ownerState_get_remove_ne ow hne]
      exact hGet

  private theorem removeDefaultedFields_preserves_owned :
      ∀ (fs : Fields) (pfx : Path) (ow0 ow : OwnerState) (p : Path) (m : ActorID) (orig : Origin),
      onlyDefaultedFields pfx ow0 fs = true →
      ow0.get p = some (.owned m orig) →
      ow.get p = some (.owned m orig) →
      (removeDefaultedFields pfx ow fs).get p = some (.owned m orig)
    | .nil, _, _, _, _, _, _, _, _, hGet => hGet
    | .cons k v rest, pfx, ow0, ow, p, m, orig, hDef, h0, hGet => by
      simp only [onlyDefaultedFields, Bool.and_eq_true] at hDef
      exact removeDefaultedFields_preserves_owned rest pfx ow0
        (removeDefaultedValue (pfx ++ [k]) ow v) p m orig hDef.2 h0
        (removeDefaultedValue_preserves_owned v (pfx ++ [k]) ow0 ow p m orig hDef.1 h0 hGet)
end

private theorem deletePruneSub_preserves_owned :
    ∀ (p : Path) (pfx : Path) (fs : Fields) (ow : OwnerState) (q : Path) (m : ActorID) (orig : Origin),
    ow.get q = some (.owned m orig) →
    (deletePruneSub pfx fs p ow).2.get q = some (.owned m orig)
  | [], _, _, _, _, _, _, hGet => hGet
  | [_], _, _, _, _, _, _, hGet => hGet
  | k :: k2 :: rest, pfx, fs, ow, q, m, orig, hGet => by
    simp only [deletePruneSub]
    cases fs.get k with
    | none => exact hGet
    | some val =>
      cases val with
      | record sub =>
        dsimp only
        have ih := deletePruneSub_preserves_owned (k2 :: rest) (pfx ++ [k]) sub ow q m orig hGet
        cases hSub : deletePruneSub (pfx ++ [k]) sub (k2 :: rest) ow with
        | mk sub' ow1 =>
          rw [hSub] at ih
          dsimp only at ih ⊢
          cases sub' with
          | nil => exact ih
          | cons k' v' rest' =>
            dsimp only
            by_cases hDef : onlyDefaultedFields (pfx ++ [k]) ow1 (.cons k' v' rest') = true
            · simp only [hDef, ↓reduceIte]
              exact removeDefaultedFields_preserves_owned (.cons k' v' rest') (pfx ++ [k]) ow1 ow1 q m orig hDef ih ih
            · simp only [hDef]
              exact ih
      | _ => exact hGet

private theorem deletePruneDoc_preserves_owned (doc : Value) (p : Path) (ow : OwnerState)
    (q : Path) (m : ActorID) (orig : Origin) (hGet : ow.get q = some (.owned m orig)) :
    (deletePruneDoc doc p ow).2.get q = some (.owned m orig) := by
  unfold deletePruneDoc
  split
  · rename_i top k rest
    split
    · rename_i sp _
      exact deletePruneSub_preserves_owned (k :: rest) ["spec"] sp ow q m orig hGet
    · exact hGet
  · rename_i top k
    split
    · split
      · exact hGet
      · exact hGet
    · exact hGet
  · exact hGet

private theorem applyOwnerOp_get_ne (ow : OwnerState) (op : OwnerOp) {p : Path}
    (hne : p ≠ op.path) : (applyOwnerOp ow op).get p = ow.get p := by
  cases op with
  | createOwned q a => exact ownerState_get_set_ne ow (.owned a .created) hne
  | keepOwned q a o => exact ownerState_get_set_ne ow (.owned a o) hne
  | adoptOwned q a => exact ownerState_get_set_ne ow (.owned a .modified) hne
  | setUnowned q => exact ownerState_get_set_ne ow .unowned hne
  | remove q => exact ownerState_get_remove_ne ow hne

private theorem applyOwnerOps_get_ne : ∀ (ops : List OwnerOp) (ow : OwnerState) {p : Path},
    (∀ op ∈ ops, op.path ≠ p) → (applyOwnerOps ow ops).get p = ow.get p
  | [], _, _, _ => rfl
  | op :: rest, ow, p, hall => by
    change (applyOwnerOps (applyOwnerOp ow op) rest).get p = ow.get p
    have h1 : p ≠ op.path := Ne.symm (hall op List.mem_cons_self)
    rw [applyOwnerOps_get_ne rest (applyOwnerOp ow op)
      (fun o ho => hall o (List.mem_cons_of_mem op ho)),
      applyOwnerOp_get_ne ow op h1]

private theorem applyDocOwnerOps_preserves_owned : ∀ (ops : List DocOp) (doc : Value) (ow : OwnerState)
    (q : Path) (m : ActorID) (orig : Origin),
    ow.get q = some (.owned m orig) →
    (ops.foldl applyDocOwnerStep (doc, ow)).2.get q = some (.owned m orig)
  | [], _, _, _, _, _, hGet => hGet
  | op :: rest, doc, ow, q, m, orig, hGet => by
    simp only [List.foldl_cons]
    cases op with
    | set p v =>
      exact applyDocOwnerOps_preserves_owned rest (setDocLeaf doc p v) ow q m orig hGet
    | delete p =>
      have h1 := deletePruneDoc_preserves_owned doc p ow q m orig hGet
      exact applyDocOwnerOps_preserves_owned rest (deletePruneDoc doc p ow).1 (deletePruneDoc doc p ow).2 q m orig h1

private theorem buildDocOp_path (p : Path) (dec : DecisionKind) (act : ActionKind)
    (decl : Option Value) (obs : Obs) (op : DocOp)
    (h : buildDocOp p dec act decl obs = some op) : op.path = p := by
  cases dec <;> cases act <;> cases decl <;> cases obs <;> simp [buildDocOp] at h
  · cases h; rfl
  · cases h.2; rfl
  · cases h; rfl

private theorem buildOwnerOp_path (p : Path) (a : ActorID) (dec : DecisionKind) (act : ActionKind)
    (st : Option LeafStatus) (obs : Obs) (op : OwnerOp)
    (h : buildOwnerOp p a dec act st obs = some op) : op.path = p := by
  cases dec with
  | noOp | deferred | blocker | policyDenied | suppressed => simp [buildOwnerOp] at h
  | authorized =>
    cases act with
    | none | explicitAdopt | explicitRelease => simp [buildOwnerOp] at h
    | setLeaf =>
      simp only [buildOwnerOp] at h
      cases st with
      | none => cases h; rfl
      | some ls =>
        cases ls with
        | unowned | defaulted => cases h; rfl
        | owned m orig =>
          by_cases hm : m = a
          · simp [hm] at h; cases h; rfl
          · simp [hm] at h
    | adoptModified => simp only [buildOwnerOp] at h; cases h; rfl
    | deleteLeaf => simp only [buildOwnerOp] at h; cases h; rfl
    | unmanageModified => simp only [buildOwnerOp] at h; cases obs <;> (cases h; rfl)
    | clearAbsentRecord => simp only [buildOwnerOp] at h; cases h; rfl

/-- **Theorem 1b (Multi-Path Foreign Frame Preservation)**:
    In `planMulti`, any path owned by a foreign actor (`m ≠ actor`) retains its exact ownership
    entry and receives no document operation. -/
theorem planMulti_foreign_frame
    (doc declDoc : Value) (ow : OwnerState) (actor : ActorID) (scope : Scope)
    (mode : Mode) (expectMatch ownerRevMatch : Bool) (out : PlanOutput)
    (hPlan : planMulti doc ow declDoc actor scope mode expectMatch ownerRevMatch = .ok out)
    (p : Path) (m : ActorID) (orig : Origin)
    (hForeign : ow.get p = some (.owned m orig)) (hNe : m ≠ actor) :
    out.nextOwner.get p = some (.owned m orig) ∧
    ∀ op ∈ out.docOps, op.path ≠ p := by
  unfold planMulti at hPlan
  split at hPlan
  · cases hPlan
  · split at hPlan
    · cases hPlan
    · split at hPlan
      · cases hPlan
      · cases hProj : projectEntranceSpec declDoc true with
        | error c =>
          rw [hProj] at hPlan
          simp only [bind, Except.bind] at hPlan
          cases hPlan
        | ok decl =>
          rw [hProj] at hPlan
          simp only [bind, Except.bind] at hPlan
          split at hPlan
          · cases hPlan
            exact ⟨hForeign, fun _ hmem => by cases hmem⟩
          · rename_i hNotBlocked
            cases hPlan
            dsimp only [applyPlanChanges]
            have hNoDoc : ∀ op ∈ (gatePlan
                ((sortDedupPaths (decl.map Prod.fst ++ ow.actorOwnedPaths actor)).map fun q =>
                  let x := observeDoc doc q
                  let isDecl := (lookupPath decl q).isSome
                  planLeaf {
                    path := q, actor := actor, declared := lookupPath decl q,
                    obs := x.1, anc := x.2, status := ow.get q, lastApplied := none,
                    hasForeignOverlap := if isDecl then computeForeignOverlap ow actor q else false,
                    selfOverlap := computeSelfOverlap ow actor decl q,
                    granted := scope.permissionsFor q
                  } mode) true true).docOps, op.path ≠ p := by
              intro op hop heq
              unfold gatePlan at hop
              simp only [Bool.not_true, Bool.false_or] at hop
              split at hop
              · cases hop
              · obtain ⟨lp, hlp, hdoc⟩ := (mem_filterMap_iff LeafPlan.docOp _ op).mp hop
                obtain ⟨q, _, rfl⟩ := List.mem_map.mp hlp
                have hqp : op.path = q := buildDocOp_path q _ _ _ _ op hdoc
                rw [heq] at hqp
                subst hqp
                let inp : LeafInput := {
                  path := p, actor := actor, declared := lookupPath decl p,
                  obs := (observeDoc doc p).1, anc := (observeDoc doc p).2,
                  status := ow.get p, lastApplied := none,
                  hasForeignOverlap := if (lookupPath decl p).isSome then computeForeignOverlap ow actor p else false,
                  selfOverlap := computeSelfOverlap ow actor decl p,
                  granted := scope.permissionsFor p
                }
                have hNoOp := (plan_no_silent_overwrite inp mode).1 (Or.inl ⟨m, orig, hForeign, hNe⟩)
                rw [hNoOp.1] at hdoc
                cases hdoc
            have hNoOwn : ∀ op ∈ (gatePlan
                ((sortDedupPaths (decl.map Prod.fst ++ ow.actorOwnedPaths actor)).map fun q =>
                  let x := observeDoc doc q
                  let isDecl := (lookupPath decl q).isSome
                  planLeaf {
                    path := q, actor := actor, declared := lookupPath decl q,
                    obs := x.1, anc := x.2, status := ow.get q, lastApplied := none,
                    hasForeignOverlap := if isDecl then computeForeignOverlap ow actor q else false,
                    selfOverlap := computeSelfOverlap ow actor decl q,
                    granted := scope.permissionsFor q
                  } mode) true true).ownerOps, op.path ≠ p := by
              intro op hop heq
              unfold gatePlan at hop
              simp only [Bool.not_true, Bool.false_or] at hop
              split at hop
              · cases hop
              · obtain ⟨lp, hlp, hown⟩ := (mem_filterMap_iff LeafPlan.ownerOp _ op).mp hop
                obtain ⟨q, _, rfl⟩ := List.mem_map.mp hlp
                have hqp : op.path = q := buildOwnerOp_path q _ _ _ _ _ op hown
                rw [heq] at hqp
                subst hqp
                let inp : LeafInput := {
                  path := p, actor := actor, declared := lookupPath decl p,
                  obs := (observeDoc doc p).1, anc := (observeDoc doc p).2,
                  status := ow.get p, lastApplied := none,
                  hasForeignOverlap := if (lookupPath decl p).isSome then computeForeignOverlap ow actor p else false,
                  selfOverlap := computeSelfOverlap ow actor decl p,
                  granted := scope.permissionsFor p
                }
                have hNoOp := (plan_no_silent_overwrite inp mode).1 (Or.inl ⟨m, orig, hForeign, hNe⟩)
                rw [hNoOp.2] at hown
                cases hown
            refine ⟨?_, hNoDoc⟩
            apply applyDocOwnerOps_preserves_owned
            exact (applyOwnerOps_get_ne _ ow hNoOwn).trans hForeign

/-! ### Part 2: Compact Trie Codec Roundtrip Theorem (Theorem 10) -/

private theorem mem_insertSortedStr (s x : String) : ∀ (l : List String),
    x ∈ insertSortedStr s l ↔ x = s ∨ x ∈ l
  | [] => by simp [insertSortedStr]
  | y :: ys => by
    simp only [insertSortedStr]
    split
    · rename_i heq
      simp only [beq_iff_eq] at heq
      subst heq
      simp only [List.mem_cons]
      constructor
      · intro h; exact Or.inr h
      · rintro (rfl | h); exact Or.inl rfl; exact h
    · split
      · simp only [List.mem_cons]
      · simp only [List.mem_cons, mem_insertSortedStr s x ys]
        constructor
        · rintro (rfl | rfl | h)
          · exact Or.inr (Or.inl rfl)
          · exact Or.inl rfl
          · exact Or.inr (Or.inr h)
        · rintro (rfl | rfl | h)
          · exact Or.inr (Or.inl rfl)
          · exact Or.inl rfl
          · exact Or.inr (Or.inr h)

private theorem mem_collectForeignActors (creator m : ActorID) (p : Path) (orig : Origin) :
    ∀ (leaves : List (Path × LeafStatus)),
    (p, .owned m orig) ∈ leaves → m ≠ creator → m ∈ collectForeignActors creator leaves
  | [], h, _ => by cases h
  | (q, ls) :: rest, h, hne => by
    rcases List.mem_cons.mp h with heq | hmem
    · cases heq
      simp only [collectForeignActors, beq_eq_false_iff_ne.mpr hne, Bool.false_eq_true, ↓reduceIte]
      exact (mem_insertSortedStr m m _).mpr (Or.inl rfl)
    · have ih := mem_collectForeignActors creator m p orig rest hmem hne
      cases ls with
      | owned m' _ =>
        simp only [collectForeignActors]
        split
        · exact ih
        · exact (mem_insertSortedStr m' m _).mpr (Or.inr ih)
      | unowned | defaulted => exact ih

private theorem mem_buildActorTable (creator m : ActorID) (p : Path) (orig : Origin)
    (leaves : List (Path × LeafStatus)) (h : (p, .owned m orig) ∈ leaves) :
    m ∈ buildActorTable creator leaves := by
  unfold buildActorTable
  by_cases hm : m = creator
  · subst hm; exact List.mem_cons_self
  · exact List.mem_cons_of_mem creator (mem_collectForeignActors creator m p orig leaves h hm)

private theorem actorIndexOfAux_ge (target : ActorID) : ∀ (idx : Nat) (l : List ActorID),
    idx ≤ actorIndexOfAux idx target l
  | idx, [] => Nat.le_refl idx
  | idx, a :: rest => by
    simp only [actorIndexOfAux]
    split
    · exact Nat.le_refl idx
    · have := actorIndexOfAux_ge target (idx + 1) rest
      omega

private theorem actorIndexOfAux_getElem? (target : ActorID) : ∀ (idx : Nat) (l : List ActorID),
    target ∈ l → ∃ k, actorIndexOfAux idx target l = idx + k ∧ l[k]? = some target
  | _, [], h => by cases h
  | idx, a :: rest, h => by
    simp only [actorIndexOfAux]
    by_cases ha : a = target
    · subst ha
      refine ⟨0, ?_, rfl⟩
      simp
    · have hbeq : (a == target) = false := beq_eq_false_iff_ne.mpr ha
      simp only [hbeq, Bool.false_eq_true, ↓reduceIte]
      rcases List.mem_cons.mp h with rfl | hmem
      · exact absurd rfl ha
      · obtain ⟨k, hidx, hget⟩ := actorIndexOfAux_getElem? target (idx + 1) rest hmem
        refine ⟨k + 1, ?_, ?_⟩
        · omega
        · simpa using hget

private theorem resolveTrieState_toTrieState (actors : List ActorID) (ls : LeafStatus)
    (hMem : ∀ m o, ls = .owned m o → m ∈ actors) :
    resolveTrieState actors (toTrieState actors ls) = some ls := by
  cases ls with
  | unowned => rfl
  | defaulted => rfl
  | owned m orig =>
    simp only [toTrieState, resolveTrieState, actorIndexOf]
    obtain ⟨k, hidx, hget⟩ := actorIndexOfAux_getElem? m 0 actors (hMem m orig rfl)
    simp only [Nat.zero_add] at hidx
    rw [hidx, hget]
    rfl

private theorem toTrieState_eq_default_iff (creator : ActorID) (rest : List ActorID) (ls : LeafStatus) :
    toTrieState (creator :: rest) ls = .owned 0 .created ↔ ls = .owned creator .created := by
  cases ls with
  | unowned => simp [toTrieState]
  | defaulted => simp [toTrieState]
  | owned m orig =>
    simp only [toTrieState, actorIndexOf, actorIndexOfAux]
    cases orig with
    | modified => simp
    | created =>
      by_cases hm : creator = m
      · subst hm; simp
      · have hbeq : (creator == m) = false := beq_eq_false_iff_ne.mpr hm
        have hge := actorIndexOfAux_ge m (0 + 1) rest
        simp only [hbeq, Bool.false_eq_true, ↓reduceIte, TrieState.owned.injEq, and_true,
          LeafStatus.owned.injEq]
        constructor
        · intro h0; omega
        · intro hEq; exact absurd hEq.symm hm

private theorem trieChildren_get_replace_self : ∀ (cs : TrieChildren) (k : String) (v : TrieNode),
    (cs.get k).isSome = true → (cs.replace k v).get k = some v
  | .nil, _, _, h => by simp [TrieChildren.get] at h
  | .cons k' v' rest, k, v, h => by
    simp only [TrieChildren.get] at h
    simp only [TrieChildren.replace]
    split
    · rename_i heq; subst heq; simp [TrieChildren.get]
    · rename_i hne
      simp only [hne, ite_false] at h
      simp only [TrieChildren.get, hne, ite_false]
      exact trieChildren_get_replace_self rest k v h

private theorem trieChildren_get_replace_ne : ∀ (cs : TrieChildren) {k key : String} (v : TrieNode),
    key ≠ k → (cs.replace k v).get key = cs.get key
  | .nil, _, _, _, _ => rfl
  | .cons k' v' rest, k, key, v, hne => by
    simp only [TrieChildren.replace]
    split
    · rename_i heq; subst heq; simp [TrieChildren.get, Ne.symm hne]
    · simp only [TrieChildren.get]
      split
      · rfl
      · exact trieChildren_get_replace_ne rest v hne

private theorem trieChildren_get_append_self : ∀ (cs : TrieChildren) (k : String) (v : TrieNode),
    cs.get k = none → (cs.append k v).get k = some v
  | .nil, k, v, _ => by simp [TrieChildren.append, TrieChildren.get]
  | .cons k' v' rest, k, v, h => by
    simp only [TrieChildren.get] at h
    split at h
    · cases h
    · rename_i hne
      simp only [TrieChildren.append, TrieChildren.get, hne, ite_false]
      exact trieChildren_get_append_self rest k v h

private theorem trieChildren_get_append_ne : ∀ (cs : TrieChildren) {k key : String} (v : TrieNode),
    key ≠ k → (cs.append k v).get key = cs.get key
  | .nil, k, key, v, hne => by simp [TrieChildren.append, TrieChildren.get, Ne.symm hne]
  | .cons k' v' rest, k, key, v, hne => by
    simp only [TrieChildren.append, TrieChildren.get]
    split
    · rfl
    · exact trieChildren_get_append_ne rest v hne

private theorem trieChildren_get_upsert_self (cs : TrieChildren) (k : String) (v : TrieNode) :
    (cs.upsert k v).get k = some v := by
  unfold TrieChildren.upsert
  cases hg : cs.get k with
  | none =>
    simp only [Option.isSome_none, Bool.false_eq_true, ↓reduceIte]
    exact trieChildren_get_append_self cs k v hg
  | some _ =>
    simp only [Option.isSome_some, ↓reduceIte]
    exact trieChildren_get_replace_self cs k v (by simp [hg])

private theorem trieChildren_get_upsert_ne (cs : TrieChildren) {k key : String} (v : TrieNode)
    (hne : key ≠ k) : (cs.upsert k v).get key = cs.get key := by
  unfold TrieChildren.upsert
  split
  · exact trieChildren_get_replace_ne cs v hne
  · exact trieChildren_get_append_ne cs v hne

mutual
  private def RawNodeWF : TrieNode → Prop
    | .mk (some _) false .nil => True
    | .mk none false cs => cs ≠ .nil ∧ RawChildrenWF cs
    | _ => False

  private def RawChildrenWF : TrieChildren → Prop
    | .nil => True
    | .cons k child rest =>
      rest.get k = none ∧ RawNodeWF child ∧ RawChildrenWF rest
end

private theorem rawChildrenWF_get : ∀ (cs : TrieChildren) (k : String) (n : TrieNode),
    RawChildrenWF cs → cs.get k = some n → RawNodeWF n
  | .nil, _, _, _, hg => by simp [TrieChildren.get] at hg
  | .cons k' c rest, k, n, hwf, hg => by
    simp only [RawChildrenWF] at hwf
    simp only [TrieChildren.get] at hg
    split at hg
    · cases hg; exact hwf.2.1
    · exact rawChildrenWF_get rest k n hwf.2.2 hg

private theorem rawChildrenWF_replace : ∀ (cs : TrieChildren) (k : String) (n : TrieNode),
    RawChildrenWF cs → RawNodeWF n → RawChildrenWF (cs.replace k n)
  | .nil, _, _, _, _ => trivial
  | .cons k' c rest, k, n, hwf, hn => by
    simp only [RawChildrenWF] at hwf
    simp only [TrieChildren.replace]
    split
    · exact ⟨hwf.1, hn, hwf.2.2⟩
    · rename_i hne
      exact ⟨by rw [trieChildren_get_replace_ne rest n hne]; exact hwf.1,
        hwf.2.1, rawChildrenWF_replace rest k n hwf.2.2 hn⟩

private theorem rawChildrenWF_append : ∀ (cs : TrieChildren) (k : String) (n : TrieNode),
    RawChildrenWF cs → cs.get k = none → RawNodeWF n → RawChildrenWF (cs.append k n)
  | .nil, _, _, _, _, hn => ⟨rfl, hn, trivial⟩
  | .cons k' c rest, k, n, hwf, hg, hn => by
    simp only [RawChildrenWF] at hwf
    simp only [TrieChildren.get] at hg
    split at hg
    · cases hg
    · rename_i hne
      exact ⟨by rw [trieChildren_get_append_ne rest n hne]; exact hwf.1,
        hwf.2.1, rawChildrenWF_append rest k n hwf.2.2 hg hn⟩

private theorem rawChildrenWF_upsert (cs : TrieChildren) (k : String) (n : TrieNode)
    (hwf : RawChildrenWF cs) (hn : RawNodeWF n) : RawChildrenWF (cs.upsert k n) := by
  unfold TrieChildren.upsert
  cases hg : cs.get k with
  | none =>
    simp only [Option.isSome_none, Bool.false_eq_true, ↓reduceIte]
    exact rawChildrenWF_append cs k n hwf hg hn
  | some _ =>
    simp only [Option.isSome_some, ↓reduceIte]
    exact rawChildrenWF_replace cs k n hwf hn

private theorem insertTrie_nonnil : ∀ (cs : TrieChildren) (p : Path) (ts : TrieState),
    p ≠ [] → insertTrie cs p ts ≠ .nil
  | _, [], _, hne => absurd rfl hne
  | cs, [k], ts, _ => by
    intro hnil
    simp only [insertTrie] at hnil
    have hg := trieChildren_get_upsert_self cs k (.mk (some ts) false .nil)
    rw [hnil] at hg
    simp [TrieChildren.get] at hg
  | cs, k :: k2 :: rest, ts, _ => by
    intro hnil
    simp only [insertTrie] at hnil
    cases hg : cs.get k with
    | none =>
      rw [hg] at hnil
      dsimp only at hnil
      have h1 := trieChildren_get_append_self cs k (.mk none false (insertTrie .nil (k2 :: rest) ts)) hg
      rw [hnil] at h1
      simp [TrieChildren.get] at h1
    | some n =>
      rw [hg] at hnil
      cases n with
      | mk st w sub =>
        dsimp only at hnil
        have h1 := trieChildren_get_replace_self cs k (.mk st w (insertTrie sub (k2 :: rest) ts)) (by simp [hg])
        rw [hnil] at h1
        simp [TrieChildren.get] at h1

private def NoOverlapChildren : TrieChildren → Path → Prop
  | _, [] => False
  | cs, [k] => cs.get k = none
  | cs, k :: k2 :: rest =>
    match cs.get k with
    | none => True
    | some (.mk none false sub) => NoOverlapChildren sub (k2 :: rest)
    | some _ => False

private theorem noOverlapChildren_nil : ∀ (p : Path), p ≠ [] → NoOverlapChildren .nil p
  | [], h => absurd rfl h
  | [_], _ => rfl
  | _ :: _ :: _, _ => trivial

private theorem insertTrie_rawWF : ∀ (p : Path) (cs : TrieChildren) (ts : TrieState),
    RawChildrenWF cs → NoOverlapChildren cs p → RawChildrenWF (insertTrie cs p ts)
  | [], _, _, _, hno => False.elim hno
  | [k], cs, ts, hwf, _ =>
    rawChildrenWF_upsert cs k (.mk (some ts) false .nil) hwf trivial
  | k :: k2 :: rest, cs, ts, hwf, hno => by
    simp only [NoOverlapChildren] at hno
    simp only [insertTrie]
    cases hg : cs.get k with
    | none =>
      have hsub := insertTrie_rawWF (k2 :: rest) .nil ts trivial (noOverlapChildren_nil (k2 :: rest) (by simp))
      have hne := insertTrie_nonnil .nil (k2 :: rest) ts (by simp)
      exact rawChildrenWF_append cs k (.mk none false (insertTrie .nil (k2 :: rest) ts)) hwf hg ⟨hne, hsub⟩
    | some n =>
      rw [hg] at hno
      cases n with
      | mk st w sub =>
        cases st with
        | some _ => exact False.elim hno
        | none =>
          cases w with
          | true => exact False.elim hno
          | false =>
            have hsubWF : RawChildrenWF sub := (rawChildrenWF_get cs k _ hwf hg).2
            have hsub' := insertTrie_rawWF (k2 :: rest) sub ts hsubWF hno
            have hne := insertTrie_nonnil sub (k2 :: rest) ts (by simp)
            exact rawChildrenWF_replace cs k (.mk none false (insertTrie sub (k2 :: rest) ts)) hwf ⟨hne, hsub'⟩

private theorem lookupRaw_insertTrie_self : ∀ (p : Path) (cs : TrieChildren) (ts : TrieState),
    NoOverlapChildren cs p → lookupRaw none (insertTrie cs p ts) p = some ts
  | [], _, _, hno => False.elim hno
  | [k], cs, ts, _ => by
    simp only [insertTrie, lookupRaw, trieChildren_get_upsert_self]
  | k :: k2 :: rest, cs, ts, hno => by
    simp only [NoOverlapChildren] at hno
    simp only [insertTrie, lookupRaw]
    cases hg : cs.get k with
    | none =>
      rw [trieChildren_get_append_self cs k _ hg]
      exact lookupRaw_insertTrie_self (k2 :: rest) .nil ts (noOverlapChildren_nil (k2 :: rest) (by simp))
    | some n =>
      rw [hg] at hno
      cases n with
      | mk st w sub =>
        cases st with
        | some _ => exact False.elim hno
        | none =>
          cases w with
          | true => exact False.elim hno
          | false =>
            rw [trieChildren_get_replace_self cs k _ (by simp [hg])]
            exact lookupRaw_insertTrie_self (k2 :: rest) sub ts hno

private theorem overlaps_cons_eq (k : String) (r₁ r₂ : Path)
    (h : overlaps (k :: r₁) (k :: r₂) = false) :
    r₁ ≠ [] ∧ r₂ ≠ [] ∧ overlaps r₁ r₂ = false := by
  have h1 : r₁ ≠ [] := by
    intro hr1; subst hr1
    cases r₂ with
    | nil => rw [overlaps_refl] at h; cases h
    | cons _ _ => simp [overlaps, isProperAncestor, isPrefixOf] at h
  have h2 : r₂ ≠ [] := by
    intro hr2; subst hr2
    cases r₁ with
    | nil => rw [overlaps_refl] at h; cases h
    | cons _ _ => simp [overlaps, isProperAncestor, isPrefixOf] at h
  refine ⟨h1, h2, ?_⟩
  cases h1' : r₁ with
  | nil => exact absurd h1' h1
  | cons a as =>
    cases h2' : r₂ with
    | nil => exact absurd h2' h2
    | cons b bs =>
      rw [h1', h2'] at h
      simp only [overlaps, isProperAncestor, isPrefixOf, Bool.or_eq_false_iff,
        Bool.and_eq_false_iff, beq_eq_false_iff_ne, List.isEmpty_cons, Bool.not_false,
        Bool.true_and, List.length_cons, Nat.add_lt_add_iff_right, decide_eq_false_iff_not] at h ⊢
      obtain ⟨⟨hne, hanc1⟩, hanc2⟩ := h
      refine ⟨⟨fun heq => hne (congrArg (k :: ·) heq), ?_⟩, ?_⟩
      · rcases hanc1 with hlen | hpf
        · exact Or.inl hlen
        · simp at hpf; exact Or.inr hpf
      · rcases hanc2 with hlen | hpf
        · exact Or.inl hlen
        · simp at hpf; exact Or.inr hpf

private theorem lookupRaw_insertTrie_ne : ∀ (p q : Path) (cs : TrieChildren) (ts : TrieState),
    NoOverlapChildren cs p → overlaps p q = false →
    lookupRaw none (insertTrie cs p ts) q = lookupRaw none cs q
  | [], _, _, _, hno, _ => False.elim hno
  | _, [], _, _, _, _ => rfl
  | [k], seg :: rest, cs, ts, _, hnoOv => by
    have hne : seg ≠ k := by
      intro heq; subst heq
      cases rest with
      | nil => rw [overlaps_refl] at hnoOv; cases hnoOv
      | cons _ _ => simp [overlaps, isProperAncestor, isPrefixOf] at hnoOv
    simp only [insertTrie, lookupRaw, trieChildren_get_upsert_ne cs _ hne]
  | k :: k2 :: rest1, seg :: rest2, cs, ts, hno, hnoOv => by
    simp only [NoOverlapChildren] at hno
    by_cases hseg : seg = k
    · subst hseg
      have ⟨_, hne2, hsubOv⟩ := overlaps_cons_eq seg (k2 :: rest1) rest2 hnoOv
      cases hr2 : rest2 with
      | nil => exact absurd hr2 hne2
      | cons s2 r2 =>
        rw [hr2] at hsubOv
        simp only [insertTrie, lookupRaw]
        cases hg : cs.get seg with
        | none =>
          rw [trieChildren_get_append_self cs seg _ hg]
          dsimp only
          have ih := lookupRaw_insertTrie_ne (k2 :: rest1) (s2 :: r2) .nil ts
            (noOverlapChildren_nil (k2 :: rest1) (by simp)) hsubOv
          simp only [lookupRaw] at ih
          exact ih
        | some n =>
          rw [hg] at hno
          cases n with
          | mk st w sub =>
            cases st with
            | some _ => exact False.elim hno
            | none =>
              cases w with
              | true => exact False.elim hno
              | false =>
                rw [trieChildren_get_replace_self cs seg _ (by simp [hg])]
                exact lookupRaw_insertTrie_ne (k2 :: rest1) (s2 :: r2) sub ts hno hsubOv
    · simp only [insertTrie, lookupRaw]
      cases hg : cs.get k with
      | none => rw [trieChildren_get_append_ne cs _ hseg]
      | some n =>
        cases n with
        | mk st w sub => rw [trieChildren_get_replace_ne cs _ hseg]

private theorem noOverlap_insertTrie : ∀ (p q : Path) (cs : TrieChildren) (ts : TrieState),
    NoOverlapChildren cs p → NoOverlapChildren cs q → overlaps p q = false →
    NoOverlapChildren (insertTrie cs p ts) q
  | [], _, _, _, hp, _, _ => False.elim hp
  | _, [], _, _, _, hq, _ => False.elim hq
  | [k], [seg], cs, ts, _, hq, hnoOv => by
    have hne : seg ≠ k := by
      intro heq; subst heq; rw [overlaps_refl] at hnoOv; cases hnoOv
    simp only [NoOverlapChildren, insertTrie] at hq ⊢
    rw [trieChildren_get_upsert_ne cs _ hne]
    exact hq
  | [k], seg :: s2 :: r2, cs, ts, _, hq, hnoOv => by
    have hne : seg ≠ k := by
      intro heq; subst heq; simp [overlaps, isProperAncestor, isPrefixOf] at hnoOv
    simp only [NoOverlapChildren, insertTrie] at hq ⊢
    rw [trieChildren_get_upsert_ne cs _ hne]
    exact hq
  | k :: k2 :: r1, [seg], cs, ts, _, hq, hnoOv => by
    have hne : seg ≠ k := by
      intro heq; subst heq; simp [overlaps, isProperAncestor, isPrefixOf] at hnoOv
    simp only [NoOverlapChildren, insertTrie] at hq ⊢
    cases hg : cs.get k with
    | none => rw [trieChildren_get_append_ne cs _ hne]; exact hq
    | some n =>
      cases n with
      | mk st w sub => rw [trieChildren_get_replace_ne cs _ hne]; exact hq
  | k :: k2 :: r1, seg :: s2 :: r2, cs, ts, hp, hq, hnoOv => by
    simp only [NoOverlapChildren] at hp hq ⊢
    by_cases hseg : seg = k
    · subst hseg
      have ⟨_, _, hsubOv⟩ := overlaps_cons_eq seg (k2 :: r1) (s2 :: r2) hnoOv
      simp only [insertTrie]
      cases hg : cs.get seg with
      | none =>
        rw [trieChildren_get_append_self cs seg _ hg]
        exact noOverlap_insertTrie (k2 :: r1) (s2 :: r2) .nil ts
          (noOverlapChildren_nil (k2 :: r1) (by simp))
          (noOverlapChildren_nil (s2 :: r2) (by simp)) hsubOv
      | some n =>
        rw [hg] at hp hq
        cases n with
        | mk st w sub =>
          cases st with
          | some _ => exact False.elim hp
          | none =>
            cases w with
            | true => exact False.elim hp
            | false =>
              rw [trieChildren_get_replace_self cs seg _ (by simp [hg])]
              exact noOverlap_insertTrie (k2 :: r1) (s2 :: r2) sub ts hp hq hsubOv
    · simp only [insertTrie]
      cases hg : cs.get k with
      | none => rw [trieChildren_get_append_ne cs _ hseg]; exact hq
      | some n =>
        cases n with
        | mk st w sub => rw [trieChildren_get_replace_ne cs _ hseg]; exact hq

private theorem insertAllTrie_noOverlap (actors : List ActorID) :
    ∀ (leaves : List (Path × LeafStatus)) (q : Path),
    TrieLeavesWF leaves → q ≠ [] →
    (∀ p ls, (p, ls) ∈ leaves → overlaps p q = false) →
    NoOverlapChildren (insertAllTrie actors leaves) q
  | [], q, _, hq, _ => noOverlapChildren_nil q hq
  | (p, ls) :: rest, q, hWF, hq, hall => by
    obtain ⟨hpw, hmem⟩ := hWF
    simp only [List.pairwise_cons] at hpw
    have hRestWF : TrieLeavesWF rest :=
      ⟨hpw.2, fun p' ls' hm => hmem p' ls' (List.mem_cons_of_mem _ hm)⟩
    have hpNe : p ≠ [] := (hmem p ls List.mem_cons_self).1
    have hpNo : NoOverlapChildren (insertAllTrie actors rest) p :=
      insertAllTrie_noOverlap actors rest p hRestWF hpNe
        (fun p' ls' hm => by rw [overlaps_symm]; exact hpw.1 (p', ls') hm)
    have hqNo : NoOverlapChildren (insertAllTrie actors rest) q :=
      insertAllTrie_noOverlap actors rest q hRestWF hq
        (fun p' ls' hm => hall p' ls' (List.mem_cons_of_mem _ hm))
    exact noOverlap_insertTrie p q (insertAllTrie actors rest) (toTrieState actors ls)
      hpNo hqNo (hall p ls List.mem_cons_self)

private theorem insertAllTrie_spec (actors : List ActorID) :
    ∀ (leaves : List (Path × LeafStatus)),
    TrieLeavesWF leaves →
    RawChildrenWF (insertAllTrie actors leaves) ∧
    ∀ p ls, (p, ls) ∈ leaves →
      lookupRaw none (insertAllTrie actors leaves) p = some (toTrieState actors ls)
  | [], _ => ⟨trivial, fun _ _ h => by cases h⟩
  | (p, ls) :: rest, hWF => by
    obtain ⟨hpw, hmem⟩ := hWF
    simp only [List.pairwise_cons] at hpw
    have hRestWF : TrieLeavesWF rest :=
      ⟨hpw.2, fun p' ls' hm => hmem p' ls' (List.mem_cons_of_mem _ hm)⟩
    obtain ⟨hRawRest, hLookRest⟩ := insertAllTrie_spec actors rest hRestWF
    have hpNe : p ≠ [] := (hmem p ls List.mem_cons_self).1
    have hpNo : NoOverlapChildren (insertAllTrie actors rest) p :=
      insertAllTrie_noOverlap actors rest p hRestWF hpNe
        (fun p' ls' hm => by rw [overlaps_symm]; exact hpw.1 (p', ls') hm)
    refine ⟨insertTrie_rawWF p _ _ hRawRest hpNo, ?_⟩
    intro q ls' hq
    simp only [insertAllTrie]
    rcases List.mem_cons.mp hq with heq | hmRest
    · cases heq
      exact lookupRaw_insertTrie_self p _ _ hpNo
    · rw [lookupRaw_insertTrie_ne p q _ _ hpNo (hpw.1 (q, ls') hmRest)]
      exact hLookRest q ls' hmRest

private theorem lookupRaw_cons_eq_lookupNodeRaw (inh : Option TrieState)
    (cs : TrieChildren) (seg : String) (rest : Path) (child : TrieNode)
    (hg : cs.get seg = some child) :
    lookupRaw inh cs (seg :: rest) = lookupNodeRaw inh child rest := by
  cases child with
  | mk st w sub =>
    cases rest <;> simp [lookupRaw, lookupNodeRaw, hg]

private theorem lookupRaw_nil_none : ∀ (p : Path), lookupRaw none .nil p = none
  | [] => rfl
  | _ :: _ => rfl

private theorem lookupRaw_nil_some (ts : TrieState) : ∀ (p : Path),
    lookupRaw (some ts) .nil p = some ts
  | [] => rfl
  | _ :: _ => rfl

mutual
  private def CompNodeWF : TrieNode → Prop
    | .mk _ _ cs => CompChildrenWF cs

  private def CompChildrenWF : TrieChildren → Prop
    | .nil => True
    | .cons k child rest =>
      rest.get k = none ∧ CompNodeWF child ∧ CompChildrenWF rest
end

private theorem get_compressChildren : ∀ (cs : TrieChildren) (k : String),
    (compressChildren cs).1.get k = (cs.get k).map (fun c => (compressNode c).1)
  | .nil, _ => rfl
  | .cons k' c .nil, k => by
    simp only [compressChildren, TrieChildren.get]
    split <;> rfl
  | .cons k' c (.cons k'' c'' rest), k => by
    simp only [compressChildren, TrieChildren.get]
    split
    · rfl
    · exact get_compressChildren (.cons k'' c'' rest) k

private theorem combineUniform_eq_some {a b : Option TrieState} {v : TrieState}
    (h : combineUniform a b = some v) : a = some v ∧ b = some v := by
  cases a <;> cases b <;> simp [combineUniform] at h
  obtain ⟨rfl, rfl⟩ := h
  exact ⟨rfl, rfl⟩

mutual
  private theorem compressNode_spec : ∀ (n : TrieNode), RawNodeWF n →
      CompNodeWF (compressNode n).1 ∧
      (∀ p ts, lookupNodeRaw none n p = some ts →
        (∀ u, (compressNode n).2.1 = some u → ts = u) ∧
        lookupNodeRaw none (compressNode n).1 p = some ts)
    | .mk st w .nil, hwf => by
      cases st with
      | none => cases w <;> simp [RawNodeWF] at hwf
      | some ts0 =>
        cases w with
        | true => simp [RawNodeWF] at hwf
        | false =>
          refine ⟨trivial, ?_⟩
          intro p ts hLook
          cases p with
          | nil =>
            simp only [lookupNodeRaw, Option.some.injEq] at hLook
            subst hLook
            exact ⟨fun u hu => by simp [compressNode] at hu; exact hu, rfl⟩
          | cons seg rest =>
            simp [lookupNodeRaw, lookupRaw_nil_none] at hLook
    | .mk st w (.cons k c rest), hwf => by
      cases st with
      | some _ => cases w <;> simp [RawNodeWF] at hwf
      | none =>
        cases w with
        | true => exact False.elim hwf
        | false =>
          have hCWF : RawChildrenWF (.cons k c rest) := hwf.2
          have hSpec := compressChildren_spec (.cons k c rest) hCWF
          generalize hcc : compressChildren (.cons k c rest) = res at hSpec ⊢
          rcases res with ⟨cs', u, cnt⟩
          obtain ⟨hCompCs, hLookCs⟩ := hSpec
          cases u with
          | none =>
            refine ⟨by simp [compressNode, hcc]; exact hCompCs, ?_⟩
            intro p ts hLook
            cases p with
            | nil => simp [lookupNodeRaw] at hLook
            | cons seg r =>
              simp only [lookupNodeRaw, Bool.false_eq_true, ↓reduceIte] at hLook
              have ⟨_, hcs'⟩ := hLookCs (seg :: r) ts hLook
              refine ⟨fun _ hu => by simp [compressNode, hcc] at hu, ?_⟩
              simp [compressNode, hcc, lookupNodeRaw, hcs']
          | some uTs =>
            by_cases hCnt : cnt ≥ 2
            · refine ⟨by simp [compressNode, hcc, hCnt]; exact trivial, ?_⟩
              intro p ts hLook
              cases p with
              | nil => simp [lookupNodeRaw] at hLook
              | cons seg r =>
                simp only [lookupNodeRaw, Bool.false_eq_true, ↓reduceIte] at hLook
                have ⟨huEq, _⟩ := hLookCs (seg :: r) ts hLook
                have htsEq : ts = uTs := huEq uTs rfl
                subst htsEq
                refine ⟨fun u' hu' => by simp [compressNode, hcc, hCnt] at hu'; exact hu', ?_⟩
                simp [compressNode, hcc, hCnt, lookupNodeRaw, lookupRaw_nil_some]
            · refine ⟨by simp [compressNode, hcc, hCnt]; exact hCompCs, ?_⟩
              intro p ts hLook
              cases p with
              | nil => simp [lookupNodeRaw] at hLook
              | cons seg r =>
                simp only [lookupNodeRaw, Bool.false_eq_true, ↓reduceIte] at hLook
                have ⟨huEq, hcs'⟩ := hLookCs (seg :: r) ts hLook
                refine ⟨fun u' hu' => by
                  simp only [compressNode, hcc, hCnt, decide_false, Bool.false_eq_true, ↓reduceIte,
                    Option.some.injEq] at hu'
                  exact huEq u' (congrArg some hu'), ?_⟩
                simp [compressNode, hcc, hCnt, lookupNodeRaw, hcs']

  private theorem compressChildren_spec : ∀ (cs : TrieChildren), RawChildrenWF cs →
      CompChildrenWF (compressChildren cs).1 ∧
      (∀ p ts, lookupRaw none cs p = some ts →
        (∀ u, (compressChildren cs).2.1 = some u → ts = u) ∧
        lookupRaw none (compressChildren cs).1 p = some ts)
    | .nil, _ => ⟨trivial, fun p _ h => by rw [lookupRaw_nil_none] at h; cases h⟩
    | .cons k c .nil, hwf => by
      have hNode := compressNode_spec c hwf.2.1
      refine ⟨⟨rfl, hNode.1, trivial⟩, ?_⟩
      intro p ts hLook
      cases p with
      | nil => simp [lookupRaw] at hLook
      | cons seg r =>
        by_cases hsk : k = seg
        · subst hsk
          rw [lookupRaw_cons_eq_lookupNodeRaw none (.cons k c .nil) k r c (by simp [TrieChildren.get])] at hLook
          have ⟨hu, hl⟩ := hNode.2 r ts hLook
          refine ⟨fun u hcu => hu u (by simpa [compressChildren] using hcu), ?_⟩
          simp only [compressChildren]
          rw [lookupRaw_cons_eq_lookupNodeRaw none (.cons k (compressNode c).1 .nil) k r (compressNode c).1 (by simp [TrieChildren.get])]
          exact hl
        · simp [lookupRaw, TrieChildren.get, hsk] at hLook
    | .cons k c (.cons k2 c2 rest), hwf => by
      have hNode := compressNode_spec c hwf.2.1
      have hRest := compressChildren_spec (.cons k2 c2 rest) hwf.2.2
      have hKeyNone : (compressChildren (.cons k2 c2 rest)).1.get k = none := by
        rw [get_compressChildren, hwf.1]; rfl
      refine ⟨⟨hKeyNone, hNode.1, hRest.1⟩, ?_⟩
      intro p ts hLook
      cases p with
      | nil => simp [lookupRaw] at hLook
      | cons seg r =>
        by_cases hsk : k = seg
        · subst hsk
          rw [lookupRaw_cons_eq_lookupNodeRaw none (.cons k c (.cons k2 c2 rest)) k r c (by simp [TrieChildren.get])] at hLook
          have ⟨hu, hl⟩ := hNode.2 r ts hLook
          refine ⟨?_, ?_⟩
          · intro u hcu
            simp only [compressChildren] at hcu
            have ⟨hc, _⟩ := combineUniform_eq_some hcu
            exact hu u hc
          · simp only [compressChildren]
            rw [lookupRaw_cons_eq_lookupNodeRaw none _ k r (compressNode c).1 (by simp [TrieChildren.get])]
            exact hl
        · have hLookRest : lookupRaw none (.cons k2 c2 rest) (seg :: r) = some ts := by
            simpa [lookupRaw, TrieChildren.get, hsk] using hLook
          have ⟨hu, hl⟩ := hRest.2 (seg :: r) ts hLookRest
          refine ⟨?_, ?_⟩
          · intro u hcu
            simp only [compressChildren] at hcu
            have ⟨_, hr⟩ := combineUniform_eq_some hcu
            exact hu u hr
          · simp only [compressChildren]
            simpa [lookupRaw, TrieChildren.get, hsk] using hl
end

private theorem get_dropDefaultChildren_none : ∀ (cs : TrieChildren) (k : String),
    cs.get k = none → (dropDefaultChildren cs).get k = none
  | .nil, _, _ => rfl
  | .cons k' c rest, k, hg => by
    simp only [TrieChildren.get] at hg
    split at hg
    · cases hg
    · rename_i hne
      simp only [dropDefaultChildren]
      cases dropDefaultNode c with
      | none => exact get_dropDefaultChildren_none rest k hg
      | some c' =>
        simp only [TrieChildren.get, hne, ite_false]
        exact get_dropDefaultChildren_none rest k hg

mutual
  private theorem dropDefaultNode_spec : ∀ (n : TrieNode), CompNodeWF n →
      (dropDefaultNode n = none →
        ∀ p ts, lookupNodeRaw none n p = some ts → ts = .owned 0 .created) ∧
      (∀ n', dropDefaultNode n = some n' →
        ∀ p ts, lookupNodeRaw none n p = some ts →
          (ts = .owned 0 .created →
            lookupNodeRaw none n' p = none ∨ lookupNodeRaw none n' p = some (.owned 0 .created)) ∧
          (ts ≠ .owned 0 .created → lookupNodeRaw none n' p = some ts))
    | .mk (some (.owned 0 .created)) w .nil, _ => by
      refine ⟨?_, fun n' h => by simp [dropDefaultNode] at h⟩
      intro _ p ts hLook
      cases p with
      | nil => simp [lookupNodeRaw] at hLook; exact hLook.symm
      | cons seg r =>
        cases w with
        | false => simp [lookupNodeRaw, lookupRaw_nil_none] at hLook
        | true => simp [lookupNodeRaw, lookupRaw_nil_some] at hLook; exact hLook.symm
    | .mk (some (.owned 0 .created)) true (.cons k c rest), _ => by
      refine ⟨fun h => by simp [dropDefaultNode] at h, ?_⟩
      intro n' h p ts hLook
      simp only [dropDefaultNode, Option.some.injEq] at h
      subst h
      exact ⟨fun h0 => Or.inr (h0 ▸ hLook), fun _ => hLook⟩
    | .mk (some (.owned 0 .created)) false (.cons k c rest), hwf => by
      have hRest := dropDefaultChildren_spec (.cons k c rest) hwf
      refine ⟨fun h => by simp [dropDefaultNode] at h, ?_⟩
      intro n' h p ts hLook
      simp only [dropDefaultNode, Option.some.injEq] at h
      subst h
      cases p with
      | nil =>
        simp only [lookupNodeRaw, Option.some.injEq] at hLook
        subst hLook
        exact ⟨fun _ => Or.inr rfl, fun hne => absurd rfl hne⟩
      | cons seg r =>
        simp only [lookupNodeRaw, Bool.false_eq_true, ↓reduceIte] at hLook ⊢
        exact hRest (seg :: r) ts hLook
    | .mk (some (.owned 0 .modified)) true cs, _
    | .mk (some (.owned (_ + 1) _)) true cs, _
    | .mk (some .unowned) true cs, _
    | .mk (some .defaulted) true cs, _
    | .mk none true cs, _ => by
      refine ⟨fun h => by simp [dropDefaultNode] at h, ?_⟩
      intro n' h p ts hLook
      simp only [dropDefaultNode, Option.some.injEq] at h
      subst h
      exact ⟨fun h0 => Or.inr (h0 ▸ hLook), fun _ => hLook⟩
    | .mk (some (.owned 0 .modified)) false cs, hwf
    | .mk (some (.owned (_ + 1) _)) false cs, hwf
    | .mk (some .unowned) false cs, hwf
    | .mk (some .defaulted) false cs, hwf => by
      have hRest := dropDefaultChildren_spec cs hwf
      refine ⟨fun h => by simp [dropDefaultNode] at h, ?_⟩
      intro n' h p ts hLook
      simp only [dropDefaultNode, Option.some.injEq] at h
      subst h
      cases p with
      | nil =>
        simp only [lookupNodeRaw, Option.some.injEq] at hLook
        subst hLook
        exact ⟨fun h0 => Or.inr (h0 ▸ rfl), fun _ => rfl⟩
      | cons seg r =>
        simp only [lookupNodeRaw, Bool.false_eq_true, ↓reduceIte] at hLook ⊢
        exact hRest (seg :: r) ts hLook
    | .mk none false cs, hwf => by
      have hRest := dropDefaultChildren_spec cs hwf
      cases hdc : dropDefaultChildren cs with
      | nil =>
        refine ⟨?_, fun n' h => by simp [dropDefaultNode, hdc] at h⟩
        intro _ p ts hLook
        cases p with
        | nil => simp [lookupNodeRaw] at hLook
        | cons seg r =>
          simp only [lookupNodeRaw, Bool.false_eq_true, ↓reduceIte] at hLook
          have hC := hRest (seg :: r) ts hLook
          by_cases h0 : ts = .owned 0 .created
          · exact h0
          · have h2 := hC.2 h0
            rw [hdc, lookupRaw_nil_none] at h2
            cases h2
      | cons k' c' rest' =>
        refine ⟨fun h => by simp [dropDefaultNode, hdc] at h, ?_⟩
        intro n' h p ts hLook
        simp only [dropDefaultNode, hdc, Option.some.injEq] at h
        subst h
        cases p with
        | nil => simp [lookupNodeRaw] at hLook
        | cons seg r =>
          simp only [lookupNodeRaw, Bool.false_eq_true, ↓reduceIte] at hLook ⊢
          rw [← hdc]
          exact hRest (seg :: r) ts hLook

  private theorem dropDefaultChildren_spec : ∀ (cs : TrieChildren), CompChildrenWF cs →
      ∀ p ts, lookupRaw none cs p = some ts →
        (ts = .owned 0 .created →
          lookupRaw none (dropDefaultChildren cs) p = none ∨
          lookupRaw none (dropDefaultChildren cs) p = some (.owned 0 .created)) ∧
        (ts ≠ .owned 0 .created → lookupRaw none (dropDefaultChildren cs) p = some ts)
    | .nil, _, p, _, h => by rw [lookupRaw_nil_none] at h; cases h
    | .cons k c rest, hwf, p, ts, hLook => by
      have hNode := dropDefaultNode_spec c hwf.2.1
      have hRest := dropDefaultChildren_spec rest hwf.2.2
      cases p with
      | nil => simp [lookupRaw] at hLook
      | cons seg r =>
        by_cases hsk : k = seg
        · subst hsk
          rw [lookupRaw_cons_eq_lookupNodeRaw none (.cons k c rest) k r c (by simp [TrieChildren.get])] at hLook
          simp only [dropDefaultChildren]
          cases hdn : dropDefaultNode c with
          | none =>
            have h0 : ts = .owned 0 .created := hNode.1 hdn r ts hLook
            have hgNone : (dropDefaultChildren rest).get k = none :=
              get_dropDefaultChildren_none rest k hwf.1
            refine ⟨?_, fun hne => absurd h0 hne⟩
            intro _
            exact Or.inl (by simp [lookupRaw, hgNone])
          | some c' =>
            rw [lookupRaw_cons_eq_lookupNodeRaw none (.cons k c' (dropDefaultChildren rest)) k r c' (by simp [TrieChildren.get])]
            exact hNode.2 c' hdn r ts hLook
        · have hLookRest : lookupRaw none rest (seg :: r) = some ts := by
            simpa [lookupRaw, TrieChildren.get, hsk] using hLook
          simp only [dropDefaultChildren]
          cases hdn : dropDefaultNode c with
          | none => exact hRest (seg :: r) ts hLookRest
          | some c' =>
            have hEq : lookupRaw none (.cons k c' (dropDefaultChildren rest)) (seg :: r) =
                lookupRaw none (dropDefaultChildren rest) (seg :: r) := by
              simp [lookupRaw, TrieChildren.get, hsk]
            rw [hEq]
            exact hRest (seg :: r) ts hLookRest
end

/-- **Theorem 10 (Compact Trie Roundtrip Equivalence)**:
    Encoding any prefix-free leaf ownership map with `encodeTrie` (including `actors[0]`
    creator fallback omission, explicit `(actors[0], .modified)` / `.unowned` / `.defaulted`
    markers, actor table compaction, and `w = true` subtree compression) and decoding any
    leaf recovers its exact `LeafStatus`. -/
theorem trie_encode_decode_roundtrip
    (creator : ActorID) (leaves : List (Path × LeafStatus))
    (hCreator : validActor creator = true)
    (hWF : TrieLeavesWF leaves) :
    ∀ p ls, (p, ls) ∈ leaves →
      decodeLeaf (encodeTrie creator leaves) p = .ok ls := by
  intro p ls hmem
  unfold encodeTrie decodeLeaf
  dsimp only [buildActorTable]
  have hIns := insertAllTrie_spec (creator :: collectForeignActors creator leaves) leaves hWF
  have hRaw := hIns.1
  have hLookRaw := hIns.2 p ls hmem
  have hComp := compressChildren_spec (insertAllTrie (creator :: collectForeignActors creator leaves) leaves) hRaw
  have hLookComp := (hComp.2 p (toTrieState (creator :: collectForeignActors creator leaves) ls) hLookRaw).2
  have hDrop := dropDefaultChildren_spec (compressChildren (insertAllTrie (creator :: collectForeignActors creator leaves) leaves)).1 hComp.1 p _ hLookComp
  simp only [hCreator, Bool.not_true, Bool.false_eq_true, ↓reduceIte]
  have hRes : resolveTrieState (creator :: collectForeignActors creator leaves)
      (toTrieState (creator :: collectForeignActors creator leaves) ls) = some ls :=
    resolveTrieState_toTrieState (creator :: collectForeignActors creator leaves) ls
      (fun m o hls => by subst hls; exact mem_buildActorTable creator m p o leaves hmem)
  by_cases hDef : toTrieState (creator :: collectForeignActors creator leaves) ls = .owned 0 .created
  · have hLsEq : ls = .owned creator .created := (toTrieState_eq_default_iff creator _ ls).mp hDef
    subst hLsEq
    rcases hDrop.1 hDef with hNone | hSome
    · rw [hNone]
    · rw [hSome]
      simp [resolveTrieState]
  · rw [hDrop.2 hDef]
    dsimp only
    rw [hRes]

/-! ### Part 3: Multi-Object Single-Txn CAS Protocol Theorems (Theorems 11–12) -/

private theorem append_slash_inj : ∀ (xs ys r₁ r₂ : List Char),
    '/' ∉ xs → '/' ∉ ys → xs ++ '/' :: r₁ = ys ++ '/' :: r₂ → xs = ys ∧ r₁ = r₂
  | [], [], _, _, _, _, h => by cases h; exact ⟨rfl, rfl⟩
  | [], y :: _, _, _, _, hy, h => by
    simp only [List.nil_append, List.cons_append, List.cons.injEq] at h
    exact absurd h.1.symm (fun heq => hy (heq ▸ List.mem_cons_self))
  | x :: _, [], _, _, hx, _, h => by
    simp only [List.nil_append, List.cons_append, List.cons.injEq] at h
    exact absurd h.1 (fun heq => hx (heq ▸ List.mem_cons_self))
  | x :: xs, y :: ys, r₁, r₂, hx, hy, h => by
    simp only [List.cons_append, List.cons.injEq] at h
    obtain ⟨rfl, hrest⟩ := h
    have hx' : '/' ∉ xs := fun hm => hx (List.mem_cons_of_mem x hm)
    have hy' : '/' ∉ ys := fun hm => hy (List.mem_cons_of_mem x hm)
    obtain ⟨hxs, hr⟩ := append_slash_inj xs ys r₁ r₂ hx' hy' hrest
    exact ⟨congrArg (x :: ·) hxs, hr⟩

private theorem noSlash_of_validSeg {s : String} (h : validSegNoSlash s = true) :
    '/' ∉ s.toList := by
  unfold validSegNoSlash at h
  simp only [Bool.and_eq_true, Bool.not_eq_true', List.contains_eq_mem, decide_eq_false_iff_not] at h
  exact h.2

private theorem validAddr_noSlashes {a : ObjAddr} (h : validAddr a = true) :
    '/' ∉ a.tenant.toList ∧ '/' ∉ a.group.toList ∧ '/' ∉ a.kind.toList ∧ '/' ∉ a.name.toList := by
  unfold validAddr at h
  simp only [Bool.and_eq_true] at h
  exact ⟨noSlash_of_validSeg h.1.1.1, noSlash_of_validSeg h.1.1.2,
    noSlash_of_validSeg h.1.2, noSlash_of_validSeg h.2⟩

private theorem oKey_ne_ownKey {a b : ObjAddr} (ha : validAddr a = true) (hb : validAddr b = true) :
    a.oKey ≠ b.ownKey := by
  intro heq
  have htl := congrArg String.toList heq
  simp only [ObjAddr.oKey, ObjAddr.ownKey, String.toList_append, List.append_assoc] at htl
  have h1 : a.tenant.toList ++ '/' :: ('o' :: '/' :: (a.group.toList ++ '/' :: (a.kind.toList ++ '/' :: a.name.toList))) =
            b.tenant.toList ++ '/' :: ('o' :: 'w' :: 'n' :: '/' :: (b.group.toList ++ '/' :: (b.kind.toList ++ '/' :: b.name.toList))) := by
    have ht : ("/t/".toList : List Char) = ['/', 't', '/'] := rfl
    have ho : ("/o/".toList : List Char) = ['/', 'o', '/'] := rfl
    have hown : ("/own/".toList : List Char) = ['/', 'o', 'w', 'n', '/'] := rfl
    have hslash : ("/".toList : List Char) = ['/'] := rfl
    rw [ht, ho, hown, hslash] at htl
    simp only [List.cons_append, List.nil_append, List.cons.injEq] at htl
    exact htl.2.2.2
  have ⟨_, hr⟩ := append_slash_inj _ _ _ _ (validAddr_noSlashes ha).1 (validAddr_noSlashes hb).1 h1
  simp at hr

private theorem oKey_inj {a b : ObjAddr} (ha : validAddr a = true) (hb : validAddr b = true)
    (heq : a.oKey = b.oKey) : a = b := by
  have htl := congrArg String.toList heq
  simp only [ObjAddr.oKey, String.toList_append, List.append_assoc] at htl
  have ht : ("/t/".toList : List Char) = ['/', 't', '/'] := rfl
  have ho : ("/o/".toList : List Char) = ['/', 'o', '/'] := rfl
  have hslash : ("/".toList : List Char) = ['/'] := rfl
  rw [ht, ho, hslash] at htl
  simp only [List.cons_append, List.nil_append, List.cons.injEq] at htl
  have h1 := append_slash_inj _ _ _ _ (validAddr_noSlashes ha).1 (validAddr_noSlashes hb).1 htl.2.2.2
  have h2 := h1.2
  simp only [List.cons.injEq] at h2
  have h3 := append_slash_inj _ _ _ _ (validAddr_noSlashes ha).2.1 (validAddr_noSlashes hb).2.1 h2.2.2
  have h4 := append_slash_inj _ _ _ _ (validAddr_noSlashes ha).2.2.1 (validAddr_noSlashes hb).2.2.1 h3.2
  cases a; cases b
  congr
  · exact String.ext h1.1
  · exact String.ext h3.1
  · exact String.ext h4.1
  · exact String.ext h4.2

private theorem ownKey_inj {a b : ObjAddr} (ha : validAddr a = true) (hb : validAddr b = true)
    (heq : a.ownKey = b.ownKey) : a = b := by
  have htl := congrArg String.toList heq
  simp only [ObjAddr.ownKey, String.toList_append, List.append_assoc] at htl
  have ht : ("/t/".toList : List Char) = ['/', 't', '/'] := rfl
  have hown : ("/own/".toList : List Char) = ['/', 'o', 'w', 'n', '/'] := rfl
  have hslash : ("/".toList : List Char) = ['/'] := rfl
  rw [ht, hown, hslash] at htl
  simp only [List.cons_append, List.nil_append, List.cons.injEq] at htl
  have h1 := append_slash_inj _ _ _ _ (validAddr_noSlashes ha).1 (validAddr_noSlashes hb).1 htl.2.2.2
  have h2 := h1.2
  simp only [List.cons.injEq] at h2
  have h3 := append_slash_inj _ _ _ _ (validAddr_noSlashes ha).2.1 (validAddr_noSlashes hb).2.1 h2.2.2.2.2
  have h4 := append_slash_inj _ _ _ _ (validAddr_noSlashes ha).2.2.1 (validAddr_noSlashes hb).2.2.1 h3.2
  cases a; cases b
  congr
  · exact String.ext h1.1
  · exact String.ext h3.1
  · exact String.ext h4.1
  · exact String.ext h4.2

private theorem foldl_commits_le_succ {f : Option KV → Nayutar.Store.Commit → Option KV}
    (r : Rev) : ∀ (cs : List Nayutar.Store.Commit) (acc : Option KV),
    (∀ c ∈ cs, c.rev ≤ r) →
    cs.foldl (fun a c => if c.rev ≤ r + 1 then f a c else a) acc =
    cs.foldl (fun a c => if c.rev ≤ r then f a c else a) acc
  | [], _, _ => rfl
  | c :: rest, acc, hle => by
    have hc : c.rev ≤ r := hle c List.mem_cons_self
    have hc1 : c.rev ≤ r + 1 := Nat.le_succ_of_le hc
    simp only [List.foldl_cons, hc, hc1, ↓reduceIte]
    exact foldl_commits_le_succ r rest (f acc c) (fun c' hm => hle c' (List.mem_cons_of_mem c hm))

private theorem find_append_put1 (s : Store) (k v key : String)
    (hrev : ∀ c ∈ s.commits, c.rev ≤ s.rev) :
    let s' : Store := { s with rev := s.rev + 1, commits := s.commits ++ [{ rev := s.rev + 1, writes := [.put k v] }] }
    s'.find? s'.rev key =
      if k = key then some { key := k, value := v, modRev := s.rev + 1 }
      else s.find? s.rev key := by
  dsimp only
  unfold Store.find?
  rw [List.foldl_append, foldl_commits_le_succ s.rev s.commits none hrev]
  simp only [List.foldl_cons, List.foldl_nil, Nat.le_refl, ↓reduceIte]
  rfl

private theorem find_append_put2 (s : Store) (k1 v1 k2 v2 key : String)
    (hrev : ∀ c ∈ s.commits, c.rev ≤ s.rev) :
    let s' : Store := { s with rev := s.rev + 1, commits := s.commits ++ [{ rev := s.rev + 1, writes := [.put k1 v1, .put k2 v2] }] }
    s'.find? s'.rev key =
      if k2 = key then some { key := k2, value := v2, modRev := s.rev + 1 }
      else if k1 = key then some { key := k1, value := v1, modRev := s.rev + 1 }
      else s.find? s.rev key := by
  dsimp only
  unfold Store.find?
  rw [List.foldl_append, foldl_commits_le_succ s.rev s.commits none hrev]
  simp only [List.foldl_cons, List.foldl_nil, Nat.le_refl, ↓reduceIte]
  rfl

private theorem find_append_del2 (s : Store) (k1 k2 key : String)
    (hrev : ∀ c ∈ s.commits, c.rev ≤ s.rev) :
    let s' : Store := { s with rev := s.rev + 1, commits := s.commits ++ [{ rev := s.rev + 1, writes := [.delete k1, .delete k2] }] }
    s'.find? s'.rev key =
      if k2 = key then none
      else if k1 = key then none
      else s.find? s.rev key := by
  dsimp only
  unfold Store.find?
  rw [List.foldl_append, foldl_commits_le_succ s.rev s.commits none hrev]
  simp only [List.foldl_cons, List.foldl_nil, Nat.le_refl, ↓reduceIte]
  rfl

private theorem txn_ok_put1 {s store' : Store} {cmps : List Cmp} {k v : String} {rev : Rev}
    (h : s.txn cmps [.put k v] = (.ok (true, rev), store')) :
    store' = { s with rev := s.rev + 1, commits := s.commits ++ [{ rev := s.rev + 1, writes := [.put k v] }] } := by
  unfold Store.txn at h
  split at h
  · cases h
  · split at h
    · cases h
    · simp only [List.filter, List.isEmpty_cons, Bool.false_eq_true, ↓reduceIte,
        Prod.mk.injEq, Except.ok.injEq] at h
      exact h.2.symm

private theorem txn_ok_put2 {s store' : Store} {cmps : List Cmp} {k1 v1 k2 v2 : String} {rev : Rev}
    (h : s.txn cmps [.put k1 v1, .put k2 v2] = (.ok (true, rev), store')) :
    store' = { s with rev := s.rev + 1, commits := s.commits ++ [{ rev := s.rev + 1, writes := [.put k1 v1, .put k2 v2] }] } := by
  unfold Store.txn at h
  split at h
  · cases h
  · split at h
    · cases h
    · simp only [List.filter, List.isEmpty_cons, Bool.false_eq_true, ↓reduceIte,
        Prod.mk.injEq, Except.ok.injEq] at h
      exact h.2.symm

private theorem txn_ok_del2 {s store' : Store} {cmps : List Cmp} {k1 k2 : String} {rev : Rev}
    (h1 : (s.find? s.rev k1).isSome = true) (h2 : (s.find? s.rev k2).isSome = true)
    (h : s.txn cmps [.delete k1, .delete k2] = (.ok (true, rev), store')) :
    store' = { s with rev := s.rev + 1, commits := s.commits ++ [{ rev := s.rev + 1, writes := [.delete k1, .delete k2] }] } := by
  unfold Store.txn at h
  split at h
  · cases h
  · split at h
    · cases h
    · simp only [List.filter, h1, h2, List.isEmpty_cons, Bool.false_eq_true, ↓reduceIte,
        Prod.mk.injEq, Except.ok.injEq] at h
      exact h.2.symm

/-! ### Projection Well-Formedness and Non-Overlap Lemmas -/

private theorem fields_unique_get : ∀ (fs : Fields) (k : String) (v : Value),
    Fields.unique fs = true → fs.get k = some v → v.unique = true
  | .nil, _, _, _, hg => by simp [Fields.get] at hg
  | .cons k' v' rest, k, v, h, hg => by
    simp only [Fields.unique, Bool.and_eq_true] at h
    simp only [Fields.get] at hg
    split at hg
    · cases hg; exact h.1
    · exact fields_unique_get rest k v h.2 hg

private theorem projectLabelsFields_spec : ∀ (ls : Fields) (rej : Bool) (out : List (Path × Value)),
    ls.keysDistinct = true →
    projectLabelsFields rej ls = .ok out →
    out.Pairwise (fun a b => overlaps a.1 b.1 = false) ∧
    (∀ p v, (p, v) ∈ out → ∃ k, ls.get k ≠ none ∧ validSeg k = true ∧ p = ["meta", "labels", k])
  | .nil, _, out, _, h => by
    cases h
    exact ⟨List.Pairwise.nil, fun _ _ hm => by cases hm⟩
  | .cons k v rest, rej, out, hd, h => by
    simp only [Fields.keysDistinct, Bool.and_eq_true] at hd
    simp only [projectLabelsFields] at h
    split at h
    · cases h
    · rename_i hseg
      have hsegT : validSeg k = true := by cases hv : validSeg k <;> simp [hv] at hseg ⊢
      cases v with
      | record sub =>
        cases sub with
        | nil => dsimp only at h; cases rej <;> cases h
        | cons _ _ _ => dsimp only at h; cases h
      | str _ | int _ | dec _ | bool _ | enum _ | qty _ _ | ref _ | list _ =>
        dsimp only at h
        split at h
        · cases h
        · cases ht : projectLabelsFields rej rest with
          | error e =>
            rw [ht] at h
            simp only [bind, Except.bind] at h
            cases h
          | ok tail =>
            rw [ht] at h
            simp only [bind, Except.bind, Except.ok.injEq] at h
            subst h
            have ⟨hpwT, hMemT⟩ := projectLabelsFields_spec rest rej tail hd.2 ht
            refine ⟨?_, ?_⟩
            · simp only [List.pairwise_cons]
              refine ⟨?_, hpwT⟩
              intro x hx
              obtain ⟨k', hk', _, hxEq⟩ := hMemT x.1 x.2 hx
              rw [hxEq]
              have hne : k ≠ k' := by
                intro heq; subst heq
                cases hrg : rest.get k with
                | none => exact hk' hrg
                | some _ => rw [hrg] at hd; simp at hd
              exact overlaps_append_key_ne ["meta", "labels"] [] [] hne
            · intro p val hp
              rcases List.mem_cons.mp hp with heq | hmt
              · cases heq
                exact ⟨k, by simp [Fields.get], hsegT, rfl⟩
              · obtain ⟨k', hk', hsk', hpEq⟩ := hMemT p val hmt
                refine ⟨k', ?_, hsk', hpEq⟩
                simp only [Fields.get]
                split
                · intro hc; cases hc
                · exact hk'

mutual
  private theorem projectSpecValue_spec : ∀ (v : Value) (pfx : Path) (rej : Bool) (out : List (Path × Value)),
      v.unique = true →
      pfx.length ≥ 2 → pfx.length ≤ maxPathDepth + 1 →
      (∃ s0 s1 r, pfx = "spec" :: s1 :: r ∧ (s1 :: r).all validSeg = true ∧ s0 = "spec") →
      projectSpecValue pfx rej v = .ok out →
      out.Pairwise (fun a b => overlaps a.1 b.1 = false) ∧
      (∀ p val, (p, val) ∈ out →
        (∃ suf, p = pfx ++ suf) ∧ inEntranceSpec p = true)
    | .record .nil, pfx, rej, out, _, _, _, _, h => by
      simp only [projectSpecValue] at h
      split at h
      · cases h
      · cases h
        exact ⟨List.Pairwise.nil, fun _ _ hm => by cases hm⟩
    | .record (.cons k val rest), pfx, rej, out, hu, hge, _, hpfx, h => by
      simp only [projectSpecValue] at h
      split at h
      · cases h
      · rename_i hlen
        have hle : pfx.length ≤ maxPathDepth := by
          simp only [decide_eq_true_eq] at hlen; omega
        simp only [Value.unique, Bool.and_eq_true] at hu
        obtain ⟨_, s1, r, hpfxEq, hall, _⟩ := hpfx
        have ⟨hpw, hmem⟩ := projectSpecFields_spec (.cons k val rest) pfx rej out hu.1 hu.2 (by omega) hle (Or.inr ⟨s1, r, hpfxEq, hall⟩) h
        refine ⟨hpw, fun p v' hm => ?_⟩
        obtain ⟨k', suf, _, hpEq, hin⟩ := hmem p v' hm
        exact ⟨⟨k' :: suf, by simpa [List.append_assoc] using hpEq⟩, hin⟩
    | .str _, pfx, _, out, _, _, hle, hpfx, h
    | .int _, pfx, _, out, _, _, hle, hpfx, h
    | .dec _, pfx, _, out, _, _, hle, hpfx, h
    | .bool _, pfx, _, out, _, _, hle, hpfx, h
    | .enum _, pfx, _, out, _, _, hle, hpfx, h
    | .qty _ _, pfx, _, out, _, _, hle, hpfx, h
    | .ref _, pfx, _, out, _, _, hle, hpfx, h
    | .list _, pfx, _, out, _, _, hle, hpfx, h => by
      simp only [projectSpecValue] at h
      split at h
      · cases h
      · cases h
        obtain ⟨_, s1, r, rfl, hall, _⟩ := hpfx
        refine ⟨by simp, fun p val hm => ?_⟩
        simp only [List.mem_singleton, Prod.mk.injEq] at hm
        obtain ⟨rfl, _⟩ := hm
        refine ⟨⟨[], by simp⟩, ?_⟩
        simp only [inEntranceSpec, Bool.and_eq_true, decide_eq_true_eq]
        simp only [List.length_cons] at hle
        exact ⟨by omega, hall⟩

  private theorem projectSpecFields_spec : ∀ (fs : Fields) (pfx : Path) (rej : Bool) (out : List (Path × Value)),
      fs.keysDistinct = true → Fields.unique fs = true →
      pfx.length ≥ 1 → pfx.length ≤ maxPathDepth →
      (pfx = ["spec"] ∨ ∃ s1 r, pfx = "spec" :: s1 :: r ∧ (s1 :: r).all validSeg = true) →
      projectSpecFields pfx rej fs = .ok out →
      out.Pairwise (fun a b => overlaps a.1 b.1 = false) ∧
      (∀ p val, (p, val) ∈ out →
        ∃ k suf, fs.get k ≠ none ∧ p = pfx ++ [k] ++ suf ∧ inEntranceSpec p = true)
    | .nil, _, _, out, _, _, _, _, _, h => by
      cases h
      exact ⟨List.Pairwise.nil, fun _ _ hm => by cases hm⟩
    | .cons k v rest, pfx, rej, out, hd, hu, hge, hle, hpfx, h => by
      simp only [Fields.keysDistinct, Bool.and_eq_true] at hd
      simp only [Fields.unique, Bool.and_eq_true] at hu
      simp only [projectSpecFields] at h
      split at h
      · cases h
      · rename_i hseg
        have hsegT : validSeg k = true := by cases hv : validSeg k <;> simp [hv] at hseg ⊢
        cases hh : projectSpecValue (pfx ++ [k]) rej v with
        | error e =>
          rw [hh] at h
          simp only [bind, Except.bind] at h
          cases h
        | ok head =>
          rw [hh] at h
          cases ht : projectSpecFields pfx rej rest with
          | error e =>
            rw [ht] at h
            simp only [bind, Except.bind] at h
            cases h
          | ok tail =>
            rw [ht] at h
            simp only [bind, Except.bind, Except.ok.injEq] at h
            subst h
            have hpfxK : ∃ s0 s1 r, pfx ++ [k] = "spec" :: s1 :: r ∧ (s1 :: r).all validSeg = true ∧ s0 = "spec" := by
              rcases hpfx with rfl | ⟨s1, r, rfl, hall⟩
              · exact ⟨"spec", k, [], rfl, by simp [hsegT], rfl⟩
              · refine ⟨"spec", s1, r ++ [k], by simp [List.cons_append], ?_, rfl⟩
                simp only [List.all_cons, List.all_append, Bool.and_eq_true] at hall ⊢
                exact ⟨hall.1, hall.2, hsegT, rfl⟩
            have ⟨hpwH, hMemH⟩ := projectSpecValue_spec v (pfx ++ [k]) rej head hu.1
              (by simp; omega) (by simp; omega) hpfxK hh
            have ⟨hpwT, hMemT⟩ := projectSpecFields_spec rest pfx rej tail hd.2 hu.2 hge hle hpfx ht
            refine ⟨?_, ?_⟩
            · rw [List.pairwise_append]
              refine ⟨hpwH, hpwT, ?_⟩
              intro a ha b hb
              obtain ⟨⟨suf1, haEq⟩, _⟩ := hMemH a.1 a.2 ha
              obtain ⟨k', suf2, hk', hbEq, _⟩ := hMemT b.1 b.2 hb
              have hne : k ≠ k' := by
                intro heq; subst heq
                cases hrg : rest.get k with
                | none => exact hk' hrg
                | some _ => rw [hrg] at hd; simp at hd
              rw [haEq, hbEq]
              exact overlaps_append_key_ne pfx suf1 suf2 hne
            · intro p val hm
              rcases List.mem_append.mp hm with hmh | hmt
              · obtain ⟨⟨suf, hpEq⟩, hin⟩ := hMemH p val hmh
                exact ⟨k, suf, by simp [Fields.get], hpEq, hin⟩
              · obtain ⟨k', suf, hk', hpEq, hin⟩ := hMemT p val hmt
                refine ⟨k', suf, ?_, hpEq, hin⟩
                simp only [Fields.get]
                split
                · intro hc; cases hc
                · exact hk'
end

private theorem projectEntranceSpec_spec (doc : Value) (rej : Bool) (decl : List (Path × Value))
    (h : projectEntranceSpec doc rej = .ok decl) :
    decl.Pairwise (fun a b => overlaps a.1 b.1 = false) ∧
    (∀ p v, (p, v) ∈ decl → inEntranceSpec p = true) := by
  unfold projectEntranceSpec at h
  split at h
  · cases h
  · rename_i hU
    have hDocU : doc.unique = true := by cases hv : doc.unique <;> simp [hv] at hU ⊢
    cases doc with
    | record top =>
      simp only [Value.unique, Bool.and_eq_true] at hDocU
      dsimp only at h
      have hFinish : ∀ (ll : List (Path × Value)),
          ll.Pairwise (fun a b => overlaps a.1 b.1 = false) →
          (∀ p v, (p, v) ∈ ll → ∃ k, validSeg k = true ∧ p = ["meta", "labels", k]) →
          (do
            let specLeaves ← match top.get "spec" with
              | none => Except.ok []
              | some (.record .nil) => Except.ok []
              | some (.record sp) => projectSpecFields ["spec"] rej sp
              | some _ => Except.error .K101
            Except.ok (sortPairs (ll ++ specLeaves))) = Except.ok decl →
          decl.Pairwise (fun a b => overlaps a.1 b.1 = false) ∧
          (∀ p v, (p, v) ∈ decl → inEntranceSpec p = true) := by
        intro ll hpwL hMemL hFin
        have hCombine : ∀ (sl : List (Path × Value)),
            sl.Pairwise (fun a b => overlaps a.1 b.1 = false) →
            (∀ p v, (p, v) ∈ sl → (∃ s1 r, p = "spec" :: s1 :: r) ∧ inEntranceSpec p = true) →
            decl = sortPairs (ll ++ sl) →
            decl.Pairwise (fun a b => overlaps a.1 b.1 = false) ∧
            (∀ p v, (p, v) ∈ decl → inEntranceSpec p = true) := by
          intro sl hpwS hMemS hDeclEq
          subst hDeclEq
          have hpwBoth : (ll ++ sl).Pairwise (fun a b => overlaps a.1 b.1 = false) := by
            rw [List.pairwise_append]
            refine ⟨hpwL, hpwS, ?_⟩
            intro a ha b hb
            obtain ⟨k, _, haEq⟩ := hMemL a.1 a.2 ha
            obtain ⟨⟨s1, r, hbEq⟩, _⟩ := hMemS b.1 b.2 hb
            rw [haEq, hbEq]
            exact overlaps_append_key_ne [] ["labels", k] (s1 :: r) (by decide)
          have hDistBoth : (ll ++ sl).Pairwise (fun a b => a.1 ≠ b.1) :=
            hpwBoth.imp (fun hno => overlaps_false_implies_ne hno)
          refine ⟨pairwise_sortPairs_of_no_overlap (ll ++ sl) hpwBoth, ?_⟩
          intro p v hp
          have hmem := (mem_sortPairs_of_distinct (ll ++ sl) (p, v) hDistBoth).mp hp
          rcases List.mem_append.mp hmem with hml | hms
          · obtain ⟨k, hsk, hpEq⟩ := hMemL p v hml
            rw [hpEq]; exact hsk
          · exact (hMemS p v hms).2
        cases hs : top.get "spec" with
        | none =>
          rw [hs] at hFin
          simp only [bind, Except.bind, Except.ok.injEq] at hFin
          exact hCombine [] List.Pairwise.nil (fun _ _ hm => by cases hm) hFin.symm
        | some sv =>
          rw [hs] at hFin
          cases sv with
          | record sp =>
            have hspU : (Value.record sp).unique = true := fields_unique_get top "spec" (.record sp) hDocU.2 hs
            simp only [Value.unique, Bool.and_eq_true] at hspU
            cases sp with
            | nil =>
              simp only [bind, Except.bind, Except.ok.injEq] at hFin
              exact hCombine [] List.Pairwise.nil (fun _ _ hm => by cases hm) hFin.symm
            | cons k' v' r' =>
              dsimp only at hFin
              cases hps : projectSpecFields ["spec"] rej (.cons k' v' r') with
              | error e =>
                rw [hps] at hFin
                simp only [bind, Except.bind] at hFin
                cases hFin
              | ok sl =>
                rw [hps] at hFin
                simp only [bind, Except.bind, Except.ok.injEq] at hFin
                have ⟨hpwS, hMemS⟩ := projectSpecFields_spec (.cons k' v' r') ["spec"] rej sl hspU.1 hspU.2
                  (by decide) (by decide) (Or.inl rfl) hps
                exact hCombine sl hpwS (fun p v hp => by
                  obtain ⟨k, suf, _, hpEq, hin⟩ := hMemS p v hp
                  exact ⟨⟨k, suf, by simpa using hpEq⟩, hin⟩) hFin.symm
          | _ =>
            simp only [bind, Except.bind] at hFin
            cases hFin
      cases hm : top.get "meta" with
      | none =>
        rw [hm] at h
        simp only [bind, Except.bind] at h
        exact hFinish [] List.Pairwise.nil (fun _ _ hm' => by cases hm') h
      | some mv =>
        rw [hm] at h
        cases mv with
        | record m =>
          dsimp only at h
          have hmU : (Value.record m).unique = true := fields_unique_get top "meta" (.record m) hDocU.2 hm
          simp only [Value.unique, Bool.and_eq_true] at hmU
          cases hl : m.get "labels" with
          | none =>
            rw [hl] at h
            simp only [bind, Except.bind] at h
            exact hFinish [] List.Pairwise.nil (fun _ _ hm' => by cases hm') h
          | some lv =>
            rw [hl] at h
            cases lv with
            | record ls =>
              have hlsU : (Value.record ls).unique = true := fields_unique_get m "labels" (.record ls) hmU.2 hl
              simp only [Value.unique, Bool.and_eq_true] at hlsU
              cases ls with
              | nil =>
                simp only [bind, Except.bind] at h
                exact hFinish [] List.Pairwise.nil (fun _ _ hm' => by cases hm') h
              | cons k' v' r' =>
                dsimp only at h
                cases hpl : projectLabelsFields rej (.cons k' v' r') with
                | error e =>
                  rw [hpl] at h
                  simp only [bind, Except.bind] at h
                  cases h
                | ok ll =>
                  rw [hpl] at h
                  simp only [bind, Except.bind] at h
                  have ⟨hpwL, hMemL⟩ := projectLabelsFields_spec (.cons k' v' r') rej ll hlsU.1 hpl
                  exact hFinish ll hpwL (fun p v hp => by
                    obtain ⟨k, _, hsk, hpEq⟩ := hMemL p v hp
                    exact ⟨k, hsk, hpEq⟩) h
            | _ =>
              simp only [bind, Except.bind] at h
              cases h
        | _ =>
          simp only [bind, Except.bind] at h
          cases h
    | _ => cases h

/-! ### CAS Protocol Invariant Proofs -/

private theorem pairwise_overlaps_false_of_mem {α : Type} : ∀ (l : List (Path × α)) {p1 p2 : Path} {v1 v2 : α},
    l.Pairwise (fun x y => overlaps x.1 y.1 = false) →
    (p1, v1) ∈ l → (p2, v2) ∈ l → p1 ≠ p2 → overlaps p1 p2 = false
  | [], _, _, _, _, _, h, _, _ => by cases h
  | x :: xs, p1, p2, v1, v2, hpw, hx1, hx2, hne => by
    simp only [List.pairwise_cons] at hpw
    rcases List.mem_cons.mp hx1 with h1 | hm1 <;>
    rcases List.mem_cons.mp hx2 with h2 | hm2
    · cases h1; cases h2; exact absurd rfl hne
    · cases h1; exact hpw.1 (p2, v2) hm2
    · cases h2; rw [overlaps_symm]; exact hpw.1 (p1, v1) hm1
    · exact pairwise_overlaps_false_of_mem xs hpw.2 hm1 hm2 hne

mutual
  private theorem removeDefaultedValue_entries_sub :
      ∀ (v : Value) (pfx : Path) (ow : OwnerState) (x : Path × LeafStatus),
      x ∈ (removeDefaultedValue pfx ow v).entries → x ∈ ow.entries
    | .record fs, pfx, ow, x, hx => removeDefaultedFields_entries_sub fs pfx ow x hx
    | .str _, pfx, ow, x, hx
    | .int _, pfx, ow, x, hx
    | .dec _, pfx, ow, x, hx
    | .bool _, pfx, ow, x, hx
    | .enum _, pfx, ow, x, hx
    | .qty _ _, pfx, ow, x, hx
    | .ref _, pfx, ow, x, hx
    | .list _, pfx, ow, x, hx => ((mem_ownerState_remove ow pfx x).mp hx).1

  private theorem removeDefaultedFields_entries_sub :
      ∀ (fs : Fields) (pfx : Path) (ow : OwnerState) (x : Path × LeafStatus),
      x ∈ (removeDefaultedFields pfx ow fs).entries → x ∈ ow.entries
    | .nil, _, _, _, hx => hx
    | .cons k v rest, pfx, ow, x, hx =>
      removeDefaultedValue_entries_sub v (pfx ++ [k]) ow x
        (removeDefaultedFields_entries_sub rest pfx (removeDefaultedValue (pfx ++ [k]) ow v) x hx)
end

private theorem remove_preserves_wf (ow : OwnerState) (p : Path) (hwf : OwnerStateWF ow) :
    OwnerStateWF (ow.remove p) :=
  ⟨hwf.1, pairwise_ownerState_remove ow p hwf.2.1,
   fun q s h => hwf.2.2.1 q s ((mem_ownerState_remove ow p (q, s)).mp h).1,
   fun p1 m1 o1 p2 m2 o2 h1 h2 hov =>
     hwf.2.2.2 p1 m1 o1 p2 m2 o2
       ((mem_ownerState_remove ow p _).mp h1).1 ((mem_ownerState_remove ow p _).mp h2).1 hov⟩

mutual
  private theorem removeDefaultedValue_preserves_wf :
      ∀ (v : Value) (pfx : Path) (ow : OwnerState),
      OwnerStateWF ow → OwnerStateWF (removeDefaultedValue pfx ow v) ∧
      (removeDefaultedValue pfx ow v).creator = ow.creator
    | .record fs, pfx, ow, hwf => removeDefaultedFields_preserves_wf fs pfx ow hwf
    | .str _, pfx, ow, hwf
    | .int _, pfx, ow, hwf
    | .dec _, pfx, ow, hwf
    | .bool _, pfx, ow, hwf
    | .enum _, pfx, ow, hwf
    | .qty _ _, pfx, ow, hwf
    | .ref _, pfx, ow, hwf
    | .list _, pfx, ow, hwf => ⟨remove_preserves_wf ow pfx hwf, rfl⟩

  private theorem removeDefaultedFields_preserves_wf :
      ∀ (fs : Fields) (pfx : Path) (ow : OwnerState),
      OwnerStateWF ow → OwnerStateWF (removeDefaultedFields pfx ow fs) ∧
      (removeDefaultedFields pfx ow fs).creator = ow.creator
    | .nil, _, _, hwf => ⟨hwf, rfl⟩
    | .cons k v rest, pfx, ow, hwf => by
      have ⟨hw1, hc1⟩ := removeDefaultedValue_preserves_wf v (pfx ++ [k]) ow hwf
      have ⟨hw2, hc2⟩ := removeDefaultedFields_preserves_wf rest pfx (removeDefaultedValue (pfx ++ [k]) ow v) hw1
      exact ⟨hw2, hc2.trans hc1⟩
end

private theorem deletePruneSub_preserves_wf :
    ∀ (p : Path) (pfx : Path) (fs : Fields) (ow : OwnerState) (w : Path → Option ActorID),
    OwnerStateWF ow → ProvenanceWF w ow →
    OwnerStateWF (deletePruneSub pfx fs p ow).2 ∧ ProvenanceWF w (deletePruneSub pfx fs p ow).2
  | [], _, _, _, _, hwf, hprov => ⟨hwf, hprov⟩
  | [_], _, _, _, _, hwf, hprov => ⟨hwf, hprov⟩
  | k :: k2 :: rest, pfx, fs, ow, w, hwf, hprov => by
    simp only [deletePruneSub]
    cases fs.get k with
    | none => exact ⟨hwf, hprov⟩
    | some val =>
      cases val with
      | record sub =>
        dsimp only
        have ih := deletePruneSub_preserves_wf (k2 :: rest) (pfx ++ [k]) sub ow w hwf hprov
        cases hSub : deletePruneSub (pfx ++ [k]) sub (k2 :: rest) ow with
        | mk sub' ow1 =>
          rw [hSub] at ih
          dsimp only at ih ⊢
          cases sub' with
          | nil => exact ih
          | cons k' v' rest' =>
            dsimp only
            by_cases hDef : onlyDefaultedFields (pfx ++ [k]) ow1 (.cons k' v' rest') = true
            · simp only [hDef, ↓reduceIte]
              refine ⟨(removeDefaultedFields_preserves_wf (.cons k' v' rest') (pfx ++ [k]) ow1 ih.1).1, ?_⟩
              intro q m hm
              exact ih.2 q m (removeDefaultedFields_entries_sub (.cons k' v' rest') (pfx ++ [k]) ow1 (q, .owned m .created) hm)
            · simp only [hDef]
              exact ih
      | _ => exact ⟨hwf, hprov⟩

private theorem deletePruneDoc_preserves_wf (doc : Value) (p : Path) (ow : OwnerState)
    (w : Path → Option ActorID) (hwf : OwnerStateWF ow) (hprov : ProvenanceWF w ow) :
    OwnerStateWF (deletePruneDoc doc p ow).2 ∧ ProvenanceWF w (deletePruneDoc doc p ow).2 := by
  unfold deletePruneDoc
  split
  · rename_i top k rest
    split
    · rename_i sp _
      exact deletePruneSub_preserves_wf (k :: rest) ["spec"] sp ow w hwf hprov
    · exact ⟨hwf, hprov⟩
  · rename_i top k
    split
    · split
      · exact ⟨hwf, hprov⟩
      · exact ⟨hwf, hprov⟩
    · exact ⟨hwf, hprov⟩
  · exact ⟨hwf, hprov⟩

private theorem applyDocOwnerOps_preserves_wf : ∀ (ops : List DocOp) (doc : Value) (ow : OwnerState)
    (w : Path → Option ActorID),
    OwnerStateWF ow → ProvenanceWF w ow →
    OwnerStateWF (ops.foldl applyDocOwnerStep (doc, ow)).2 ∧
    ProvenanceWF w (ops.foldl applyDocOwnerStep (doc, ow)).2
  | [], _, _, _, hwf, hprov => ⟨hwf, hprov⟩
  | op :: rest, doc, ow, w, hwf, hprov => by
    simp only [List.foldl_cons]
    cases op with
    | set p v => exact applyDocOwnerOps_preserves_wf rest (setDocLeaf doc p v) ow w hwf hprov
    | delete p =>
      have ⟨hw1, hp1⟩ := deletePruneDoc_preserves_wf doc p ow w hwf hprov
      exact applyDocOwnerOps_preserves_wf rest (deletePruneDoc doc p ow).1 (deletePruneDoc doc p ow).2 w hw1 hp1

private def OwnerOpAddsOwned : OwnerOp → Bool
  | .createOwned _ _ => true
  | .keepOwned _ _ _ => true
  | .adoptOwned _ _ => true
  | .setUnowned _ => false
  | .remove _ => false

private theorem applyOwnerOp_and_witness_step
    (ow : OwnerState) (w : Path → Option ActorID) (op : OwnerOp)
    (hwf : OwnerStateWF ow) (hprov : ProvenanceWF w ow)
    (hInSpec : inEntranceSpec op.path = true)
    (hActor : ∀ a, (op = .createOwned op.path a ∨ op = .adoptOwned op.path a ∨
        ∃ o, op = .keepOwned op.path a o) → validActor a = true)
    (hNoOv : OwnerOpAddsOwned op = true →
      ∀ q m o, (q, .owned m o) ∈ ow.entries → q ≠ op.path → overlaps op.path q = false)
    (hKeepWit : ∀ a, op = .keepOwned op.path a .created → w op.path = some a) :
    OwnerStateWF (applyOwnerOp ow op) ∧ ProvenanceWF (updateWitnessOp w op) (applyOwnerOp ow op) := by
  cases op with
  | createOwned p a =>
    dsimp only [OwnerOp.path] at hInSpec hActor hNoOv hKeepWit
    have ha : validActor a = true := hActor a (Or.inl rfl)
    refine ⟨⟨hwf.1, pairwise_ownerState_set ow p (.owned a .created) hwf.2.1, ?_, ?_⟩, ?_⟩
    · intro q s hq
      rcases (mem_ownerState_set ow p (.owned a .created) (q, s)).mp hq with heq | ⟨hm, _⟩
      · cases heq
        exact ⟨hInSpec, fun m o h => by cases h; exact ha⟩
      · exact hwf.2.2.1 q s hm
    · intro p1 m1 o1 p2 m2 o2 h1 h2 hov
      rcases (mem_ownerState_set ow p _ _).mp h1 with he1 | ⟨hm1, hne1⟩ <;>
      rcases (mem_ownerState_set ow p _ _).mp h2 with he2 | ⟨hm2, hne2⟩
      · cases he1; cases he2; exact ⟨rfl, rfl, rfl⟩
      · cases he1
        have hfalse := hNoOv rfl p2 m2 o2 hm2 hne2
        rw [hfalse] at hov; cases hov
      · cases he2
        have hfalse := hNoOv rfl p1 m1 o1 hm1 hne1
        rw [overlaps_symm, hfalse] at hov; cases hov
      · exact hwf.2.2.2 p1 m1 o1 p2 m2 o2 hm1 hm2 hov
    · intro q m hq
      rcases (mem_ownerState_set ow p _ _).mp hq with heq | ⟨hm, hne⟩
      · cases heq; simp [updateWitnessOp]
      · simp only [updateWitnessOp, beq_eq_false_iff_ne.mpr hne, Bool.false_eq_true, ↓reduceIte]
        exact hprov q m hm
  | keepOwned p a orig =>
    dsimp only [OwnerOp.path] at hInSpec hActor hNoOv hKeepWit
    have ha : validActor a = true := hActor a (Or.inr (Or.inr ⟨orig, rfl⟩))
    refine ⟨⟨hwf.1, pairwise_ownerState_set ow p (.owned a orig) hwf.2.1, ?_, ?_⟩, ?_⟩
    · intro q s hq
      rcases (mem_ownerState_set ow p (.owned a orig) (q, s)).mp hq with heq | ⟨hm, _⟩
      · cases heq
        exact ⟨hInSpec, fun m o h => by cases h; exact ha⟩
      · exact hwf.2.2.1 q s hm
    · intro p1 m1 o1 p2 m2 o2 h1 h2 hov
      rcases (mem_ownerState_set ow p _ _).mp h1 with he1 | ⟨hm1, hne1⟩ <;>
      rcases (mem_ownerState_set ow p _ _).mp h2 with he2 | ⟨hm2, hne2⟩
      · cases he1; cases he2; exact ⟨rfl, rfl, rfl⟩
      · cases he1
        have hfalse := hNoOv rfl p2 m2 o2 hm2 hne2
        rw [hfalse] at hov; cases hov
      · cases he2
        have hfalse := hNoOv rfl p1 m1 o1 hm1 hne1
        rw [overlaps_symm, hfalse] at hov; cases hov
      · exact hwf.2.2.2 p1 m1 o1 p2 m2 o2 hm1 hm2 hov
    · intro q m hq
      rcases (mem_ownerState_set ow p _ _).mp hq with heq | ⟨hm, _⟩
      · cases heq
        exact hKeepWit a rfl
      · exact hprov q m hm
  | adoptOwned p a =>
    dsimp only [OwnerOp.path] at hInSpec hActor hNoOv hKeepWit
    have ha : validActor a = true := hActor a (Or.inr (Or.inl rfl))
    refine ⟨⟨hwf.1, pairwise_ownerState_set ow p (.owned a .modified) hwf.2.1, ?_, ?_⟩, ?_⟩
    · intro q s hq
      rcases (mem_ownerState_set ow p (.owned a .modified) (q, s)).mp hq with heq | ⟨hm, _⟩
      · cases heq
        exact ⟨hInSpec, fun m o h => by cases h; exact ha⟩
      · exact hwf.2.2.1 q s hm
    · intro p1 m1 o1 p2 m2 o2 h1 h2 hov
      rcases (mem_ownerState_set ow p _ _).mp h1 with he1 | ⟨hm1, hne1⟩ <;>
      rcases (mem_ownerState_set ow p _ _).mp h2 with he2 | ⟨hm2, hne2⟩
      · cases he1; cases he2; exact ⟨rfl, rfl, rfl⟩
      · cases he1
        have hfalse := hNoOv rfl p2 m2 o2 hm2 hne2
        rw [hfalse] at hov; cases hov
      · cases he2
        have hfalse := hNoOv rfl p1 m1 o1 hm1 hne1
        rw [overlaps_symm, hfalse] at hov; cases hov
      · exact hwf.2.2.2 p1 m1 o1 p2 m2 o2 hm1 hm2 hov
    · intro q m hq
      rcases (mem_ownerState_set ow p _ _).mp hq with heq | ⟨hm, hne⟩
      · cases heq
      · simp only [updateWitnessOp, beq_eq_false_iff_ne.mpr hne, Bool.false_eq_true, ↓reduceIte]
        exact hprov q m hm
  | setUnowned p =>
    refine ⟨⟨hwf.1, pairwise_ownerState_set ow p .unowned hwf.2.1, ?_, ?_⟩, ?_⟩
    · intro q s hq
      rcases (mem_ownerState_set ow p .unowned (q, s)).mp hq with heq | ⟨hm, _⟩
      · cases heq; exact ⟨hInSpec, fun _ _ h => by cases h⟩
      · exact hwf.2.2.1 q s hm
    · intro p1 m1 o1 p2 m2 o2 h1 h2 hov
      rcases (mem_ownerState_set ow p .unowned _).mp h1 with he1 | ⟨hm1, _⟩
      · cases he1
      · rcases (mem_ownerState_set ow p .unowned _).mp h2 with he2 | ⟨hm2, _⟩
        · cases he2
        · exact hwf.2.2.2 p1 m1 o1 p2 m2 o2 hm1 hm2 hov
    · intro q m hq
      rcases (mem_ownerState_set ow p .unowned _).mp hq with heq | ⟨hm, hne⟩
      · cases heq
      · simp only [updateWitnessOp, beq_eq_false_iff_ne.mpr hne, Bool.false_eq_true, ↓reduceIte]
        exact hprov q m hm
  | remove p =>
    refine ⟨⟨hwf.1, pairwise_ownerState_remove ow p hwf.2.1, ?_, ?_⟩, ?_⟩
    · intro q s hq
      exact hwf.2.2.1 q s ((mem_ownerState_remove ow p _).mp hq).1
    · intro p1 m1 o1 p2 m2 o2 h1 h2 hov
      exact hwf.2.2.2 p1 m1 o1 p2 m2 o2
        ((mem_ownerState_remove ow p _).mp h1).1 ((mem_ownerState_remove ow p _).mp h2).1 hov
    · intro q m hq
      obtain ⟨hm, hne⟩ := (mem_ownerState_remove ow p _).mp hq
      simp only [updateWitnessOp, beq_eq_false_iff_ne.mpr hne, Bool.false_eq_true, ↓reduceIte]
      exact hprov q m hm

private theorem applyOwnerOps_and_witness_preserves_wf :
    ∀ (ops : List OwnerOp) (ow : OwnerState) (w : Path → Option ActorID),
    OwnerStateWF ow → ProvenanceWF w ow →
    ops.Pairwise (fun a b => a.path ≠ b.path ∧
      (OwnerOpAddsOwned a = true → OwnerOpAddsOwned b = true → overlaps a.path b.path = false)) →
    (∀ op ∈ ops, inEntranceSpec op.path = true) →
    (∀ op ∈ ops, ∀ a, (op = .createOwned op.path a ∨ op = .adoptOwned op.path a ∨
        ∃ o, op = .keepOwned op.path a o) → validActor a = true) →
    (∀ op ∈ ops, OwnerOpAddsOwned op = true →
      ∀ q m o, (q, .owned m o) ∈ ow.entries → q ≠ op.path → overlaps op.path q = false) →
    (∀ op ∈ ops, ∀ a, op = .keepOwned op.path a .created → w op.path = some a) →
    OwnerStateWF (applyOwnerOps ow ops) ∧ ProvenanceWF (updateWitnessOps w ops) (applyOwnerOps ow ops)
  | [], _, _, hwf, hprov, _, _, _, _, _ => ⟨hwf, hprov⟩
  | op :: rest, ow, w, hwf, hprov, hpw, hSpec, hAct, hNoOv, hKeep => by
    simp only [List.pairwise_cons] at hpw
    have ⟨hw1, hp1⟩ := applyOwnerOp_and_witness_step ow w op hwf hprov
      (hSpec op List.mem_cons_self)
      (hAct op List.mem_cons_self)
      (hNoOv op List.mem_cons_self)
      (hKeep op List.mem_cons_self)
    change OwnerStateWF (applyOwnerOps (applyOwnerOp ow op) rest) ∧
      ProvenanceWF (updateWitnessOps (updateWitnessOp w op) rest) (applyOwnerOps (applyOwnerOp ow op) rest)
    apply applyOwnerOps_and_witness_preserves_wf rest (applyOwnerOp ow op) (updateWitnessOp w op)
      hw1 hp1 hpw.2
      (fun o ho => hSpec o (List.mem_cons_of_mem op ho))
      (fun o ho => hAct o (List.mem_cons_of_mem op ho))
    · intro o ho hAdds q m orig hq hne
      have hOld : (q, .owned m orig) ∈ ow.entries ∨ (q = op.path ∧ OwnerOpAddsOwned op = true) := by
        cases op with
        | createOwned p a =>
          rcases (mem_ownerState_set ow p _ _).mp hq with heq | ⟨hm, _⟩
          · cases heq; exact Or.inr ⟨rfl, rfl⟩
          · exact Or.inl hm
        | keepOwned p a orig' =>
          rcases (mem_ownerState_set ow p _ _).mp hq with heq | ⟨hm, _⟩
          · cases heq; exact Or.inr ⟨rfl, rfl⟩
          · exact Or.inl hm
        | adoptOwned p a =>
          rcases (mem_ownerState_set ow p _ _).mp hq with heq | ⟨hm, _⟩
          · cases heq; exact Or.inr ⟨rfl, rfl⟩
          · exact Or.inl hm
        | setUnowned p =>
          rcases (mem_ownerState_set ow p _ _).mp hq with heq | ⟨hm, _⟩
          · cases heq
          · exact Or.inl hm
        | remove p =>
          exact Or.inl ((mem_ownerState_remove ow p _).mp hq).1
      rcases hOld with hmOld | ⟨rfl, hOpAdds⟩
      · exact hNoOv o (List.mem_cons_of_mem op ho) hAdds q m orig hmOld hne
      · rw [overlaps_symm]
        exact (hpw.1 o ho).2 hOpAdds hAdds
    · intro o ho a hko
      have hnePath : o.path ≠ op.path := Ne.symm (hpw.1 o ho).1
      have hWitEq : updateWitnessOp w op o.path = w o.path := by
        cases op with
        | keepOwned _ _ _ => rfl
        | createOwned p _ | adoptOwned p _ | setUnowned p | remove p =>
          have hne' : o.path ≠ p := hnePath
          simp [updateWitnessOp, beq_eq_false_iff_ne.mpr hne']
      rw [hWitEq]
      exact hKeep o (List.mem_cons_of_mem op ho) a hko

private theorem planLeaf_ownerOp_addsOwned_spec (inp : LeafInput) (mode : Mode) (op : OwnerOp)
    (hOp : (planLeaf inp mode).ownerOp = some op) :
    op.path = inp.path ∧
    (∀ a, (op = .createOwned op.path a ∨ op = .adoptOwned op.path a ∨
        ∃ o, op = .keepOwned op.path a o) → a = inp.actor) ∧
    (OwnerOpAddsOwned op = true →
      inp.declared.isSome = true ∧ inp.hasForeignOverlap = false ∧ inp.selfOverlap = .none) ∧
    (∀ a, op = .keepOwned op.path a .created → inp.status = some (.owned inp.actor .created)) := by
  have hpEq : op.path = inp.path := buildOwnerOp_path inp.path inp.actor _ _ _ _ op hOp
  refine ⟨hpEq, ?_, ?_, ?_⟩
  · intro a hCase
    change buildOwnerOp inp.path inp.actor (planLeaf inp mode).decision (planLeaf inp mode).action inp.status inp.obs = some op at hOp
    cases hDec : (planLeaf inp mode).decision with
    | noOp | deferred | blocker | policyDenied | suppressed =>
      rw [hDec] at hOp; simp [buildOwnerOp] at hOp
    | authorized =>
      rw [hDec] at hOp
      cases hAct : (planLeaf inp mode).action with
      | none | explicitAdopt | explicitRelease =>
        rw [hAct] at hOp; simp [buildOwnerOp] at hOp
      | setLeaf =>
        rw [hAct] at hOp; simp only [buildOwnerOp] at hOp
        cases hSt : inp.status with
        | none => rw [hSt] at hOp; cases hOp; rcases hCase with h | h | ⟨_, h⟩ <;> cases h; rfl
        | some ls =>
          rw [hSt] at hOp
          cases ls with
          | unowned | defaulted => cases hOp; rcases hCase with h | h | ⟨_, h⟩ <;> cases h; rfl
          | owned m orig =>
            by_cases hm : m = inp.actor
            · simp [hm] at hOp; cases hOp; rcases hCase with h | h | ⟨_, h⟩ <;> cases h; rfl
            · simp [hm] at hOp
      | adoptModified =>
        rw [hAct] at hOp; simp only [buildOwnerOp] at hOp
        cases hOp; rcases hCase with h | h | ⟨_, h⟩ <;> cases h; rfl
      | deleteLeaf =>
        rw [hAct] at hOp; simp only [buildOwnerOp] at hOp
        cases hOp; rcases hCase with h | h | ⟨_, h⟩ <;> cases h
      | unmanageModified =>
        rw [hAct] at hOp; simp only [buildOwnerOp] at hOp
        cases hObs : inp.obs <;> (rw [hObs] at hOp; cases hOp; rcases hCase with h | h | ⟨_, h⟩ <;> cases h)
      | clearAbsentRecord =>
        rw [hAct] at hOp; simp only [buildOwnerOp] at hOp
        cases hOp; rcases hCase with h | h | ⟨_, h⟩ <;> cases h
  · intro hAdds
    have hDec : (planLeaf inp mode).decision = .authorized :=
      buildOps_some_implies_authorized inp.path inp.actor (planLeaf inp mode).decision (planLeaf inp mode).action
        inp.declared inp.status inp.obs (Or.inr (show (planLeaf inp mode).ownerOp.isSome = true by simp [hOp]))
    have hActOr : (planLeaf inp mode).action = .setLeaf ∨ (planLeaf inp mode).action = .adoptModified := by
      change buildOwnerOp inp.path inp.actor (planLeaf inp mode).decision (planLeaf inp mode).action inp.status inp.obs = some op at hOp
      rw [hDec] at hOp
      cases hAct : (planLeaf inp mode).action with
      | none | explicitAdopt | explicitRelease => rw [hAct] at hOp; simp [buildOwnerOp] at hOp
      | setLeaf => exact Or.inl rfl
      | adoptModified => exact Or.inr rfl
      | deleteLeaf => rw [hAct] at hOp; simp only [buildOwnerOp] at hOp; cases hOp; simp [OwnerOpAddsOwned] at hAdds
      | unmanageModified =>
        rw [hAct] at hOp; simp only [buildOwnerOp] at hOp
        cases hObs : inp.obs <;> (rw [hObs] at hOp; cases hOp; simp [OwnerOpAddsOwned] at hAdds)
      | clearAbsentRecord => rw [hAct] at hOp; simp only [buildOwnerOp] at hOp; cases hOp; simp [OwnerOpAddsOwned] at hAdds
    have hScOr : (planLeaf inp mode).stateClass = .declaredSet ∨
        (planLeaf inp mode).stateClass = .declaredAdoptModified := by
      change selectModeAction (planLeaf inp mode).stateClass = .setLeaf ∨
        selectModeAction (planLeaf inp mode).stateClass = .adoptModified at hActOr
      cases hSc : (planLeaf inp mode).stateClass with
      | declaredSet => exact Or.inl rfl
      | declaredAdoptModified => exact Or.inr rfl
      | _ => rw [hSc] at hActOr; rcases hActOr with h1 | h1 <;> simp [selectModeAction] at h1
    change (classifyLeaf inp.actor inp.declared inp.obs inp.anc inp.status inp.lastApplied inp.hasForeignOverlap inp.selfOverlap).1 = .declaredSet ∨
      (classifyLeaf inp.actor inp.declared inp.obs inp.anc inp.status inp.lastApplied inp.hasForeignOverlap inp.selfOverlap).1 = .declaredAdoptModified at hScOr
    cases hDecl : inp.declared with
    | none =>
      rw [hDecl] at hScOr
      unfold classifyLeaf at hScOr
      cases hSt : inp.status with
      | none => rw [hSt] at hScOr; rcases hScOr with h | h <;> cases h
      | some ls =>
        rw [hSt] at hScOr
        cases ls with
        | unowned | defaulted => rcases hScOr with h | h <;> cases h
        | owned m orig =>
          by_cases hm : (m != inp.actor) = true
          · simp [hm] at hScOr
          · cases orig with
            | modified =>
              by_cases hu : isObsUnknown inp.obs inp.anc = true <;> simp [hm, hu] at hScOr
            | created =>
              by_cases hso : (inp.selfOverlap == .retiredCreated) = true
              · simp [hm, hso] at hScOr
              · by_cases hfo : inp.hasForeignOverlap = true
                · simp [hm, hso, hfo] at hScOr
                · by_cases hb : (isObsUnknown inp.obs inp.anc || isProtectedStructure inp.obs inp.anc) = true
                  · simp [hm, hso, hfo, hb] at hScOr
                  · cases hObs : inp.obs with
                    | leaf actual =>
                      rw [hObs] at hScOr hb
                      cases hLa : inp.lastApplied with
                      | none => rw [hLa] at hScOr; simp [hm, hso, hfo, hb] at hScOr
                      | some la =>
                        rw [hLa] at hScOr
                        by_cases he : Value.eqv actual la = true <;> simp [hm, hso, hfo, hb, he] at hScOr
                    | _ => rw [hObs] at hScOr hb; simp [hm, hso, hfo, hb] at hScOr
    | some desired =>
      rw [hDecl] at hScOr
      refine ⟨rfl, ?_, ?_⟩
      · cases hfo : inp.hasForeignOverlap with
        | false => rfl
        | true =>
          rw [hfo] at hScOr
          cases hso : inp.selfOverlap <;> (rw [hso] at hScOr; simp [classifyLeaf] at hScOr)
      · cases hso : inp.selfOverlap with
        | none => rfl
        | retiredCreated => rw [hso] at hScOr; simp [classifyLeaf] at hScOr
        | retiredModified =>
          rw [hso] at hScOr
          cases hSt : inp.status with
          | none =>
            rw [hSt] at hScOr
            cases hfo : inp.hasForeignOverlap <;> (rw [hfo] at hScOr; simp [classifyLeaf] at hScOr)
          | some ls =>
            rw [hSt] at hScOr
            cases ls with
            | unowned | defaulted =>
              cases hfo : inp.hasForeignOverlap <;> (rw [hfo] at hScOr; simp [classifyLeaf] at hScOr)
            | owned m orig =>
              by_cases hm : (m != inp.actor) = true <;>
                cases hfo : inp.hasForeignOverlap <;>
                (rw [hfo] at hScOr; simp [classifyLeaf, hm] at hScOr)
  · intro a hKeep
    rw [hpEq] at hKeep
    subst hKeep
    change buildOwnerOp inp.path inp.actor (planLeaf inp mode).decision (planLeaf inp mode).action inp.status inp.obs = some (.keepOwned inp.path a .created) at hOp
    cases hDec : (planLeaf inp mode).decision with
    | noOp | deferred | blocker | policyDenied | suppressed =>
      rw [hDec] at hOp; simp [buildOwnerOp] at hOp
    | authorized =>
      rw [hDec] at hOp
      cases hAct : (planLeaf inp mode).action with
      | none | adoptModified | deleteLeaf | clearAbsentRecord | explicitAdopt | explicitRelease =>
        rw [hAct] at hOp; simp [buildOwnerOp] at hOp
      | unmanageModified =>
        rw [hAct] at hOp; simp only [buildOwnerOp] at hOp
        cases hObs : inp.obs <;> (rw [hObs] at hOp; cases hOp)
      | setLeaf =>
        rw [hAct] at hOp; simp only [buildOwnerOp] at hOp
        cases hSt : inp.status with
        | none => rw [hSt] at hOp; cases hOp
        | some ls =>
          rw [hSt] at hOp
          cases ls with
          | unowned | defaulted => cases hOp
          | owned m orig =>
            by_cases hm : m = inp.actor
            · simp only [hm, beq_self_eq_true, ↓reduceIte, Option.some.injEq, OwnerOp.keepOwned.injEq] at hOp
              obtain ⟨_, rfl, rfl⟩ := hOp
              rw [hm]
            · simp [hm] at hOp

/-- **Theorem 11a (Initial CAS Protocol Invariant)**:
    The empty initial `CASState` satisfies `CASInvariant`. -/
theorem cas_init_invariant : CASInvariant CASState.empty := by
  refine ⟨⟨?_, ?_⟩, ?_, ?_⟩
  · intro c hc; cases hc
  · intro addr _
    exact ⟨rfl, rfl⟩
  · intro _ _ h; cases h
  · intro _ _ h; cases h

private theorem filterMap_map_eq {α β γ : Type} (f : α → β) (g : β → Option γ) :
    ∀ (l : List α), (l.map f).filterMap g = l.filterMap (fun a => g (f a))
  | [] => rfl
  | a :: rest => by
    simp only [List.map_cons, List.filterMap_cons]
    cases g (f a) with
    | none => exact filterMap_map_eq f g rest
    | some b => exact congrArg (b :: ·) (filterMap_map_eq f g rest)

private theorem planMulti_preserves_wf
    (doc declDoc : Value) (ow : OwnerState) (w : Path → Option ActorID)
    (actor : ActorID) (scope : Scope) (mode : Mode) (expectMatch : Bool) (plan : PlanOutput)
    (hRecWF : OwnerStateWF ow) (hRecProv : ProvenanceWF w ow)
    (hPlan : planMulti doc ow declDoc actor scope mode expectMatch true = .ok plan)
    (hNotBlocked : plan.blocked = false) :
    OwnerStateWF plan.nextOwner ∧ ProvenanceWF (updateWitnessOps w plan.ownerOps) plan.nextOwner := by
  unfold planMulti at hPlan
  split at hPlan
  · cases hPlan
  · rename_i hPre
    simp only [Bool.or_eq_true, Bool.not_eq_true', not_or] at hPre
    have hAct : validActor actor = true := by cases hv : validActor actor <;> simp [hv] at hPre ⊢
    split at hPlan
    · cases hPlan
    · split at hPlan
      · cases hPlan
      · cases hProj : projectEntranceSpec declDoc true with
        | error c => rw [hProj] at hPlan; simp only [bind, Except.bind] at hPlan; cases hPlan
        | ok decl =>
          rw [hProj] at hPlan
          simp only [bind, Except.bind] at hPlan
          have ⟨hDeclNoOv, hDeclSpec⟩ := projectEntranceSpec_spec declDoc true decl hProj
          split at hPlan
          · cases hPlan; cases hNotBlocked
          · rename_i hGateNotBlocked
            cases hPlan
            dsimp only [applyPlanChanges]
            let evalPaths := sortDedupPaths (decl.map Prod.fst ++ ow.actorOwnedPaths actor)
            let mkPlan := fun q =>
              let x := observeDoc doc q
              let isDecl := (lookupPath decl q).isSome
              planLeaf {
                path := q, actor := actor, declared := lookupPath decl q,
                obs := x.1, anc := x.2, status := ow.get q, lastApplied := none,
                hasForeignOverlap := if isDecl then computeForeignOverlap ow actor q else false,
                selfOverlap := computeSelfOverlap ow actor decl q,
                granted := scope.permissionsFor q
              } mode
            let lps := evalPaths.map mkPlan
            let g := gatePlan lps true true
            have hGateOps : g.ownerOps = lps.filterMap LeafPlan.ownerOp := by
              unfold g gatePlan
              simp only [Bool.not_true, Bool.false_or]
              split
              · rename_i hb
                have : (gatePlan lps true true).blocked = true := by
                  unfold gatePlan; simp [hb]
                exact absurd this hGateNotBlocked
              · rfl
            have hNoOvDecl : ∀ q₁ q₂, (lookupPath decl q₁).isSome = true →
                (lookupPath decl q₂).isSome = true → q₁ ≠ q₂ → overlaps q₁ q₂ = false := by
              intro q₁ q₂ h1 h2 hne
              cases hl1 : lookupPath decl q₁ with
              | none => rw [hl1] at h1; cases h1
              | some v1 =>
                cases hl2 : lookupPath decl q₂ with
                | none => rw [hl2] at h2; cases h2
                | some v2 =>
                  have hm1 := lookupPath_mem decl q₁ v1 hl1
                  have hm2 := lookupPath_mem decl q₂ v2 hl2
                  exact pairwise_overlaps_false_of_mem decl hDeclNoOv hm1 hm2 hne
            have hOpsPw : g.ownerOps.Pairwise (fun a b => a.path ≠ b.path ∧
                (OwnerOpAddsOwned a = true → OwnerOpAddsOwned b = true → overlaps a.path b.path = false)) := by
              rw [hGateOps, filterMap_map_eq]
              apply pairwise_filterMap_of (fun q => (mkPlan q).ownerOp)
              · intro q1 q2 o1 o2 hqNe ho1 ho2
                obtain ⟨h1p, _, h1a, _⟩ := planLeaf_ownerOp_addsOwned_spec _ mode o1 ho1
                obtain ⟨h2p, _, h2a, _⟩ := planLeaf_ownerOp_addsOwned_spec _ mode o2 ho2
                dsimp only at h1p h2p
                rw [h1p, h2p]
                refine ⟨hqNe, fun ha1 ha2 => ?_⟩
                exact hNoOvDecl q1 q2 (h1a ha1).1 (h2a ha2).1 hqNe
              · exact pairwise_ne_sortDedupPaths _
            have hOpsWF : OwnerStateWF (applyOwnerOps ow g.ownerOps) ∧
                ProvenanceWF (updateWitnessOps w g.ownerOps) (applyOwnerOps ow g.ownerOps) := by
              apply applyOwnerOps_and_witness_preserves_wf g.ownerOps ow w
                hRecWF hRecProv hOpsPw
              · intro op hop
                rw [hGateOps] at hop
                obtain ⟨lp, hlp, ho⟩ := (mem_filterMap_iff LeafPlan.ownerOp lps op).mp hop
                obtain ⟨q, hq, rfl⟩ := List.mem_map.mp hlp
                have hpEq := (planLeaf_ownerOp_addsOwned_spec _ mode op ho).1
                rw [hpEq]
                have hmemEval := (mem_sortDedupPaths q _).mp hq
                rcases List.mem_append.mp hmemEval with hmd | hma
                · obtain ⟨⟨q', v'⟩, hqv, hqEq⟩ := List.mem_map.mp hmd
                  dsimp only at hqEq; subst hqEq
                  exact hDeclSpec q' v' hqv
                · obtain ⟨⟨q', st'⟩, hqs, hfm⟩ := (mem_filterMap_iff _ ow.entries q).mp hma
                  cases st' with
                  | owned m orig =>
                    dsimp only at hfm
                    split at hfm
                    · cases hfm; exact (hRecWF.2.2.1 q _ hqs).1
                    · cases hfm
                  | unowned | defaulted => simp at hfm
              · intro op hop a hCase
                rw [hGateOps] at hop
                obtain ⟨lp, hlp, ho⟩ := (mem_filterMap_iff LeafPlan.ownerOp lps op).mp hop
                obtain ⟨q, _, rfl⟩ := List.mem_map.mp hlp
                have haEq := (planLeaf_ownerOp_addsOwned_spec _ mode op ho).2.1 a hCase
                rw [haEq]; exact hAct
              · intro op hop hAdds q m orig hq hne
                rw [hGateOps] at hop
                obtain ⟨lp, hlp, ho⟩ := (mem_filterMap_iff LeafPlan.ownerOp lps op).mp hop
                obtain ⟨p, _, rfl⟩ := List.mem_map.mp hlp
                have ⟨hpEq, _, hAddSpec, _⟩ := planLeaf_ownerOp_addsOwned_spec _ mode op ho
                have ⟨hIsDecl, hNoFor, hNoSelf⟩ := hAddSpec hAdds
                dsimp only at hpEq hIsDecl hNoFor hNoSelf
                rw [hpEq] at hne ⊢
                simp only [hIsDecl, ↓reduceIte] at hNoFor
                by_cases hm : m = actor
                · rw [hm] at hq
                  cases hl : lookupPath decl p with
                  | none => rw [hl] at hIsDecl; cases hIsDecl
                  | some v =>
                    unfold computeSelfOverlap at hNoSelf
                    rw [hl] at hNoSelf
                    dsimp only at hNoSelf
                    split at hNoSelf
                    · cases hNoSelf
                    · rename_i hNoC
                      split at hNoSelf
                      · cases hNoSelf
                      · rename_i hNoM
                        by_cases hov : overlaps p q = true
                        · cases orig with
                          | created =>
                            exfalso; apply hNoC
                            apply (any_eq_true_iff _ ow.entries).mpr
                            refine ⟨(q, .owned actor .created), hq, ?_⟩
                            simp [bne_iff_ne.mpr hne, hov]
                          | modified =>
                            exfalso; apply hNoM
                            apply (any_eq_true_iff _ ow.entries).mpr
                            refine ⟨(q, .owned actor .modified), hq, ?_⟩
                            simp [bne_iff_ne.mpr hne, hov]
                        · exact Bool.eq_false_iff.mpr hov
                · by_cases hov : overlaps p q = true
                  · have hany : computeForeignOverlap ow actor p = true := by
                      unfold computeForeignOverlap
                      apply (any_eq_true_iff _ ow.entries).mpr
                      refine ⟨(q, .owned m orig), hq, ?_⟩
                      simp [bne_iff_ne.mpr hm, hov]
                    rw [hany] at hNoFor; cases hNoFor
                  · exact Bool.eq_false_iff.mpr hov
              · intro op hop a hKeep
                rw [hGateOps] at hop
                obtain ⟨lp, hlp, ho⟩ := (mem_filterMap_iff LeafPlan.ownerOp lps op).mp hop
                obtain ⟨p, _, rfl⟩ := List.mem_map.mp hlp
                have ⟨hpEq, hActEq, _, hStEq⟩ := planLeaf_ownerOp_addsOwned_spec _ mode op ho
                have haEq : a = actor := hActEq a (Or.inr (Or.inr ⟨.created, hKeep⟩))
                subst haEq
                have hGet := hStEq a hKeep
                dsimp only at hpEq hGet
                rw [hpEq]
                exact hRecProv p a (lookupPath_mem ow.entries p (.owned a .created) hGet)
            exact applyDocOwnerOps_preserves_wf g.docOps doc
              (applyOwnerOps ow g.ownerOps) (updateWitnessOps w g.ownerOps) hOpsWF.1 hOpsWF.2

private theorem planResolve_preserves_wf
    (kind : ResolveKind) (actor : ActorID) (scope : Scope) (path : Path)
    (doc : Value) (ow : OwnerState) (w : Path → Option ActorID) (expectMatch : Bool)
    (hRecWF : OwnerStateWF ow) (hRecProv : ProvenanceWF w ow)
    (hAcc : (planResolve {
      kind := kind, actor := actor, scope := scope, path := path,
      doc := doc, owner := ow, expectMatch := expectMatch, ownerRevMatch := true
    }).accepted = true) :
    OwnerStateWF (planResolve {
      kind := kind, actor := actor, scope := scope, path := path,
      doc := doc, owner := ow, expectMatch := expectMatch, ownerRevMatch := true
    }).nextOwner ∧
    ProvenanceWF (fun p => if p == path then none else w p) (planResolve {
      kind := kind, actor := actor, scope := scope, path := path,
      doc := doc, owner := ow, expectMatch := expectMatch, ownerRevMatch := true
    }).nextOwner := by
  unfold planResolve at hAcc ⊢
  dsimp only at hAcc ⊢
  split at hAcc
  · cases hAcc
  · rename_i hPre
    simp only [hPre]
    simp only [Bool.or_eq_true, Bool.not_eq_true', not_or] at hPre
    have hAct : validActor actor = true := by cases hv : validActor actor <;> simp [hv] at hPre ⊢
    have hInSpec : inEntranceSpec path = true := by cases hv : inEntranceSpec path <;> simp [hv] at hPre ⊢
    split at hAcc
    · cases hAcc
    · rename_i hPerm
      simp only [hPerm]
      cases kind with
      | adopt =>
        dsimp only at hAcc ⊢
        split at hAcc
        · cases hAcc
        · rename_i hNoOv
          simp only [hNoOv]
          simp only [Bool.not_true, Bool.false_eq_true, ↓reduceIte] at hAcc ⊢
          cases hStruct : primaryCodeOfCodes (obsDiags (observeDoc doc path).1 (observeDoc doc path).2) with
          | some _ => rw [hStruct] at hAcc; cases hAcc
          | none =>
            rw [hStruct] at hAcc
            dsimp only at hAcc ⊢
            cases hObs : (observeDoc doc path).1 with
            | leaf _ =>
              rw [hObs] at hAcc
              dsimp only at hAcc ⊢
              split at hAcc
              · cases hAcc
              · rename_i hExp
                simp only [hExp]
                have hNoAdoptOv : ∀ q m o, (q, .owned m o) ∈ ow.entries →
                    q ≠ path → overlaps path q = false := by
                  intro q m o hq hne
                  by_cases hov : overlaps path q = true
                  · have hany : hasAdoptOverlap ow actor path = true := by
                      unfold hasAdoptOverlap
                      apply (any_eq_true_iff _ ow.entries).mpr
                      refine ⟨(q, .owned m o), hq, ?_⟩
                      by_cases hm : m = actor
                      · subst hm; simp [bne_iff_ne.mpr hne, hov]
                      · simp [bne_iff_ne.mpr hm, hov]
                    exact absurd hany hNoOv
                  · exact Bool.eq_false_iff.mpr hov
                exact applyOwnerOp_and_witness_step ow w
                  (.adoptOwned path actor) hRecWF hRecProv hInSpec
                  (fun a hc => by rcases hc with h | h | ⟨_, h⟩ <;> cases h; exact hAct)
                  (fun _ => hNoAdoptOv) (fun _ h => by cases h)
            | _ => rw [hObs] at hAcc; cases hAcc
      | release =>
        dsimp only at hAcc ⊢
        cases hGet : ow.get path with
        | none => rw [hGet] at hAcc; cases hAcc
        | some ls =>
          rw [hGet] at hAcc
          cases ls with
          | unowned | defaulted => cases hAcc
          | owned m orig =>
            dsimp only at hAcc ⊢
            split at hAcc
            · cases hAcc
            · rename_i hSelf
              simp only [hSelf] at hAcc ⊢
              simp only [Bool.not_true, Bool.false_eq_true, ↓reduceIte] at hAcc ⊢
              split at hAcc
              · cases hAcc
              · rename_i hKnown
                simp only [hKnown]
                split at hAcc
                · cases hAcc
                · rename_i hExp
                  simp only [hExp]
                  cases (observeDoc doc path).1 with
                  | absent =>
                    exact applyOwnerOp_and_witness_step ow w
                      (.remove path) hRecWF hRecProv hInSpec
                      (fun _ hc => by rcases hc with h | h | ⟨_, h⟩ <;> cases h)
                      (fun h => by cases h) (fun _ h => by cases h)
                  | _ =>
                    exact applyOwnerOp_and_witness_step ow w
                      (.setUnowned path) hRecWF hRecProv hInSpec
                      (fun _ hc => by rcases hc with h | h | ⟨_, h⟩ <;> cases h)
                      (fun h => by cases h) (fun _ h => by cases h)

private theorem stepCreate_preserves_invariant (st st' : CASState) (addr : ObjAddr)
    (actor : ActorID) (scope : Scope) (doc : Value) (out : CASOutcome)
    (hInv : CASInvariant st) (hStep : stepCreate st addr actor scope doc = (out, st')) :
    CASInvariant st' := by
  unfold stepCreate at hStep
  split at hStep
  · cases hStep; exact hInv
  · rename_i hPre
    simp only [Bool.or_eq_true, Bool.not_eq_true', not_or] at hPre
    have hAddr : validAddr addr = true := by cases hv : validAddr addr <;> simp [hv] at hPre ⊢
    have hAct : validActor actor = true := by cases hv : validActor actor <;> simp [hv] at hPre ⊢
    cases hProj : projectEntranceSpec doc true with
    | error c => rw [hProj] at hStep; dsimp only at hStep; cases hStep; exact hInv
    | ok decl =>
      rw [hProj] at hStep
      dsimp only at hStep
      split at hStep
      · cases hStep; exact hInv
      · split at hStep
        · rename_i rev store' hTxn
          cases hStep
          have hStore' := txn_ok_put2 hTxn
          have ⟨hDeclNoOv, hDeclSpec⟩ := projectEntranceSpec_spec doc true decl hProj
          have hDeclDist : decl.Pairwise (fun a b => a.1 ≠ b.1) :=
            hDeclNoOv.imp (fun h => overlaps_false_implies_ne h)
          refine ⟨⟨?_, ?_⟩, ?_, ?_⟩
          · intro c hc
            rw [hStore'] at hc ⊢
            rcases List.mem_append.mp hc with hm | hm
            · exact Nat.le_succ_of_le (hInv.pairing.1 c hm)
            · simp at hm; subst hm; exact Nat.le_refl _
          · intro a ha
            rw [hStore']
            have hFindO := find_append_put2 st.store addr.oKey "o" addr.ownKey "own" a.oKey hInv.pairing.1
            have hFindOwn := find_append_put2 st.store addr.oKey "o" addr.ownKey "own" a.ownKey hInv.pairing.1
            by_cases heq : a = addr
            · subst a
              have hne : addr.ownKey ≠ addr.oKey := Ne.symm (oKey_ne_ownKey hAddr hAddr)
              simp only [beq_self_eq_true, ↓reduceIte, Option.isSome_some]
              rw [hFindO, hFindOwn]
              simp [hne]
            · have hbeq : (a == addr) = false := beq_eq_false_iff_ne.mpr heq
              have h1 : addr.ownKey ≠ a.oKey := Ne.symm (oKey_ne_ownKey ha hAddr)
              have h2 : addr.oKey ≠ a.oKey := fun h => heq (oKey_inj ha hAddr h.symm)
              have h3 : addr.ownKey ≠ a.ownKey := fun h => heq (ownKey_inj ha hAddr h.symm)
              have h4 : addr.oKey ≠ a.ownKey := oKey_ne_ownKey hAddr ha
              simp only [hbeq, Bool.false_eq_true, ↓reduceIte]
              rw [hFindO, hFindOwn]
              simp only [h1, h2, h3, h4, ↓reduceIte]
              exact hInv.pairing.2 a ha
          · intro a rec ha
            dsimp only at ha
            split at ha
            · rename_i heq
              cases ha
              refine ⟨hAct, ?_, ?_, ?_⟩
              · exact pairwise_map_of
                  (fun x : Path × Value => (x.1, LeafStatus.owned actor .created))
                  (fun _ _ h => h) decl hDeclDist
              · intro p s hp
                obtain ⟨⟨q, v⟩, hq, hEq⟩ := List.mem_map.mp hp
                cases hEq
                exact ⟨hDeclSpec q v hq, fun m o hm => by cases hm; exact hAct⟩
              · intro p1 m1 o1 p2 m2 o2 h1 h2 hov
                obtain ⟨⟨q1, v1⟩, hq1, he1⟩ := List.mem_map.mp h1
                obtain ⟨⟨q2, v2⟩, hq2, he2⟩ := List.mem_map.mp h2
                cases he1; cases he2
                have hpEq : q1 = q2 := by
                  by_cases hne : q1 = q2
                  · exact hne
                  · have hFalse : overlaps q1 q2 = false :=
                      pairwise_overlaps_false_of_mem decl hDeclNoOv hq1 hq2 hne
                    rw [hFalse] at hov; cases hov
                exact ⟨hpEq, rfl, rfl⟩
            · exact hInv.ownerWF a rec ha
          · intro a rec ha p m hp
            dsimp only at ha ⊢
            split at ha
            · rename_i heq
              simp only [beq_iff_eq] at heq
              subst heq
              cases ha
              obtain ⟨⟨q, v⟩, hq, hEq⟩ := List.mem_map.mp hp
              cases hEq
              have hLook := mem_lookupPath_of_pairwise decl q v hDeclDist hq
              simp [hLook]
            · rename_i hne
              simp only [hne]
              exact hInv.provenance a rec ha p m hp
        · cases hStep; exact hInv
        · cases hStep; exact hInv

private theorem stepPlanMode_preserves_invariant (st st' : CASState) (addr : ObjAddr) (actor : ActorID)
    (scope : Scope) (declDoc : Value) (mode : Mode) (oModRev ownModRev : Rev) (expectMatch : Bool)
    (out : CASOutcome) (hInv : CASInvariant st)
    (hStep : stepPlanMode st addr actor scope declDoc mode oModRev ownModRev expectMatch = (out, st')) :
    CASInvariant st' := by
  unfold stepPlanMode at hStep
  split at hStep
  · cases hStep; exact hInv
  · rename_i hAddrNot
    have hAddr : validAddr addr = true := by cases hv : validAddr addr <;> simp [hv] at hAddrNot ⊢
    cases hObj : st.objects addr with
    | none => rw [hObj] at hStep; dsimp only at hStep; cases hStep; exact hInv
    | some rec =>
      rw [hObj] at hStep
      dsimp only at hStep
      cases hPlan : planMulti rec.doc rec.owner declDoc actor scope mode expectMatch true with
      | error c => rw [hPlan] at hStep; dsimp only at hStep; cases hStep; exact hInv
      | ok plan =>
        rw [hPlan] at hStep
        dsimp only at hStep
        split at hStep
        · cases hStep; exact hInv
        · rename_i hNotBlocked
          have hNotBlk : plan.blocked = false := Bool.eq_false_iff.mpr hNotBlocked
          split at hStep
          · cases hStep; exact hInv
          · split at hStep
            · rename_i rev store' hTxn
              cases hStep
              have hFinalWF := planMulti_preserves_wf rec.doc declDoc rec.owner (st.witness addr)
                actor scope mode expectMatch plan (hInv.ownerWF addr rec hObj)
                (hInv.provenance addr rec hObj) hPlan hNotBlk
              refine ⟨⟨?_, ?_⟩, ?_, ?_⟩
              · intro c hc
                by_cases hEmpty : plan.docOps.isEmpty = true
                · simp only [hEmpty, ↓reduceIte] at hTxn
                  have hStore' := txn_ok_put1 hTxn
                  rw [hStore'] at hc ⊢
                  rcases List.mem_append.mp hc with hm | hm
                  · exact Nat.le_succ_of_le (hInv.pairing.1 c hm)
                  · simp at hm; subst hm; exact Nat.le_refl _
                · simp only [hEmpty] at hTxn
                  have hStore' := txn_ok_put2 hTxn
                  rw [hStore'] at hc ⊢
                  rcases List.mem_append.mp hc with hm | hm
                  · exact Nat.le_succ_of_le (hInv.pairing.1 c hm)
                  · simp at hm; subst hm; exact Nat.le_refl _
              · intro a ha
                by_cases hEmpty : plan.docOps.isEmpty = true
                · simp only [hEmpty, ↓reduceIte] at hTxn
                  have hStore' := txn_ok_put1 hTxn
                  rw [hStore']
                  have hFindO := find_append_put1 st.store addr.ownKey "own" a.oKey hInv.pairing.1
                  have hFindOwn := find_append_put1 st.store addr.ownKey "own" a.ownKey hInv.pairing.1
                  by_cases heq : a = addr
                  · subst a
                    have hne : addr.ownKey ≠ addr.oKey := Ne.symm (oKey_ne_ownKey hAddr hAddr)
                    have hPair := hInv.pairing.2 addr hAddr
                    rw [hObj] at hPair
                    simp only [beq_self_eq_true, ↓reduceIte, Option.isSome_some]
                    rw [hFindO, hFindOwn]
                    simp [hne, hPair.1]
                  · have hbeq : (a == addr) = false := beq_eq_false_iff_ne.mpr heq
                    have h1 : addr.ownKey ≠ a.oKey := Ne.symm (oKey_ne_ownKey ha hAddr)
                    have h3 : addr.ownKey ≠ a.ownKey := fun h => heq (ownKey_inj ha hAddr h.symm)
                    simp only [hbeq, Bool.false_eq_true, ↓reduceIte]
                    rw [hFindO, hFindOwn]
                    simp only [h1, h3, ↓reduceIte]
                    exact hInv.pairing.2 a ha
                · simp only [hEmpty] at hTxn
                  have hStore' := txn_ok_put2 hTxn
                  rw [hStore']
                  have hFindO := find_append_put2 st.store addr.oKey "o" addr.ownKey "own" a.oKey hInv.pairing.1
                  have hFindOwn := find_append_put2 st.store addr.oKey "o" addr.ownKey "own" a.ownKey hInv.pairing.1
                  by_cases heq : a = addr
                  · subst a
                    have hne : addr.ownKey ≠ addr.oKey := Ne.symm (oKey_ne_ownKey hAddr hAddr)
                    simp only [beq_self_eq_true, ↓reduceIte, Option.isSome_some]
                    rw [hFindO, hFindOwn]
                    simp [hne]
                  · have hbeq : (a == addr) = false := beq_eq_false_iff_ne.mpr heq
                    have h1 : addr.ownKey ≠ a.oKey := Ne.symm (oKey_ne_ownKey ha hAddr)
                    have h2 : addr.oKey ≠ a.oKey := fun h => heq (oKey_inj ha hAddr h.symm)
                    have h3 : addr.ownKey ≠ a.ownKey := fun h => heq (ownKey_inj ha hAddr h.symm)
                    have h4 : addr.oKey ≠ a.ownKey := oKey_ne_ownKey hAddr ha
                    simp only [hbeq, Bool.false_eq_true, ↓reduceIte]
                    rw [hFindO, hFindOwn]
                    simp only [h1, h2, h3, h4, ↓reduceIte]
                    exact hInv.pairing.2 a ha
              · intro a r ha
                dsimp only at ha
                split at ha
                · cases ha; exact hFinalWF.1
                · exact hInv.ownerWF a r ha
              · intro a r ha p m hp
                dsimp only at ha ⊢
                split at ha
                · rename_i heq
                  simp only [beq_iff_eq] at heq; subst heq
                  cases ha
                  simp only [beq_self_eq_true, ↓reduceIte]
                  exact hFinalWF.2 p m hp
                · rename_i hne
                  simp only [hne]
                  exact hInv.provenance a r ha p m hp
            · cases hStep; exact hInv
            · cases hStep; exact hInv

private theorem stepResolveKind_preserves_invariant (st st' : CASState) (kind : ResolveKind)
    (addr : ObjAddr) (actor : ActorID) (scope : Scope) (path : Path)
    (oModRev ownModRev : Rev) (expectMatch : Bool) (out : CASOutcome) (hInv : CASInvariant st)
    (hStep : stepResolveKind st kind addr actor scope path oModRev ownModRev expectMatch = (out, st')) :
    CASInvariant st' := by
  unfold stepResolveKind at hStep
  split at hStep
  · cases hStep; exact hInv
  · rename_i hAddrNot
    have hAddr : validAddr addr = true := by cases hv : validAddr addr <;> simp [hv] at hAddrNot ⊢
    cases hObj : st.objects addr with
    | none => rw [hObj] at hStep; dsimp only at hStep; cases hStep; exact hInv
    | some rec =>
      rw [hObj] at hStep
      dsimp only at hStep
      split at hStep
      · cases hStep; exact hInv
      · rename_i hAccNot
        have hAcc : (planResolve {
          kind := kind, actor := actor, scope := scope, path := path,
          doc := rec.doc, owner := rec.owner, expectMatch := expectMatch, ownerRevMatch := true
        }).accepted = true := by
          cases hv : (planResolve _).accepted <;> simp [hv] at hAccNot ⊢
        split at hStep
        · rename_i rev store' hTxn
          cases hStep
          have hStore' := txn_ok_put1 hTxn
          have hNextWF := planResolve_preserves_wf kind actor scope path rec.doc rec.owner
            (st.witness addr) expectMatch (hInv.ownerWF addr rec hObj)
            (hInv.provenance addr rec hObj) hAcc
          refine ⟨⟨?_, ?_⟩, ?_, ?_⟩
          · intro c hc
            rw [hStore'] at hc ⊢
            rcases List.mem_append.mp hc with hm | hm
            · exact Nat.le_succ_of_le (hInv.pairing.1 c hm)
            · simp at hm; subst hm; exact Nat.le_refl _
          · intro a ha
            rw [hStore']
            have hFindO := find_append_put1 st.store addr.ownKey "own" a.oKey hInv.pairing.1
            have hFindOwn := find_append_put1 st.store addr.ownKey "own" a.ownKey hInv.pairing.1
            by_cases heq : a = addr
            · subst a
              have hne : addr.ownKey ≠ addr.oKey := Ne.symm (oKey_ne_ownKey hAddr hAddr)
              have hPair := hInv.pairing.2 addr hAddr
              rw [hObj] at hPair
              simp only [beq_self_eq_true, ↓reduceIte, Option.isSome_some]
              rw [hFindO, hFindOwn]
              simp [hne, hPair.1]
            · have hbeq : (a == addr) = false := beq_eq_false_iff_ne.mpr heq
              have h1 : addr.ownKey ≠ a.oKey := Ne.symm (oKey_ne_ownKey ha hAddr)
              have h3 : addr.ownKey ≠ a.ownKey := fun h => heq (ownKey_inj ha hAddr h.symm)
              simp only [hbeq, Bool.false_eq_true, ↓reduceIte]
              rw [hFindO, hFindOwn]
              simp only [h1, h3, ↓reduceIte]
              exact hInv.pairing.2 a ha
          · intro a r ha
            dsimp only at ha
            split at ha
            · cases ha; exact hNextWF.1
            · exact hInv.ownerWF a r ha
          · intro a r ha p m hp
            dsimp only at ha ⊢
            split at ha
            · rename_i heq
              simp only [beq_iff_eq] at heq; subst heq
              cases ha
              simp only [beq_self_eq_true, ↓reduceIte]
              exact hNextWF.2 p m hp
            · rename_i hne
              simp only [hne]
              exact hInv.provenance a r ha p m hp
        · cases hStep; exact hInv
        · cases hStep; exact hInv

private theorem stepDelete_preserves_invariant (st st' : CASState) (addr : ObjAddr)
    (actor : ActorID) (scope : Scope) (oRev ownRev : Rev) (out : CASOutcome)
    (hInv : CASInvariant st) (hStep : stepDelete st addr actor scope oRev ownRev = (out, st')) :
    CASInvariant st' := by
  unfold stepDelete at hStep
  split at hStep
  · cases hStep; exact hInv
  · rename_i hPre
    simp only [Bool.or_eq_true, Bool.not_eq_true', not_or] at hPre
    have hAddr : validAddr addr = true := by cases hv : validAddr addr <;> simp [hv] at hPre ⊢
    cases hObj : st.objects addr with
    | none => rw [hObj] at hStep; dsimp only at hStep; cases hStep; exact hInv
    | some rec =>
      rw [hObj] at hStep
      dsimp only at hStep
      split at hStep
      · cases hStep; exact hInv
      · split at hStep
        · cases hStep; exact hInv
        · split at hStep
          · rename_i rev store' hTxn
            cases hStep
            have hPairAddr := hInv.pairing.2 addr hAddr
            rw [hObj] at hPairAddr
            simp only [Option.isSome_some] at hPairAddr
            have hStore' := txn_ok_del2 hPairAddr.1 hPairAddr.2 hTxn
            refine ⟨⟨?_, ?_⟩, ?_, ?_⟩
            · intro c hc
              rw [hStore'] at hc ⊢
              rcases List.mem_append.mp hc with hm | hm
              · exact Nat.le_succ_of_le (hInv.pairing.1 c hm)
              · simp at hm; subst hm; exact Nat.le_refl _
            · intro a ha
              rw [hStore']
              have hFindO := find_append_del2 st.store addr.oKey addr.ownKey a.oKey hInv.pairing.1
              have hFindOwn := find_append_del2 st.store addr.oKey addr.ownKey a.ownKey hInv.pairing.1
              by_cases heq : a = addr
              · subst a
                have hne : addr.ownKey ≠ addr.oKey := Ne.symm (oKey_ne_ownKey hAddr hAddr)
                simp only [beq_self_eq_true, ↓reduceIte, Option.isSome_none]
                rw [hFindO, hFindOwn]
                simp [hne]
              · have hbeq : (a == addr) = false := beq_eq_false_iff_ne.mpr heq
                have h1 : addr.ownKey ≠ a.oKey := Ne.symm (oKey_ne_ownKey ha hAddr)
                have h2 : addr.oKey ≠ a.oKey := fun h => heq (oKey_inj ha hAddr h.symm)
                have h3 : addr.ownKey ≠ a.ownKey := fun h => heq (ownKey_inj ha hAddr h.symm)
                have h4 : addr.oKey ≠ a.ownKey := oKey_ne_ownKey hAddr ha
                simp only [hbeq, Bool.false_eq_true, ↓reduceIte]
                rw [hFindO, hFindOwn]
                simp only [h1, h2, h3, h4, ↓reduceIte]
                exact hInv.pairing.2 a ha
            · intro a r ha
              dsimp only at ha
              split at ha
              · cases ha
              · exact hInv.ownerWF a r ha
            · intro a r ha p m hp
              dsimp only at ha ⊢
              split at ha
              · cases ha
              · rename_i hne
                simp only [hne]
                exact hInv.provenance a r ha p m hp
          · cases hStep; exact hInv
          · cases hStep; exact hInv

/-- **Theorem 11b (CAS Step Preserves Protocol Invariant)**:
    Every single-`Txn` CAS protocol step (`create`, `apply`, `prune`, `adopt`, `release`,
    `delete`) preserves multi-object single-owner exclusivity, `.created` provenance
    soundness, and atomic `o/`+`own/` pairing in `Nayutar.Store`. -/
theorem cas_step_preserves_invariant (st st' : CASState) (act : CASAction) (out : CASOutcome)
    (hInv : CASInvariant st) (hStep : stepCAS st act = (out, st')) :
    CASInvariant st' := by
  cases act with
  | create addr actor scope doc =>
    exact stepCreate_preserves_invariant st st' addr actor scope doc out hInv hStep
  | apply addr actor scope declDoc oRev ownRev exp =>
    exact stepPlanMode_preserves_invariant st st' addr actor scope declDoc .apply oRev ownRev exp out hInv hStep
  | prune addr actor scope declDoc oRev ownRev exp =>
    exact stepPlanMode_preserves_invariant st st' addr actor scope declDoc .prune oRev ownRev exp out hInv hStep
  | adopt addr actor scope path oRev ownRev exp =>
    exact stepResolveKind_preserves_invariant st st' .adopt addr actor scope path oRev ownRev exp out hInv hStep
  | release addr actor scope path oRev ownRev exp =>
    exact stepResolveKind_preserves_invariant st st' .release addr actor scope path oRev ownRev exp out hInv hStep
  | delete addr actor scope oRev ownRev =>
    exact stepDelete_preserves_invariant st st' addr actor scope oRev ownRev out hInv hStep

/-- **Theorem 11c (Trace Invariant)**:
    `CASInvariant` holds across arbitrary sequences of `CASAction` steps. -/
theorem cas_trace_invariant (st : CASState) (acts : List CASAction)
    (hInv : CASInvariant st) :
    CASInvariant (runCAS st acts) := by
  induction acts generalizing st with
  | nil => exact hInv
  | cons act rest ih =>
    simp only [runCAS]
    cases hStep : stepCAS st act with
    | mk out st1 =>
      exact ih st1 (cas_step_preserves_invariant st st1 act out hInv hStep)

/-- **Theorem 12a (CAS Conflict Leaves Store Completely Unchanged)**:
    Whenever any CAS protocol step encounters a CAS conflict (stale `o/` or `own/` `modRev`),
    the entire `CASState` (`store`, `objects`, and `witness`) is completely unchanged. -/
theorem cas_conflict_unchanged (st st' : CASState) (act : CASAction)
    (hStep : stepCAS st act = (.casConflict, st')) :
    st' = st := by
  cases act with
  | create addr actor scope doc =>
    simp only [stepCAS] at hStep
    unfold stepCreate at hStep
    split at hStep
    · cases hStep
    · cases hProj : projectEntranceSpec doc true with
      | error _ => rw [hProj] at hStep; dsimp only at hStep; cases hStep
      | ok decl =>
        rw [hProj] at hStep
        dsimp only at hStep
        split at hStep
        · cases hStep
        · split at hStep <;> cases hStep
          rfl
  | apply addr actor scope declDoc oRev ownRev exp =>
    exact stepPlanMode_conflict_unchanged hStep
  | prune addr actor scope declDoc oRev ownRev exp =>
    exact stepPlanMode_conflict_unchanged hStep
  | adopt addr actor scope path oRev ownRev exp =>
    exact stepResolveKind_conflict_unchanged hStep
  | release addr actor scope path oRev ownRev exp =>
    exact stepResolveKind_conflict_unchanged hStep
  | delete addr actor scope oRev ownRev =>
    simp only [stepCAS] at hStep
    unfold stepDelete at hStep
    split at hStep
    · cases hStep
    · cases hObj : st.objects addr with
      | none => rw [hObj] at hStep; dsimp only at hStep; cases hStep
      | some rec =>
        rw [hObj] at hStep
        dsimp only at hStep
        split at hStep
        · cases hStep
        · split at hStep
          · cases hStep
          · split at hStep <;> cases hStep
            rfl
where
  stepPlanMode_conflict_unchanged {st st' : CASState} {addr : ObjAddr} {actor : ActorID}
      {scope : Scope} {declDoc : Value} {mode : Mode} {oRev ownRev : Rev} {exp : Bool}
      (hStep : stepPlanMode st addr actor scope declDoc mode oRev ownRev exp = (.casConflict, st')) :
      st' = st := by
    unfold stepPlanMode at hStep
    split at hStep
    · cases hStep
    · cases hObj : st.objects addr with
      | none => rw [hObj] at hStep; dsimp only at hStep; cases hStep
      | some rec =>
        rw [hObj] at hStep
        dsimp only at hStep
        cases hPlan : planMulti rec.doc rec.owner declDoc actor scope mode exp true with
        | error _ => rw [hPlan] at hStep; dsimp only at hStep; cases hStep
        | ok plan =>
          rw [hPlan] at hStep
          dsimp only at hStep
          split at hStep
          · cases hStep
          · split at hStep
            · cases hStep
            · split at hStep <;> cases hStep
              rfl

  stepResolveKind_conflict_unchanged {st st' : CASState} {kind : ResolveKind} {addr : ObjAddr}
      {actor : ActorID} {scope : Scope} {path : Path} {oRev ownRev : Rev} {exp : Bool}
      (hStep : stepResolveKind st kind addr actor scope path oRev ownRev exp = (.casConflict, st')) :
      st' = st := by
    unfold stepResolveKind at hStep
    split at hStep
    · cases hStep
    · cases hObj : st.objects addr with
      | none => rw [hObj] at hStep; dsimp only at hStep; cases hStep
      | some rec =>
        rw [hObj] at hStep
        dsimp only at hStep
        split at hStep
        · cases hStep
        · split at hStep <;> cases hStep
          rfl

/-- **Theorem 12b (Atomic `o/`+`own/` Pairing and Zero Spurious `o/` Writes on `Adopt`/`Release`)**:
    Every CAS step preserves atomic `o/`+`own/` pairing in `Nayutar.Store`, and `Adopt`/`Release`
    never modify the stored document `o/` or bump `o/`'s `modRev`. -/
theorem cas_atomic_pair (st st' : CASState) (act : CASAction) (out : CASOutcome)
    (hInv : CASInvariant st) (hStep : stepCAS st act = (out, st')) :
    StorePairingWF st' ∧
    (∀ addr actor scope path oRev ownRev exp,
      (act = .adopt addr actor scope path oRev ownRev exp ∨
       act = .release addr actor scope path oRev ownRev exp) →
      st'.store.modRev addr.oKey = st.store.modRev addr.oKey ∧
      ∀ rec rec', st.objects addr = some rec → st'.objects addr = some rec' →
        rec'.doc = rec.doc) := by
  refine ⟨(cas_step_preserves_invariant st st' act out hInv hStep).pairing, ?_⟩
  intro addr actor scope path oRev ownRev exp hRes
  have hAux : ∀ kind, stepResolveKind st kind addr actor scope path oRev ownRev exp = (out, st') →
      st'.store.modRev addr.oKey = st.store.modRev addr.oKey ∧
      ∀ rec rec', st.objects addr = some rec → st'.objects addr = some rec' →
        rec'.doc = rec.doc := by
    intro kind hs
    unfold stepResolveKind at hs
    split at hs
    · cases hs; exact ⟨rfl, fun _ _ h1 h2 => by rw [h1] at h2; cases h2; rfl⟩
    · rename_i hAddrNot
      have hAddr : validAddr addr = true := by cases hv : validAddr addr <;> simp [hv] at hAddrNot ⊢
      cases hObj : st.objects addr with
      | none => rw [hObj] at hs; dsimp only at hs; cases hs; exact ⟨rfl, fun _ _ h1 _ => by cases h1⟩
      | some rec0 =>
        rw [hObj] at hs
        dsimp only at hs
        split at hs
        · cases hs; exact ⟨rfl, fun _ _ h1 h2 => by rw [hObj] at h2; rw [h1] at h2; cases h2; rfl⟩
        · split at hs
          · rename_i rev store' hTxn
            cases hs
            have hStore' := txn_ok_put1 hTxn
            have hne : addr.ownKey ≠ addr.oKey := Ne.symm (oKey_ne_ownKey hAddr hAddr)
            refine ⟨?_, ?_⟩
            · unfold Store.modRev
              rw [hStore', find_append_put1 st.store addr.ownKey "own" addr.oKey hInv.pairing.1]
              simp [hne]
            · intro rec rec' h1 h2
              cases h1
              simp only [beq_self_eq_true, ↓reduceIte, Option.some.injEq] at h2
              subst h2
              exact (resolve_safety _).1
          · cases hs; exact ⟨rfl, fun _ _ h1 h2 => by rw [hObj] at h2; rw [h1] at h2; cases h2; rfl⟩
          · cases hs; exact ⟨rfl, fun _ _ h1 h2 => by rw [hObj] at h2; rw [h1] at h2; cases h2; rfl⟩
  rcases hRes with rfl | rfl
  · exact hAux .adopt hStep
  · exact hAux .release hStep

end Nayutar.Ownership
