import Nayutar.Netpol

set_option autoImplicit false

namespace Nayutar.Netpol

/-! ### String & RFC 1123 tenant name lemmas -/

private theorem isLabelChar_ne_underscore (c : Char) (h : isLabelChar c = true) : c ≠ '_' := by
  intro heq
  subst heq
  simp [isLabelChar, isLowerAlnum] at h

private theorem allLabelTail_all_labelChar : ∀ (cs : List Char),
    allLabelTail cs = true → ∀ c ∈ cs, isLabelChar c = true
  | [], h, _, _ => by simp [allLabelTail] at h
  | [x], h, c, hc => by
    simp only [List.mem_singleton] at hc
    subst hc
    simp only [allLabelTail] at h
    simp [isLabelChar, h]
  | x :: y :: rest, h, c, hc => by
    simp only [allLabelTail, Bool.and_eq_true] at h
    rcases List.mem_cons.mp hc with rfl | hmem
    · exact h.1
    · exact allLabelTail_all_labelChar (y :: rest) h.2 c hmem

private theorem isLabelChars_all_labelChar : ∀ (cs : List Char),
    isLabelChars cs = true → ∀ c ∈ cs, isLabelChar c = true
  | [], h, _, _ => by simp [isLabelChars] at h
  | [x], h, c, hc => by
    simp only [List.mem_singleton] at hc
    subst hc
    simp only [isLabelChars] at h
    simp [isLabelChar, h]
  | x :: y :: rest, h, c, hc => by
    simp only [isLabelChars, Bool.and_eq_true] at h
    rcases List.mem_cons.mp hc with rfl | hmem
    · simp [isLabelChar, h.1]
    · exact allLabelTail_all_labelChar (y :: rest) h.2 c hmem

private theorem validTenantName_not_mem_underscore {s : String}
    (h : isValidTenantName s = true) : '_' ∉ s.toList := by
  simp only [isValidTenantName, Bool.and_eq_true] at h
  intro hmem
  exact isLabelChar_ne_underscore '_' (isLabelChars_all_labelChar s.toList h.2 '_' hmem) rfl

private theorem listCharLt_asymm : ∀ (as bs : List Char),
    listCharLt as bs = true → listCharLt bs as = false
  | [], [], h => by simp [listCharLt] at h
  | [], _ :: _, _ => by simp [listCharLt]
  | _ :: _, [], h => by simp [listCharLt] at h
  | a :: as, b :: bs, h => by
    simp only [listCharLt] at h ⊢
    split at h
    · rename_i hlt
      have hgt : ¬ (b.toNat < a.toNat) := by omega
      simp [hgt, hlt]
    · rename_i hnotlt
      split at h
      · cases h
      · rename_i hnotgt
        simp [hnotgt, hnotlt]
        exact listCharLt_asymm as bs h

private theorem listCharLt_trans : ∀ (as bs cs : List Char),
    listCharLt as bs = true → listCharLt bs cs = true → listCharLt as cs = true
  | [], [], _, hab, _ => by simp [listCharLt] at hab
  | [], _ :: _, [], _, hbc => by simp [listCharLt] at hbc
  | [], _ :: _, _ :: _, _, _ => by simp [listCharLt]
  | _ :: _, [], _, hab, _ => by simp [listCharLt] at hab
  | _ :: _, _ :: _, [], _, hbc => by simp [listCharLt] at hbc
  | a :: as, b :: bs, c :: cs, hab, hbc => by
    simp only [listCharLt] at hab hbc ⊢
    split at hab
    · rename_i hablt
      split at hbc
      · rename_i hbclt
        have haclt : a.toNat < c.toNat := by omega
        simp [haclt]
      · rename_i hbcnotlt
        split at hbc
        · cases hbc
        · rename_i hbcnotgt
          have haclt : a.toNat < c.toNat := by omega
          simp [haclt]
    · rename_i habnotlt
      split at hab
      · cases hab
      · rename_i habnotgt
        have habeq : a.toNat = b.toNat := by omega
        split at hbc
        · rename_i hbclt
          have haclt : a.toNat < c.toNat := by omega
          simp [haclt]
        · rename_i hbcnotlt
          split at hbc
          · cases hbc
          · rename_i hbcnotgt
            have hacnotlt : ¬ (a.toNat < c.toNat) := by omega
            have hacnotgt : ¬ (c.toNat < a.toNat) := by omega
            simp [hacnotlt, hacnotgt]
            exact listCharLt_trans as bs cs hab hbc

private theorem char_eq_of_toNat_eq {a b : Char} (h : a.toNat = b.toNat) : a = b := by
  apply Char.ext
  exact UInt32.toNat_inj.mp h

private theorem listCharLt_trichotomy : ∀ (as bs : List Char),
    as ≠ bs → listCharLt as bs = true ∨ listCharLt bs as = true
  | [], [], h => absurd rfl h
  | [], _ :: _, _ => Or.inl rfl
  | _ :: _, [], _ => Or.inr rfl
  | a :: as, b :: bs, h => by
    by_cases hlt : a.toNat < b.toNat
    · left; simp [listCharLt, hlt]
    · by_cases hgt : b.toNat < a.toNat
      · right; simp [listCharLt, hgt]
      · have hceq : a = b := char_eq_of_toNat_eq (by omega)
        subst hceq
        have htail : as ≠ bs := fun h' => h (congrArg (List.cons a) h')
        rcases listCharLt_trichotomy as bs htail with h1 | h2
        · left; simp [listCharLt, h1]
        · right; simp [listCharLt, h2]

private theorem strLt_asymm (a b : String) (h : strLt a b = true) : strLt b a = false :=
  listCharLt_asymm a.toList b.toList h

private theorem strLt_trans (a b c : String) (h1 : strLt a b = true) (h2 : strLt b c = true) :
    strLt a c = true :=
  listCharLt_trans a.toList b.toList c.toList h1 h2

private theorem strLt_trichotomy (a b : String) (hne : a ≠ b) :
    strLt a b = true ∨ strLt b a = true := by
  apply listCharLt_trichotomy
  intro heq
  exact hne (String.ext heq)

/-! ### Prefix interval lemmas -/

private theorem prefix_overlaps_comm (a b : Prefix) : a.overlaps b = b.overlaps a := by
  simp only [Prefix.overlaps]
  cases a.family <;> cases b.family
  · simp [and_comm]
  · rfl
  · rfl
  · simp [and_comm]

private theorem prefix_not_overlaps_disjoint {a b : Prefix} (fam : Family) (addr : Nat)
    (hno : a.overlaps b = false) :
    ¬ (a.contains fam addr = true ∧ b.contains fam addr = true) := by
  intro ⟨ha, hb⟩
  simp only [Prefix.contains, Bool.and_eq_true, beq_iff_eq, decide_eq_true_eq] at ha hb
  have hov : a.overlaps b = true := by
    simp only [Prefix.overlaps, Bool.and_eq_true, beq_iff_eq, decide_eq_true_eq]
    exact ⟨ha.1.trans hb.1.symm, by omega, by omega⟩
  rw [hov] at hno
  cases hno

private theorem tenant_disjoint_of_cidrs_disjoint {t1 t2 : TenantPolicy} {fam : Family} {addr : Nat}
    (hdisj : ∀ c1 ∈ t1.cidrs, ∀ c2 ∈ t2.cidrs, c1.overlaps c2 = false) :
    ¬ (tenantContains t1 fam addr = true ∧ tenantContains t2 fam addr = true) := by
  intro ⟨h1, h2⟩
  simp only [tenantContains, List.any_eq_true] at h1 h2
  obtain ⟨c1, hc1, hmc1⟩ := h1
  obtain ⟨c2, hc2, hmc2⟩ := h2
  exact prefix_not_overlaps_disjoint fam addr (hdisj c1 hc1 c2 hc2) ⟨hmc1, hmc2⟩

/-! ### Sorting & compilation invariant lemmas -/

private theorem mem_insertPrefix (p : Prefix) : ∀ (ps : List Prefix) (x : Prefix),
    x ∈ insertPrefix p ps ↔ x = p ∨ x ∈ ps
  | [], x => by simp [insertPrefix]
  | y :: ys, x => by
    simp only [insertPrefix]
    split
    · simp [List.mem_cons]
    · simp only [List.mem_cons, mem_insertPrefix p ys x]
      constructor <;> (intro h; rcases h with h | h | h <;> simp [h])

private theorem mem_sortPrefixes : ∀ (ps : List Prefix) (x : Prefix),
    x ∈ sortPrefixes ps ↔ x ∈ ps
  | [], x => by simp [sortPrefixes]
  | p :: ps, x => by
    simp only [sortPrefixes, mem_insertPrefix, mem_sortPrefixes ps, List.mem_cons]

private theorem mem_insertTenant (t : TenantPolicy) : ∀ (ts : List TenantPolicy) (x : TenantPolicy),
    x ∈ insertTenant t ts ↔ x = t ∨ x ∈ ts
  | [], x => by simp [insertTenant]
  | y :: ys, x => by
    simp only [insertTenant]
    split
    · simp [List.mem_cons]
    · simp only [List.mem_cons, mem_insertTenant t ys x]
      constructor <;> (intro h; rcases h with h | h | h <;> simp [h])

