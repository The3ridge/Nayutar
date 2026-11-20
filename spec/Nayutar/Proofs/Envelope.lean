import Nayutar.Envelope

namespace Nayutar.Envelope

private theorem at_nil (v : Value) : v.at [] = some v := by
  cases v <;> rfl

private theorem at_record_cons (fs : Fields) (k : String) (p : List String) :
    (Value.record fs).at (k :: p) = (fs.get k).bind (fun v => v.at p) := by
  simp only [Value.at]
  cases fs.get k <;> rfl

private theorem at_record_one (fs : Fields) (k : String) :
    (Value.record fs).at [k] = fs.get k := by
  rw [at_record_cons]
  cases fs.get k <;> simp [at_nil]

private theorem get_optField_ne {k key : String} (v : Option Value) (rest : Fields) (h : k ≠ key) :
    (optField k v rest).get key = rest.get key := by
  cases v <;> simp [optField, Fields.get, h]

private theorem get_optField_self {k : String} (v : Option Value) {rest : Fields}
    (h : rest.get k = none) : (optField k v rest).get k = v := by
  cases v <;> simp [optField, Fields.get, h]

private theorem get_canonMeta_group (m : Fields) (u g : Value) :
    (canonMeta m u g).get "group" = m.get "group" := by
  unfold canonMeta
  apply get_optField_self
  rw [get_optField_ne _ _ (by decide), get_optField_ne _ _ (by decide),
    get_optField_ne _ _ (by decide)]
  simp [Fields.get]

private theorem get_canonMeta_kind (m : Fields) (u g : Value) :
    (canonMeta m u g).get "kind" = m.get "kind" := by
  unfold canonMeta
  rw [get_optField_ne _ _ (by decide)]
  apply get_optField_self
  rw [get_optField_ne _ _ (by decide), get_optField_ne _ _ (by decide)]
  simp [Fields.get]

private theorem get_canonMeta_name (m : Fields) (u g : Value) :
    (canonMeta m u g).get "name" = m.get "name" := by
  unfold canonMeta
  rw [get_optField_ne _ _ (by decide), get_optField_ne _ _ (by decide)]
  apply get_optField_self
  rw [get_optField_ne _ _ (by decide)]
  simp [Fields.get]

private theorem get_canonMeta_labels (m : Fields) (u g : Value) :
    (canonMeta m u g).get "labels" = m.get "labels" := by
  unfold canonMeta
  rw [get_optField_ne _ _ (by decide), get_optField_ne _ _ (by decide),
    get_optField_ne _ _ (by decide)]
  apply get_optField_self
  simp [Fields.get]

private theorem get_canonMeta_uid (m : Fields) (u g : Value) :
    (canonMeta m u g).get "uid" = some u := by
  unfold canonMeta
  rw [get_optField_ne _ _ (by decide), get_optField_ne _ _ (by decide),
    get_optField_ne _ _ (by decide), get_optField_ne _ _ (by decide)]
  simp [Fields.get]

private theorem get_canonMeta_generation (m : Fields) (u g : Value) :
    (canonMeta m u g).get "generation" = some g := by
  unfold canonMeta
  rw [get_optField_ne _ _ (by decide), get_optField_ne _ _ (by decide),
    get_optField_ne _ _ (by decide), get_optField_ne _ _ (by decide)]
  simp [Fields.get]

private theorem get_canonMeta_revision (m : Fields) (u g : Value) :
    (canonMeta m u g).get "revision" = none := by
  unfold canonMeta
  rw [get_optField_ne _ _ (by decide), get_optField_ne _ _ (by decide),
    get_optField_ne _ _ (by decide), get_optField_ne _ _ (by decide)]
  simp [Fields.get]

private theorem get_mkDoc_meta (m : Fields) (spec status : Option Value) :
    (Fields.cons "meta" (.record m) (optField "spec" spec (optField "status" status .nil))).get "meta"
      = some (.record m) := by
  simp [Fields.get]

private theorem get_mkDoc_spec (m : Fields) (spec status : Option Value) :
    (Fields.cons "meta" (.record m) (optField "spec" spec (optField "status" status .nil))).get "spec"
      = spec := by
  have h : (optField "status" status .nil).get "spec" = none := by
    rw [get_optField_ne _ _ (by decide)]
    rfl
  simp [Fields.get, get_optField_self spec h]

private theorem get_mkDoc_status (m : Fields) (spec status : Option Value) :
    (Fields.cons "meta" (.record m) (optField "spec" spec (optField "status" status .nil))).get "status"
      = status := by
  have h : Fields.nil.get "status" = none := rfl
  simp [Fields.get, get_optField_ne spec _ (show "spec" ≠ "status" by decide),
    get_optField_self status h]

private theorem at_mkDoc_meta (m : Fields) (spec status : Option Value) (p : List String) :
    (mkDoc m spec status).at ("meta" :: p) = (Value.record m).at p := by
  unfold mkDoc
  rw [at_record_cons, get_mkDoc_meta]
  rfl

private theorem at_mkDoc_spec (m : Fields) (spec status : Option Value) :
    (mkDoc m spec status).at ["spec"] = spec := by
  unfold mkDoc
  rw [at_record_one, get_mkDoc_spec]

private theorem at_mkDoc_status (m : Fields) (spec status : Option Value) :
    (mkDoc m spec status).at ["status"] = status := by
  unfold mkDoc
  rw [at_record_one, get_mkDoc_status]

private theorem at_mkDoc_status_cons (m : Fields) (spec status : Option Value) (k : String)
    (p : List String) :
    (mkDoc m spec status).at ("status" :: k :: p) = status.bind (fun v => v.at (k :: p)) := by
  unfold mkDoc
  rw [at_record_cons, get_mkDoc_status]

