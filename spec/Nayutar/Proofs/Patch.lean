import Nayutar.Patch
import Nayutar.Proofs.Envelope
import Nayutar.Proofs.OrderedSet

set_option autoImplicit false

namespace Nayutar.Patch

open Nayutar.Envelope (Value Values Fields optEqv)

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

private theorem at_nil (v : Value) : v.at [] = some v := by
  cases v <;> rfl

private theorem at_record_cons (fs : Fields) (k : String) (p : List String) :
    (Value.record fs).at (k :: p) = (fs.get k).bind (fun v => v.at p) := by
  simp only [Value.at]
  cases fs.get k <;> rfl

private theorem at_unique : ∀ (p : List String) (v x : Value),
    v.unique = true → v.at p = some x → x.unique = true
  | [], v, x, hv, hat => by
    rw [at_nil] at hat
    cases hat
    exact hv
  | k :: rest, v, x, hv, hat => by
    cases v <;> try (simp [Value.at] at hat; done)
    rename_i fs
    simp only [unique_record, Bool.and_eq_true] at hv
    rw [at_record_cons] at hat
    cases hg : fs.get k with
    | none =>
      rw [hg] at hat
      simp at hat
    | some w =>
      rw [hg] at hat
      exact at_unique rest w x (fields_unique_get fs k w hv.2 hg) hat

private theorem get_replaceField_self : ∀ (fs : Fields) (k : String) (v : Value),
    (fs.get k).isSome = true → (replaceField fs k v).get k = some v
  | .nil, _, _, hs => by simp [Fields.get] at hs
  | .cons k' v' rest, k, v, hs => by
    simp only [Fields.get] at hs
    simp only [replaceField]
    split
    · rename_i heq
      subst heq
      simp [Fields.get]
    · rename_i hne
      simp only [hne, ite_false] at hs
      simp only [Fields.get, hne, ite_false]
      exact get_replaceField_self rest k v hs

private theorem get_replaceField_ne : ∀ (fs : Fields) {k key : String} (v : Value),
    key ≠ k → (replaceField fs k v).get key = fs.get key
  | .nil, _, _, _, _ => rfl
  | .cons k' v' rest, k, key, v, hne => by
    simp only [replaceField]
    split
    · rename_i heq
      subst heq
      simp [Fields.get, Ne.symm hne]
    · simp only [Fields.get]
      split
      · rfl
      · exact get_replaceField_ne rest v hne

private theorem keysDistinct_replaceField : ∀ (fs : Fields) (k : String) (v : Value),
    fs.keysDistinct = true → (replaceField fs k v).keysDistinct = true
  | .nil, _, _, _ => rfl
  | .cons k' v' rest, k, v, hd => by
    simp only [Fields.keysDistinct, Bool.and_eq_true] at hd
    simp only [replaceField]
    split
    · simp [Fields.keysDistinct, hd.1, hd.2]
    · rename_i hne
      simp only [Fields.keysDistinct, Bool.and_eq_true]
      exact ⟨by rw [get_replaceField_ne rest v hne]; exact hd.1,
        keysDistinct_replaceField rest k v hd.2⟩

private theorem fieldsUnique_replaceField : ∀ (fs : Fields) (k : String) (v : Value),
    Fields.unique fs = true → v.unique = true → Fields.unique (replaceField fs k v) = true
  | .nil, _, _, _, _ => rfl
  | .cons k' v' rest, k, v, hu, hv => by
    simp only [Fields.unique, Bool.and_eq_true] at hu
    simp only [replaceField]
    split
    · simp [Fields.unique, hv, hu.2]
    · simp [Fields.unique, hu.1, fieldsUnique_replaceField rest k v hu.2 hv]

private theorem unique_replaceField (fs : Fields) (k : String) (v : Value)
    (hfs : (Value.record fs).unique = true) (hv : v.unique = true) :
    (Value.record (replaceField fs k v)).unique = true := by
  simp only [unique_record, Bool.and_eq_true] at hfs ⊢
  exact ⟨keysDistinct_replaceField fs k v hfs.1, fieldsUnique_replaceField fs k v hfs.2 hv⟩

private theorem replaceField_replaceField : ∀ (fs : Fields) (k : String) (v1 v2 : Value),
    replaceField (replaceField fs k v1) k v2 = replaceField fs k v2
  | .nil, _, _, _ => rfl
  | .cons k' v' rest, k, v1, v2 => by
    simp only [replaceField]
    split
    · rename_i heq
      simp [replaceField, heq]
    · rename_i hne
      simp [replaceField, hne, replaceField_replaceField rest k v1 v2]

private theorem replaceField_self_eq : ∀ (fs : Fields) (k : String) (v : Value),
    fs.get k = some v → replaceField fs k v = fs
  | .nil, _, _, hg => by simp [Fields.get] at hg
  | .cons k' v' rest, k, v, hg => by
    simp only [Fields.get] at hg
    simp only [replaceField]
    split
    · rename_i heq
      simp only [heq, ite_true, Option.some.injEq] at hg
      subst hg
      rfl
    · rename_i hne
      simp only [hne, ite_false] at hg
      rw [replaceField_self_eq rest k v hg]

private theorem get_appendField_ne : ∀ (fs : Fields) {k key : String} (v : Value),
    key ≠ k → (appendField fs k v).get key = fs.get key
  | .nil, k, key, v, hne => by simp [appendField, Fields.get, Ne.symm hne]
  | .cons k' v' rest, k, key, v, hne => by
    simp only [appendField, Fields.get]
    split
    · rfl
    · exact get_appendField_ne rest v hne

private theorem get_appendField_self : ∀ (fs : Fields) (k : String) (v : Value),
    fs.get k = none → (appendField fs k v).get k = some v
  | .nil, k, v, _ => by simp [appendField, Fields.get]
  | .cons k' v' rest, k, v, hg => by
    simp only [Fields.get] at hg
    split at hg
    · cases hg
    · rename_i hne
      simp only [appendField, Fields.get, hne, ite_false]
      exact get_appendField_self rest k v hg

private theorem keysDistinct_appendField : ∀ (fs : Fields) (k : String) (v : Value),
    fs.keysDistinct = true → fs.get k = none → (appendField fs k v).keysDistinct = true
  | .nil, _, _, _, _ => rfl
  | .cons k' v' rest, k, v, hd, hg => by
    simp only [Fields.keysDistinct, Bool.and_eq_true] at hd
    simp only [Fields.get] at hg
    split at hg
    · cases hg
    · rename_i hne
      simp only [appendField, Fields.keysDistinct, Bool.and_eq_true]
      exact ⟨by rw [get_appendField_ne rest v hne]; exact hd.1,
        keysDistinct_appendField rest k v hd.2 hg⟩

private theorem fieldsUnique_appendField : ∀ (fs : Fields) (k : String) (v : Value),
    Fields.unique fs = true → v.unique = true → Fields.unique (appendField fs k v) = true
  | .nil, _, _, _, hv => by simp [appendField, Fields.unique, hv]
  | .cons k' v' rest, k, v, hu, hv => by
    simp only [Fields.unique, Bool.and_eq_true] at hu
    simp [appendField, Fields.unique, hu.1, fieldsUnique_appendField rest k v hu.2 hv]

private theorem unique_appendField (fs : Fields) (k : String) (v : Value)
    (hfs : (Value.record fs).unique = true) (hg : fs.get k = none) (hv : v.unique = true) :
    (Value.record (appendField fs k v)).unique = true := by
  simp only [unique_record, Bool.and_eq_true] at hfs ⊢
  exact ⟨keysDistinct_appendField fs k v hfs.1 hg, fieldsUnique_appendField fs k v hfs.2 hv⟩

private theorem get_removeField_self : ∀ (fs : Fields) (k : String),
    fs.keysDistinct = true → (removeField fs k).get k = none
  | .nil, _, _ => rfl
  | .cons k' v' rest, k, hd => by
    simp only [Fields.keysDistinct, Bool.and_eq_true] at hd
    simp only [removeField]
    split
    · rename_i heq
      subst heq
      cases hrg : rest.get k' with
      | none => rfl
      | some _ =>
        rw [hrg] at hd
        simp at hd
    · rename_i hne
      simp only [Fields.get, hne, ite_false]
      exact get_removeField_self rest k hd.2

private theorem get_removeField_ne : ∀ (fs : Fields) {k key : String},
    key ≠ k → (removeField fs k).get key = fs.get key
  | .nil, _, _, _ => rfl
  | .cons k' v' rest, k, key, hne => by
    simp only [removeField]
    split
    · rename_i heq
      subst heq
      simp [Fields.get, Ne.symm hne]
    · simp only [Fields.get]
      split
      · rfl
      · exact get_removeField_ne rest hne

private theorem keysDistinct_removeField : ∀ (fs : Fields) (k : String),
    fs.keysDistinct = true → (removeField fs k).keysDistinct = true
  | .nil, _, _ => rfl
  | .cons k' v' rest, k, hd => by
    simp only [Fields.keysDistinct, Bool.and_eq_true] at hd
    simp only [removeField]
    split
    · exact hd.2
    · rename_i hne
      simp only [Fields.keysDistinct, Bool.and_eq_true]
      exact ⟨by rw [get_removeField_ne rest hne]; exact hd.1,
        keysDistinct_removeField rest k hd.2⟩

private theorem fieldsUnique_removeField : ∀ (fs : Fields) (k : String),
    Fields.unique fs = true → Fields.unique (removeField fs k) = true
  | .nil, _, _ => rfl
  | .cons k' v' rest, k, hu => by
    simp only [Fields.unique, Bool.and_eq_true] at hu
    simp only [removeField]
    split
    · exact hu.2
    · simp [Fields.unique, hu.1, fieldsUnique_removeField rest k hu.2]

private theorem unique_removeField (fs : Fields) (k : String)
    (hfs : (Value.record fs).unique = true) :
    (Value.record (removeField fs k)).unique = true := by
  simp only [unique_record, Bool.and_eq_true] at hfs ⊢
  exact ⟨keysDistinct_removeField fs k hfs.1, fieldsUnique_removeField fs k hfs.2⟩