private theorem mem_sortTenants : ∀ (ts : List TenantPolicy) (x : TenantPolicy),
    x ∈ sortTenants ts ↔ x ∈ ts
  | [], x => by simp [sortTenants]
  | t :: ts, x => by
    simp only [sortTenants, mem_insertTenant, mem_sortTenants ts, List.mem_cons]

private theorem mem_insertRule (r : Rule) : ∀ (rs : List Rule) (x : Rule),
    x ∈ insertRule r rs ↔ x = r ∨ x ∈ rs
  | [], x => by simp [insertRule]
  | y :: ys, x => by
    simp only [insertRule]
    split
    · simp [List.mem_cons]
    · simp only [List.mem_cons, mem_insertRule r ys x]
      constructor <;> (intro h; rcases h with h | h | h <;> simp [h])

private theorem mem_sortRules : ∀ (rs : List Rule) (x : Rule),
    x ∈ sortRules rs ↔ x ∈ rs
  | [], x => by simp [sortRules]
  | r :: rs, x => by
    simp only [sortRules, mem_insertRule, mem_sortRules rs, List.mem_cons]

private theorem insertTenant_pairwise_ne {t : TenantPolicy} : ∀ {ts : List TenantPolicy},
    ts.Pairwise (fun a b => a.tenant ≠ b.tenant) →
    (∀ x ∈ ts, t.tenant ≠ x.tenant) →
    (insertTenant t ts).Pairwise (fun a b => a.tenant ≠ b.tenant)
  | [], _, _ => List.pairwise_singleton _ _
  | y :: ys, hp, hne => by
    simp only [insertTenant]
    split
    · exact List.Pairwise.cons hne hp
    · rw [List.pairwise_cons] at hp ⊢
      refine ⟨?_, insertTenant_pairwise_ne hp.2 (fun z hz => hne z (List.mem_cons_of_mem y hz))⟩
      intro z hz
      rcases (mem_insertTenant t ys z).mp hz with rfl | hmem
      · exact Ne.symm (hne y (List.mem_cons_self ..))
      · exact hp.1 z hmem

private theorem sortTenants_pairwise_ne : ∀ {ts : List TenantPolicy},
    ts.Pairwise (fun a b => a.tenant ≠ b.tenant) →
    (sortTenants ts).Pairwise (fun a b => a.tenant ≠ b.tenant)
  | [], _ => List.Pairwise.nil
  | t :: ts, hp => by
    rw [List.pairwise_cons] at hp
    simp only [sortTenants]
    apply insertTenant_pairwise_ne (sortTenants_pairwise_ne hp.2)
    intro x hx
    exact hp.1 x ((mem_sortTenants ts x).mp hx)