/-- What a successful `create` establishes. -/
private theorem create_ok {cand : Value} {uid : String} {d : Value} (h : create cand uid = .ok d) :
    ∃ top m, cand = .record top ∧ top.get "meta" = some (.record m) ∧ isUID uid = true
      ∧ shape cand = true ∧ top.get "status" = none
      ∧ d = mkDoc (canonMeta m (.str uid) (.int 1)) (top.get "spec") none := by
  unfold create at h
  split at h
  · cases h
  · rename_i huid
    split at h
    · cases h
    · rename_i hshape
      split at h
      · rename_i top
        split at h
        · rename_i m hm
          split at h
          · cases h
          · rename_i hforbid
            refine ⟨top, m, rfl, hm, by simpa using huid, by simpa using hshape, ?_, ?_⟩
            · simp only [Bool.or_eq_true, not_or] at hforbid
              simpa using hforbid.2
            · cases h
              rfl
        · cases h
      · cases h

/-- What a successful `update` establishes. -/
private theorem update_ok {old : Value} {rev : Int} {cand d : Value}
    (h : update old rev cand = .ok d) :
    ∃ ot ct om cm u g, old = .record ot ∧ cand = .record ct
      ∧ ot.get "meta" = some (.record om) ∧ ct.get "meta" = some (.record cm)
      ∧ stored old = true ∧ shape cand = true
      ∧ staleRevision cm rev = false
      ∧ differs (ct.get "status") (ot.get "status") = false
      ∧ om.get "uid" = some u ∧ om.get "generation" = some (.int g)
      ∧ d = mkDoc
          (canonMeta cm u (.int (if optEqv (ot.get "spec") (ct.get "spec") then g else g + 1)))
          (ct.get "spec") (ot.get "status") := by
  unfold update at h
  split at h
  · cases h
  · rename_i hpre
    split at h
    · cases h
    · rename_i hshape
      split at h
      · rename_i ot ct
        split at h
        · rename_i om cm hom hcm
          split at h
          · cases h
          · split at h
            · cases h
            · rename_i hstale
              split at h
              · cases h
              · rename_i hdiff
                split at h
                · rename_i u g hu hg
                  simp only [Bool.or_eq_true, not_or, Bool.not_eq_true'] at hpre
                  simp only [Bool.or_eq_true, not_or] at hdiff
                  refine ⟨ot, ct, om, cm, u, g, rfl, rfl, hom, hcm, ?_, by simpa using hshape,
                    by simpa using hstale, by simpa using hdiff.2, hu, hg, ?_⟩
                  · simpa using hpre.1
                  · cases h
                    rfl
                · cases h
        · cases h
      · cases h

/-- What a successful `updateStatus` establishes. -/
private theorem updateStatus_ok {old : Value} {rev : Int} {cand : Value} {now : String} {d : Value}
    (h : updateStatus old rev cand now = .ok d) :
    ∃ ot ct om cm g, old = .record ot ∧ cand = .record ct
      ∧ ot.get "meta" = some (.record om) ∧ ct.get "meta" = some (.record cm)
      ∧ stored old = true ∧ isTimestamp now = true ∧ shape cand = true
      ∧ staleRevision cm rev = false
      ∧ sinceForbidden (condsOf old) ((condsOf cand).getD .nil) = false
      ∧ om.get "generation" = some (.int g)
      ∧ observedBad (old.at ["status", "observedGeneration"])
          (cand.at ["status", "observedGeneration"]) g = false
      ∧ d = mkDoc om (ot.get "spec") (stampStatusValue (condsOf old) now (ct.get "status")) := by
  unfold updateStatus at h
  split at h
  · cases h
  · rename_i hpre
    split at h
    · cases h
    · rename_i hshape
      split at h
      · rename_i ot ct
        split at h
        · rename_i om cm hom hcm
          split at h
          · cases h
          · split at h
            · cases h
            · rename_i hstale
              split at h
              · cases h
              · rename_i hdiff
                split at h
                · rename_i g hg
                  split at h
                  · cases h
                  · rename_i hobs
                    simp only [Bool.or_eq_true, not_or, Bool.not_eq_true'] at hpre
                    simp only [Bool.or_eq_true, not_or] at hdiff
                    refine ⟨ot, ct, om, cm, g, rfl, rfl, hom, hcm, ?_, ?_, by simpa using hshape,
                      by simpa using hstale, by simpa using hdiff.2, hg, by simpa using hobs, ?_⟩
                    · simpa using hpre.1.1
                    · simpa using hpre.2
                    · cases h
                      rfl
                · cases h
        · cases h
      · cases h

private theorem unique_record (fs : Fields) :
    (Value.record fs).unique = (fs.keysDistinct && Fields.unique fs) := by
  simp only [Value.unique]

private theorem unique_get : ∀ (fs : Fields) (k : String) (v : Value),
    Fields.unique fs = true → fs.get k = some v → v.unique = true
  | .nil, _, _, _, hg => by simp [Fields.get] at hg
  | .cons k' v' rest, k, v, h, hg => by
    simp only [Fields.unique, Bool.and_eq_true] at h
    simp only [Fields.get] at hg
    split at hg
    · cases hg
      exact h.1
    · exact unique_get rest k v h.2 hg

private theorem topOk_get : ∀ (fs : Fields) (k : String) (v : Value),
    topOk fs = true → fs.get k = some v → v.isRecord = true
  | .nil, _, _, _, hg => by simp [Fields.get] at hg
  | .cons k' v' rest, k, v, h, hg => by
    simp only [topOk, Bool.and_eq_true] at h
    simp only [Fields.get] at hg
    split at hg
    · cases hg
      exact h.1.2
    · exact topOk_get rest k v h.2 hg

private theorem isRecord_elim {v : Value} (h : v.isRecord = true) : ∃ fs, v = .record fs := by
  cases v <;> simp [Value.isRecord] at h
  exact ⟨_, rfl⟩

private theorem record_unique_get {fs : Fields} {k : String} {inner : Fields}
    (h : Fields.unique fs = true) (hg : fs.get k = some (.record inner)) :
    inner.keysDistinct = true ∧ Fields.unique inner = true := by
  have := unique_get fs k _ h hg
  simpa [unique_record] using this

private theorem shape_record {top : Fields} (h : shape (.record top) = true) :
    top.keysDistinct = true ∧ Fields.unique top = true ∧ topOk top = true
      ∧ (∃ m, top.get "meta" = some (.record m) ∧ metaOk m = true)
      ∧ (∀ st, top.get "status" = some (.record st) → statusOk st = true) := by
  simp only [shape, unique_record, Bool.and_eq_true] at h
  obtain ⟨⟨hd, hu⟩, ⟨htop, hmeta⟩, hstatus⟩ := h
  refine ⟨hd, hu, htop, ?_, ?_⟩
  · split at hmeta
    · rename_i m hm
      exact ⟨m, hm, hmeta⟩
    · cases hmeta
  · intro st hst
    rw [hst] at hstatus
    exact hstatus

private theorem stored_record {top : Fields} (h : stored (.record top) = true) :
    shape (.record top) = true ∧ ∃ m, top.get "meta" = some (.record m) ∧ metaOk m = true
      ∧ (m.get "uid").isSome = true ∧ (m.get "generation").isSome = true
      ∧ m.get "revision" = none
      ∧ (∀ st, top.get "status" = some (.record st) →
          (∀ cs, st.get "condition" = some (.record cs) → condsHaveSince cs = true)
          ∧ observedWithin (st.get "observedGeneration") (m.get "generation") = true) := by
  simp only [stored, Bool.and_eq_true] at h
  obtain ⟨⟨⟨⟨⟨hshape, huid⟩, hgen⟩, hrev⟩, hconds⟩, hobs⟩ := h
  obtain ⟨-, -, -, ⟨m, hm, hmeta⟩, -⟩ := shape_record hshape
  have atm : ∀ k, (Value.record top).at ["meta", k] = m.get k := by
    intro k
    rw [at_record_cons, hm]
    exact at_record_one m k
  rw [atm] at huid hgen hrev hobs
  refine ⟨hshape, m, hm, hmeta, huid, hgen, by simpa using hrev, ?_⟩
  intro st hst
  have ats : ∀ k, (Value.record top).at ["status", k] = st.get k := by
    intro k
    rw [at_record_cons, hst]
    exact at_record_one st k
  rw [ats] at hconds hobs
  refine ⟨?_, hobs⟩
  intro cs hcs
  rw [hcs] at hconds
  exact hconds

private theorem unique_optField {k : String} {v : Option Value} {rest : Fields}
    (hv : ∀ x, v = some x → x.unique = true) (hrest : Fields.unique rest = true) :
    Fields.unique (optField k v rest) = true := by
  cases v with
  | none => exact hrest
  | some x => simp [optField, Fields.unique, hv x rfl, hrest]

private theorem canonMeta_keysDistinct (m : Fields) (u g : Value) :
    (canonMeta m u g).keysDistinct = true := by
  unfold canonMeta
  cases m.get "group" <;> cases m.get "kind" <;> cases m.get "name" <;> cases m.get "labels" <;>
    simp [optField, Fields.keysDistinct, Fields.get]

private theorem canonMeta_unique {m : Fields} {u g : Value} (hm : Fields.unique m = true)
    (hu : u.unique = true) (hg : g.unique = true) : Fields.unique (canonMeta m u g) = true := by
  unfold canonMeta
  refine unique_optField (fun x hx => unique_get m _ x hm hx)
    (unique_optField (fun x hx => unique_get m _ x hm hx)
      (unique_optField (fun x hx => unique_get m _ x hm hx)
        (unique_optField (fun x hx => unique_get m _ x hm hx) ?_)))
  simp [Fields.unique, hu, hg]

private theorem canonMeta_keysOk (m : Fields) (u g : Value) :
    metaKeysOk (canonMeta m u g) = true := by
  unfold canonMeta
  cases m.get "group" <;> cases m.get "kind" <;> cases m.get "name" <;> cases m.get "labels" <;>
    simp [optField, metaKeysOk]

private theorem metaOk_canonMeta {cm : Fields} {u g : Value} (hcm : metaOk cm = true)
    (hu : optString (some u) isUID = true) (hg : optPositive (some g) = true) :
    metaOk (canonMeta cm u g) = true := by
  simp only [metaOk, Bool.and_eq_true] at hcm ⊢
  obtain ⟨⟨⟨⟨⟨-, hid⟩, hlabels⟩, -⟩, -⟩, -⟩ := hcm
  refine ⟨⟨⟨⟨⟨canonMeta_keysOk cm u g, ?_⟩, ?_⟩, ?_⟩, ?_⟩, ?_⟩
  · simpa only [identityOk, get_canonMeta_kind, get_canonMeta_name, get_canonMeta_group] using hid
  · rw [get_canonMeta_labels]
    exact hlabels
  · rw [get_canonMeta_uid]
    exact hu
  · rw [get_canonMeta_revision]
    rfl
  · rw [get_canonMeta_generation]
    exact hg

private theorem observedWithin_mono {o : Option Value} {g g' : Int}
    (h : observedWithin o (some (.int g)) = true) (hle : g ≤ g') :
    observedWithin o (some (.int g')) = true := by
  cases o with
  | none => rfl
  | some v =>
    cases v <;> simp only [observedWithin] at h ⊢
    rename_i n
    simp only [decide_eq_true_eq] at h ⊢
    omega

/-- The assembled document is a stored document when its three parts are. -/
private theorem stored_mkDoc {m : Fields} {spec status : Option Value}
    (hmD : m.keysDistinct = true) (hmU : Fields.unique m = true) (hm : metaOk m = true)
    (huid : (m.get "uid").isSome = true) (hgen : (m.get "generation").isSome = true)
    (hrev : m.get "revision" = none)
    (hspec : ∀ v, spec = some v → v.isRecord = true ∧ v.unique = true)
    (hstatus : ∀ v, status = some v → ∃ st, v = .record st ∧ st.keysDistinct = true
      ∧ Fields.unique st = true ∧ statusOk st = true
      ∧ (∀ cs, st.get "condition" = some (.record cs) → condsHaveSince cs = true)
      ∧ observedWithin (st.get "observedGeneration") (m.get "generation") = true) :
    stored (mkDoc m spec status) = true := by
  have hshape : shape (mkDoc m spec status) = true := by
    unfold mkDoc
    simp only [shape, get_mkDoc_meta, get_mkDoc_status, unique_record, Bool.and_eq_true]
    cases spec with
    | none =>
      cases status with
      | none =>
        simp [optField, Fields.keysDistinct, Fields.get, Fields.unique, unique_record, topOk,
          Value.isRecord, hmD, hmU, hm]
      | some sv =>
        obtain ⟨st, rfl, hsD, hsU, hsOk, -, -⟩ := hstatus _ rfl
        simp [optField, Fields.keysDistinct, Fields.get, Fields.unique, unique_record, topOk,
          Value.isRecord, hmD, hmU, hm, hsD, hsU, hsOk]
    | some pv =>
      obtain ⟨hpR, hpU⟩ := hspec _ rfl
      obtain ⟨pf, rfl⟩ := isRecord_elim hpR
      cases status with
      | none =>
        simp [optField, Fields.keysDistinct, Fields.get, Fields.unique, unique_record, topOk,
          Value.isRecord, hmD, hmU, hm, hpU]
      | some sv =>
        obtain ⟨st, rfl, hsD, hsU, hsOk, -, -⟩ := hstatus _ rfl
        simp [optField, Fields.keysDistinct, Fields.get, Fields.unique, unique_record, topOk,
          Value.isRecord, hmD, hmU, hm, hpU, hsD, hsU, hsOk]
  simp only [stored, hshape, at_mkDoc_meta, at_record_one, at_mkDoc_status_cons, Bool.true_and,
    Bool.and_eq_true]
  refine ⟨⟨⟨⟨huid, hgen⟩, by simp [hrev]⟩, ?_⟩, ?_⟩
  · cases status with
    | none => rfl
    | some sv =>
      obtain ⟨st, rfl, -, -, -, hsince, -⟩ := hstatus _ rfl
      simp only [Option.bind_some, at_record_one]
      split
      · rename_i cs hcs
        exact hsince cs hcs
      · rfl
  · cases status with
    | none => rfl
    | some sv =>
      obtain ⟨st, rfl, -, -, -, -, hobs⟩ := hstatus _ rfl
      simpa only [Option.bind_some, at_record_one] using hobs

theorem create_is_stored (cand : Value) (uid : String) (d : Value)
    (h : create cand uid = .ok d) : stored d = true := by
  obtain ⟨top, m, rfl, hm, huid, hshape, -, rfl⟩ := create_ok h
  obtain ⟨-, hU, htop, ⟨m', hm', hmeta⟩, -⟩ := shape_record hshape
  rw [hm] at hm'
  cases hm'
  have hmU := record_unique_get hU hm
  apply stored_mkDoc
  · exact canonMeta_keysDistinct _ _ _
  · exact canonMeta_unique hmU.2 (by simp [Value.unique]) (by simp [Value.unique])
  · exact metaOk_canonMeta hmeta (by simpa [optString] using huid) (by simp [optPositive])
  · rw [get_canonMeta_uid]
    rfl
  · rw [get_canonMeta_generation]
    rfl
  · exact get_canonMeta_revision _ _ _
  · intro v hv
    exact ⟨topOk_get top "spec" v htop hv, unique_get top "spec" v hU hv⟩
  · intro v hv
    cases hv

theorem update_is_stored (old : Value) (rev : Int) (cand d : Value)
    (h : update old rev cand = .ok d) : stored d = true := by
  obtain ⟨ot, ct, om, cm, u, g, rfl, rfl, hom, hcm, hstored, hshape, -, -, hu, hg, rfl⟩ :=
    update_ok h
  obtain ⟨hoshape, om', hom', hometa, -, -, -, hostatus⟩ := stored_record hstored
  rw [hom] at hom'
  cases hom'
  obtain ⟨-, hoU, hotop, -, hostatusOk⟩ := shape_record hoshape
  obtain ⟨-, hcU, hctop, ⟨cm', hcm', hcmeta⟩, -⟩ := shape_record hshape
  rw [hcm] at hcm'
  cases hcm'
  have homU := record_unique_get hoU hom
  have hcmU := record_unique_get hcU hcm
  have hpos : (1 : Int) ≤ g := by
    simp only [metaOk, Bool.and_eq_true] at hometa
    have := hometa.2
    rw [hg] at this
    simpa [optPositive] using this
  have huidOk : optString (some u) isUID = true := by
    simp only [metaOk, Bool.and_eq_true] at hometa
    have := hometa.1.1.2
    rw [hu] at this
    exact this
  apply stored_mkDoc
  · exact canonMeta_keysDistinct _ _ _
  · exact canonMeta_unique hcmU.2 (unique_get om "uid" u homU.2 hu) (by simp [Value.unique])
  · refine metaOk_canonMeta hcmeta huidOk ?_
    simp only [optPositive, decide_eq_true_eq]
    split <;> omega
  · rw [get_canonMeta_uid]
    rfl
  · rw [get_canonMeta_generation]
    rfl
  · exact get_canonMeta_revision _ _ _
  · intro v hv
    exact ⟨topOk_get ct "spec" v hctop hv, unique_get ct "spec" v hcU hv⟩
  · intro v hv
    obtain ⟨st, rfl⟩ := isRecord_elim (topOk_get ot "status" v hotop hv)
    have hstU := record_unique_get hoU hv
    obtain ⟨hsince, hobs⟩ := hostatus st hv
    refine ⟨st, rfl, hstU.1, hstU.2, hostatusOk st hv, hsince, ?_⟩
    rw [get_canonMeta_generation]
    rw [hg] at hobs
    refine observedWithin_mono hobs ?_
    split <;> omega

private theorem at_two {top : Fields} {k1 k2 : String} {x : Value}
    (h : (Value.record top).at [k1, k2] = some x) :
    ∃ st, top.get k1 = some (.record st) ∧ st.get k2 = some x := by
  rw [at_record_cons] at h
  cases hg : top.get k1 with
  | none => simp [hg] at h
  | some v =>
    rw [hg] at h
    simp only [Option.bind_some] at h
    cases v <;> first
      | (simp [Value.at] at h; done)
      | exact ⟨_, rfl, by rwa [at_record_one] at h⟩

private theorem statusOk_conds {st cs : Fields} (h : statusOk st = true)
    (hc : st.get "condition" = some (.record cs)) : condsOk cs = true := by
  simp only [statusOk, Bool.and_eq_true] at h
  have := h.2
  rw [hc] at this
  exact this

private theorem condsOk_get : ∀ (cs : Fields) (t : String) (c : Value),
    condsOk cs = true → cs.get t = some c → condOk c = true
  | .nil, _, _, _, hg => by simp [Fields.get] at hg
  | .cons t' c' rest, t, c, h, hg => by
    simp only [condsOk, Bool.and_eq_true] at h
    simp only [Fields.get] at hg
    split at hg
    · cases hg
      exact h.1.2
    · exact condsOk_get rest t c h.2 hg

private theorem condsHaveSince_get : ∀ (cs : Fields) (t : String) (c : Value),
    condsHaveSince cs = true → cs.get t = some c → (c.at ["since"]).isSome = true
  | .nil, _, _, _, hg => by simp [Fields.get] at hg
  | .cons t' c' rest, t, c, h, hg => by
    simp only [condsHaveSince, Bool.and_eq_true] at h
    simp only [Fields.get] at hg
    split at hg
    · cases hg
      exact h.1
    · exact condsHaveSince_get rest t c h.2 hg

private theorem condOk_elim {c : Value} (h : condOk c = true) :
    ∃ cf n r, c = .record cf ∧ cf.get "state" = some (.enum n)
      ∧ stateOk (some (.enum n)) = true ∧ cf.get "reason" = some (.str r) ∧ isReason r = true
      ∧ optString (cf.get "since") isTimestamp = true := by
  cases c <;> try (simp [condOk] at h; done)
  rename_i cf
  simp only [condOk, Bool.and_eq_true] at h
  obtain ⟨⟨⟨-, hs⟩, hr⟩, hsince⟩ := h
  have hstate : ∃ n, cf.get "state" = some (.enum n) := by
    cases hst : cf.get "state" with
    | none =>
      rw [hst] at hs
      simp [stateOk] at hs
    | some v =>
      rw [hst] at hs
      cases v <;> first
        | (simp [stateOk] at hs; done)
        | exact ⟨_, rfl⟩
  have hreason : ∃ r, cf.get "reason" = some (.str r) := by
    cases hre : cf.get "reason" with
    | none =>
      rw [hre] at hr
      simp [reasonOk] at hr
    | some v =>
      rw [hre] at hr
      cases v <;> first
        | (simp [reasonOk] at hr; done)
        | exact ⟨_, rfl⟩
  obtain ⟨n, hn⟩ := hstate
  obtain ⟨r, hre⟩ := hreason
  rw [hn] at hs
  rw [hre] at hr
  exact ⟨cf, n, r, rfl, hn, hs, hre, by simpa [reasonOk] using hr, hsince⟩

private theorem sinceFor_str {oc : Option Fields} {now t : String} {c : Value}
    (hoc : ∀ cs, oc = some cs → condsOk cs = true) (hnow : isTimestamp now = true) :
    ∃ s, sinceFor oc now t c = .str s ∧ isTimestamp s = true := by
  unfold sinceFor
  cases hold : oldCond oc t with
  | none => exact ⟨now, rfl, hnow⟩
  | some ocv =>
    simp only
    split
    · have hcond : condOk ocv = true := by
        cases oc with
        | none => simp [oldCond] at hold
        | some cs => exact condsOk_get cs t ocv (hoc cs rfl) (by simpa [oldCond] using hold)
      obtain ⟨cf, n, r, rfl, -, -, -, -, hsince⟩ := condOk_elim hcond
      rw [at_record_one]
      cases hs : cf.get "since" with
      | none => exact ⟨now, rfl, hnow⟩
      | some v =>
        rw [hs] at hsince
        cases v <;> first
          | (simp [optString] at hsince; done)
          | exact ⟨_, rfl, by simpa [optString] using hsince⟩
    · exact ⟨now, rfl, hnow⟩

private theorem stampCond_eq {oc : Option Fields} {now t : String} {c : Value}
    (hc : condOk c = true) (hoc : ∀ cs, oc = some cs → condsOk cs = true)
    (hnow : isTimestamp now = true) :
    ∃ n r s, stampCond oc now t c
        = .record (.cons "state" (.enum n) (.cons "reason" (.str r) (.cons "since" (.str s) .nil)))
      ∧ stateOk (some (.enum n)) = true ∧ isReason r = true ∧ isTimestamp s = true := by
  obtain ⟨cf, n, r, rfl, hst, hstOk, hr, hrOk, -⟩ := condOk_elim hc
  obtain ⟨s, hs, hsOk⟩ := sinceFor_str (c := .record cf) (t := t) hoc hnow
  refine ⟨n, r, s, ?_, hstOk, hrOk, hsOk⟩
  simp [stampCond, at_record_one, hst, hr, hs, optField]

private theorem get_stampConds : ∀ (cs : Fields) (oc : Option Fields) (now t : String),
    (stampConds oc now cs).get t = (cs.get t).map (stampCond oc now t)
  | .nil, _, _, _ => rfl
  | .cons t' c rest, oc, now, t => by
    simp only [stampConds, Fields.get]
    split
    · rename_i h
      subst h
      rfl
    · exact get_stampConds rest oc now t

private theorem keysDistinct_stampConds : ∀ (cs : Fields) (oc : Option Fields) (now : String),
    cs.keysDistinct = true → (stampConds oc now cs).keysDistinct = true
  | .nil, _, _, _ => rfl
  | .cons t c rest, oc, now, h => by
    simp only [Fields.keysDistinct, Bool.and_eq_true] at h
    simp only [stampConds, Fields.keysDistinct, Bool.and_eq_true]
    refine ⟨?_, keysDistinct_stampConds rest oc now h.2⟩
    rw [get_stampConds]
    cases hg : rest.get t with
    | none => rfl
    | some v =>
      have := h.1
      rw [hg] at this
      simp at this

private theorem stampConds_ok : ∀ (cs : Fields) (oc : Option Fields) (now : String),
    condsOk cs = true → (∀ ocs, oc = some ocs → condsOk ocs = true) → isTimestamp now = true →
    condsOk (stampConds oc now cs) = true ∧ Fields.unique (stampConds oc now cs) = true
      ∧ condsHaveSince (stampConds oc now cs) = true
  | .nil, _, _, _, _, _ => ⟨rfl, rfl, rfl⟩
  | .cons t c rest, oc, now, h, hoc, hnow => by
    simp only [condsOk, Bool.and_eq_true] at h
    obtain ⟨⟨ht, hc⟩, hrest⟩ := h
    obtain ⟨n, r, s, heq, hn, hr, hs⟩ := stampCond_eq (t := t) hc hoc hnow
    obtain ⟨ih1, ih2, ih3⟩ := stampConds_ok rest oc now hrest hoc hnow
    simp only [stampConds, heq]
    refine ⟨?_, ?_, ?_⟩
    · simp [condsOk, condOk, condKeysOk, Fields.get, ht, hn, hr, hs, optString, reasonOk, ih1]
    · simp [Fields.unique, Fields.keysDistinct, Fields.get, Value.unique, ih2]
    · simp [condsHaveSince, at_record_one, Fields.get, ih3]

private theorem get_stampStatus_ne : ∀ (st : Fields) (oc : Option Fields) (now k : String),
    k ≠ "condition" → (stampStatus oc now st).get k = st.get k
  | .nil, _, _, _, _ => rfl
  | .cons k' v rest, oc, now, k, hk => by
    simp only [stampStatus]
    split
    · rename_i hc
      subst hc
      split <;> simp [Fields.get, Ne.symm hk]
    · simp only [Fields.get]
      split
      · rfl
      · exact get_stampStatus_ne rest oc now k hk

private theorem get_stampStatus_condition : ∀ (st : Fields) (oc : Option Fields) (now : String),
    (stampStatus oc now st).get "condition" =
      (match st.get "condition" with
        | some (.record cs) => some (.record (stampConds oc now cs))
        | other => other)
  | .nil, _, _ => rfl
  | .cons k v rest, oc, now => by
    simp only [stampStatus]
    split
    · rename_i hc
      subst hc
      cases v <;> simp [Fields.get]
    · rename_i hc
      simp only [Fields.get, hc, ite_false]
      exact get_stampStatus_condition rest oc now

private theorem isNone_get_stampStatus (st : Fields) (oc : Option Fields) (now k : String) :
    ((stampStatus oc now st).get k).isNone = (st.get k).isNone := by
  by_cases hk : k = "condition"
  · subst hk
    rw [get_stampStatus_condition]
    cases h : st.get "condition" with
    | none => rfl
    | some v => cases v <;> rfl
  · rw [get_stampStatus_ne st oc now k hk]

private theorem keysDistinct_stampStatus : ∀ (st : Fields) (oc : Option Fields) (now : String),
    st.keysDistinct = true → (stampStatus oc now st).keysDistinct = true
  | .nil, _, _, _ => rfl
  | .cons k v rest, oc, now, h => by
    simp only [Fields.keysDistinct, Bool.and_eq_true] at h
    simp only [stampStatus]
    split
    · split <;> simp [Fields.keysDistinct, h.1, h.2]
    · simp only [Fields.keysDistinct, Bool.and_eq_true]
      exact ⟨by rw [isNone_get_stampStatus]; exact h.1, keysDistinct_stampStatus rest oc now h.2⟩

private theorem unique_stampStatus : ∀ (st : Fields) (oc : Option Fields) (now : String),
    Fields.unique st = true →
    (∀ cs, st.get "condition" = some (.record cs) → condsOk cs = true) →
    (∀ ocs, oc = some ocs → condsOk ocs = true) → isTimestamp now = true →
    Fields.unique (stampStatus oc now st) = true
  | .nil, _, _, _, _, _, _ => rfl
  | .cons k v rest, oc, now, h, hcs, hoc, hnow => by
    simp only [Fields.unique, Bool.and_eq_true] at h
    simp only [stampStatus]
    split
    · rename_i hk
      subst hk
      split
      · rename_i cs
        have hcok := hcs cs (by simp [Fields.get])
        have hvU : cs.keysDistinct = true ∧ Fields.unique cs = true := by
          simpa [unique_record] using h.1
        simp [Fields.unique, unique_record, keysDistinct_stampConds cs oc now hvU.1,
          (stampConds_ok cs oc now hcok hoc hnow).2.1, h.2]
      · simp [Fields.unique, h.1, h.2]
    · rename_i hk
      simp only [Fields.unique, Bool.and_eq_true]
      refine ⟨h.1, unique_stampStatus rest oc now h.2 ?_ hoc hnow⟩
      intro cs hcs'
      exact hcs cs (by simp [Fields.get, hk, hcs'])

private theorem stampStatus_ok {st : Fields} {oc : Option Fields} {now : String}
    (hst : statusOk st = true) (hoc : ∀ ocs, oc = some ocs → condsOk ocs = true)
    (hnow : isTimestamp now = true) :
    statusOk (stampStatus oc now st) = true
      ∧ (∀ cs, (stampStatus oc now st).get "condition" = some (.record cs) →
          condsHaveSince cs = true) := by
  simp only [statusOk, Bool.and_eq_true] at hst ⊢
  rw [get_stampStatus_ne st oc now _ (by decide), get_stampStatus_condition]
  obtain ⟨hobs, hcond⟩ := hst
  cases hc : st.get "condition" with
  | none => simp [hobs]
  | some v =>
    rw [hc] at hcond
    cases v <;> try (simp at hcond; done)
    rename_i cs
    obtain ⟨h1, -, h3⟩ := stampConds_ok cs oc now hcond hoc hnow
    refine ⟨⟨hobs, h1⟩, ?_⟩
    intro cs' hcs'
    simp only [Option.some.injEq, Value.record.injEq] at hcs'
    subst hcs'
    exact h3

private theorem condsOf_record {top cs : Fields} (h : condsOf (.record top) = some cs) :
    ∃ st, top.get "status" = some (.record st) ∧ st.get "condition" = some (.record cs) := by
  unfold condsOf at h
  split at h
  · rename_i cs' heq
    cases h
    exact at_two heq
  · cases h

theorem update_status_is_stored (old : Value) (rev : Int) (cand : Value) (now : String)
    (d : Value) (h : updateStatus old rev cand now = .ok d) : stored d = true := by
  obtain ⟨ot, ct, om, cm, g, rfl, rfl, hom, hcm, hstored, hnow, hshape, -, -, hg, hobs, rfl⟩ :=
    updateStatus_ok h
  obtain ⟨hoshape, om', hom', hometa, houid, hogen, horev, -⟩ := stored_record hstored
  rw [hom] at hom'
  cases hom'
  obtain ⟨-, hoU, hotop, -, hostatusOk⟩ := shape_record hoshape
  obtain ⟨-, hcU, hctop, -, hcstatusOk⟩ := shape_record hshape
  have homU := record_unique_get hoU hom
  have hoc : ∀ cs, condsOf (.record ot) = some cs → condsOk cs = true := by
    intro cs hcs
    obtain ⟨st, hst, hcond⟩ := condsOf_record hcs
    exact statusOk_conds (hostatusOk st hst) hcond
  apply stored_mkDoc homU.1 homU.2 hometa houid hogen horev
  · intro v hv
    exact ⟨topOk_get ot "spec" v hotop hv, unique_get ot "spec" v hoU hv⟩
  · intro v hv
    cases hcs : ct.get "status" with
    | none =>
      rw [hcs] at hv
      simp [stampStatusValue] at hv
    | some sv =>
      obtain ⟨st, rfl⟩ := isRecord_elim (topOk_get ct "status" sv hctop hcs)
      rw [hcs] at hv
      simp only [stampStatusValue, Option.some.injEq] at hv
      subst hv
      have hstU := record_unique_get hcU hcs
      have hstOk := hcstatusOk st hcs
      obtain ⟨hok, hsince⟩ := stampStatus_ok hstOk hoc hnow
      refine ⟨_, rfl, keysDistinct_stampStatus st _ _ hstU.1,
        unique_stampStatus st _ _ hstU.2 (fun cs hc => statusOk_conds hstOk hc) hoc hnow, hok,
        hsince, ?_⟩
      rw [get_stampStatus_ne st _ _ _ (by decide), hg]
      have hcandAt : (Value.record ct).at ["status", "observedGeneration"]
          = st.get "observedGeneration" := by
        rw [at_record_cons, hcs]
        exact at_record_one st _
      rw [hcandAt] at hobs
      cases hn : st.get "observedGeneration" with
      | none => rfl
      | some nv =>
        rw [hn] at hobs
        cases nv <;> try rfl
        rename_i n
        simp only [observedBad, Bool.or_eq_false_iff, decide_eq_false_iff_not] at hobs
        simp only [observedWithin, decide_eq_true_eq]
        omega

theorem update_status_observed_is_monotone (old : Value) (rev : Int) (cand : Value)
    (now : String) (d : Value) (h : updateStatus old rev cand now = .ok d) (m : Int)
    (hm : old.at ["status", "observedGeneration"] = some (.int m)) :
    ∃ n, d.at ["status", "observedGeneration"] = some (.int n) ∧ m ≤ n := by
  obtain ⟨ot, ct, om, cm, g, rfl, rfl, -, -, -, -, -, -, -, -, hobs, rfl⟩ := updateStatus_ok h
  rw [hm] at hobs
  cases hn : (Value.record ct).at ["status", "observedGeneration"] with
  | none =>
    rw [hn] at hobs
    simp [observedBad] at hobs
  | some nv =>
    rw [hn] at hobs
    obtain ⟨st, hst, hget⟩ := at_two hn
    cases nv <;> try (simp [observedBad] at hobs; done)
    rename_i n
    simp only [observedBad, Bool.or_eq_false_iff, decide_eq_false_iff_not] at hobs
    refine ⟨n, ?_, by omega⟩
    rw [at_mkDoc_status_cons, hst]
    simp only [stampStatusValue, Option.bind_some]
    rw [at_record_one, get_stampStatus_ne st _ _ _ (by decide)]
    exact hget

private theorem at_append (v : Value) (p q : List String) :
    v.at (p ++ q) = (v.at p).bind (fun x => x.at q) := by
  induction p generalizing v with
  | nil => simp [at_nil]
  | cons k rest ih =>
    cases v <;> try (simp [Value.at]; done)
    rename_i fs
    rw [List.cons_append, at_record_cons, at_record_cons]
    cases fs.get k with
    | none => rfl
    | some x => simpa using ih x

theorem update_status_since_follows_state (old : Value) (rev : Int) (cand : Value)
    (now : String) (d : Value) (h : updateStatus old rev cand now = .ok d) (t : String)
    (c : Value) (hc : d.at ["status", "condition", t] = some c) :
    c.at ["since"] =
      (match old.at ["status", "condition", t] with
        | some oc =>
          if optEqv (oc.at ["state"]) (c.at ["state"]) then oc.at ["since"] else some (.str now)
        | none => some (.str now)) := by
  obtain ⟨ot, ct, om, cm, g, rfl, rfl, -, -, hstored, -, -, -, -, -, -, rfl⟩ := updateStatus_ok h
  obtain ⟨-, om', -, -, -, -, -, hostatus⟩ := stored_record hstored
  -- the written condition comes from a candidate condition
  have hd : ∃ st cs c0, ct.get "status" = some (.record st)
      ∧ st.get "condition" = some (.record cs) ∧ cs.get t = some c0
      ∧ c = stampCond (condsOf (.record ot)) now t c0 := by
    rw [at_mkDoc_status_cons] at hc
    cases hcs : ct.get "status" with
    | none =>
      rw [hcs] at hc
      simp [stampStatusValue] at hc
    | some sv =>
      rw [hcs] at hc
      cases sv <;> try (simp [stampStatusValue, Value.at] at hc; done)
      rename_i st
      simp only [stampStatusValue, Option.bind_some] at hc
      rw [at_record_cons, get_stampStatus_condition] at hc
      cases hcond : st.get "condition" with
      | none =>
        rw [hcond] at hc
        simp at hc
      | some cv =>
        rw [hcond] at hc
        cases cv <;> try (simp [Value.at] at hc; done)
        rename_i cs
        simp only [Option.bind_some] at hc
        rw [at_record_one, get_stampConds] at hc
        cases hc0 : cs.get t with
        | none =>
          rw [hc0] at hc
          simp at hc
        | some c0 =>
          rw [hc0] at hc
          simp only [Option.map_some, Option.some.injEq] at hc
          exact ⟨st, cs, c0, rfl, hcond, hc0, hc.symm⟩
  obtain ⟨st, cs, c0, -, -, -, rfl⟩ := hd
  have hstate : (stampCond (condsOf (.record ot)) now t c0).at ["state"] = c0.at ["state"] := by
    unfold stampCond
    rw [at_record_one]
    apply get_optField_self
    rw [get_optField_ne _ _ (by decide)]
    simp [Fields.get]
  have hsince : (stampCond (condsOf (.record ot)) now t c0).at ["since"]
      = some (sinceFor (condsOf (.record ot)) now t c0) := by
    unfold stampCond
    rw [at_record_one, get_optField_ne _ _ (by decide), get_optField_ne _ _ (by decide)]
    simp [Fields.get]
  rw [hsince, hstate]
  -- the stored condition of the same type, seen through both lookups
  have hold : (Value.record ot).at ["status", "condition", t]
      = oldCond (condsOf (.record ot)) t := by
    have := at_append (Value.record ot) ["status", "condition"] [t]
    simp only [List.cons_append, List.nil_append] at this
    rw [this]
    unfold condsOf
    cases hat : (Value.record ot).at ["status", "condition"] with
    | none => rfl
    | some v =>
      cases v <;> try (simp [oldCond, Value.at]; done)
      rename_i ocs
      simp [oldCond, at_record_one]
  rw [hold]
  unfold sinceFor
  cases hoc : oldCond (condsOf (.record ot)) t with
  | none => rfl
  | some ocv =>
    simp only
    split
    · -- the stored condition has a since
      have hsome : (ocv.at ["since"]).isSome = true := by
        cases hco : condsOf (.record ot) with
        | none =>
          rw [hco] at hoc
          simp [oldCond] at hoc
        | some ocs =>
          rw [hco] at hoc
          obtain ⟨ost, host, hcond⟩ := condsOf_record hco
          exact condsHaveSince_get ocs t ocv ((hostatus ost host).1 ocs hcond)
            (by simpa [oldCond] using hoc)
      cases hs : ocv.at ["since"] with
      | none =>
        rw [hs] at hsome
        cases hsome
      | some s => rfl
    · rfl

theorem create_starts_at_generation_one (cand : Value) (uid : String) (d : Value)
    (h : create cand uid = .ok d) :
    d.at ["meta", "generation"] = some (.int 1) ∧ d.at ["meta", "uid"] = some (.str uid) := by
  obtain ⟨top, m, -, -, -, -, -, rfl⟩ := create_ok h
  rw [at_mkDoc_meta, at_mkDoc_meta, at_record_one, at_record_one, get_canonMeta_generation,
    get_canonMeta_uid]
  exact ⟨rfl, rfl⟩

theorem update_keeps_uid_and_status (old : Value) (rev : Int) (cand d : Value)
    (h : update old rev cand = .ok d) :
    d.at ["meta", "uid"] = old.at ["meta", "uid"] ∧ d.at ["status"] = old.at ["status"] := by
  obtain ⟨ot, ct, om, cm, u, g, rfl, rfl, hom, -, -, -, -, -, hu, -, rfl⟩ := update_ok h
  rw [at_mkDoc_meta, at_mkDoc_status, at_record_one, get_canonMeta_uid, at_record_cons, hom,
    at_record_one]
  simp [at_record_one, hu]

theorem update_bumps_generation_iff_spec_changes (old : Value) (rev : Int) (cand d : Value)
    (h : update old rev cand = .ok d) :
    ∃ g, old.at ["meta", "generation"] = some (.int g) ∧
      d.at ["meta", "generation"]
        = some (.int (if optEqv (old.at ["spec"]) (cand.at ["spec"]) then g else g + 1))
      ∧ d.at ["spec"] = cand.at ["spec"] := by
  obtain ⟨ot, ct, om, cm, u, g, rfl, rfl, hom, -, -, -, -, -, -, hg, rfl⟩ := update_ok h
  refine ⟨g, ?_, ?_, ?_⟩
  · rw [at_record_cons, hom]
    simp [at_record_one, hg]
  · rw [at_mkDoc_meta, at_record_one, get_canonMeta_generation, at_record_one, at_record_one]
  · rw [at_mkDoc_spec, at_record_one]

theorem update_status_keeps_meta_and_spec (old : Value) (rev : Int) (cand : Value) (now : String)
    (d : Value) (h : updateStatus old rev cand now = .ok d) :
    d.at ["meta"] = old.at ["meta"] ∧ d.at ["spec"] = old.at ["spec"] := by
  obtain ⟨ot, ct, om, cm, g, rfl, rfl, hom, -, -, -, -, -, -, -, -, rfl⟩ := updateStatus_ok h
  rw [at_mkDoc_meta, at_mkDoc_spec, at_nil, at_record_one, at_record_one, hom]
  exact ⟨rfl, rfl⟩

theorem stale_revision_is_rejected (old : Value) (rev : Int) (cand : Value) (now : String)
    (r : Int) (hr : cand.at ["meta", "revision"] = some (.int r)) (hne : r ≠ rev) (d : Value) :
    update old rev cand ≠ .ok d ∧ updateStatus old rev cand now ≠ .ok d := by
  have key : ∀ ct cm, cand = .record ct → ct.get "meta" = some (.record cm) →
      staleRevision cm rev = true := by
    intro ct cm hc hm
    subst hc
    rw [at_record_cons, hm] at hr
    have hr' : cm.get "revision" = some (.int r) := by simpa [at_record_one] using hr
    simp [staleRevision, hr', hne]
  constructor
  · intro h
    obtain ⟨ot, ct, om, cm, u, g, -, hc, -, hcm, -, -, hstale, -⟩ := update_ok h
    rw [key ct cm hc hcm] at hstale
    cases hstale
  · intro h
    obtain ⟨ot, ct, om, cm, g, -, hc, -, hcm, -, -, -, hstale, -⟩ := updateStatus_ok h
    rw [key ct cm hc hcm] at hstale
    cases hstale

end Nayutar.Envelope