private theorem setAt_preserves_unique : ∀ (segs : List String) (fs fs' : Fields) (v : Value),
    (Value.record fs).unique = true → v.unique = true →
    setAt fs segs v = .ok fs' → (Value.record fs').unique = true
  | [], _, _, _, _, _, h => by simp [setAt] at h
  | [k], fs, fs', v, hfs, hv, h => by
    simp only [setAt] at h
    split at h
    · cases h
      exact unique_replaceField fs k v hfs hv
    · rename_i hnot
      cases h
      have hnone : fs.get k = none := by
        cases hg : fs.get k with
        | none => rfl
        | some _ =>
          rw [hg] at hnot
          simp at hnot
      exact unique_appendField fs k v hfs hnone hv
  | k :: k2 :: rest, fs, fs', v, hfs, hv, h => by
    simp only [setAt] at h
    cases hg : fs.get k with
    | none =>
      rw [hg] at h
      cases hs : setAt .nil (k2 :: rest) v with
      | error e =>
        rw [hs] at h
        cases h
      | ok sub =>
        rw [hs] at h
        cases h
        have hsub := setAt_preserves_unique (k2 :: rest) .nil sub v rfl hv hs
        exact unique_appendField fs k (.record sub) hfs hg hsub
    | some w =>
      simp only [hg] at h
      cases w with
      | record sub =>
        cases hs : setAt sub (k2 :: rest) v with
        | error e =>
          simp [hs, Except.map] at h
        | ok sub' =>
          simp only [hs, Except.map, Except.ok.injEq] at h
          subst h
          simp only [unique_record, Bool.and_eq_true] at hfs
          have hsubU : (Value.record sub).unique = true :=
            fields_unique_get fs k (.record sub) hfs.2 hg
          have hsub' := setAt_preserves_unique (k2 :: rest) sub sub' v hsubU hv hs
          exact unique_replaceField fs k (.record sub')
            (by simp [unique_record, hfs.1, hfs.2]) hsub'
      | _ => simp at h

private theorem deleteAt_preserves_unique : ∀ (segs : List String) (fs fs' : Fields),
    (Value.record fs).unique = true →
    deleteAt fs segs = .ok fs' → (Value.record fs').unique = true
  | [], _, _, _, h => by simp [deleteAt] at h
  | [k], fs, fs', hfs, h => by
    simp only [deleteAt] at h
    split at h
    · cases h
      exact unique_removeField fs k hfs
    · cases h
  | k :: k2 :: rest, fs, fs', hfs, h => by
    simp only [deleteAt] at h
    cases hg : fs.get k with
    | none =>
      rw [hg] at h
      cases h
    | some w =>
      simp only [hg] at h
      cases w with
      | record sub =>
        cases hs : deleteAt sub (k2 :: rest) with
        | error e =>
          simp [hs, Except.map] at h
        | ok sub' =>
          simp only [hs, Except.map, Except.ok.injEq] at h
          subst h
          simp only [unique_record, Bool.and_eq_true] at hfs
          have hsubU : (Value.record sub).unique = true :=
            fields_unique_get fs k (.record sub) hfs.2 hg
          have hsub' := deleteAt_preserves_unique (k2 :: rest) sub sub' hsubU hs
          exact unique_replaceField fs k (.record sub')
            (by simp [unique_record, hfs.1, hfs.2]) hsub'
      | _ => simp at h

private theorem stepOp_preserves_unique (cur next : Fields) (op : Op)
    (hcur : (Value.record cur).unique = true) (hop : opOk op = true)
    (hstep : stepOp cur op = .ok next) : (Value.record next).unique = true := by
  unfold opOk at hop
  simp only [Bool.and_eq_true] at hop
  unfold stepOp at hstep
  cases hp : parsePath op.path with
  | none =>
    rw [hp] at hstep
    cases hstep
  | some segs =>
    rw [hp] at hstep
    cases hk : op.kind <;> cases hv : op.value <;> simp [hk, hv] at hop hstep
    · cases hat : (Value.record cur).at segs with
      | none =>
        simp [hat] at hstep
      | some actual =>
        simp only [hat] at hstep
        split at hstep
        · cases hstep
          exact hcur
        · cases hstep
    · rename_i v
      exact setAt_preserves_unique segs cur next v hcur hop.2 hstep
    · exact deleteAt_preserves_unique segs cur next hcur hstep

private theorem runOps_preserves_unique : ∀ (ops : List Op) (cur res : Fields),
    (Value.record cur).unique = true → opsOk ops = true →
    runOps cur ops = .ok res → (Value.record res).unique = true
  | [], cur, res, hcur, _, h => by
    simp only [runOps, Except.ok.injEq] at h
    subst h
    exact hcur
  | op :: rest, cur, res, hcur, hops, h => by
    simp only [opsOk, List.all_cons, Bool.and_eq_true] at hops
    simp only [runOps] at h
    cases hs : stepOp cur op with
    | error e =>
      rw [hs] at h
      cases h
    | ok mid =>
      rw [hs] at h
      have hmid := stepOp_preserves_unique cur mid op hcur hops.1 hs
      exact runOps_preserves_unique rest mid res hmid hops.2 h

theorem apply_empty (doc : Fields) (h : (Value.record doc).unique = true) :
    apply doc [] = .ok doc := by
  simp [apply, h, opsOk, runOps]

theorem apply_preserves_unique (doc : Fields) (ops : List Op) (res : Fields)
    (h : apply doc ops = .ok res) :
    (Value.record res).unique = true := by
  unfold apply at h
  split at h
  · cases h
  · rename_i hpre
    simp only [Bool.or_eq_true, Bool.not_eq_true', not_or] at hpre
    have hdoc : (Value.record doc).unique = true := by simpa using hpre.1
    have hops : opsOk ops = true := by simpa using hpre.2
    exact runOps_preserves_unique ops doc res hdoc hops h

private theorem runOps_append : ∀ (ops₁ ops₂ : List Op) (doc : Fields),
    runOps doc (ops₁ ++ ops₂) = (runOps doc ops₁).bind (fun mid => runOps mid ops₂)
  | [], _, _ => rfl
  | op :: rest, ops₂, doc => by
    simp only [List.cons_append, runOps]
    cases stepOp doc op with
    | error e => rfl
    | ok mid => exact runOps_append rest ops₂ mid

theorem apply_append (doc : Fields) (ops₁ ops₂ : List Op)
    (h₁ : opsOk ops₁ = true) (h₂ : opsOk ops₂ = true) :
    apply doc (ops₁ ++ ops₂) = (apply doc ops₁).bind (fun mid => apply mid ops₂) := by
  cases hdoc : (Value.record doc).unique with
  | false => simp [apply, hdoc, Except.bind]
  | true =>
    have h12 : opsOk (ops₁ ++ ops₂) = true := by
      simp only [opsOk, List.all_append, Bool.and_eq_true]
      exact ⟨h₁, h₂⟩
    simp only [apply, hdoc, h12, h₁, Bool.not_true, Bool.or_self]
    rw [runOps_append]
    cases hr : runOps doc ops₁ with
    | error e => rfl
    | ok mid =>
      have hmid := runOps_preserves_unique ops₁ doc mid hdoc h₁ hr
      simp [Except.bind, hmid, h₂]

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

private theorem fields_within_of_get : ∀ (a b : Fields),
    a.keysDistinct = true →
    (∀ k v, a.get k = some v → ∃ w, b.get k = some w ∧ Value.eqv v w = true) →
    Fields.within a b = true
  | .nil, _, _, _ => rfl
  | .cons k v rest, b, hd, h => by
    simp only [Fields.keysDistinct, Bool.and_eq_true] at hd
    simp only [Fields.within, Bool.and_eq_true]
    obtain ⟨w, hbg, hvw⟩ := h k v (by simp [Fields.get])
    refine ⟨by rw [hbg]; exact hvw, fields_within_of_get rest b hd.2 ?_⟩
    intro k' v' hg
    have hne : k ≠ k' := by
      intro heq
      subst heq
      rw [hg] at hd
      simp at hd
    exact h k' v' (by simp [Fields.get, hne, hg])

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

private theorem not_mem_removeFirst_of_nodup {x : String} : ∀ (ys : List String),
    ys.Nodup → x ∉ removeFirst x ys
  | [], _ => by simp [removeFirst]
  | y :: ys, hnd => by
    simp only [List.nodup_cons] at hnd
    simp only [removeFirst]
    split
    · rename_i heq
      subst heq
      exact hnd.1
    · rename_i hne
      simp only [List.mem_cons, not_or]
      exact ⟨hne, not_mem_removeFirst_of_nodup ys hnd.2⟩

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

private theorem nodup_length_eq_of_mem_iff : ∀ (xs ys : List String),
    xs.Nodup → ys.Nodup → (∀ k, k ∈ xs ↔ k ∈ ys) → xs.length = ys.length
  | [], ys, _, _, hiff => by
    cases ys with
    | nil => rfl
    | cons y _ =>
      have := (hiff y).mpr List.mem_cons_self
      cases this
  | x :: xs, ys, hnx, hny, hiff => by
    simp only [List.nodup_cons] at hnx
    have hxys : x ∈ ys := (hiff x).mp List.mem_cons_self
    have hiff' : ∀ k, k ∈ xs ↔ k ∈ removeFirst x ys := by
      intro k
      constructor
      · intro hk
        have hne : k ≠ x := fun heq => hnx.1 (heq ▸ hk)
        exact mem_removeFirst_of_ne ys ((hiff k).mp (List.mem_cons_of_mem x hk)) hne
      · intro hk
        have hne : k ≠ x := fun heq => not_mem_removeFirst_of_nodup ys hny (heq ▸ hk)
        have hmem := (hiff k).mpr (mem_of_mem_removeFirst ys hk)
        rcases List.mem_cons.mp hmem with rfl | hrest
        · exact absurd rfl hne
        · exact hrest
    have ih := nodup_length_eq_of_mem_iff xs (removeFirst x ys) hnx.2 (removeFirst_nodup ys hny) hiff'
    have hr := removeFirst_length ys hxys
    simp only [List.length_cons]
    omega

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

private theorem record_eqv_of_get {a b : Fields}
    (ha : a.keysDistinct = true) (hb : b.keysDistinct = true)
    (hdom : ∀ k, (a.get k).isSome = (b.get k).isSome)
    (hval : ∀ k v w, a.get k = some v → b.get k = some w → Value.eqv v w = true) :
    Value.eqv (.record a) (.record b) = true := by
  simp only [Value.eqv, Bool.and_eq_true, beq_iff_eq]
  refine ⟨?_, fields_within_of_get a b ha ?_⟩
  · have hlen := nodup_length_eq_of_mem_iff (fieldsKeys a) (fieldsKeys b)
      (nodup_fieldsKeys a ha) (nodup_fieldsKeys b hb)
      (fun k => by rw [mem_fieldsKeys_iff, mem_fieldsKeys_iff, hdom k])
    rw [fieldsKeys_length, fieldsKeys_length] at hlen
    exact hlen
  · intro k v hag
    have hisSome : (b.get k).isSome = true := by rw [← hdom k, hag]; rfl
    cases hbg : b.get k with
    | none => simp [hbg] at hisSome
    | some w => exact ⟨w, rfl, hval k v w hag hbg⟩

mutual
  private theorem value_eqv_symm : ∀ (a : Value), a.unique = true →
      ∀ (b : Value), b.unique = true → Value.eqv a b = true → Value.eqv b a = true
    | .str s1, _, b, _, h => by
      cases b <;> simp [Value.eqv] at h ⊢
      exact h.symm
    | .int i1, _, b, _, h => by
      cases b <;> simp [Value.eqv] at h ⊢
      exact h.symm
    | .dec d1, _, b, _, h => by
      cases b <;> simp [Value.eqv] at h ⊢
      exact h.symm
    | .bool b1, _, b, _, h => by
      cases b <;> simp [Value.eqv] at h ⊢
      exact h.symm
    | .enum e1, _, b, _, h => by
      cases b <;> simp [Value.eqv] at h ⊢
      exact h.symm
    | .qty n1 u1, _, b, _, h => by
      cases b <;> simp [Value.eqv] at h ⊢
      exact ⟨h.1.symm, h.2.symm⟩
    | .ref r1, _, b, _, h => by
      cases b <;> simp [Value.eqv] at h ⊢
      exact h.symm
    | .list as, ha, b, hb, h => by
      cases b <;> try (simp [Value.eqv] at h; done)
      rename_i bs
      simp only [Value.unique] at ha hb
      simp only [Value.eqv] at h ⊢
      exact values_eqv_symm as ha bs hb h
    | .record as, ha, b, hb, h => by
      cases b <;> try (simp [Value.eqv] at h; done)
      rename_i bs
      simp only [Value.unique, Bool.and_eq_true] at ha hb
      obtain ⟨_, hbwd⟩ := record_eqv_get_iff ha.1 hb.1 h
      simp only [Value.eqv, Bool.and_eq_true, beq_iff_eq] at h ⊢
      refine ⟨h.1.symm, fields_within_of_get bs as hb.1 ?_⟩
      intro k w hbg
      obtain ⟨v, hag, hvw⟩ := hbwd k w hbg
      exact ⟨v, hag, fields_get_symm as ha.2 k v hag w (fields_unique_get bs k w hb.2 hbg) hvw⟩

  private theorem values_eqv_symm : ∀ (as : Values), as.unique = true →
      ∀ (bs : Values), bs.unique = true → Values.eqv as bs = true → Values.eqv bs as = true
    | .nil, _, bs, _, h => by
      cases bs <;> simp [Values.eqv] at h ⊢
    | .cons a as, ha, bs, hb, h => by
      cases bs <;> try (simp [Values.eqv] at h; done)
      rename_i b bs'
      simp only [Values.unique, Bool.and_eq_true] at ha hb
      simp only [Values.eqv, Bool.and_eq_true] at h ⊢
      exact ⟨value_eqv_symm a ha.1 b hb.1 h.1, values_eqv_symm as ha.2 bs' hb.2 h.2⟩

  private theorem fields_get_symm : ∀ (as : Fields), Fields.unique as = true →
      ∀ k v, as.get k = some v → ∀ w, w.unique = true → Value.eqv v w = true → Value.eqv w v = true
    | .nil, _, _, _, hg, _, _, _ => by simp [Fields.get] at hg
    | .cons k' v' rest, ha, k, v, hg, w, hw, hvw => by
      simp only [Fields.unique, Bool.and_eq_true] at ha
      simp only [Fields.get] at hg
      split at hg
      · cases hg
        exact value_eqv_symm v' ha.1 w hw hvw
      · exact fields_get_symm rest ha.2 k v hg w hw hvw
end

mutual
  private theorem value_eqv_trans : ∀ (a b c : Value),
      Value.eqv a b = true → Value.eqv b c = true → Value.eqv a c = true
    | .str s1, b, c, hab, hbc => by
      cases b <;> try (simp [Value.eqv] at hab; done)
      cases c <;> try (simp [Value.eqv] at hbc; done)
      simp [Value.eqv] at hab hbc ⊢
      exact hab.trans hbc
    | .int i1, b, c, hab, hbc => by
      cases b <;> try (simp [Value.eqv] at hab; done)
      cases c <;> try (simp [Value.eqv] at hbc; done)
      simp [Value.eqv] at hab hbc ⊢
      exact hab.trans hbc
    | .dec d1, b, c, hab, hbc => by
      cases b <;> try (simp [Value.eqv] at hab; done)
      cases c <;> try (simp [Value.eqv] at hbc; done)
      simp [Value.eqv] at hab hbc ⊢
      exact hab.trans hbc
    | .bool b1, b, c, hab, hbc => by
      cases b <;> try (simp [Value.eqv] at hab; done)
      cases c <;> try (simp [Value.eqv] at hbc; done)
      simp [Value.eqv] at hab hbc ⊢
      exact hab.trans hbc
    | .enum e1, b, c, hab, hbc => by
      cases b <;> try (simp [Value.eqv] at hab; done)
      cases c <;> try (simp [Value.eqv] at hbc; done)
      simp [Value.eqv] at hab hbc ⊢
      exact hab.trans hbc
    | .qty n1 u1, b, c, hab, hbc => by
      cases b <;> try (simp [Value.eqv] at hab; done)
      cases c <;> try (simp [Value.eqv] at hbc; done)
      simp [Value.eqv] at hab hbc ⊢
      exact ⟨hab.1.trans hbc.1, hab.2.trans hbc.2⟩
    | .ref r1, b, c, hab, hbc => by
      cases b <;> try (simp [Value.eqv] at hab; done)
      cases c <;> try (simp [Value.eqv] at hbc; done)
      simp [Value.eqv] at hab hbc ⊢
      exact hab.trans hbc
    | .list as, b, c, hab, hbc => by
      cases b <;> try (simp [Value.eqv] at hab; done)
      cases c <;> try (simp [Value.eqv] at hbc; done)
      rename_i bs cs
      simp only [Value.eqv] at hab hbc ⊢
      exact values_eqv_trans as bs cs hab hbc
    | .record as, b, c, hab, hbc => by
      cases b <;> try (simp [Value.eqv] at hab; done)
      cases c <;> try (simp [Value.eqv] at hbc; done)
      rename_i bs cs
      simp only [Value.eqv, Bool.and_eq_true, beq_iff_eq] at hab hbc ⊢
      exact ⟨hab.1.trans hbc.1, fields_within_trans as bs cs hab.2 hbc.2⟩

  private theorem values_eqv_trans : ∀ (as bs cs : Values),
      Values.eqv as bs = true → Values.eqv bs cs = true → Values.eqv as cs = true
    | .nil, bs, cs, hab, hbc => by
      cases bs <;> try (simp [Values.eqv] at hab; done)
      cases cs <;> simp [Values.eqv] at hbc ⊢
    | .cons a as, bs, cs, hab, hbc => by
      cases bs <;> try (simp [Values.eqv] at hab; done)
      cases cs <;> try (simp [Values.eqv] at hbc; done)
      rename_i b bs' c cs'
      simp only [Values.eqv, Bool.and_eq_true] at hab hbc ⊢
      exact ⟨value_eqv_trans a b c hab.1 hbc.1, values_eqv_trans as bs' cs' hab.2 hbc.2⟩

  private theorem fields_within_trans : ∀ (as bs cs : Fields),
      Fields.within as bs = true → Fields.within bs cs = true → Fields.within as cs = true
    | .nil, _, _, _, _ => rfl
    | .cons k v rest, bs, cs, hab, hbc => by
      simp only [Fields.within, Bool.and_eq_true] at hab ⊢
      cases hbg : bs.get k with
      | none =>
        rw [hbg] at hab
        simp at hab
      | some w =>
        rw [hbg] at hab
        obtain ⟨u, hcg, hwu⟩ := within_get_some bs cs hbc k w hbg
        refine ⟨by rw [hcg]; exact value_eqv_trans v w u hab.1 hwu,
          fields_within_trans rest bs cs hab.2 hbc⟩
end

private theorem at_eqv : ∀ (segs : List String) (va vb : Value),
    va.unique = true → vb.unique = true → Value.eqv va vb = true →
    (va.at segs = none ↔ vb.at segs = none) ∧
    (∀ xa xb, va.at segs = some xa → vb.at segs = some xb → Value.eqv xa xb = true)
  | [], va, vb, _, _, heqv => by
    simp only [at_nil, Option.some.injEq]
    exact ⟨by simp, fun xa xb ha hb => by cases ha; cases hb; exact heqv⟩
  | k :: rest, va, vb, hva, hvb, heqv => by
    cases va with
    | record fa =>
      cases vb <;> try (simp [Value.eqv] at heqv; done)
      rename_i fb
      simp only [unique_record, Bool.and_eq_true] at hva hvb
      obtain ⟨hfwd, hbwd⟩ := record_eqv_get_iff hva.1 hvb.1 heqv
      rw [at_record_cons, at_record_cons]
      cases hga : fa.get k with
      | none =>
        have hgb : fb.get k = none := by
          cases hgb' : fb.get k with
          | none => rfl
          | some wb =>
            obtain ⟨wa, hga', _⟩ := hbwd k wb hgb'
            rw [hga] at hga'
            cases hga'
        simp [hgb]
      | some wa =>
        obtain ⟨wb, hgb, hw_eqv⟩ := hfwd k wa hga
        rw [hgb]
        simp only [Option.bind_some]
        exact at_eqv rest wa wb (fields_unique_get fa k wa hva.2 hga)
          (fields_unique_get fb k wb hvb.2 hgb) hw_eqv
    | _ =>
      cases vb <;> try (simp [Value.eqv] at heqv; done)
      all_goals simp [Value.at]

private theorem setAt_eqv : ∀ (segs : List String) (a b : Fields) (v : Value),
    (Value.record a).unique = true → (Value.record b).unique = true → v.unique = true →
    Value.eqv (.record a) (.record b) = true →
    (∀ err, setAt a segs v = .error err ↔ setAt b segs v = .error err) ∧
    (∀ ra rb, setAt a segs v = .ok ra → setAt b segs v = .ok rb →
      Value.eqv (.record ra) (.record rb) = true)
  | [], _, _, _, _, _, _, _ =>
    ⟨fun _ => Iff.rfl, fun _ _ h _ => by simp [setAt] at h⟩
  | [k], a, b, v, ha, hb, hv, heqv => by
    have haU := ha
    have hbU := hb
    simp only [unique_record, Bool.and_eq_true] at ha hb
    obtain ⟨hfwd, hbwd⟩ := record_eqv_get_iff ha.1 hb.1 heqv
    have hdom : ∀ key, (a.get key).isSome = (b.get key).isSome := by
      intro key
      cases hga : a.get key with
      | none =>
        cases hgb : b.get key with
        | none => rfl
        | some w =>
          obtain ⟨u, hga', _⟩ := hbwd key w hgb
          rw [hga] at hga'
          cases hga'
      | some u =>
        obtain ⟨w, hgb, _⟩ := hfwd key u hga
        simp [hgb]
    cases hga : a.get k with
    | none =>
      have hgb : b.get k = none := by
        cases hgb' : b.get k with
        | none => rfl
        | some w =>
          obtain ⟨u, hga', _⟩ := hbwd k w hgb'
          rw [hga] at hga'
          cases hga'
      simp only [setAt, hga, hgb, Option.isSome_none, Bool.false_eq_true, ite_false]
      refine ⟨fun _ => by simp, ?_⟩
      intro ra rb hra hrb
      cases hra; cases hrb
      refine record_eqv_of_get (keysDistinct_appendField a k v ha.1 hga)
        (keysDistinct_appendField b k v hb.1 hgb) ?_ ?_
      · intro key
        by_cases hk : key = k
        · subst hk
          rw [get_appendField_self a key v hga, get_appendField_self b key v hgb]
        · rw [get_appendField_ne a v hk, get_appendField_ne b v hk, hdom key]
      · intro key va vb hva hvb
        by_cases hk : key = k
        · subst hk
          rw [get_appendField_self a key v hga] at hva
          rw [get_appendField_self b key v hgb] at hvb
          cases hva; cases hvb
          exact value_eqv_refl v hv
        · rw [get_appendField_ne a v hk] at hva
          rw [get_appendField_ne b v hk] at hvb
          obtain ⟨vb', hvb', hvw⟩ := hfwd key va hva
          rw [hvb] at hvb'
          cases hvb'
          exact hvw
    | some va0 =>
      obtain ⟨vb0, hgb, _⟩ := hfwd k va0 hga
      simp only [setAt, hga, hgb, Option.isSome_some, ite_true]
      refine ⟨fun _ => by simp, ?_⟩
      intro ra rb hra hrb
      cases hra; cases hrb
      refine record_eqv_of_get (keysDistinct_replaceField a k v ha.1)
        (keysDistinct_replaceField b k v hb.1) ?_ ?_
      · intro key
        by_cases hk : key = k
        · subst hk
          rw [get_replaceField_self a key v (by simp [hga]),
            get_replaceField_self b key v (by simp [hgb])]
        · rw [get_replaceField_ne a v hk, get_replaceField_ne b v hk, hdom key]
      · intro key va vb hva hvb
        by_cases hk : key = k
        · subst hk
          rw [get_replaceField_self a key v (by simp [hga])] at hva
          rw [get_replaceField_self b key v (by simp [hgb])] at hvb
          cases hva; cases hvb
          exact value_eqv_refl v hv
        · rw [get_replaceField_ne a v hk] at hva
          rw [get_replaceField_ne b v hk] at hvb
          obtain ⟨vb', hvb', hvw⟩ := hfwd key va hva
          rw [hvb] at hvb'
          cases hvb'
          exact hvw
  | k :: k2 :: rest, a, b, v, ha, hb, hv, heqv => by
    simp only [unique_record, Bool.and_eq_true] at ha hb
    obtain ⟨hfwd, hbwd⟩ := record_eqv_get_iff ha.1 hb.1 heqv
    have hdom : ∀ key, (a.get key).isSome = (b.get key).isSome := by
      intro key
      cases hga : a.get key with
      | none =>
        cases hgb : b.get key with
        | none => rfl
        | some w =>
          obtain ⟨u, hga', _⟩ := hbwd key w hgb
          rw [hga] at hga'
          cases hga'
      | some u =>
        obtain ⟨w, hgb, _⟩ := hfwd key u hga
        simp [hgb]
    cases hga : a.get k with
    | none =>
      have hgb : b.get k = none := by
        cases hgb' : b.get k with
        | none => rfl
        | some w =>
          obtain ⟨u, hga', _⟩ := hbwd k w hgb'
          rw [hga] at hga'
          cases hga'
      simp only [setAt, hga, hgb]
      cases hs : setAt .nil (k2 :: rest) v with
      | error e =>
        simp only [Except.map]
        exact ⟨fun _ => by simp, fun _ _ h _ => by cases h⟩
      | ok sub =>
        simp only [Except.map]
        refine ⟨fun _ => by simp, ?_⟩
        intro ra rb hra hrb
        cases hra; cases hrb
        have hsubU := setAt_preserves_unique (k2 :: rest) .nil sub v rfl hv hs
        refine record_eqv_of_get (keysDistinct_appendField a k (.record sub) ha.1 hga)
          (keysDistinct_appendField b k (.record sub) hb.1 hgb) ?_ ?_
        · intro key
          by_cases hk : key = k
          · subst hk
            rw [get_appendField_self a key _ hga, get_appendField_self b key _ hgb]
          · rw [get_appendField_ne a _ hk, get_appendField_ne b _ hk, hdom key]
        · intro key va vb hva hvb
          by_cases hk : key = k
          · subst hk
            rw [get_appendField_self a key _ hga] at hva
            rw [get_appendField_self b key _ hgb] at hvb
            cases hva; cases hvb
            exact value_eqv_refl (.record sub) hsubU
          · rw [get_appendField_ne a _ hk] at hva
            rw [get_appendField_ne b _ hk] at hvb
            obtain ⟨vb', hvb', hvw⟩ := hfwd key va hva
            rw [hvb] at hvb'
            cases hvb'
            exact hvw
    | some va0 =>
      obtain ⟨vb0, hgb, hvw0⟩ := hfwd k va0 hga
      cases va0 with
      | record suba =>
        cases vb0 <;> try (simp [Value.eqv] at hvw0; done)
        rename_i subb
        have hsubaU : (Value.record suba).unique = true :=
          fields_unique_get a k (.record suba) ha.2 hga
        have hsubbU : (Value.record subb).unique = true :=
          fields_unique_get b k (.record subb) hb.2 hgb
        obtain ⟨ih_err, ih_ok⟩ := setAt_eqv (k2 :: rest) suba subb v hsubaU hsubbU hv hvw0
        simp only [setAt, hga, hgb]
        cases hsa : setAt suba (k2 :: rest) v with
        | error e =>
          have hsb : setAt subb (k2 :: rest) v = .error e := (ih_err e).mp hsa
          rw [hsb]
          exact ⟨fun _ => Iff.rfl, fun _ _ h _ => by cases h⟩
        | ok suba' =>
          cases hsb : setAt subb (k2 :: rest) v with
          | error e =>
            have hcontra := (ih_err e).mpr hsb
            rw [hsa] at hcontra
            cases hcontra
          | ok subb' =>
            simp only [Except.map]
            refine ⟨fun _ => by simp, ?_⟩
            intro ra rb hra hrb
            cases hra; cases hrb
            have hsub_eqv := ih_ok suba' subb' hsa hsb
            refine record_eqv_of_get (keysDistinct_replaceField a k (.record suba') ha.1)
              (keysDistinct_replaceField b k (.record subb') hb.1) ?_ ?_
            · intro key
              by_cases hk : key = k
              · subst hk
                rw [get_replaceField_self a key _ (by simp [hga]),
                  get_replaceField_self b key _ (by simp [hgb])]
                rfl
              · rw [get_replaceField_ne a _ hk, get_replaceField_ne b _ hk, hdom key]
            · intro key va vb hva hvb
              by_cases hk : key = k
              · subst hk
                rw [get_replaceField_self a key _ (by simp [hga])] at hva
                rw [get_replaceField_self b key _ (by simp [hgb])] at hvb
                cases hva; cases hvb
                exact hsub_eqv
              · rw [get_replaceField_ne a _ hk] at hva
                rw [get_replaceField_ne b _ hk] at hvb
                obtain ⟨vb', hvb', hvw⟩ := hfwd key va hva
                rw [hvb] at hvb'
                cases hvb'
                exact hvw
      | _ =>
        cases vb0 <;> try (simp [Value.eqv] at hvw0; done)
        all_goals
          simp only [setAt, hga, hgb]
          exact ⟨fun _ => by simp, fun _ _ h _ => by cases h⟩

private theorem deleteAt_eqv : ∀ (segs : List String) (a b : Fields),
    (Value.record a).unique = true → (Value.record b).unique = true →
    Value.eqv (.record a) (.record b) = true →
    (∀ err, deleteAt a segs = .error err ↔ deleteAt b segs = .error err) ∧
    (∀ ra rb, deleteAt a segs = .ok ra → deleteAt b segs = .ok rb →
      Value.eqv (.record ra) (.record rb) = true)
  | [], _, _, _, _, _ =>
    ⟨fun _ => Iff.rfl, fun _ _ h _ => by simp [deleteAt] at h⟩
  | [k], a, b, ha, hb, heqv => by
    simp only [unique_record, Bool.and_eq_true] at ha hb
    obtain ⟨hfwd, hbwd⟩ := record_eqv_get_iff ha.1 hb.1 heqv
    have hdom : ∀ key, (a.get key).isSome = (b.get key).isSome := by
      intro key
      cases hga : a.get key with
      | none =>
        cases hgb : b.get key with
        | none => rfl
        | some w =>
          obtain ⟨u, hga', _⟩ := hbwd key w hgb
          rw [hga] at hga'
          cases hga'
      | some u =>
        obtain ⟨w, hgb, _⟩ := hfwd key u hga
        simp [hgb]
    cases hga : a.get k with
    | none =>
      have hgb : b.get k = none := by
        cases hgb' : b.get k with
        | none => rfl
        | some w =>
          obtain ⟨u, hga', _⟩ := hbwd k w hgb'
          rw [hga] at hga'
          cases hga'
      simp only [deleteAt, hga, hgb, Option.isSome_none, Bool.false_eq_true, ite_false]
      exact ⟨fun _ => by simp, fun _ _ h _ => by cases h⟩
    | some va0 =>
      obtain ⟨vb0, hgb, _⟩ := hfwd k va0 hga
      simp only [deleteAt, hga, hgb, Option.isSome_some, ite_true]
      refine ⟨fun _ => by simp, ?_⟩
      intro ra rb hra hrb
      cases hra; cases hrb
      refine record_eqv_of_get (keysDistinct_removeField a k ha.1)
        (keysDistinct_removeField b k hb.1) ?_ ?_
      · intro key
        by_cases hk : key = k
        · subst hk
          rw [get_removeField_self a key ha.1, get_removeField_self b key hb.1]
        · rw [get_removeField_ne a hk, get_removeField_ne b hk, hdom key]
      · intro key va vb hva hvb
        by_cases hk : key = k
        · subst hk
          rw [get_removeField_self a key ha.1] at hva
          cases hva
        · rw [get_removeField_ne a hk] at hva
          rw [get_removeField_ne b hk] at hvb
          obtain ⟨vb', hvb', hvw⟩ := hfwd key va hva
          rw [hvb] at hvb'
          cases hvb'
          exact hvw
  | k :: k2 :: rest, a, b, ha, hb, heqv => by
    simp only [unique_record, Bool.and_eq_true] at ha hb
    obtain ⟨hfwd, hbwd⟩ := record_eqv_get_iff ha.1 hb.1 heqv
    have hdom : ∀ key, (a.get key).isSome = (b.get key).isSome := by
      intro key
      cases hga : a.get key with
      | none =>
        cases hgb : b.get key with
        | none => rfl
        | some w =>
          obtain ⟨u, hga', _⟩ := hbwd key w hgb
          rw [hga] at hga'
          cases hga'
      | some u =>
        obtain ⟨w, hgb, _⟩ := hfwd key u hga
        simp [hgb]
    cases hga : a.get k with
    | none =>
      have hgb : b.get k = none := by
        cases hgb' : b.get k with
        | none => rfl
        | some w =>
          obtain ⟨u, hga', _⟩ := hbwd k w hgb'
          rw [hga] at hga'
          cases hga'
      simp only [deleteAt, hga, hgb]
      exact ⟨fun _ => by simp, fun _ _ h _ => by cases h⟩
    | some va0 =>
      obtain ⟨vb0, hgb, hvw0⟩ := hfwd k va0 hga
      cases va0 with
      | record suba =>
        cases vb0 <;> try (simp [Value.eqv] at hvw0; done)
        rename_i subb
        have hsubaU : (Value.record suba).unique = true :=
          fields_unique_get a k (.record suba) ha.2 hga
        have hsubbU : (Value.record subb).unique = true :=
          fields_unique_get b k (.record subb) hb.2 hgb
        obtain ⟨ih_err, ih_ok⟩ := deleteAt_eqv (k2 :: rest) suba subb hsubaU hsubbU hvw0
        simp only [deleteAt, hga, hgb]
        cases hsa : deleteAt suba (k2 :: rest) with
        | error e =>
          have hsb : deleteAt subb (k2 :: rest) = .error e := (ih_err e).mp hsa
          rw [hsb]
          exact ⟨fun _ => Iff.rfl, fun _ _ h _ => by cases h⟩
        | ok suba' =>
          cases hsb : deleteAt subb (k2 :: rest) with
          | error e =>
            have hcontra := (ih_err e).mpr hsb
            rw [hsa] at hcontra
            cases hcontra
          | ok subb' =>
            simp only [Except.map]
            refine ⟨fun _ => by simp, ?_⟩
            intro ra rb hra hrb
            cases hra; cases hrb
            have hsub_eqv := ih_ok suba' subb' hsa hsb
            refine record_eqv_of_get (keysDistinct_replaceField a k (.record suba') ha.1)
              (keysDistinct_replaceField b k (.record subb') hb.1) ?_ ?_
            · intro key
              by_cases hk : key = k
              · subst hk
                rw [get_replaceField_self a key _ (by simp [hga]),
                  get_replaceField_self b key _ (by simp [hgb])]
                rfl
              · rw [get_replaceField_ne a _ hk, get_replaceField_ne b _ hk, hdom key]
            · intro key va vb hva hvb
              by_cases hk : key = k
              · subst hk
                rw [get_replaceField_self a key _ (by simp [hga])] at hva
                rw [get_replaceField_self b key _ (by simp [hgb])] at hvb
                cases hva; cases hvb
                exact hsub_eqv
              · rw [get_replaceField_ne a _ hk] at hva
                rw [get_replaceField_ne b _ hk] at hvb
                obtain ⟨vb', hvb', hvw⟩ := hfwd key va hva
                rw [hvb] at hvb'
                cases hvb'
                exact hvw
      | _ =>
        cases vb0 <;> try (simp [Value.eqv] at hvw0; done)
        all_goals
          simp only [deleteAt, hga, hgb]
          exact ⟨fun _ => by simp, fun _ _ h _ => by cases h⟩

private theorem stepOp_eqv (a b : Fields) (op : Op)
    (ha : (Value.record a).unique = true) (hb : (Value.record b).unique = true)
    (hop : opOk op = true) (heqv : Value.eqv (.record a) (.record b) = true) :
    (∀ err, stepOp a op = .error err ↔ stepOp b op = .error err) ∧
    (∀ ra rb, stepOp a op = .ok ra → stepOp b op = .ok rb →
      Value.eqv (.record ra) (.record rb) = true) := by
  unfold opOk at hop
  simp only [Bool.and_eq_true] at hop
  unfold stepOp
  cases hp : parsePath op.path with
  | none =>
    rw [hp] at hop
    simp at hop
  | some segs =>
    cases hk : op.kind <;> cases hv : op.value <;> simp [hk, hv] at hop ⊢
    · rename_i expected
      obtain ⟨hnone, hsome⟩ := at_eqv segs (.record a) (.record b) ha hb heqv
      cases hata : (Value.record a).at segs with
      | none =>
        have hatb : (Value.record b).at segs = none := hnone.mp hata
        rw [hatb]
        exact ⟨fun _ => Iff.rfl, fun _ _ h _ => by cases h⟩
      | some xa =>
        cases hatb : (Value.record b).at segs with
        | none =>
          have hcontra := hnone.mpr hatb
          rw [hata] at hcontra
          cases hcontra
        | some xb =>
          have hxab : Value.eqv xa xb = true := hsome xa xb hata hatb
          have hxaU : xa.unique = true := at_unique segs (.record a) xa ha hata
          have hxbU : xb.unique = true := at_unique segs (.record b) xb hb hatb
          have hxba : Value.eqv xb xa = true := value_eqv_symm xa hxaU xb hxbU hxab
          have hcmp : Value.eqv xa expected = Value.eqv xb expected := by
            cases h1 : Value.eqv xa expected with
            | true =>
              symm
              exact value_eqv_trans xb xa expected hxba h1
            | false =>
              cases h2 : Value.eqv xb expected with
              | false => rfl
              | true =>
                have := value_eqv_trans xa xb expected hxab h2
                rw [h1] at this
                cases this
          simp only
          rw [hcmp]
          split
          · exact ⟨fun _ => by simp, fun ra rb hra hrb => by cases hra; cases hrb; exact heqv⟩
          · exact ⟨fun _ => by simp, fun _ _ h _ => by cases h⟩
    · rename_i v
      exact setAt_eqv segs a b v ha hb hop.2 heqv
    · exact deleteAt_eqv segs a b ha hb heqv

private theorem runOps_eqv : ∀ (ops : List Op) (a b : Fields),
    (Value.record a).unique = true → (Value.record b).unique = true →
    opsOk ops = true → Value.eqv (.record a) (.record b) = true →
    (∀ err, runOps a ops = .error err ↔ runOps b ops = .error err) ∧
    (∀ ra rb, runOps a ops = .ok ra → runOps b ops = .ok rb →
      Value.eqv (.record ra) (.record rb) = true)
  | [], a, b, _, _, _, heqv =>
    ⟨fun _ => by simp [runOps], fun ra rb hra hrb => by
      simp only [runOps, Except.ok.injEq] at hra hrb
      subst hra; subst hrb; exact heqv⟩
  | op :: rest, a, b, ha, hb, hops, heqv => by
    simp only [opsOk, List.all_cons, Bool.and_eq_true] at hops
    obtain ⟨hs_err, hs_ok⟩ := stepOp_eqv a b op ha hb hops.1 heqv
    simp only [runOps]
    cases hsa : stepOp a op with
    | error e =>
      have hsb : stepOp b op = .error e := (hs_err e).mp hsa
      rw [hsb]
      exact ⟨fun _ => Iff.rfl, fun _ _ h _ => by cases h⟩
    | ok ma =>
      cases hsb : stepOp b op with
      | error e =>
        have hcontra := (hs_err e).mpr hsb
        rw [hsa] at hcontra
        cases hcontra
      | ok mb =>
        have hmaU := stepOp_preserves_unique a ma op ha hops.1 hsa
        have hmbU := stepOp_preserves_unique b mb op hb hops.1 hsb
        have hm_eqv := hs_ok ma mb hsa hsb
        exact runOps_eqv rest ma mb hmaU hmbU hops.2 hm_eqv

theorem apply_eqv_invariant (a b : Fields) (ops : List Op)
    (ha : (Value.record a).unique = true)
    (hb : (Value.record b).unique = true)
    (heqv : Value.eqv (.record a) (.record b) = true) :
    (∀ err, apply a ops = .error err ↔ apply b ops = .error err) ∧
    (∀ ra rb, apply a ops = .ok ra → apply b ops = .ok rb →
      Value.eqv (.record ra) (.record rb) = true) := by
  cases hops : opsOk ops with
  | false =>
    simp only [apply, ha, hb, hops, Bool.not_false, Bool.or_true, ite_true]
    exact ⟨fun _ => by simp, fun _ _ h _ => by cases h⟩
  | true =>
    simp only [apply, ha, hb, hops, Bool.not_true, Bool.or_self]
    exact runOps_eqv ops a b ha hb hops heqv

private theorem mem_insertKey (k x : String) : ∀ (xs : List String),
    x ∈ insertKey k xs ↔ x = k ∨ x ∈ xs
  | [] => by simp [insertKey]
  | y :: ys => by
    simp only [insertKey]
    split
    · simp [List.mem_cons]
    · simp only [List.mem_cons, mem_insertKey k x ys]
      constructor
      · rintro (rfl | rfl | hmem)
        · exact Or.inr (Or.inl rfl)
        · exact Or.inl rfl
        · exact Or.inr (Or.inr hmem)
      · rintro (rfl | rfl | hmem)
        · exact Or.inr (Or.inl rfl)
        · exact Or.inl rfl
        · exact Or.inr (Or.inr hmem)

private theorem mem_sortKeys (x : String) : ∀ (ks : List String),
    x ∈ sortKeys ks ↔ x ∈ ks
  | [] => by simp [sortKeys]
  | k :: ks => by simp [sortKeys, mem_insertKey, mem_sortKeys x ks]

private theorem mem_sortedKeys (fs : Fields) (k : String) :
    k ∈ sortedKeys fs ↔ (fs.get k).isSome = true := by
  rw [sortedKeys, mem_sortKeys, mem_fieldsKeys_iff]

private theorem nodup_insertKey (k : String) : ∀ (xs : List String),
    k ∉ xs → xs.Nodup → (insertKey k xs).Nodup
  | [], _, _ => by simp [insertKey]
  | y :: ys, hnot, hnd => by
    simp only [List.mem_cons, not_or] at hnot
    simp only [List.nodup_cons] at hnd
    simp only [insertKey]
    split
    · exact List.nodup_cons.mpr ⟨by simp [List.mem_cons, hnot.1, hnot.2],
        List.nodup_cons.mpr hnd⟩
    · refine List.nodup_cons.mpr ⟨?_, nodup_insertKey k ys hnot.2 hnd.2⟩
      intro hmem
      rcases (mem_insertKey k y ys).mp hmem with rfl | hy
      · exact hnot.1 rfl
      · exact hnd.1 hy

private theorem nodup_sortKeys : ∀ (ks : List String),
    ks.Nodup → (sortKeys ks).Nodup
  | [], _ => by simp [sortKeys]
  | k :: ks, hnd => by
    simp only [List.nodup_cons] at hnd
    refine nodup_insertKey k (sortKeys ks) ?_ (nodup_sortKeys ks hnd.2)
    intro hmem
    exact hnd.1 ((mem_sortKeys k ks).mp hmem)

private theorem nodup_sortedKeys (fs : Fields) (hd : fs.keysDistinct = true) :
    (sortedKeys fs).Nodup :=
  nodup_sortKeys (fieldsKeys fs) (nodup_fieldsKeys fs hd)

private theorem topSegsOk_get : ∀ (fs : Fields) (k : String),
    topSegsOk fs = true → (fs.get k).isSome = true → validSeg k = true
  | .nil, _, _, hs => by simp [Fields.get] at hs
  | .cons k' _ rest, k, htop, hs => by
    simp only [topSegsOk, Bool.and_eq_true] at htop
    simp only [Fields.get] at hs
    split at hs
    · rename_i heq
      subst heq
      exact htop.1
    · exact topSegsOk_get rest k htop.2 hs

private theorem diffDels_self (pfx : List String) (doc : Fields) : ∀ (ks : List String),
    (∀ k ∈ ks, (doc.get k).isSome = true) → diffDels pfx doc ks = []
  | [], _ => rfl
  | k :: ks, hall => by
    have hs := hall k List.mem_cons_self
    have hnone : (doc.get k).isNone = false := by
      cases hg : doc.get k with
      | none => simp [hg] at hs
      | some _ => rfl
    simp only [diffDels, hnone, Bool.false_eq_true, ite_false]
    exact diffDels_self pfx doc ks (fun x hx => hall x (List.mem_cons_of_mem k hx))

private def diffSetEntry (pfx : List String) (k : String) (oldVal : Option Value) (nv : Value) :
    List Op :=
  match oldVal with
  | none => [{ kind := .set, path := joinSlash (pfx ++ [k]), value := some nv }]
  | some ov =>
    if Value.eqv ov nv then []
    else
      match ov, nv with
      | .record osub, .record nsub =>
        if (decide (osub.length > 0) || decide (nsub.length > 0))
            && topSegsOk osub && topSegsOk nsub then
          diffFields (pfx ++ [k]) osub nsub
        else
          [{ kind := .set, path := joinSlash (pfx ++ [k]), value := some nv }]
      | _, _ =>
        [{ kind := .set, path := joinSlash (pfx ++ [k]), value := some nv }]

private theorem diffSets_cons_none (pfx : List String) (old next : Fields) (k : String)
    (ks : List String) (h : next.get k = none) :
    diffSets pfx old next (k :: ks) = diffSets pfx old next ks := by
  rw [diffSets]
  split
  · rfl
  · rename_i nv hnv
    rw [h] at hnv
    cases hnv

private theorem diffSets_cons_some (pfx : List String) (old next : Fields) (k : String)
    (ks : List String) (nv : Value) (h : next.get k = some nv) :
    diffSets pfx old next (k :: ks) =
      diffSetEntry pfx k (old.get k) nv ++ diffSets pfx old next ks := by
  rw [diffSets]
  split
  · rename_i hnone
    rw [h] at hnone
    cases hnone
  · rename_i nv' hnv'
    rw [h] at hnv'
    cases hnv'
    congr 1
    unfold diffSetEntry
    cases old.get k with
    | none => rfl
    | some ov =>
      simp only
      split
      · rfl
      · cases ov <;> cases nv <;> rfl

private theorem diffSets_self (pfx : List String) (doc : Fields)
    (hu : Fields.unique doc = true) : ∀ (ks : List String), diffSets pfx doc doc ks = []
  | [] => by simp [diffSets]
  | k :: ks => by
    cases hg : doc.get k with
    | none =>
      rw [diffSets_cons_none pfx doc doc k ks hg]
      exact diffSets_self pfx doc hu ks
    | some v =>
      rw [diffSets_cons_some pfx doc doc k ks v hg, hg]
      have hv : Value.eqv v v = true := value_eqv_refl v (fields_unique_get doc k v hu hg)
      simp only [diffSetEntry, hv, ite_true, List.nil_append]
      exact diffSets_self pfx doc hu ks

theorem diff_refl (doc : Fields)
    (hu : (Value.record doc).unique = true)
    (htop : topSegsOk doc = true) :
    diff doc doc = .ok [] := by
  simp only [diff, hu, htop, Bool.not_true, Bool.or_self]
  rw [diffFields]
  have hdels : diffDels [] doc (sortedKeys doc) = [] :=
    diffDels_self [] doc (sortedKeys doc) (fun k hk => (mem_sortedKeys doc k).mp hk)
  simp only [unique_record, Bool.and_eq_true] at hu
  have hsets : diffSets [] doc doc (sortedKeys doc) = [] :=
    diffSets_self [] doc hu.2 (sortedKeys doc)
  rw [hdels, hsets]
  rfl

private def pushChars (cur : String) : List Char → String
  | [] => cur
  | c :: cs => pushChars (cur.push c) cs

private theorem toList_pushChars : ∀ (cs : List Char) (cur : String),
    (pushChars cur cs).toList = cur.toList ++ cs
  | [], cur => by simp [pushChars]
  | c :: cs, cur => by
    simp only [pushChars, toList_pushChars cs (cur.push c), String.toList_push, List.append_assoc,
      List.singleton_append]

private theorem pushChars_empty_toList (s : String) : pushChars "" s.toList = s := by
  apply String.ext
  rw [toList_pushChars]
  rfl

private theorem splitSlashAux_append_no_slash : ∀ (cs1 cs2 : List Char) (cur : String)
    (acc : List String),
    cs1.all (fun c => c != '/') = true →
    splitSlashAux cur acc (cs1 ++ cs2) = splitSlashAux (pushChars cur cs1) acc cs2
  | [], cs2, cur, acc, _ => rfl
  | c :: cs1, cs2, cur, acc, hall => by
    simp only [List.all_cons, Bool.and_eq_true, bne_iff_ne, ne_eq] at hall
    simp only [List.cons_append, splitSlashAux, beq_iff_eq, hall.1, ite_false, pushChars]
    exact splitSlashAux_append_no_slash cs1 cs2 (cur.push c) acc hall.2

private theorem splitSlashAux_acc : ∀ (cs : List Char) (cur : String) (acc : List String),
    splitSlashAux cur acc cs = acc.reverse ++ splitSlashAux cur [] cs
  | [], cur, acc => by simp [splitSlashAux]
  | c :: cs, cur, acc => by
    simp only [splitSlashAux]
    split
    · rw [splitSlashAux_acc cs "" (cur :: acc), splitSlashAux_acc cs "" [cur]]
      simp [List.append_assoc]
    · exact splitSlashAux_acc cs (cur.push c) acc

private theorem splitSlashAux_ne_nil : ∀ (cs : List Char) (cur : String) (acc : List String),
    splitSlashAux cur acc cs ≠ []
  | [], cur, acc => by simp [splitSlashAux]
  | c :: cs, cur, acc => by
    simp only [splitSlashAux]
    split
    · exact splitSlashAux_ne_nil cs "" (cur :: acc)
    · exact splitSlashAux_ne_nil cs (cur.push c) acc

private theorem validSeg_chars {s : String} (h : validSeg s = true) :
    s.toList ≠ [] ∧ s.toList.all (fun c => c != '/') = true := by
  unfold validSeg Nayutar.OrderedSet.validKey at h
  cases hlist : s.toList with
  | nil =>
    rw [hlist] at h
    simp at h
  | cons c cs =>
    rw [hlist] at h
    simp only [Bool.and_eq_true] at h
    exact ⟨by simp, by simp [List.all_cons, h.1.2, h.2]⟩

private theorem joinSlash_singleton (a : String) : joinSlash [a] = a := rfl

private theorem intercalate_slash_acc : ∀ (xs : List String) (acc b : String),
    String.intercalate "/" ((acc ++ "/" ++ b) :: xs) =
      acc ++ "/" ++ String.intercalate "/" (b :: xs)
  | [], _, _ => rfl
  | c :: cs, acc, b => by
    change String.intercalate "/" (((acc ++ "/" ++ b) ++ "/" ++ c) :: cs) =
      acc ++ "/" ++ String.intercalate "/" ((b ++ "/" ++ c) :: cs)
    have hstr : (acc ++ "/" ++ b) ++ "/" ++ c = acc ++ "/" ++ (b ++ "/" ++ c) := by
      simp [String.append_assoc]
    rw [hstr, intercalate_slash_acc cs acc (b ++ "/" ++ c)]

private theorem joinSlash_cons_cons (a b : String) (rest : List String) :
    joinSlash (a :: b :: rest) = a ++ "/" ++ joinSlash (b :: rest) := by
  unfold joinSlash
  change String.intercalate "/" ((a ++ "/" ++ b) :: rest) =
    a ++ "/" ++ String.intercalate "/" (b :: rest)
  exact intercalate_slash_acc rest a b

private theorem splitSlash_validSeg {s : String} (h : validSeg s = true) :
    splitSlash s = [s] := by
  have ⟨_, hno⟩ := validSeg_chars h
  unfold splitSlash
  have := splitSlashAux_append_no_slash s.toList [] "" [] hno
  simp only [List.append_nil, splitSlashAux, List.reverse_cons, List.reverse_nil, List.nil_append,
    pushChars_empty_toList] at this
  exact this

private theorem parsePath_validSeg {s : String} (h : validSeg s = true) :
    parsePath s = some [s] := by
  have ⟨hne_list, _⟩ := validSeg_chars h
  have hne : s ≠ "" := by
    intro heq
    subst heq
    exact hne_list rfl
  simp [parsePath, hne, splitSlash_validSeg h, h]

private theorem parsePath_prefix {k p : String} {segs : List String}
    (hk : validSeg k = true) (hp : parsePath p = some segs) :
    parsePath (k ++ "/" ++ p) = some (k :: segs) := by
  unfold parsePath at hp
  split at hp
  · cases hp
  · rename_i hpne
    dsimp only at hp
    split at hp
    · rename_i hall
      cases hp
      have ⟨hk_ne, hk_no⟩ := validSeg_chars hk
      have hsplit : splitSlash (k ++ "/" ++ p) = k :: splitSlash p := by
        unfold splitSlash
        have hchars : (k ++ "/" ++ p).toList = k.toList ++ ('/' :: p.toList) := by
          simp [String.toList_append, List.append_assoc]
        rw [hchars, splitSlashAux_append_no_slash k.toList ('/' :: p.toList) "" [] hk_no,
          pushChars_empty_toList]
        simp only [splitSlashAux, beq_self_eq_true, ite_true]
        rw [splitSlashAux_acc p.toList "" [k]]
        rfl
      have hkp_ne : (k ++ "/" ++ p == "") = false := by
        simp only [beq_eq_false_iff_ne, ne_eq]
        intro heq
        have htl := congrArg String.toList heq
        simp [String.toList_append] at htl
      simp [parsePath, hkp_ne, hsplit, List.all_cons, hk, hall]
    · cases hp

private theorem parsePath_ne_nil {p : String} {segs : List String}
    (h : parsePath p = some segs) : segs ≠ [] := by
  unfold parsePath at h
  split at h
  · cases h
  · dsimp only at h
    split at h
    · cases h
      exact splitSlashAux_ne_nil p.toList "" []
    · cases h

private def prefixOp (k : String) (op : Op) : Op :=
  { kind := op.kind, path := k ++ "/" ++ op.path, value := op.value }

private def prefixOps (k : String) (ops : List Op) : List Op :=
  ops.map (prefixOp k)

private theorem prefixOp_opOk {k : String} {op : Op} (hk : validSeg k = true)
    (hop : opOk op = true) : opOk (prefixOp k op) = true := by
  unfold opOk at hop ⊢
  simp only [Bool.and_eq_true] at hop ⊢
  cases hp : parsePath op.path with
  | none =>
    rw [hp] at hop
    simp at hop
  | some segs =>
    refine ⟨?_, hop.2⟩
    simp only [prefixOp]
    rw [parsePath_prefix hk hp]
    rfl

private theorem prefixOps_opsOk {k : String} : ∀ (ops : List Op),
    validSeg k = true → opsOk ops = true → opsOk (prefixOps k ops) = true
  | [], _, _ => rfl
  | op :: rest, hk, hops => by
    simp only [opsOk, List.all_cons, Bool.and_eq_true] at hops
    simp only [prefixOps, List.map_cons, opsOk, List.all_cons, Bool.and_eq_true]
    exact ⟨prefixOp_opOk hk hops.1, prefixOps_opsOk rest hk hops.2⟩

private theorem joinSlash_cons_append (k : String) (pfx : List String) (k' : String) :
    joinSlash (k :: (pfx ++ [k'])) = k ++ "/" ++ joinSlash (pfx ++ [k']) := by
  cases pfx with
  | nil => exact joinSlash_cons_cons k k' []
  | cons a rest => exact joinSlash_cons_cons k a (rest ++ [k'])

private theorem diffDels_prefix (k : String) (pfx : List String) (next : Fields) :
    ∀ (ks : List String), diffDels (k :: pfx) next ks = prefixOps k (diffDels pfx next ks)
  | [] => rfl
  | k' :: ks => by
    simp only [diffDels]
    split
    · simp only [prefixOps, List.map_cons, prefixOp, List.cons_append,
        joinSlash_cons_append k pfx k', diffDels_prefix k pfx next ks]
    · exact diffDels_prefix k pfx next ks

private theorem fieldsSize_get_lt : ∀ (fs : Fields) (k : String) (v : Value),
    fs.get k = some v → valueSize v < fieldsSize fs
  | .nil, _, _, h => by simp [Fields.get] at h
  | .cons k' v' rest, k, v, h => by
    simp only [Fields.get] at h
    split at h
    · cases h
      simp [fieldsSize]
      omega
    · have ih := fieldsSize_get_lt rest k v h
      simp [fieldsSize]
      omega

private theorem fieldsSize_get_record_lt (fs : Fields) (k : String) (sub : Fields)
    (h : fs.get k = some (.record sub)) : fieldsSize sub < fieldsSize fs := by
  have h1 := fieldsSize_get_lt fs k (.record sub) h
  simp only [valueSize] at h1
  omega

private theorem diffFields_prefix_both (k : String) :
    ∀ (n : Nat) (old next : Fields), fieldsSize next ≤ n →
    (∀ (pfx : List String),
      diffFields (k :: pfx) old next = prefixOps k (diffFields pfx old next)) ∧
    (∀ (pfx : List String) (ks : List String),
      diffSets (k :: pfx) old next ks = prefixOps k (diffSets pfx old next ks))
  | 0, old, next, hle => by
    have hpos : 0 < fieldsSize next := by cases next <;> simp [fieldsSize]
    omega
  | n + 1, old, next, hle => by
    have hsets : ∀ (pfx : List String) (ks : List String),
        diffSets (k :: pfx) old next ks = prefixOps k (diffSets pfx old next ks) := by
      intro pfx ks
      induction ks with
      | nil =>
        rw [diffSets, diffSets]
        rfl
      | cons k' ks' ih =>
        cases h2 : next.get k' with
        | none =>
          rw [diffSets_cons_none (k :: pfx) old next k' ks' h2,
            diffSets_cons_none pfx old next k' ks' h2]
          exact ih
        | some nv =>
          rw [diffSets_cons_some (k :: pfx) old next k' ks' nv h2,
            diffSets_cons_some pfx old next k' ks' nv h2, ih]
          simp only [prefixOps, List.map_append]
          congr 1
          unfold diffSetEntry
          cases h1 : old.get k' with
          | none =>
            simp [prefixOp, List.cons_append, joinSlash_cons_append k pfx k']
          | some ov =>
            simp only
            split
            · rfl
            · cases ov with
              | record osub =>
                cases nv with
                | record nsub =>
                  simp only
                  split
                  · have hlt : fieldsSize nsub < fieldsSize next :=
                      fieldsSize_get_record_lt next k' nsub h2
                    have ih_sub := (diffFields_prefix_both k n osub nsub (by omega)).1 (pfx ++ [k'])
                    simpa [prefixOps, List.cons_append] using ih_sub
                  · simp [prefixOp, List.cons_append, joinSlash_cons_append k pfx k']
                | _ => simp [prefixOp, List.cons_append, joinSlash_cons_append k pfx k']
              | _ => simp [prefixOp, List.cons_append, joinSlash_cons_append k pfx k']
    refine ⟨?_, hsets⟩
    intro pfx
    rw [diffFields, diffFields]
    simp only [prefixOps, List.map_append]
    rw [diffDels_prefix k pfx next (sortedKeys old), hsets pfx (sortedKeys next)]
    rfl

private theorem diffFields_singleton_prefix (k : String) (old next : Fields) :
    diffFields [k] old next = prefixOps k (diffFields [] old next) :=
  (diffFields_prefix_both k (fieldsSize next) old next (Nat.le_refl _)).1 []

private theorem stepOp_prefix {k : String} {doc sub sub' : Fields} {op : Op}
    (hk : validSeg k = true) (hget : doc.get k = some (.record sub))
    (hop : opOk op = true) (hstep : stepOp sub op = .ok sub') :
    stepOp doc (prefixOp k op) = .ok (replaceField doc k (.record sub')) := by
  unfold opOk at hop
  simp only [Bool.and_eq_true] at hop
  unfold stepOp at hstep ⊢
  cases hp : parsePath op.path with
  | none =>
    rw [hp] at hstep
    cases hstep
  | some segs =>
    rw [hp] at hstep
    have hne := parsePath_ne_nil hp
    cases segs with
    | nil => exact absurd rfl hne
    | cons s1 srest =>
      have hp' : parsePath (prefixOp k op).path = some (k :: s1 :: srest) :=
        parsePath_prefix hk hp
      rw [hp']
      simp only [prefixOp]
      cases hk' : op.kind <;> cases hv : op.value <;> simp [hk', hv] at hop hstep ⊢
      · rename_i expected
        rw [at_record_cons, hget]
        simp only [Option.bind_some]
        cases hat : (Value.record sub).at (s1 :: srest) with
        | none =>
          simp [hat] at hstep
        | some actual =>
          simp only [hat] at hstep
          split at hstep
          · rename_i heq
            cases hstep
            simp only [heq, ite_true, Except.ok.injEq]
            exact (replaceField_self_eq doc k (.record sub) hget).symm
          · cases hstep
      · rename_i v
        simp only [setAt, hget, hstep, Except.map]
      · simp only [deleteAt, hget, hstep, Except.map]

private theorem runOps_prefix {k : String} : ∀ (ops : List Op) (doc sub sub' : Fields),
    validSeg k = true → doc.get k = some (.record sub) →
    opsOk ops = true → runOps sub ops = .ok sub' →
    runOps doc (prefixOps k ops) = .ok (replaceField doc k (.record sub'))
  | [], doc, sub, sub', _, hget, _, hrun => by
    simp only [runOps, Except.ok.injEq] at hrun
    subst hrun
    simp only [prefixOps, List.map_nil, runOps, Except.ok.injEq]
    exact (replaceField_self_eq doc k (.record sub) hget).symm
  | op :: rest, doc, sub, sub', hk, hget, hops, hrun => by
    simp only [opsOk, List.all_cons, Bool.and_eq_true] at hops
    simp only [runOps] at hrun
    cases hs : stepOp sub op with
    | error e =>
      simp [hs] at hrun
    | ok mid =>
      simp only [hs] at hrun
      simp only [prefixOps, List.map_cons, runOps]
      rw [stepOp_prefix hk hget hops.1 hs]
      simp only
      have hget' : (replaceField doc k (.record mid)).get k = some (.record mid) :=
        get_replaceField_self doc k (.record mid) (by simp [hget])
      have ih := runOps_prefix rest (replaceField doc k (.record mid)) mid sub' hk hget' hops.2 hrun
      rw [replaceField_replaceField] at ih
      exact ih

private theorem diffDels_run (next : Fields) : ∀ (ks : List String) (cur : Fields),
    ks.Nodup → (∀ k ∈ ks, validSeg k = true) → (∀ k ∈ ks, (cur.get k).isSome = true) →
    (Value.record cur).unique = true →
    opsOk (diffDels [] next ks) = true ∧
    ∃ mid, runOps cur (diffDels [] next ks) = .ok mid ∧
      (Value.record mid).unique = true ∧
      ∀ key, mid.get key = if key ∈ ks ∧ (next.get key).isNone then none else cur.get key
  | [], cur, _, _, _, hcur =>
    ⟨rfl, cur, rfl, hcur, fun _ => by simp⟩
  | k :: ks, cur, hnd, hvalid, hdom, hcur => by
    simp only [List.nodup_cons] at hnd
    have hk_valid := hvalid k List.mem_cons_self
    have hk_some := hdom k List.mem_cons_self
    cases hng : next.get k with
    | some _ =>
      simp only [diffDels, hng, Option.isNone_some, Bool.false_eq_true, ite_false]
      obtain ⟨hops, mid, hrun, hmidU, hmidGet⟩ := diffDels_run next ks cur hnd.2
        (fun x hx => hvalid x (List.mem_cons_of_mem k hx))
        (fun x hx => hdom x (List.mem_cons_of_mem k hx)) hcur
      refine ⟨hops, mid, hrun, hmidU, ?_⟩
      intro key
      rw [hmidGet key]
      by_cases hkk : key = k
      · subst hkk
        simp [hng, hnd.1]
      · simp [List.mem_cons, hkk]
    | none =>
      simp only [diffDels, hng, Option.isNone_none, ite_true, List.nil_append]
      have hop_ok : opOk { kind := .del, path := joinSlash [k], value := none } = true := by
        rw [joinSlash_singleton]
        simp [opOk, parsePath_validSeg hk_valid]
      have hstep : stepOp cur { kind := .del, path := joinSlash [k], value := none } =
          .ok (removeField cur k) := by
        rw [joinSlash_singleton]
        simp [stepOp, parsePath_validSeg hk_valid, deleteAt, hk_some]
      have hcur'U : (Value.record (removeField cur k)).unique = true :=
        unique_removeField cur k hcur
      have hdom' : ∀ x ∈ ks, ((removeField cur k).get x).isSome = true := by
        intro x hx
        have hne : x ≠ k := fun heq => hnd.1 (heq ▸ hx)
        rw [get_removeField_ne cur hne]
        exact hdom x (List.mem_cons_of_mem k hx)
      obtain ⟨hops, mid, hrun, hmidU, hmidGet⟩ := diffDels_run next ks (removeField cur k) hnd.2
        (fun x hx => hvalid x (List.mem_cons_of_mem k hx)) hdom' hcur'U
      refine ⟨by simp only [opsOk, List.all_cons, hop_ok, Bool.true_and]; exact hops,
        mid, by simp [runOps, hstep, hrun], hmidU, ?_⟩
      intro key
      rw [hmidGet key]
      simp only [unique_record, Bool.and_eq_true] at hcur
      by_cases hkk : key = k
      · subst hkk
        rw [get_removeField_self cur key hcur.1]
        simp [List.mem_cons, hng]
      · rw [get_removeField_ne cur hkk]
        simp [List.mem_cons, hkk]

private theorem diffFields_roundtrip : ∀ (n : Nat) (old next : Fields),
    fieldsSize next ≤ n →
    (Value.record old).unique = true →
    (Value.record next).unique = true →
    topSegsOk old = true →
    topSegsOk next = true →
    opsOk (diffFields [] old next) = true ∧
    ∃ res, runOps old (diffFields [] old next) = .ok res ∧
      (Value.record res).unique = true ∧
      Value.eqv (.record res) (.record next) = true
  | 0, _, next, hle, _, _, _, _ => by
    have hpos : 0 < fieldsSize next := by cases next <;> simp [fieldsSize]
    omega
  | n + 1, old, next, hle, holdU, hnextU, htopO, htopN => by
    have holdU' := holdU
    have hnextU' := hnextU
    simp only [unique_record, Bool.and_eq_true] at holdU' hnextU'
    have hdels := diffDels_run next (sortedKeys old) old (nodup_sortedKeys old holdU'.1)
      (fun k hk => topSegsOk_get old k htopO ((mem_sortedKeys old k).mp hk))
      (fun k hk => (mem_sortedKeys old k).mp hk) holdU
    obtain ⟨hdelsOk, mid, hdelsRun, hmidU, hmidGet⟩ := hdels
    have hsets_aux : ∀ (ks : List String) (cur : Fields),
        ks.Nodup →
        (∀ k ∈ ks, (next.get k).isSome = true) →
        (Value.record cur).unique = true →
        (∀ key, key ∉ ks → (cur.get key).isSome = (next.get key).isSome ∧
          ∀ v w, cur.get key = some v → next.get key = some w → Value.eqv v w = true) →
        (∀ key ∈ ks, cur.get key = old.get key) →
        opsOk (diffSets [] old next ks) = true ∧
        ∃ res, runOps cur (diffSets [] old next ks) = .ok res ∧
          (Value.record res).unique = true ∧
          (∀ key, (res.get key).isSome = (next.get key).isSome ∧
            ∀ v w, res.get key = some v → next.get key = some w → Value.eqv v w = true) := by
      intro ks
      induction ks with
      | nil =>
        intro cur _ _ hcurU hdone _
        rw [diffSets]
        exact ⟨rfl, cur, rfl, hcurU, fun key => hdone key (by simp)⟩
      | cons k ks' ih =>
        intro cur hnd hksSome hcurU hdone htodo
        simp only [List.nodup_cons] at hnd
        have hkSome : (next.get k).isSome = true := hksSome k List.mem_cons_self
        have hkValid : validSeg k = true := topSegsOk_get next k htopN hkSome
        have hkCurOld : cur.get k = old.get k := htodo k List.mem_cons_self
        cases hng : next.get k with
        | none => simp [hng] at hkSome
        | some nv =>
          rw [diffSets_cons_some [] old next k ks' nv hng]
          have hnvU : nv.unique = true := fields_unique_get next k nv hnextU'.2 hng
          have hhead : ∃ (cur' : Fields),
              opsOk (diffSetEntry [] k (old.get k) nv) = true ∧
              runOps cur (diffSetEntry [] k (old.get k) nv) = .ok cur' ∧
              (Value.record cur').unique = true ∧
              (∃ cv, cur'.get k = some cv ∧ Value.eqv cv nv = true) ∧
              (∀ key, key ≠ k → cur'.get key = cur.get key) := by
            have hset_case :
                opsOk [{ kind := .set, path := joinSlash ([] ++ [k]), value := some nv }] = true ∧
                ∃ cur', runOps cur [{ kind := .set, path := joinSlash ([] ++ [k]), value := some nv }]
                    = .ok cur' ∧
                  (Value.record cur').unique = true ∧
                  (∃ cv, cur'.get k = some cv ∧ Value.eqv cv nv = true) ∧
                  (∀ key, key ≠ k → cur'.get key = cur.get key) := by
              simp only [List.nil_append, joinSlash_singleton]
              have hopOk : opOk { kind := .set, path := k, value := some nv } = true := by
                simp [opOk, parsePath_validSeg hkValid, hnvU]
              refine ⟨by simp [opsOk, hopOk], ?_⟩
              cases hcg : cur.get k with
              | none =>
                refine ⟨appendField cur k nv, ?_, unique_appendField cur k nv hcurU hcg hnvU,
                  ⟨nv, get_appendField_self cur k nv hcg, value_eqv_refl nv hnvU⟩,
                  fun key hne => get_appendField_ne cur nv hne⟩
                simp [runOps, stepOp, parsePath_validSeg hkValid, setAt, hcg]
              | some _ =>
                refine ⟨replaceField cur k nv, ?_, unique_replaceField cur k nv hcurU hnvU,
                  ⟨nv, get_replaceField_self cur k nv (by simp [hcg]), value_eqv_refl nv hnvU⟩,
                  fun key hne => get_replaceField_ne cur nv hne⟩
                simp [runOps, stepOp, parsePath_validSeg hkValid, setAt, hcg]
            unfold diffSetEntry
            cases hog : old.get k with
            | none =>
              obtain ⟨h1, cur', h2, h3, h4, h5⟩ := hset_case
              exact ⟨cur', h1, h2, h3, h4, h5⟩
            | some ov =>
              cases heq_ov : Value.eqv ov nv with
              | true =>
                simp only [heq_ov, ite_true]
                exact ⟨cur, rfl, rfl, hcurU, ⟨ov, by rw [hkCurOld, hog], heq_ov⟩, fun _ _ => rfl⟩
              | false =>
                simp only [heq_ov, Bool.false_eq_true, ite_false]
                cases ov with
                | record osub =>
                  cases nv with
                  | record nsub =>
                    cases hcond : ((decide (osub.length > 0) || decide (nsub.length > 0))
                        && topSegsOk osub && topSegsOk nsub) with
                    | true =>
                      simp only [hcond, ite_true]
                      simp only [Bool.and_eq_true] at hcond
                      have hosubU : (Value.record osub).unique = true :=
                        fields_unique_get old k (.record osub) holdU'.2 hog
                      have hlt : fieldsSize nsub < fieldsSize next :=
                        fieldsSize_get_record_lt next k nsub hng
                      obtain ⟨hsubOps, osub', hsubRun, hosub'U, hosub'Eqv⟩ :=
                        diffFields_roundtrip n osub nsub (by omega) hosubU hnvU
                          hcond.1.2 hcond.2
                      have hcur_k : cur.get k = some (.record osub) := by rw [hkCurOld, hog]
                      have hpref_eq : diffFields ([] ++ [k]) osub nsub =
                          prefixOps k (diffFields [] osub nsub) := by
                        simp only [List.nil_append]
                        exact diffFields_singleton_prefix k osub nsub
                      refine ⟨replaceField cur k (.record osub'), ?_, ?_, ?_, ?_, ?_⟩
                      · rw [hpref_eq]
                        exact prefixOps_opsOk _ hkValid hsubOps
                      · rw [hpref_eq]
                        exact runOps_prefix _ cur osub osub' hkValid hcur_k hsubOps hsubRun
                      · exact unique_replaceField cur k (.record osub') hcurU hosub'U
                      · exact ⟨.record osub',
                          get_replaceField_self cur k (.record osub') (by simp [hcur_k]), hosub'Eqv⟩
                      · intro key hne
                        exact get_replaceField_ne cur (.record osub') hne
                    | false =>
                      simp only [hcond, Bool.false_eq_true, ite_false]
                      obtain ⟨h1, cur', h2, h3, h4, h5⟩ := hset_case
                      exact ⟨cur', h1, h2, h3, h4, h5⟩
                  | _ =>
                    obtain ⟨h1, cur', h2, h3, h4, h5⟩ := hset_case
                    exact ⟨cur', h1, h2, h3, h4, h5⟩
                | _ =>
                  obtain ⟨h1, cur', h2, h3, h4, h5⟩ := hset_case
                  exact ⟨cur', h1, h2, h3, h4, h5⟩
          obtain ⟨cur', hheadOk, hheadRun, hcur'U, ⟨cv, hcvGet, hcvEqv⟩, hcur'Other⟩ := hhead
          have hdone' : ∀ key, key ∉ ks' →
              (cur'.get key).isSome = (next.get key).isSome ∧
              ∀ v w, cur'.get key = some v → next.get key = some w → Value.eqv v w = true := by
            intro key hnot
            by_cases hkk : key = k
            · subst hkk
              refine ⟨by simp [hcvGet, hng], ?_⟩
              intro v w hv hw
              rw [hcvGet] at hv
              rw [hng] at hw
              cases hv; cases hw
              exact hcvEqv
            · rw [hcur'Other key hkk]
              exact hdone key (by simp [List.mem_cons, hkk, hnot])
          have htodo' : ∀ key ∈ ks', cur'.get key = old.get key := by
            intro key hmem
            have hne : key ≠ k := fun heq => hnd.1 (heq ▸ hmem)
            rw [hcur'Other key hne]
            exact htodo key (List.mem_cons_of_mem k hmem)
          obtain ⟨htailOk, res, htailRun, hresU, hresProps⟩ := ih cur' hnd.2
            (fun x hx => hksSome x (List.mem_cons_of_mem k hx)) hcur'U hdone' htodo'
          refine ⟨?_, res, ?_, hresU, hresProps⟩
          · simp only [opsOk, List.all_append, Bool.and_eq_true]
            exact ⟨hheadOk, htailOk⟩
          · rw [runOps_append, hheadRun]
            exact htailRun
    obtain ⟨hsetsOk, res, hsetsRun, hresU, hresProps⟩ := hsets_aux (sortedKeys next) mid
      (nodup_sortedKeys next hnextU'.1)
      (fun k hk => (mem_sortedKeys next k).mp hk) hmidU
      (fun key hnot => by
        have hng : next.get key = none := by
          cases hng' : next.get key with
          | none => rfl
          | some _ => exact absurd ((mem_sortedKeys next key).mpr (by simp [hng'])) hnot
        have hmg : mid.get key = none := by
          rw [hmidGet key]
          cases hmem : decide (key ∈ sortedKeys old) with
          | true =>
            have hmem' : key ∈ sortedKeys old := of_decide_eq_true hmem
            simp [hmem', hng]
          | false =>
            have hmem' : key ∉ sortedKeys old := of_decide_eq_false hmem
            have hog : old.get key = none := by
              cases hog' : old.get key with
              | none => rfl
              | some _ => exact absurd ((mem_sortedKeys old key).mpr (by simp [hog'])) hmem'
            simp [hmem', hog]
        refine ⟨by simp [hmg, hng], fun _ _ h _ => by rw [hmg] at h; cases h⟩)
      (fun key hmem => by
        have hns : (next.get key).isSome = true := (mem_sortedKeys next key).mp hmem
        have hnn : (next.get key).isNone = false := by
          cases hng' : next.get key with
          | none => simp [hng'] at hns
          | some _ => rfl
        rw [hmidGet key]
        simp [hnn])
    rw [diffFields]
    refine ⟨?_, res, ?_, hresU, ?_⟩
    · simp only [opsOk, List.all_append, Bool.and_eq_true]
      exact ⟨hdelsOk, hsetsOk⟩
    · rw [runOps_append, hdelsRun]
      exact hsetsRun
    · simp only [unique_record, Bool.and_eq_true] at hresU
      exact record_eqv_of_get hresU.1 hnextU'.1
        (fun k => (hresProps k).1) (fun k v w hv hw => (hresProps k).2 v w hv hw)

theorem diff_apply_roundtrip (old next : Fields) (ops : List Op)
    (h : diff old next = .ok ops) :
    ∃ res, apply old ops = .ok res ∧
      (Value.record res).unique = true ∧
      Value.eqv (.record res) (.record next) = true := by
  unfold diff at h
  split at h
  · cases h
  · rename_i hpre
    cases h
    simp only [Bool.or_eq_true, Bool.not_eq_true', not_or] at hpre
    have holdU : (Value.record old).unique = true := by simpa using hpre.1.1.1
    have hnextU : (Value.record next).unique = true := by simpa using hpre.1.1.2
    have htopO : topSegsOk old = true := by simpa using hpre.1.2
    have htopN : topSegsOk next = true := by simpa using hpre.2
    obtain ⟨hopsOk, res, hrun, hresU, hresEqv⟩ :=
      diffFields_roundtrip (fieldsSize next) old next (Nat.le_refl _) holdU hnextU htopO htopN
    exact ⟨res, by simp [apply, holdU, hopsOk, hrun], hresU, hresEqv⟩

private theorem opsToItems_length (idx : Nat) : ∀ (ops : List Op),
    (opsToItems idx ops).length = ops.length
  | [] => rfl
  | _ :: rest => by simp [opsToItems, opsToItems_length (idx + 1) rest]

private theorem opsToItems_getElem_value (idx : Nat) : ∀ (ops : List Op)
    (i : Nat) (hi : i < ops.length) (hit : i < (opsToItems idx ops).length),
    ((opsToItems idx ops)[i]'hit).value = opToFields (ops[i]'hi)
  | [], _, hi, _ => by cases hi
  | op :: rest, 0, _, _ => rfl
  | op :: rest, i + 1, hi, hit =>
    opsToItems_getElem_value (idx + 1) rest i
      (Nat.lt_of_succ_lt_succ hi) (Nat.lt_of_succ_lt_succ hit)

private theorem decodeEntry_opToFields (e : Nayutar.OrderedSet.Entry) (op : Op)
    (hok : opOk op = true) (hval : e.value = opToFields op) :
    decodeEntry e = some op := by
  unfold opOk at hok
  simp only [Bool.and_eq_true] at hok
  cases hp : parsePath op.path with
  | none =>
    rw [hp] at hok
    simp at hok
  | some segs =>
    unfold decodeEntry
    rw [hval]
    cases op with
    | mk kind path val =>
      simp only at hok hp ⊢
      cases kind <;> cases val <;> simp [opToFields, opKindName, parseOpKind, Fields.get,
        Fields.length, hp] at hok ⊢

private theorem decodeEntries_eq_some : ∀ (es : List Nayutar.OrderedSet.Entry) (ops : List Op),
    es.length = ops.length →
    (∀ (i : Nat) (he : i < es.length) (ho : i < ops.length),
      decodeEntry (es[i]'he) = some (ops[i]'ho)) →
    decodeEntries es = some ops
  | [], [], _, _ => rfl
  | [], _ :: _, hlen, _ => by simp at hlen
  | _ :: _, [], hlen, _ => by simp at hlen
  | e :: es, op :: ops, hlen, hat => by
    have h0 : decodeEntry e = some op :=
      hat 0 (Nat.zero_lt_succ _) (Nat.zero_lt_succ _)
    have htail := decodeEntries_eq_some es ops (by simp at hlen; exact hlen)
      (fun i he ho => hat (i + 1) (Nat.succ_lt_succ he) (Nat.succ_lt_succ ho))
    simp [decodeEntries, h0, htail]

theorem toRecord_fromRecord_roundtrip (ops : List Op) (rec : Fields)
    (h : toRecord ops = .ok rec) :
    (Value.record rec).unique = true ∧ fromRecord rec = .ok ops := by
  unfold toRecord at h
  split at h
  · cases h
  · rename_i hops
    have hops' : opsOk ops = true := by simpa using hops
    cases hfrom : Nayutar.OrderedSet.fromItems (opsToItems 0 ops) with
    | error err =>
      rw [hfrom] at h
      cases h
    | ok set =>
      rw [hfrom] at h
      cases h
      obtain ⟨hsetU, es, hentries, hes_len, hes_at⟩ :=
        Nayutar.OrderedSet.fromItems_entries_roundtrip (opsToItems 0 ops) set hfrom
      have hrecU : (Value.record (.cons patchField (.record set) .nil)).unique = true := by
        simp [unique_record, Fields.keysDistinct, Fields.get, Fields.unique, hsetU]
      have hlen : es.length = ops.length := by
        rw [hes_len, opsToItems_length 0 ops]
      have hdec : decodeEntries es = some ops := by
        refine decodeEntries_eq_some es ops hlen ?_
        intro i he ho
        have hit : i < (opsToItems 0 ops).length := by rw [opsToItems_length]; exact ho
        have hval : (es[i]'he).value = opToFields (ops[i]'ho) := by
          rw [(hes_at i hit he).2.2, opsToItems_getElem_value 0 ops i ho hit]
        have hopOk : opOk (ops[i]'ho) = true := List.all_eq_true.mp hops' (ops[i]'ho) (List.getElem_mem ho)
        exact decodeEntry_opToFields (es[i]'he) (ops[i]'ho) hopOk hval
      refine ⟨hrecU, ?_⟩
      simp [fromRecord, hrecU, Fields.length, Fields.get, hentries, hdec]

theorem patch_is_stored (old : Fields) (rev : Int) (ops : List Op) (next : Value)
    (h : patch old rev ops = .ok next) :
    Nayutar.Envelope.stored next = true ∧
    next.at ["meta", "uid"] = (Value.record old).at ["meta", "uid"] ∧
    next.at ["status"] = (Value.record old).at ["status"] := by
  unfold patch at h
  cases ha : applyEntrance .spec old rev ops with
  | error e =>
    rw [ha] at h
    cases h
  | ok cand =>
    rw [ha] at h
    simp only [bind, Except.bind] at h
    cases hu : Nayutar.Envelope.update (.record old) rev (.record cand) with
    | error e =>
      rw [hu] at h
      cases h
    | ok d =>
      rw [hu] at h
      cases h
      have hstored := Nayutar.Envelope.update_is_stored (.record old) rev (.record cand) next hu
      have hkeep := Nayutar.Envelope.update_keeps_uid_and_status (.record old) rev (.record cand) next hu
      exact ⟨hstored, hkeep.1, hkeep.2⟩

theorem patch_bumps_generation_iff_spec_changes (old : Fields) (rev : Int) (ops : List Op)
    (next : Value) (h : patch old rev ops = .ok next) :
    ∃ g, (Value.record old).at ["meta", "generation"] = some (.int g) ∧
      next.at ["meta", "generation"] =
        some (.int (if optEqv ((Value.record old).at ["spec"]) (next.at ["spec"]) then g else g + 1)) := by
  unfold patch at h
  cases ha : applyEntrance .spec old rev ops with
  | error e =>
    rw [ha] at h
    cases h
  | ok cand =>
    rw [ha] at h
    simp only [bind, Except.bind] at h
    cases hu : Nayutar.Envelope.update (.record old) rev (.record cand) with
    | error e =>
      rw [hu] at h
      cases h
    | ok d =>
      rw [hu] at h
      cases h
      obtain ⟨g, hgen, hdgen, hdspec⟩ :=
        Nayutar.Envelope.update_bumps_generation_iff_spec_changes (.record old) rev (.record cand) next hu
      rw [← hdspec] at hdgen
      exact ⟨g, hgen, hdgen⟩

theorem patchStatus_is_stored (old : Fields) (rev : Int) (ops : List Op) (now : String)
    (next : Value) (h : patchStatus old rev ops now = .ok next) :
    Nayutar.Envelope.stored next = true ∧
    next.at ["meta"] = (Value.record old).at ["meta"] ∧
    next.at ["spec"] = (Value.record old).at ["spec"] := by
  unfold patchStatus at h
  split at h
  · cases h
  · cases ha : applyEntrance .status old rev ops with
    | error e =>
      rw [ha] at h
      cases h
    | ok cand =>
      rw [ha] at h
      simp only [bind, Except.bind] at h
      cases hu : Nayutar.Envelope.updateStatus (.record old) rev (.record cand) now with
      | error e =>
        rw [hu] at h
        cases h
      | ok d =>
        rw [hu] at h
        cases h
        have hstored :=
          Nayutar.Envelope.update_status_is_stored (.record old) rev (.record cand) now next hu
        have hkeep :=
          Nayutar.Envelope.update_status_keeps_meta_and_spec (.record old) rev (.record cand) now next hu
        exact ⟨hstored, hkeep.1, hkeep.2⟩

end Nayutar.Patch