private theorem pass1_valid_and_distinct : ∀ (seen : List String) (ts : List TenantPolicy),
    pass1ValidateTenants seen ts = .ok () →
    (∀ t ∈ ts, isValidTenantName t.tenant = true ∧ t.tenant ∉ seen) ∧
    ts.Pairwise (fun a b => a.tenant ≠ b.tenant)
  | _, [], _ => ⟨(fun _ h => nomatch h), List.Pairwise.nil⟩
  | seen, t :: rest, h => by
    simp only [pass1ValidateTenants] at h
    split at h
    · cases h
    · rename_i hval
      simp only [Bool.not_eq_true'] at hval
      have hval' : isValidTenantName t.tenant = true := by
        cases hv : isValidTenantName t.tenant <;> simp [hv] at hval ⊢
      split at h
      · cases h
      · rename_i hnotseen
        split at h
        · cases h
        · simp only [bind, Except.bind] at h
          cases hcidrs : validateTenantCIDRs [] t.cidrs with
          | error e => rw [hcidrs] at h; cases h
          | ok _ =>
            rw [hcidrs] at h
            have ⟨hrest, hpw⟩ := pass1_valid_and_distinct (t.tenant :: seen) rest h
            have htn : t.tenant ∉ seen := by
              intro hmem
              have hc : seen.contains t.tenant = true := List.contains_iff_mem.mpr hmem
              exact hnotseen hc
            refine ⟨?_, ?_⟩
            · intro u hu
              rcases List.mem_cons.mp hu with rfl | hmem
              · exact ⟨hval', htn⟩
              · have ⟨hv, hns⟩ := hrest u hmem
                exact ⟨hv, fun hms => hns (List.mem_cons_of_mem t.tenant hms)⟩
            · refine List.Pairwise.cons ?_ hpw
              intro u hu heq
              have hns := (hrest u hu).2
              rw [← heq] at hns
              exact hns (List.mem_cons_self ..)

private theorem checkCrossTenantDisjoint_pairwise : ∀ (ts : List TenantPolicy),
    checkCrossTenantDisjoint ts = .ok () →
    ts.Pairwise (fun a b => ∀ c1 ∈ a.cidrs, ∀ c2 ∈ b.cidrs, c1.overlaps c2 = false)
  | [], _ => List.Pairwise.nil
  | t :: rest, h => by
    simp only [checkCrossTenantDisjoint] at h
    split at h
    · cases h
    · rename_i hno
      refine List.Pairwise.cons ?_ (checkCrossTenantDisjoint_pairwise rest h)
      intro u hu c1 hc1 c2 hc2
      cases hov : c1.overlaps c2 with
      | false => rfl
      | true =>
        exfalso
        apply hno
        rw [List.any_eq_true]
        refine ⟨u, hu, ?_⟩
        simp only [cidrsOverlap, List.any_eq_true]
        exact ⟨c1, hc1, c2, hc2, hov⟩

private theorem pass4_preserves_shape : ∀ (all ts out : List TenantPolicy),
    pass4ValidateAndSortRules all ts = .ok out →
    out.length = ts.length ∧
    ∀ i (h1 : i < out.length) (h2 : i < ts.length),
      (out[i]'h1).tenant = (ts[i]'h2).tenant ∧
      (out[i]'h1).intraTenant = (ts[i]'h2).intraTenant ∧
      (out[i]'h1).cidrs = (ts[i]'h2).cidrs
  | _, [], out, h => by
    simp only [pass4ValidateAndSortRules] at h
    cases h
    exact ⟨rfl, fun _ h1 _ => nomatch h1⟩
  | all, t :: rest, out, h => by
    simp only [pass4ValidateAndSortRules] at h
    split at h
    · cases h
    · simp only [bind, Except.bind] at h
      cases hvr : validateRules all (sortRules t.rules) with
      | error e => rw [hvr] at h; cases h
      | ok _ =>
        rw [hvr] at h
        cases htail : pass4ValidateAndSortRules all rest with
        | error e => rw [htail] at h; cases h
        | ok tail =>
          rw [htail] at h
          cases h
          have ⟨hlen, hidx⟩ := pass4_preserves_shape all rest tail htail
          refine ⟨by simp [hlen], ?_⟩
          intro i h1 h2
          cases i with
          | zero => exact ⟨rfl, rfl, rfl⟩
          | succ k =>
            exact hidx k (Nat.lt_of_succ_lt_succ h1) (Nat.lt_of_succ_lt_succ h2)

private theorem pairwise_map_rel {α β : Type} {R : α → α → Prop} {S : β → β → Prop}
    (f : α → β) (hf : ∀ a b, R a b → S (f a) (f b)) :
    ∀ {xs : List α}, xs.Pairwise R → (xs.map f).Pairwise S
  | [], _ => List.Pairwise.nil
  | x :: xs, hp => by
    rw [List.pairwise_cons] at hp
    rw [List.map_cons, List.pairwise_cons]
    refine ⟨?_, pairwise_map_rel f hf hp.2⟩
    intro y hy
    rcases List.mem_map.mp hy with ⟨z, hz, rfl⟩
    exact hf x z (hp.1 z hz)

private theorem pairwise_of_same_shape {α : Type} {R : α → α → Prop} {xs ys : List α}
    (hlen : ys.length = xs.length)
    (hrel : ∀ i j (h1 : i < ys.length) (h2 : j < ys.length) (h1' : i < xs.length) (h2' : j < xs.length),
      i < j → R (xs[i]'h1') (xs[j]'h2') → R (ys[i]'h1) (ys[j]'h2))
    (hp : xs.Pairwise R) : ys.Pairwise R := by
  rw [List.pairwise_iff_getElem] at hp ⊢
  intro i j hi hj hlt
  have hi' : i < xs.length := hlen ▸ hi
  have hj' : j < xs.length := hlen ▸ hj
  exact hrel i j hi hj hi' hj' hlt (hp i j hi' hj' hlt)

/-- Compiled structural invariants extracted from `compile bundle = .ok cp`. -/
private structure CompiledInv (cp : CompiledPolicy) : Prop where
  validNames : ∀ t ∈ cp.tenants, isValidTenantName t.tenant = true
  distinctNames : cp.tenants.Pairwise (fun a b => a.tenant ≠ b.tenant)
  crossDisjoint : cp.tenants.Pairwise
    (fun a b => ∀ c1 ∈ a.cidrs, ∀ c2 ∈ b.cidrs, c1.overlaps c2 = false)

private theorem compile_inv {bundle : PolicyBundle} {cp : CompiledPolicy}
    (hc : compile bundle = .ok cp) : CompiledInv cp := by
  simp only [compile, bind, Except.bind] at hc
  cases hp1 : pass1ValidateTenants [] bundle.tenants with
  | error e => rw [hp1] at hc; cases hc
  | ok _ =>
    rw [hp1] at hc
    cases hp3 : checkCrossTenantDisjoint (sortTenants bundle.tenants) with
    | error e => rw [hp3] at hc; cases hc
    | ok _ =>
      rw [hp3] at hc
      dsimp only at hc
      let sorted := sortTenants bundle.tenants
      let withCIDRs := sorted.map (fun t => { t with cidrs := sortPrefixes t.cidrs })
      cases hp4 : pass4ValidateAndSortRules withCIDRs withCIDRs with
      | error e =>
        rw [show pass4ValidateAndSortRules
            (List.map (fun t => { t with cidrs := sortPrefixes t.cidrs }) (sortTenants bundle.tenants))
            (List.map (fun t => { t with cidrs := sortPrefixes t.cidrs }) (sortTenants bundle.tenants)) =
            Except.error e from hp4] at hc
        cases hc
      | ok finalTs =>
        rw [show pass4ValidateAndSortRules
            (List.map (fun t => { t with cidrs := sortPrefixes t.cidrs }) (sortTenants bundle.tenants))
            (List.map (fun t => { t with cidrs := sortPrefixes t.cidrs }) (sortTenants bundle.tenants)) =
            Except.ok finalTs from hp4] at hc
        cases hc
        have ⟨hv1, hd1⟩ := pass1_valid_and_distinct [] bundle.tenants hp1
        have hdSorted : sorted.Pairwise (fun a b => a.tenant ≠ b.tenant) :=
          sortTenants_pairwise_ne hd1
        have hvSorted : ∀ t ∈ sorted, isValidTenantName t.tenant = true := by
          intro t ht
          exact (hv1 t ((mem_sortTenants bundle.tenants t).mp ht)).1
        have hdisjSorted := checkCrossTenantDisjoint_pairwise sorted hp3
        have hdWith : withCIDRs.Pairwise (fun a b => a.tenant ≠ b.tenant) :=
          pairwise_map_rel (fun t : TenantPolicy => { t with cidrs := sortPrefixes t.cidrs }) (fun _ _ h => h) hdSorted
        have hdisjWith : withCIDRs.Pairwise
            (fun a b => ∀ c1 ∈ a.cidrs, ∀ c2 ∈ b.cidrs, c1.overlaps c2 = false) := by
          refine pairwise_map_rel (fun t : TenantPolicy => { t with cidrs := sortPrefixes t.cidrs }) ?_ hdisjSorted
          intro a b hab c1 hc1 c2 hc2
          exact hab c1 ((mem_sortPrefixes a.cidrs c1).mp hc1) c2 ((mem_sortPrefixes b.cidrs c2).mp hc2)
        have hvWith : ∀ t ∈ withCIDRs, isValidTenantName t.tenant = true := by
          intro t ht
          rcases List.mem_map.mp ht with ⟨u, hu, rfl⟩
          exact hvSorted u hu
        have ⟨hlen, hshape⟩ := pass4_preserves_shape withCIDRs withCIDRs finalTs hp4
        refine ⟨?_, ?_, ?_⟩
        · intro t ht
          rcases List.mem_iff_getElem.mp ht with ⟨i, hi, rfl⟩
          have hi' : i < withCIDRs.length := hlen ▸ hi
          rw [(hshape i hi hi').1]
          exact hvWith (withCIDRs[i]'hi') (List.getElem_mem hi')
        · refine pairwise_of_same_shape hlen ?_ hdWith
          intro i j hi hj hi' hj' _ hab
          rw [(hshape i hi hi').1, (hshape j hj hj').1]
          exact hab
        · refine pairwise_of_same_shape hlen ?_ hdisjWith
          intro i j hi hj hi' hj' _ hab c1 hc1 c2 hc2
          rw [(hshape i hi hi').2.2] at hc1
          rw [(hshape j hj hj').2.2] at hc2
          exact hab c1 hc1 c2 hc2

private theorem inv_mem_eq_of_tenant_eq {cp : CompiledPolicy} (hinv : CompiledInv cp)
    {t1 t2 : TenantPolicy} (h1 : t1 ∈ cp.tenants) (h2 : t2 ∈ cp.tenants)
    (heq : t1.tenant = t2.tenant) : t1 = t2 := by
  rcases List.mem_iff_getElem.mp h1 with ⟨i, hi, rfl⟩
  rcases List.mem_iff_getElem.mp h2 with ⟨j, hj, rfl⟩
  have hpw := List.pairwise_iff_getElem.mp hinv.distinctNames
  by_cases hij : i = j
  · subst hij; rfl
  · exfalso
    rcases Nat.lt_or_gt_of_ne hij with hlt | hgt
    · exact hpw i j hi hj hlt heq
    · exact hpw j i hj hi hgt heq.symm

private theorem inv_cross_disjoint_symm {cp : CompiledPolicy} (hinv : CompiledInv cp)
    {t1 t2 : TenantPolicy} (h1 : t1 ∈ cp.tenants) (h2 : t2 ∈ cp.tenants)
    (hne : t1.tenant ≠ t2.tenant) :
    ∀ c1 ∈ t1.cidrs, ∀ c2 ∈ t2.cidrs, c1.overlaps c2 = false := by
  rcases List.mem_iff_getElem.mp h1 with ⟨i, hi, rfl⟩
  rcases List.mem_iff_getElem.mp h2 with ⟨j, hj, rfl⟩
  have hij : i ≠ j := fun h => by subst h; exact hne rfl
  have hpw := List.pairwise_iff_getElem.mp hinv.crossDisjoint
  rcases Nat.lt_or_gt_of_ne hij with hlt | hgt
  · exact hpw i j hi hj hlt
  · intro c1 hc1 c2 hc2
    rw [prefix_overlaps_comm]
    exact hpw j i hj hi hgt c2 hc2 c1 hc1

private theorem inv_unique_tenant_contains {cp : CompiledPolicy} (hinv : CompiledInv cp)
    {fam : Family} {addr : Nat} {t1 t2 : TenantPolicy}
    (h1 : t1 ∈ cp.tenants) (h2 : t2 ∈ cp.tenants)
    (hc1 : tenantContains t1 fam addr = true) (hc2 : tenantContains t2 fam addr = true) :
    t1 = t2 := by
  by_cases hname : t1.tenant = t2.tenant
  · exact inv_mem_eq_of_tenant_eq hinv h1 h2 hname
  · exfalso
    exact tenant_disjoint_of_cidrs_disjoint
      (inv_cross_disjoint_symm hinv h1 h2 hname) ⟨hc1, hc2⟩

private theorem findTenant_some {cp : CompiledPolicy} {fam : Family} {addr : Nat} {t : TenantPolicy}
    (hf : findTenant cp fam addr = some t) :
    t ∈ cp.tenants ∧ tenantContains t fam addr = true := by
  simp only [findTenant] at hf
  exact ⟨List.mem_of_find?_eq_some hf, List.find?_some (p := fun u => tenantContains u fam addr) hf⟩

private theorem findTenant_eq_some_of_mem {cp : CompiledPolicy} (hinv : CompiledInv cp)
    {fam : Family} {addr : Nat} {t : TenantPolicy}
    (hmem : t ∈ cp.tenants) (hcont : tenantContains t fam addr = true) :
    findTenant cp fam addr = some t := by
  cases hf : findTenant cp fam addr with
  | none =>
    simp only [findTenant, List.find?_eq_none] at hf
    exact absurd hcont (hf t hmem)
  | some u =>
    have ⟨huMem, huCont⟩ := findTenant_some hf
    have heq : u = t := inv_unique_tenant_contains hinv huMem hmem huCont hcont
    subst heq
    rfl

/-! ### Core policy theorems (1–8) -/

/-- Packets with `ct state invalid` are unconditionally dropped by both `evaluate` and `evalNft`. -/
theorem invalid_conn_dropped (cp : CompiledPolicy) (pkt : Packet)
    (hinv : pkt.connState = .invalid) :
    evaluate cp pkt = .drop ∧ evalNft (compileNft cp) pkt = .drop := by
  constructor
  · simp [evaluate, hinv]
  · simp [evalNft, compileNft, evalRules, nftRuleMatches, hinv]

/-- Packets with `ct state established` or `related` are unconditionally accepted by both `evaluate` and `evalNft`. -/
theorem established_related_accepted (cp : CompiledPolicy) (pkt : Packet)
    (hest : pkt.connState = .established ∨ pkt.connState = .related) :
    evaluate cp pkt = .accept ∧ evalNft (compileNft cp) pkt = .accept := by
  rcases hest with h | h <;>
    exact ⟨by simp [evaluate, h], by simp [evalNft, compileNft, evalRules, nftRuleMatches, h]⟩

/-- Compiled policies have pairwise non-overlapping CIDRs across distinct tenants, so at most one tenant contains any address. -/
theorem compile_tenants_cidrs_disjoint (bundle : PolicyBundle) (cp : CompiledPolicy)
    (hc : compile bundle = .ok cp) :
    (∀ t1 ∈ cp.tenants, ∀ t2 ∈ cp.tenants, t1.tenant ≠ t2.tenant →
      ∀ c1 ∈ t1.cidrs, ∀ c2 ∈ t2.cidrs, Prefix.overlaps c1 c2 = false) ∧
    (∀ fam addr, ∀ t1 ∈ cp.tenants, ∀ t2 ∈ cp.tenants,
      tenantContains t1 fam addr = true → tenantContains t2 fam addr = true →
      t1 = t2) := by
  have hinv := compile_inv hc
  refine ⟨fun t1 h1 t2 h2 hne => inv_cross_disjoint_symm hinv h1 h2 hne, ?_⟩
  intro fam addr t1 h1 t2 h2 hc1 hc2
  exact inv_unique_tenant_contains hinv h1 h2 hc1 hc2

/-- With empty rule lists on all tenants, every `.new` packet that is cross-tenant or involves an external endpoint is dropped. -/
theorem default_deny_cross_tenant_when_no_rules (bundle : PolicyBundle) (cp : CompiledPolicy)
    (pkt : Packet)
    (hc : compile bundle = .ok cp)
    (hnew : pkt.connState = .new)
    (hempty : ∀ t ∈ cp.tenants, t.rules = [])
    (hcross : ∀ t ∈ cp.tenants,
      !(tenantContains t pkt.family pkt.srcAddr && tenantContains t pkt.family pkt.dstAddr)) :
    evaluate cp pkt = .drop := by
  have hinv := compile_inv hc
  simp only [evaluate, hnew]
  cases hsrc : findTenant cp pkt.family pkt.srcAddr with
  | none =>
    cases hdst : findTenant cp pkt.family pkt.dstAddr with
    | none => rfl
    | some dstT =>
      have ⟨hdMem, _⟩ := findTenant_some hdst
      simp [matchesTenantRules, hempty dstT hdMem]
  | some srcT =>
    have ⟨hsMem, hsCont⟩ := findTenant_some hsrc
    cases hdst : findTenant cp pkt.family pkt.dstAddr with
    | none =>
      simp [matchesTenantRules, hempty srcT hsMem]
    | some dstT =>
      have ⟨hdMem, hdCont⟩ := findTenant_some hdst
      have hne : srcT.tenant ≠ dstT.tenant := by
        intro heq
        have htEq := inv_mem_eq_of_tenant_eq hinv hsMem hdMem heq
        subst htEq
        have hnot := hcross srcT hsMem
        simp [hsCont, hdCont] at hnot
      have hbeq : (srcT.tenant == dstT.tenant) = false := beq_false_of_ne hne
      simp [hbeq, matchesTenantRules, hempty srcT hsMem]

/-- Intra-tenant `.new` packets are accepted when the tenant's `intraTenant` mode is `.allow`. -/
theorem intra_tenant_allow_mode_accepts (bundle : PolicyBundle) (cp : CompiledPolicy)
    (pkt : Packet) (t : TenantPolicy)
    (hc : compile bundle = .ok cp)
    (hnew : pkt.connState = .new)
    (ht : t ∈ cp.tenants)
    (hallow : t.intraTenant = .allow)
    (hsrc : tenantContains t pkt.family pkt.srcAddr = true)
    (hdst : tenantContains t pkt.family pkt.dstAddr = true) :
    evaluate cp pkt = .accept := by
  have hinv := compile_inv hc
  have hfs := findTenant_eq_some_of_mem hinv ht hsrc
  have hfd := findTenant_eq_some_of_mem hinv ht hdst
  simp [evaluate, hnew, hfs, hfd, hallow]

/-- Cross-tenant `.new` traffic is accepted if and only if BOTH the source egress rules and destination ingress rules match. -/
theorem cross_tenant_requires_mutual_allow (bundle : PolicyBundle) (cp : CompiledPolicy)
    (pkt : Packet) (srcT dstT : TenantPolicy)
    (hc : compile bundle = .ok cp)
    (hnew : pkt.connState = .new)
    (hs : srcT ∈ cp.tenants) (hd : dstT ∈ cp.tenants)
    (hne : srcT.tenant ≠ dstT.tenant)
    (hsrc : tenantContains srcT pkt.family pkt.srcAddr = true)
    (hdst : tenantContains dstT pkt.family pkt.dstAddr = true) :
    evaluate cp pkt = .accept ↔
      (matchesTenantRules cp srcT .egress pkt = true ∧
       matchesTenantRules cp dstT .ingress pkt = true) := by
  have hinv := compile_inv hc
  have hfs := findTenant_eq_some_of_mem hinv hs hsrc
  have hfd := findTenant_eq_some_of_mem hinv hd hdst
  have hbeq : (srcT.tenant == dstT.tenant) = false := beq_false_of_ne hne
  simp only [evaluate, hnew, hfs, hfd, hbeq, Bool.false_and, Bool.false_eq_true, ite_false]
  cases he : matchesTenantRules cp srcT .egress pkt <;>
    cases hi : matchesTenantRules cp dstT .ingress pkt <;>
    simp

/-- External-to-external `.new` transit traffic is always dropped. -/
theorem external_transit_always_dropped (cp : CompiledPolicy) (pkt : Packet)
    (hnew : pkt.connState = .new)
    (hsrc : findTenant cp pkt.family pkt.srcAddr = none)
    (hdst : findTenant cp pkt.family pkt.dstAddr = none) :
    evaluate cp pkt = .drop := by
  simp [evaluate, hnew, hsrc, hdst]

/-- Valid RFC 1123 tenant names yield disjoint and injective `forward`, `tenant_<T>`, and `tenant_<T>_in` chain names. -/
theorem chain_names_disjoint (t1 t2 : String)
    (h1 : isValidTenantName t1 = true) (h2 : isValidTenantName t2 = true) :
    tenantChainName t1 ≠ tenantInChainName t2 ∧
    tenantChainName t1 ≠ forwardChainName ∧
    tenantInChainName t1 ≠ forwardChainName ∧
    (tenantChainName t1 = tenantChainName t2 → t1 = t2) ∧
    (tenantInChainName t1 = tenantInChainName t2 → t1 = t2) := by
  have _ := h2
  have hpre : ("tenant_".toList : List Char) = ['t', 'e', 'n', 'a', 'n', 't', '_'] := rfl
  have hsuf : ("_in".toList : List Char) = ['_', 'i', 'n'] := rfl
  have hfwd : (forwardChainName.toList : List Char) = ['f', 'o', 'r', 'w', 'a', 'r', 'd'] := rfl
  refine ⟨?_, ?_, ?_, ?_, ?_⟩
  · intro heq
    have htl := congrArg String.toList heq
    simp only [tenantChainName, tenantInChainName, String.toList_append, List.append_assoc] at htl
    have hrest := List.append_cancel_left htl
    have hmem : '_' ∈ t1.toList := by
      rw [hrest, hsuf]
      exact List.mem_append_right _ (List.mem_cons_self ..)
    exact validTenantName_not_mem_underscore h1 hmem
  · intro heq
    have htl := congrArg String.toList heq
    simp only [tenantChainName, String.toList_append, hpre, hfwd, List.cons_append] at htl
    cases htl
  · intro heq
    have htl := congrArg String.toList heq
    simp only [tenantInChainName, String.toList_append, List.append_assoc, hpre, hfwd, List.cons_append] at htl
    cases htl
  · intro heq
    have htl := congrArg String.toList heq
    simp only [tenantChainName, String.toList_append] at htl
    exact String.ext (List.append_cancel_left htl)
  · intro heq
    have htl := congrArg String.toList heq
    simp only [tenantInChainName, String.toList_append, List.append_assoc] at htl
    exact String.ext (List.append_cancel_right (List.append_cancel_left htl))

/-! ### Compiled `nftables` compiler correctness (Theorem 9) -/

private theorem evalRules_append_no_ret (chains : List NftChain) (fuel : Nat)
    (rs1 rs2 : List NftRule) (pkt : Packet)
    (hnoret : ∀ r ∈ rs1, r.action ≠ .ret) :
    evalRules chains fuel (rs1 ++ rs2) pkt =
      match evalRules chains fuel rs1 pkt with
      | some v => some v
      | none => evalRules chains fuel rs2 pkt := by
  induction rs1 with
  | nil => simp [evalRules]
  | cons r rest ih =>
    have hr : r.action ≠ .ret := hnoret r (List.mem_cons_self ..)
    have hrest : ∀ x ∈ rest, x.action ≠ .ret := fun x hx => hnoret x (List.mem_cons_of_mem r hx)
    simp only [List.cons_append, evalRules]
    split
    · cases hact : r.action with
      | accept => rfl
      | drop => rfl
      | ret => exact absurd hact hr
      | jump target =>
        cases fuel with
        | zero => rfl
        | succ fuel' =>
          cases hfc : findChain chains target with
          | none => simp [hfc]
          | some sub =>
            cases hsub : evalRules chains fuel' sub.rules pkt with
            | some v => simp [hfc, hsub]
            | none =>
              simp only [hfc, hsub]
              exact ih hrest
    · exact ih hrest

private theorem evalRules_accept_rules (chains : List NftChain) (fuel : Nat)
    (rs rest : List NftRule) (pkt : Packet)
    (hacc : ∀ r ∈ rs, r.action = .accept) :
    evalRules chains fuel (rs ++ rest) pkt =
      if rs.any (fun r => nftRuleMatches r pkt) then some .accept
      else evalRules chains fuel rest pkt := by
  induction rs with
  | nil => simp
  | cons r rs ih =>
    have hr := hacc r (List.mem_cons_self ..)
    have hrs := fun x hx => hacc x (List.mem_cons_of_mem r hx)
    simp only [List.cons_append, List.any_cons, evalRules]
    by_cases hm : nftRuleMatches r pkt = true
    · simp [hm, hr]
    · simp [hm, ih hrs]

private theorem evalRules_ret_rules (chains : List NftChain) (fuel : Nat)
    (rs rest : List NftRule) (pkt : Packet)
    (hret : ∀ r ∈ rs, r.action = .ret) :
    evalRules chains fuel (rs ++ rest) pkt =
      if rs.any (fun r => nftRuleMatches r pkt) then none
      else evalRules chains fuel rest pkt := by
  induction rs with
  | nil => simp
  | cons r rs ih =>
    have hr := hret r (List.mem_cons_self ..)
    have hrs := fun x hx => hret x (List.mem_cons_of_mem r hx)
    simp only [List.cons_append, List.any_cons, evalRules]
    by_cases hm : nftRuleMatches r pkt = true
    · simp [hm, hr]
    · simp [hm, ih hrs]

private theorem compileEgressRule_all_ret (cp : CompiledPolicy) (r : Rule) :
    ∀ nr ∈ compileEgressRule cp r, nr.action = .ret := by
  intro nr hnr
  simp only [compileEgressRule] at hnr
  split at hnr
  · rcases List.mem_map.mp hnr with ⟨_, _, rfl⟩
    rfl
  · cases hnr

private theorem compileIngressRule_all_accept (cp : CompiledPolicy) (r : Rule) :
    ∀ nr ∈ compileIngressRule cp r, nr.action = .accept := by
  intro nr hnr
  simp only [compileIngressRule] at hnr
  split at hnr
  · rcases List.mem_map.mp hnr with ⟨_, _, rfl⟩
    rfl
  · cases hnr

private theorem rulePeerPrefixes_any_iff (cp : CompiledPolicy) (r : Rule) (fam : Family) (addr : Nat) :
    (rulePeerPrefixes cp r).any (fun p => p.contains fam addr) = matchesPeer cp r fam addr := by
  cases hk : r.peerKind with
  | cidr =>
    simp [rulePeerPrefixes, matchesPeer, hk]
  | tenant =>
    simp only [rulePeerPrefixes, matchesPeer, hk]
    cases findTenantByName cp r.peerTenant with
    | none => rfl
    | some peer => rfl

private theorem list_any_and_const {α : Type} (xs : List α) (p : α → Bool) (b : Bool) :
    xs.any (fun x => p x && b) = (xs.any p && b) := by
  induction xs with
  | nil => simp
  | cons x xs ih =>
    simp only [List.any_cons, ih]
    cases p x <;> cases b <;> simp

private theorem compileEgressRule_any_iff (cp : CompiledPolicy) (r : Rule) (pkt : Packet) :
    (compileEgressRule cp r).any (fun nr => nftRuleMatches nr pkt) = matchesRule cp r .egress pkt := by
  simp only [compileEgressRule, matchesRule]
  cases hdir : r.direction with
  | ingress => simp
  | egress =>
    simp only [beq_self_eq_true, ite_true, Bool.true_and, List.any_map]
    rw [← rulePeerPrefixes_any_iff cp r pkt.family pkt.dstAddr]
    have hsimp : (fun p : Prefix => nftRuleMatches
        { dstPrefix := some p, protocol := some r.protocol, hasPorts := r.hasPorts,
          portMin := r.portMin, portMax := r.portMax, action := .ret } pkt) =
        (fun p : Prefix => p.contains pkt.family pkt.dstAddr &&
          (protoMatches r.protocol pkt.protocol &&
           (!r.hasPorts || decide (r.portMin ≤ pkt.dstPort ∧ pkt.dstPort ≤ r.portMax)))) := by
      funext p
      simp [nftRuleMatches, Bool.and_assoc]
    simp only [Function.comp_def, hsimp, Bool.and_assoc]
    exact list_any_and_const (rulePeerPrefixes cp r) (fun p => p.contains pkt.family pkt.dstAddr) _

private theorem compileIngressRule_any_iff (cp : CompiledPolicy) (r : Rule) (pkt : Packet) :
    (compileIngressRule cp r).any (fun nr => nftRuleMatches nr pkt) = matchesRule cp r .ingress pkt := by
  simp only [compileIngressRule, matchesRule]
  cases hdir : r.direction with
  | egress => simp
  | ingress =>
    simp only [beq_self_eq_true, ite_true, Bool.true_and, List.any_map]
    rw [← rulePeerPrefixes_any_iff cp r pkt.family pkt.srcAddr]
    have hsimp : (fun p : Prefix => nftRuleMatches
        { srcPrefix := some p, protocol := some r.protocol, hasPorts := r.hasPorts,
          portMin := r.portMin, portMax := r.portMax, action := .accept } pkt) =
        (fun p : Prefix => p.contains pkt.family pkt.srcAddr &&
          (protoMatches r.protocol pkt.protocol &&
           (!r.hasPorts || decide (r.portMin ≤ pkt.dstPort ∧ pkt.dstPort ≤ r.portMax)))) := by
      funext p
      simp [nftRuleMatches, Bool.and_assoc]
    simp only [Function.comp_def, hsimp, Bool.and_assoc]
    exact list_any_and_const (rulePeerPrefixes cp r) (fun p => p.contains pkt.family pkt.srcAddr) _

private theorem evalEgressChain (chains : List NftChain) (fuel : Nat)
    (cp : CompiledPolicy) (t : TenantPolicy) (pkt : Packet) :
    evalRules chains fuel (compileEgressChain cp t).rules pkt =
      if t.intraTenant == .allow && tenantContains t pkt.family pkt.dstAddr then
        some .accept
      else if matchesTenantRules cp t .egress pkt then
        none
      else
        some .drop := by
  simp only [compileEgressChain]
  have hegrRet : ∀ nr ∈ t.rules.flatMap (compileEgressRule cp), nr.action = .ret := by
    intro nr hnr
    rcases List.mem_flatMap.mp hnr with ⟨r, _, hmr⟩
    exact compileEgressRule_all_ret cp r nr hmr
  have hegrAny : (t.rules.flatMap (compileEgressRule cp)).any (fun nr => nftRuleMatches nr pkt) =
      matchesTenantRules cp t .egress pkt := by
    simp only [matchesTenantRules, List.any_flatMap]
    congr 1
    funext r
    exact compileEgressRule_any_iff cp r pkt
  have htail : evalRules chains fuel (t.rules.flatMap (compileEgressRule cp) ++ [{ action := .drop }]) pkt =
      if matchesTenantRules cp t .egress pkt then none else some .drop := by
    rw [evalRules_ret_rules chains fuel _ _ pkt hegrRet, hegrAny]
    simp [evalRules, nftRuleMatches]
  cases hmode : t.intraTenant with
  | deny =>
    simp [htail]
  | allow =>
    simp only [beq_self_eq_true, ite_true, Bool.true_and, List.append_assoc]
    have hintraAcc : ∀ nr ∈ t.cidrs.map (fun c => ({ dstPrefix := some c, action := .accept } : NftRule)),
        nr.action = .accept := by
      intro nr hnr
      rcases List.mem_map.mp hnr with ⟨_, _, rfl⟩
      rfl
    rw [evalRules_accept_rules chains fuel _ _ pkt hintraAcc]
    have hintraAny : (t.cidrs.map (fun c => ({ dstPrefix := some c, action := .accept } : NftRule))).any
        (fun nr => nftRuleMatches nr pkt) = tenantContains t pkt.family pkt.dstAddr := by
      simp only [tenantContains, List.any_map]
      congr 1
      funext c
      simp [nftRuleMatches]
    rw [hintraAny, htail]

private theorem evalIngressChain (chains : List NftChain) (fuel : Nat)
    (cp : CompiledPolicy) (u : TenantPolicy) (pkt : Packet) :
    evalRules chains fuel (compileIngressChain cp u).rules pkt =
      if matchesTenantRules cp u .ingress pkt then some .accept else some .drop := by
  simp only [compileIngressChain]
  have hingAcc : ∀ nr ∈ u.rules.flatMap (compileIngressRule cp), nr.action = .accept := by
    intro nr hnr
    rcases List.mem_flatMap.mp hnr with ⟨r, _, hmr⟩
    exact compileIngressRule_all_accept cp r nr hmr
  have hingAny : (u.rules.flatMap (compileIngressRule cp)).any (fun nr => nftRuleMatches nr pkt) =
      matchesTenantRules cp u .ingress pkt := by
    simp only [matchesTenantRules, List.any_flatMap]
    congr 1
    funext r
    exact compileIngressRule_any_iff cp r pkt
  rw [evalRules_accept_rules chains fuel _ _ pkt hingAcc, hingAny]
  simp [evalRules, nftRuleMatches]

private theorem findChain_append_right {c1 c2 : List NftChain} {name : String}
    (hnone : ∀ ch ∈ c1, ch.name ≠ name) :
    findChain (c1 ++ c2) name = findChain c2 name := by
  have h1 : findChain c1 name = none := by
    simp only [findChain, List.find?_eq_none]
    intro ch hch
    simp [hnone ch hch]
  simp only [findChain] at h1 ⊢
  rw [List.find?_append, h1]
  rfl

private theorem findChain_egress_of_mem {cp : CompiledPolicy} (hinv : CompiledInv cp)
    {t : TenantPolicy} (ht : t ∈ cp.tenants) :
    findChain (compileNft cp).chains (tenantChainName t.tenant) = some (compileEgressChain cp t) := by
  simp only [compileNft]
  have go : ∀ (ts : List TenantPolicy),
      (∀ u ∈ ts, isValidTenantName u.tenant = true) →
      ts.Pairwise (fun a b => a.tenant ≠ b.tenant) →
      t ∈ ts →
      findChain (ts.flatMap (fun u => [compileEgressChain cp u, compileIngressChain cp u]))
        (tenantChainName t.tenant) = some (compileEgressChain cp t) := by
    intro ts hv hpw hmem
    induction ts with
    | nil => cases hmem
    | cons u rest ih =>
      rw [List.pairwise_cons] at hpw
      simp only [List.flatMap_cons]
      rcases List.mem_cons.mp hmem with rfl | hrest
      · simp [findChain, compileEgressChain]
      · have hvu : isValidTenantName u.tenant = true := hv u (List.mem_cons_self ..)
        have hvt : isValidTenantName t.tenant = true := hv t (List.mem_cons_of_mem u hrest)
        have hdisj := chain_names_disjoint u.tenant t.tenant hvu hvt
        have hdisj' := chain_names_disjoint t.tenant u.tenant hvt hvu
        have hne1 : tenantChainName u.tenant ≠ tenantChainName t.tenant :=
          fun heq => hpw.1 t hrest (hdisj.2.2.2.1 heq)
        have hne2 : tenantInChainName u.tenant ≠ tenantChainName t.tenant :=
          Ne.symm hdisj'.1
        rw [findChain_append_right]
        · exact ih (fun z hz => hv z (List.mem_cons_of_mem u hz)) hpw.2 hrest
        · intro ch hch
          simp only [List.mem_cons, List.not_mem_nil, or_false] at hch
          rcases hch with rfl | rfl
          · exact hne1
          · exact hne2
  exact go cp.tenants hinv.validNames hinv.distinctNames ht

private theorem findChain_ingress_of_mem {cp : CompiledPolicy} (hinv : CompiledInv cp)
    {t : TenantPolicy} (ht : t ∈ cp.tenants) :
    findChain (compileNft cp).chains (tenantInChainName t.tenant) = some (compileIngressChain cp t) := by
  simp only [compileNft]
  have go : ∀ (ts : List TenantPolicy),
      (∀ u ∈ ts, isValidTenantName u.tenant = true) →
      ts.Pairwise (fun a b => a.tenant ≠ b.tenant) →
      t ∈ ts →
      findChain (ts.flatMap (fun u => [compileEgressChain cp u, compileIngressChain cp u]))
        (tenantInChainName t.tenant) = some (compileIngressChain cp t) := by
    intro ts hv hpw hmem
    induction ts with
    | nil => cases hmem
    | cons u rest ih =>
      rw [List.pairwise_cons] at hpw
      simp only [List.flatMap_cons]
      rcases List.mem_cons.mp hmem with rfl | hrest
      · have hvt : isValidTenantName t.tenant = true := hv t (List.mem_cons_self ..)
        have hne : tenantChainName t.tenant ≠ tenantInChainName t.tenant :=
          (chain_names_disjoint t.tenant t.tenant hvt hvt).1
        simp [findChain, compileEgressChain, compileIngressChain, beq_false_of_ne hne]
      · have hvu : isValidTenantName u.tenant = true := hv u (List.mem_cons_self ..)
        have hvt : isValidTenantName t.tenant = true := hv t (List.mem_cons_of_mem u hrest)
        have hdisj := chain_names_disjoint u.tenant t.tenant hvu hvt
        have hne1 : tenantChainName u.tenant ≠ tenantInChainName t.tenant := hdisj.1
        have hne2 : tenantInChainName u.tenant ≠ tenantInChainName t.tenant :=
          fun heq => hpw.1 t hrest (hdisj.2.2.2.2 heq)
        rw [findChain_append_right]
        · exact ih (fun z hz => hv z (List.mem_cons_of_mem u hz)) hpw.2 hrest
        · intro ch hch
          simp only [List.mem_cons, List.not_mem_nil, or_false] at hch
          rcases hch with rfl | rfl
          · exact hne1
          · exact hne2
  exact go cp.tenants hinv.validNames hinv.distinctNames ht

private theorem evalStage1_cidrs (chains : List NftChain) (fuel : Nat)
    (target : String) (pkt : Packet) (sub : NftChain)
    (hfc : findChain chains target = some sub) :
    ∀ (cs : List Prefix),
      evalRules chains (fuel + 1) (cs.map (fun c => { srcPrefix := some c, action := .jump target })) pkt =
        if cs.any (fun c => c.contains pkt.family pkt.srcAddr) then
          evalRules chains fuel sub.rules pkt
        else
          none
  | [] => by simp [evalRules]
  | c :: cs => by
    simp only [List.map_cons, List.any_cons, evalRules, nftRuleMatches, List.isEmpty_nil,
      Bool.true_or, Bool.true_and, Bool.and_true, Bool.not_false]
    by_cases hc : c.contains pkt.family pkt.srcAddr = true
    · simp only [hc, ite_true, Bool.true_or, hfc]
      cases hsub : evalRules chains fuel sub.rules pkt with
      | some v => rfl
      | none =>
        rw [evalStage1_cidrs chains fuel target pkt sub hfc cs]
        cases cs.any (fun c => c.contains pkt.family pkt.srcAddr) <;> simp [hsub]
    · have hcf : c.contains pkt.family pkt.srcAddr = false := Bool.eq_false_iff.mpr hc
      simp only [hcf, Bool.false_eq_true, ite_false, Bool.false_or]
      exact evalStage1_cidrs chains fuel target pkt sub hfc cs

private theorem evalStage1_tenant (chains : List NftChain) (fuel : Nat)
    (t : TenantPolicy) (pkt : Packet) (sub : NftChain)
    (hfc : findChain chains (tenantChainName t.tenant) = some sub) :
    evalRules chains (fuel + 1) (stage1RulesForTenant t) pkt =
      if tenantContains t pkt.family pkt.srcAddr then
        evalRules chains fuel sub.rules pkt
      else
        none :=
  evalStage1_cidrs chains fuel (tenantChainName t.tenant) pkt sub hfc t.cidrs

private theorem evalStage2_cidrs (chains : List NftChain) (fuel : Nat)
    (target : String) (pkt : Packet) (sub : NftChain)
    (hfc : findChain chains target = some sub) :
    ∀ (cs : List Prefix),
      evalRules chains (fuel + 1) (cs.map (fun c => { dstPrefix := some c, action := .jump target })) pkt =
        if cs.any (fun c => c.contains pkt.family pkt.dstAddr) then
          evalRules chains fuel sub.rules pkt
        else
          none
  | [] => by simp [evalRules]
  | c :: cs => by
    simp only [List.map_cons, List.any_cons, evalRules, nftRuleMatches, List.isEmpty_nil,
      Bool.true_or, Bool.true_and, Bool.and_true, Bool.not_false]
    by_cases hc : c.contains pkt.family pkt.dstAddr = true
    · simp only [hc, ite_true, Bool.true_or, hfc]
      cases hsub : evalRules chains fuel sub.rules pkt with
      | some v => rfl
      | none =>
        rw [evalStage2_cidrs chains fuel target pkt sub hfc cs]
        cases cs.any (fun c => c.contains pkt.family pkt.dstAddr) <;> simp [hsub]
    · have hcf : c.contains pkt.family pkt.dstAddr = false := Bool.eq_false_iff.mpr hc
      simp only [hcf, Bool.false_eq_true, ite_false, Bool.false_or]
      exact evalStage2_cidrs chains fuel target pkt sub hfc cs

private theorem evalStage2_tenant (chains : List NftChain) (fuel : Nat)
    (t : TenantPolicy) (pkt : Packet) (sub : NftChain)
    (hfc : findChain chains (tenantInChainName t.tenant) = some sub) :
    evalRules chains (fuel + 1) (stage2RulesForTenant t) pkt =
      if tenantContains t pkt.family pkt.dstAddr then
        evalRules chains fuel sub.rules pkt
      else
        none :=
  evalStage2_cidrs chains fuel (tenantInChainName t.tenant) pkt sub hfc t.cidrs

private theorem evalStage3_cidrs (chains : List NftChain) (fuel : Nat) (pkt : Packet) :
    ∀ (cs : List Prefix),
      evalRules chains fuel (cs.map (fun c => { srcPrefix := some c, action := .accept })) pkt =
        if cs.any (fun c => c.contains pkt.family pkt.srcAddr) then some .accept else none
  | [] => by simp [evalRules]
  | c :: cs => by
    simp only [List.map_cons, List.any_cons, evalRules, nftRuleMatches, List.isEmpty_nil,
      Bool.true_or, Bool.true_and, Bool.and_true, Bool.not_false]
    by_cases hc : c.contains pkt.family pkt.srcAddr = true
    · simp [hc]
    · have hcf : c.contains pkt.family pkt.srcAddr = false := Bool.eq_false_iff.mpr hc
      simp only [hcf, Bool.false_eq_true, ite_false, Bool.false_or]
      exact evalStage3_cidrs chains fuel pkt cs

private theorem evalStage3_tenant (chains : List NftChain) (fuel : Nat)
    (t : TenantPolicy) (pkt : Packet) :
    evalRules chains fuel (stage3RulesForTenant t) pkt =
      if tenantContains t pkt.family pkt.srcAddr then some .accept else none :=
  evalStage3_cidrs chains fuel pkt t.cidrs

private theorem stage1_no_ret (ts : List TenantPolicy) :
    ∀ r ∈ ts.flatMap stage1RulesForTenant, r.action ≠ .ret := by
  intro r hr
  rcases List.mem_flatMap.mp hr with ⟨t, _, hmt⟩
  rcases List.mem_map.mp hmt with ⟨_, _, rfl⟩
  intro h; cases h

private theorem stage2_no_ret (ts : List TenantPolicy) :
    ∀ r ∈ ts.flatMap stage2RulesForTenant, r.action ≠ .ret := by
  intro r hr
  rcases List.mem_flatMap.mp hr with ⟨t, _, hmt⟩
  rcases List.mem_map.mp hmt with ⟨_, _, rfl⟩
  intro h; cases h

private theorem evalStage1_all {cp : CompiledPolicy} (hinv : CompiledInv cp) (fuel : Nat) (pkt : Packet) :
    evalRules (compileNft cp).chains (fuel + 1) (cp.tenants.flatMap stage1RulesForTenant) pkt =
      match findTenant cp pkt.family pkt.srcAddr with
      | none => none
      | some srcT => evalRules (compileNft cp).chains fuel (compileEgressChain cp srcT).rules pkt := by
  have go : ∀ (ts : List TenantPolicy),
      (∀ t ∈ ts, t ∈ cp.tenants) →
      evalRules (compileNft cp).chains (fuel + 1) (ts.flatMap stage1RulesForTenant) pkt =
        match ts.find? (fun t => tenantContains t pkt.family pkt.srcAddr) with
        | none => none
        | some srcT => evalRules (compileNft cp).chains fuel (compileEgressChain cp srcT).rules pkt := by
    intro ts hsub
    induction ts with
    | nil => simp [evalRules]
    | cons t rest ih =>
      have htMem : t ∈ cp.tenants := hsub t (List.mem_cons_self ..)
      have hrestSub : ∀ u ∈ rest, u ∈ cp.tenants := fun u hu => hsub u (List.mem_cons_of_mem t hu)
      simp only [List.flatMap_cons, List.find?_cons]
      rw [evalRules_append_no_ret _ _ _ _ _ (by
        intro r hr
        rcases List.mem_map.mp hr with ⟨_, _, rfl⟩
        intro h; cases h)]
      rw [evalStage1_tenant _ fuel t pkt (compileEgressChain cp t) (findChain_egress_of_mem hinv htMem)]
      cases htc : tenantContains t pkt.family pkt.srcAddr with
      | false =>
        simp only [Bool.false_eq_true, ite_false]
        exact ih hrestSub
      | true =>
        simp only [ite_true]
        cases hres : evalRules (compileNft cp).chains fuel (compileEgressChain cp t).rules pkt with
        | some v => rfl
        | none =>
          rw [ih hrestSub]
          cases hf : rest.find? (fun u => tenantContains u pkt.family pkt.srcAddr) with
          | none => rfl
          | some u =>
            have huMem : u ∈ cp.tenants := hrestSub u (List.mem_of_find?_eq_some hf)
            have huCont : tenantContains u pkt.family pkt.srcAddr = true :=
              List.find?_some (p := fun w => tenantContains w pkt.family pkt.srcAddr) hf
            have heq : u = t := inv_unique_tenant_contains hinv huMem htMem huCont htc
            subst heq
            exact hres
  exact go cp.tenants (fun _ h => h)

private theorem evalStage2_all {cp : CompiledPolicy} (hinv : CompiledInv cp) (fuel : Nat) (pkt : Packet) :
    evalRules (compileNft cp).chains (fuel + 1) (cp.tenants.flatMap stage2RulesForTenant) pkt =
      match findTenant cp pkt.family pkt.dstAddr with
      | none => none
      | some dstT => evalRules (compileNft cp).chains fuel (compileIngressChain cp dstT).rules pkt := by
  have go : ∀ (ts : List TenantPolicy),
      (∀ t ∈ ts, t ∈ cp.tenants) →
      evalRules (compileNft cp).chains (fuel + 1) (ts.flatMap stage2RulesForTenant) pkt =
        match ts.find? (fun t => tenantContains t pkt.family pkt.dstAddr) with
        | none => none
        | some dstT => evalRules (compileNft cp).chains fuel (compileIngressChain cp dstT).rules pkt := by
    intro ts hsub
    induction ts with
    | nil => simp [evalRules]
    | cons t rest ih =>
      have htMem : t ∈ cp.tenants := hsub t (List.mem_cons_self ..)
      have hrestSub : ∀ u ∈ rest, u ∈ cp.tenants := fun u hu => hsub u (List.mem_cons_of_mem t hu)
      simp only [List.flatMap_cons, List.find?_cons]
      rw [evalRules_append_no_ret _ _ _ _ _ (by
        intro r hr
        rcases List.mem_map.mp hr with ⟨_, _, rfl⟩
        intro h; cases h)]
      rw [evalStage2_tenant _ fuel t pkt (compileIngressChain cp t) (findChain_ingress_of_mem hinv htMem)]
      cases htc : tenantContains t pkt.family pkt.dstAddr with
      | false =>
        simp only [Bool.false_eq_true, ite_false]
        exact ih hrestSub
      | true =>
        simp only [ite_true]
        cases hres : evalRules (compileNft cp).chains fuel (compileIngressChain cp t).rules pkt with
        | some v => rfl
        | none =>
          rw [evalIngressChain] at hres
          split at hres <;> cases hres
  exact go cp.tenants (fun _ h => h)

private theorem evalStage3_all (chains : List NftChain) (fuel : Nat)
    (cp : CompiledPolicy) (pkt : Packet) :
    evalRules chains fuel (cp.tenants.flatMap stage3RulesForTenant) pkt =
      match findTenant cp pkt.family pkt.srcAddr with
      | none => none
      | some _ => some .accept := by
  simp only [findTenant]
  induction cp.tenants with
  | nil => simp [evalRules]
  | cons t rest ih =>
    simp only [List.flatMap_cons, List.find?_cons]
    rw [evalRules_append_no_ret _ _ _ _ _ (by
      intro r hr
      rcases List.mem_map.mp hr with ⟨_, _, rfl⟩
      intro h; cases h)]
    rw [evalStage3_tenant]
    cases tenantContains t pkt.family pkt.srcAddr with
    | true => rfl
    | false => exact ih

/-- Compiler correctness: for every valid compiled policy and every packet, evaluating the compiled `nftables` table matches declarative `evaluate` with zero `jump` fallthrough. -/
theorem compiled_nft_matches_evaluate (bundle : PolicyBundle) (cp : CompiledPolicy) (pkt : Packet)
    (hc : compile bundle = .ok cp) :
    evalNft (compileNft cp) pkt = evaluate cp pkt := by
  have hinv := compile_inv hc
  cases hcs : pkt.connState with
  | invalid =>
    have h := invalid_conn_dropped cp pkt hcs
    rw [h.1, h.2]
  | established =>
    have h := established_related_accepted cp pkt (Or.inl hcs)
    rw [h.1, h.2]
  | related =>
    have h := established_related_accepted cp pkt (Or.inr hcs)
    rw [h.1, h.2]
  | new =>
    have hbase : evalRules (compileNft cp).chains ((compileNft cp).chains.length + 1) (compileNft cp).baseChain.rules pkt =
        evalRules (compileNft cp).chains ((compileNft cp).chains.length + 1)
          (cp.tenants.flatMap stage1RulesForTenant ++
           (cp.tenants.flatMap stage2RulesForTenant ++
            cp.tenants.flatMap stage3RulesForTenant)) pkt := by
      simp only [compileNft, List.append_assoc, List.cons_append, List.nil_append,
        evalRules, nftRuleMatches, hcs]
      rfl
    have hsplit : evalRules (compileNft cp).chains ((compileNft cp).chains.length + 1) (compileNft cp).baseChain.rules pkt =
        match evalRules (compileNft cp).chains ((compileNft cp).chains.length + 1) (cp.tenants.flatMap stage1RulesForTenant) pkt with
        | some v => some v
        | none =>
          match evalRules (compileNft cp).chains ((compileNft cp).chains.length + 1) (cp.tenants.flatMap stage2RulesForTenant) pkt with
          | some v => some v
          | none => evalRules (compileNft cp).chains ((compileNft cp).chains.length + 1) (cp.tenants.flatMap stage3RulesForTenant) pkt := by
      rw [hbase]
      rw [evalRules_append_no_ret _ _ _ _ _ (stage1_no_ret cp.tenants)]
      cases evalRules (compileNft cp).chains ((compileNft cp).chains.length + 1) (cp.tenants.flatMap stage1RulesForTenant) pkt with
      | some v => rfl
      | none => rw [evalRules_append_no_ret _ _ _ _ _ (stage2_no_ret cp.tenants)]
    simp only [evalNft, hsplit,
      evalStage1_all hinv (compileNft cp).chains.length pkt,
      evalStage2_all hinv (compileNft cp).chains.length pkt,
      evalStage3_all, evaluate, hcs]
    cases hsrc : findTenant cp pkt.family pkt.srcAddr with
    | none =>
      cases hdst : findTenant cp pkt.family pkt.dstAddr with
      | none => rfl
      | some dstT =>
        dsimp only
        rw [evalIngressChain]
        cases matchesTenantRules cp dstT .ingress pkt <;> rfl
    | some srcT =>
      have ⟨hsMem, _⟩ := findTenant_some hsrc
      dsimp only
      rw [evalEgressChain]
      cases hdst : findTenant cp pkt.family pkt.dstAddr with
      | none =>
        have hnotdst : tenantContains srcT pkt.family pkt.dstAddr = false := by
          simp only [findTenant, List.find?_eq_none] at hdst
          cases hc' : tenantContains srcT pkt.family pkt.dstAddr with
          | false => rfl
          | true => exact absurd hc' (hdst srcT hsMem)
        simp only [hnotdst, Bool.and_false, Bool.false_eq_true, ite_false]
        cases matchesTenantRules cp srcT .egress pkt <;> rfl
      | some dstT =>
        have ⟨hdMem, hdCont⟩ := findTenant_some hdst
        have hdstEq : tenantContains srcT pkt.family pkt.dstAddr = (srcT.tenant == dstT.tenant) := by
          by_cases hteq : srcT.tenant = dstT.tenant
          · have heq := inv_mem_eq_of_tenant_eq hinv hsMem hdMem hteq
            subst heq
            simp [hdCont]
          · have hnot : tenantContains srcT pkt.family pkt.dstAddr = false := by
              cases hc' : tenantContains srcT pkt.family pkt.dstAddr with
              | false => rfl
              | true =>
                have heq := inv_unique_tenant_contains hinv hsMem hdMem hc' hdCont
                subst heq
                exact absurd rfl hteq
            simp [hnot, beq_false_of_ne hteq]
        dsimp only
        rw [hdstEq, Bool.and_comm]
        rw [evalIngressChain]
        cases (srcT.tenant == dstT.tenant && srcT.intraTenant == .allow) with
        | true => rfl
        | false =>
          cases matchesTenantRules cp srcT .egress pkt <;>
            cases matchesTenantRules cp dstT .ingress pkt <;> rfl

/-! ### Input-order independence of compilation (Theorem 10) -/

private theorem insertTenant_swap (a b : TenantPolicy) (hne : a.tenant ≠ b.tenant) :
    ∀ (ts : List TenantPolicy),
      insertTenant a (insertTenant b ts) = insertTenant b (insertTenant a ts)
  | [] => by
    simp only [insertTenant]
    rcases strLt_trichotomy a.tenant b.tenant hne with hab | hba
    · simp [hab, strLt_asymm _ _ hab]
    · simp [hba, strLt_asymm _ _ hba]
  | x :: xs => by
    simp only [insertTenant]
    rcases strLt_trichotomy a.tenant b.tenant hne with hab | hba
    · have hba' : strLt b.tenant a.tenant = false := strLt_asymm _ _ hab
      cases hbx : strLt b.tenant x.tenant with
      | true =>
        have hax : strLt a.tenant x.tenant = true := strLt_trans _ _ _ hab hbx
        simp [insertTenant, hab, hba', hbx, hax]
      | false =>
        cases hax : strLt a.tenant x.tenant with
        | true =>
          simp [insertTenant, hba', hbx, hax]
        | false =>
          simp [insertTenant, hbx, hax, insertTenant_swap a b hne xs]
    · have hab' : strLt a.tenant b.tenant = false := strLt_asymm _ _ hba
      cases hax : strLt a.tenant x.tenant with
      | true =>
        have hbx : strLt b.tenant x.tenant = true := strLt_trans _ _ _ hba hax
        simp [insertTenant, hba, hab', hax, hbx]
      | false =>
        cases hbx : strLt b.tenant x.tenant with
        | true =>
          simp [insertTenant, hab', hax, hbx]
        | false =>
          simp [insertTenant, hax, hbx, insertTenant_swap a b hne xs]

private theorem pairwise_ne_of_perm {xs ys : List TenantPolicy}
    (hperm : xs.Perm ys) (hp : xs.Pairwise (fun a b => a.tenant ≠ b.tenant)) :
    ys.Pairwise (fun a b => a.tenant ≠ b.tenant) := by
  have hnodup : (xs.map TenantPolicy.tenant).Nodup := by
    rw [List.Nodup, List.pairwise_map]
    exact hp
  have hnodup' : (ys.map TenantPolicy.tenant).Nodup :=
    (hperm.map TenantPolicy.tenant).nodup_iff.mp hnodup
  rw [List.Nodup, List.pairwise_map] at hnodup'
  exact hnodup'

private theorem sortTenants_perm_eq {xs ys : List TenantPolicy}
    (hperm : xs.Perm ys) (hp : xs.Pairwise (fun a b => a.tenant ≠ b.tenant)) :
    sortTenants xs = sortTenants ys := by
  induction hperm with
  | nil => rfl
  | cons x p ih =>
    rw [List.pairwise_cons] at hp
    simp only [sortTenants, ih hp.2]
  | swap x y l =>
    rw [List.pairwise_cons] at hp
    have hne : y.tenant ≠ x.tenant := hp.1 x (List.mem_cons_self ..)
    simp only [sortTenants]
    exact insertTenant_swap y x hne (sortTenants l)
  | trans p1 p2 ih1 ih2 =>
    exact (ih1 hp).trans (ih2 (pairwise_ne_of_perm p1 hp))

/-- Compilation is independent of input tenant ordering: permuting `bundle.tenants` yields the identical `CompiledPolicy` and compiled `NftTable`. -/
theorem compile_order_independent (b1 b2 : PolicyBundle) (cp1 cp2 : CompiledPolicy)
    (h1 : compile b1 = .ok cp1) (h2 : compile b2 = .ok cp2)
    (hperm : b1.tenants.Perm b2.tenants) :
    cp1 = cp2 ∧ compileNft cp1 = compileNft cp2 := by
  simp only [compile, bind, Except.bind] at h1 h2
  cases hp1 : pass1ValidateTenants [] b1.tenants with
  | error e => rw [hp1] at h1; cases h1
  | ok _ =>
    rw [hp1] at h1
    cases hp2 : pass1ValidateTenants [] b2.tenants with
    | error e => rw [hp2] at h2; cases h2
    | ok _ =>
      rw [hp2] at h2
      have ⟨_, hd1⟩ := pass1_valid_and_distinct [] b1.tenants hp1
      have hsort : sortTenants b1.tenants = sortTenants b2.tenants :=
        sortTenants_perm_eq hperm hd1
      rw [hsort] at h1
      rw [h1] at h2
      cases h2
      exact ⟨rfl, rfl⟩

end Nayutar.Netpol
