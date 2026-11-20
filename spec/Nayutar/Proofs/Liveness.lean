import Nayutar.Liveness

namespace Nayutar.Liveness

private theorem assocLookup_mem {α : Type} {key : String} {v : α} {l : List (String × α)}
    (h : assocLookup key l = some v) : (key, v) ∈ l := by
  induction l with
  | nil => simp [assocLookup] at h
  | cons p rest ih =>
    obtain ⟨k, w⟩ := p
    simp only [assocLookup] at h
    split at h
    · rename_i hk
      have hk' : k = key := by simpa using hk
      cases h
      subst hk'
      simp
    · exact List.mem_cons_of_mem _ (ih h)

private theorem mem_assocInsert {α : Type} {key : String} {val : α} {q : String × α}
    {l : List (String × α)} (h : q ∈ assocInsert key val l) : q = (key, val) ∨ q ∈ l := by
  induction l with
  | nil =>
    simp only [assocInsert, List.mem_singleton] at h
    exact Or.inl h
  | cons p rest ih =>
    obtain ⟨k, w⟩ := p
    simp only [assocInsert] at h
    split at h
    · rcases List.mem_cons.mp h with h | h
      · exact Or.inl h
      · exact Or.inr h
    · split at h
      · rcases List.mem_cons.mp h with h | h
        · exact Or.inl h
        · exact Or.inr (List.mem_cons_of_mem _ h)
      · rcases List.mem_cons.mp h with h | h
        · exact Or.inr (by rw [h]; simp)
        · rcases ih h with h | h
          · exact Or.inl h
          · exact Or.inr (List.mem_cons_of_mem _ h)

private theorem mem_assocErase {α : Type} {key : String} {q : String × α} {l : List (String × α)}
    (h : q ∈ assocErase key l) : q ∈ l := by
  induction l with
  | nil => simp [assocErase] at h
  | cons p rest ih =>
    obtain ⟨k, w⟩ := p
    simp only [assocErase] at h
    split at h
    · exact List.mem_cons_of_mem _ h
    · rcases List.mem_cons.mp h with h | h
      · rw [h]; simp
      · exact List.mem_cons_of_mem _ (ih h)

private theorem assocLookup_assocInsert_self {α : Type} (key : String) (val : α)
    (l : List (String × α)) : assocLookup key (assocInsert key val l) = some val := by
  induction l with
  | nil => simp [assocInsert, assocLookup]
  | cons p rest ih =>
    obtain ⟨k, w⟩ := p
    simp only [assocInsert]
    split
    · simp [assocLookup]
    · split
      · simp [assocLookup]
      · rename_i hk
        simp only [assocLookup, hk]
        simpa using ih

private theorem insertAsc_length (x : Nat) (l : List Nat) : (insertAsc x l).length = l.length + 1 := by
  induction l with
  | nil => simp [insertAsc]
  | cons e es ih =>
    simp only [insertAsc]
    split
    · simp
    · simp [ih]

private theorem mem_insertAsc {x e : Nat} {l : List Nat} (h : e ∈ insertAsc x l) : e = x ∨ e ∈ l := by
  induction l with
  | nil =>
    simp only [insertAsc, List.mem_singleton] at h
    exact Or.inl h
  | cons y ys ih =>
    simp only [insertAsc] at h
    split at h
    · rcases List.mem_cons.mp h with h | h
      · exact Or.inl h
      · exact Or.inr h
    · rcases List.mem_cons.mp h with h | h
      · exact Or.inr (by rw [h]; simp)
      · rcases ih h with h | h
        · exact Or.inl h
        · exact Or.inr (List.mem_cons_of_mem _ h)

private theorem tickFold_chunks (cfg : Config) (s : State) (now : Nat) (susp : Bool)
    (l : List (String × Track)) (acc : TickAcc) (chunk : List Action)
    (h : chunk ∈ (l.foldl (tickStep cfg s now susp) acc).2.2) :
    chunk ∈ acc.2.2 ∨ ∃ p seq, p ∈ l ∧ chunk = (tickNode cfg s now susp seq p.1 p.2).2.2 := by
  induction l generalizing acc with
  | nil => exact Or.inl h
  | cons p rest ih =>
    simp only [List.foldl_cons] at h
    rcases ih _ h with h | ⟨q, seq, hq, hc⟩
    · simp only [tickStep] at h
      rcases List.mem_cons.mp h with h | h
      · exact Or.inr ⟨p, acc.2.1, by simp, h⟩
      · exact Or.inl h
    · exact Or.inr ⟨q, seq, List.mem_cons_of_mem _ hq, hc⟩

private theorem tickFold_tracks (cfg : Config) (s : State) (now : Nat) (susp : Bool)
    (l : List (String × Track)) (acc : TickAcc) (q : String × Track)
    (h : q ∈ (l.foldl (tickStep cfg s now susp) acc).1) :
    q ∈ acc.1 ∨ ∃ p seq, p ∈ l ∧ q = (p.1, (tickNode cfg s now susp seq p.1 p.2).1) := by
  induction l generalizing acc with
  | nil => exact Or.inl h
  | cons p rest ih =>
    simp only [List.foldl_cons] at h
    rcases ih _ h with h | ⟨r, seq, hr, hc⟩
    · simp only [tickStep] at h
      rcases List.mem_cons.mp h with h | h
      · exact Or.inr ⟨p, acc.2.1, by simp, h⟩
      · exact Or.inl h
    · exact Or.inr ⟨r, seq, List.mem_cons_of_mem _ hr, hc⟩

private theorem tick_action_source {cfg : Config} {s : State} {now : Nat} {a : Action}
    (ha : a ∈ (tick cfg s now).2) :
    ∃ p seq, p ∈ s.tracks ∧
      a ∈ (tickNode cfg s (natMax now s.now) (selfSuspected cfg s (natMax now s.now)) seq p.1 p.2).2.2 := by
  simp only [tick, List.mem_flatMap, List.mem_reverse] at ha
  obtain ⟨chunk, hchunk, hmem⟩ := ha
  rcases tickFold_chunks _ _ _ _ _ _ _ hchunk with h | ⟨p, seq, hp, hc⟩
  · simp at h
  · exact ⟨p, seq, hp, hc ▸ hmem⟩

private theorem tickRound_keeps {cfg : Config} {s : State} {now : Nat} {susp : Bool} {seq : Nat}
    {node : String} {rec : Record} {tr : Track} :
    (tickRound cfg s now susp seq node rec tr).1.notReadySince = tr.notReadySince := by
  unfold tickRound
  cases tr.round with
  | none => dsimp only; split <;> rfl
  | some rnd => dsimp only; split <;> rfl

private theorem confirms_spec {cfg : Config} {susp : Bool} {node : String} {rec : Record} {rnd : Round}
    (h : confirms cfg susp node rec rnd = true) :
    rec.state = .ready ∧ rec.modRev = rnd.modRev ∧ quorum cfg node ≤ 1 + rnd.suspectVotes ∧
      (1 ≤ (rnd.probers.filter (fun p => p.isNode)).length →
        natMin cfg.minFailedProbes (rnd.probers.filter (fun p => p.isNode)).length ≤
          (rnd.failed.filter (fun p => p.isNode)).length) ∧
      ((rnd.probers.filter (fun p => p.isNode)).length = 0 → susp = false ∧ 1 ≤ rnd.probers.length ∧
        natMin cfg.minFailedProbes rnd.probers.length ≤ rnd.failed.length) := by
  simp only [confirms] at h
  split at h
  · rename_i hn
    simp only [Bool.and_eq_true, beq_iff_eq, decide_eq_true_eq, ge_iff_le] at h
    obtain ⟨⟨⟨h1, h2⟩, h3⟩, h4⟩ := h
    refine ⟨h1, h2, h3, fun _ => h4, fun h0 => ?_⟩
    omega
  · rename_i hn
    split at h
    · cases h
    · rename_i hs
      simp only [Bool.and_eq_true, beq_iff_eq, decide_eq_true_eq, ge_iff_le] at h
      obtain ⟨⟨⟨⟨h1, h2⟩, h3⟩, h4⟩, h5⟩ := h
      refine ⟨h1, h2, h3, fun h0 => ?_, fun _ => ⟨by simpa using hs, h4, h5⟩⟩
      omega

private theorem tickNode_notReady {cfg : Config} {s : State} {now : Nat} {susp : Bool} {seq : Nat}
    {node : String} {tr : Track} {a : Action}
    (ha : a ∈ (tickNode cfg s now susp seq node tr).2.2) (hk : a.kind = .proposeNotReady) :
    ∃ rnd rec, tr.round = some rnd ∧ assocLookup node s.known = some rec ∧ a.node = node ∧
      a.modRev = rnd.modRev ∧ confirms cfg (rnd.doubted || susp) node rec rnd = true := by
  cases hrec : assocLookup node s.known with
  | none => simp [tickNode, hrec] at ha
  | some rec =>
    simp only [tickNode, hrec, List.mem_append] at ha
    rcases ha with ha | ha
    · cases hr : tr.round with
      | none =>
        simp only [tickRound, hr] at ha
        split at ha
        · simp only [List.mem_append, List.mem_map] at ha
          rcases ha with ⟨v, _, rfl⟩ | ⟨p, _, rfl⟩ <;> simp at hk
        · simp at ha
      | some rnd =>
        simp only [tickRound, hr] at ha
        split at ha
        · split at ha
          · rename_i hc
            simp only [List.mem_singleton] at ha
            subst ha
            exact ⟨rnd, rec, rfl, rfl, rfl, rfl, hc⟩
          · simp at ha
        · simp at ha
    · simp only [tickEvict] at ha
      split at ha
      · simp only [List.mem_singleton] at ha
        subst ha
        simp at hk
      · simp at ha

private theorem tickNode_evictable {cfg : Config} {s : State} {now : Nat} {susp : Bool} {seq : Nat}
    {node : String} {tr : Track} {a : Action}
    (ha : a ∈ (tickNode cfg s now susp seq node tr).2.2) (hk : a.kind = .proposeEvictable) :
    ∃ rec, assocLookup node s.known = some rec ∧ a.node = node ∧ a.modRev = rec.modRev ∧
      susp = false ∧ rec.state = .notready ∧ rec.evictable = false ∧
      tr.notReadySince + cfg.evictionGrace ≤ now := by
  cases hrec : assocLookup node s.known with
  | none => simp [tickNode, hrec] at ha
  | some rec =>
    simp only [tickNode, hrec, List.mem_append] at ha
    rcases ha with ha | ha
    · cases hr : tr.round with
      | none =>
        simp only [tickRound, hr] at ha
        split at ha
        · simp only [List.mem_append, List.mem_map] at ha
          rcases ha with ⟨v, _, rfl⟩ | ⟨p, _, rfl⟩ <;> simp at hk
        · simp at ha
      | some rnd =>
        simp only [tickRound, hr] at ha
        split at ha
        · split at ha
          · simp only [List.mem_singleton] at ha
            subst ha
            simp at hk
          · simp at ha
        · simp at ha
    · simp only [tickEvict] at ha
      split at ha
      · rename_i hdue
        simp only [List.mem_singleton] at ha
        subst ha
        simp only [evictionDue, Bool.and_eq_true, Bool.not_eq_true', beq_iff_eq, decide_eq_true_eq,
          ge_iff_le] at hdue
        obtain ⟨⟨⟨⟨⟨h1, h2⟩, h3⟩, h4⟩, _⟩, _⟩ := hdue
        rw [tickRound_keeps] at h4
        exact ⟨rec, rfl, rfl, rfl, h1, h2, h3, h4⟩
      · simp at ha

/-- A NotReady proposal comes only from a round on a record that is Ready at the revision the round
started on, in which the detector and enough other owners suspected the node to make a majority, and
probers reported failures. If the round asked any node, the failures counted are those reported by
nodes. Only a round that asked no node counts failures reported by aggregators, and only while the
detector does not suspect itself and the round is not marked as doubted. A prober that did not answer
is not in `failed`. -/
theorem notready_needs_quorum_and_failed_probes (cfg : Config) (s : State) (now : Nat) (a : Action)
    (ha : a ∈ (tick cfg s now).2) (hk : a.kind = .proposeNotReady) :
    ∃ tr rnd rec, (a.node, tr) ∈ s.tracks ∧ tr.round = some rnd ∧
      assocLookup a.node s.known = some rec ∧ rec.state = .ready ∧ rec.modRev = rnd.modRev ∧
      a.modRev = rnd.modRev ∧ quorum cfg a.node ≤ 1 + rnd.suspectVotes ∧
      (1 ≤ (rnd.probers.filter (fun p => p.isNode)).length →
        natMin cfg.minFailedProbes (rnd.probers.filter (fun p => p.isNode)).length ≤
          (rnd.failed.filter (fun p => p.isNode)).length) ∧
      ((rnd.probers.filter (fun p => p.isNode)).length = 0 →
        rnd.doubted = false ∧ (tick cfg s now).1.suspended = false ∧ 1 ≤ rnd.probers.length ∧
        natMin cfg.minFailedProbes rnd.probers.length ≤ rnd.failed.length) := by
  obtain ⟨p, seq, hp, hmem⟩ := tick_action_source ha
  obtain ⟨rnd, rec, hr, hrec, hnode, hrev, hc⟩ := tickNode_notReady hmem hk
  obtain ⟨h1, h2, h3, h4, h5⟩ := confirms_spec hc
  refine ⟨p.2, rnd, rec, ?_, hr, ?_, h1, h2, hrev, ?_, h4, ?_⟩
  · rw [hnode]; exact hp
  · rw [hnode]; exact hrec
  · rw [hnode]; exact h3
  · intro hs
    obtain ⟨hd, hrest⟩ := h5 hs
    rw [Bool.or_eq_false_iff] at hd
    exact ⟨hd.1, hd.2, hrest⟩

private theorem tickNode_doubted {cfg : Config} {s : State} {now : Nat} {seq : Nat} {node : String}
    {tr : Track} {rnd : Round} {rec : Record} (hk : assocLookup node s.known = some rec)
    (h : (tickNode cfg s now true seq node tr).1.round = some rnd) : rnd.doubted = true := by
  simp only [tickNode, hk] at h
  have hev : ∀ t : Track, (tickEvict cfg now true node rec t).1.round = t.round := by
    intro t
    simp only [tickEvict]
    split <;> rfl
  rw [hev] at h
  cases hr : tr.round with
  | none =>
    simp only [tickRound, hr] at h
    split at h
    · simp only [Option.some.injEq] at h
      subst h
      rfl
    · rw [hr] at h
      cases h
  | some r =>
    simp only [tickRound, hr] at h
    split at h
    · cases h
    · simp only [Option.some.injEq] at h
      subst h
      simp

/-- After a tick in which the detector suspects itself, every round that is open for a node with a
known record is marked as doubted, whether the round was already open or opened in that tick. -/
theorem suspected_tick_marks_rounds (cfg : Config) (s : State) (now : Nat) (node : String)
    (tr : Track) (rnd : Round) (rec : Record)
    (hs : (tick cfg s now).1.suspended = true) (hk : assocLookup node s.known = some rec)
    (hmem : (node, tr) ∈ (tick cfg s now).1.tracks) (hr : tr.round = some rnd) :
    rnd.doubted = true := by
  simp only [tick, List.mem_reverse] at hmem hs
  rcases tickFold_tracks _ _ _ _ _ _ _ hmem with h | ⟨p, seq, hp, hq⟩
  · simp at h
  · cases hq
    rw [hs] at hr
    exact tickNode_doubted hk hr

/-- An eviction proposal comes only for a record that is NotReady and not yet evictable, after the
grace has passed on the detector's own clock since `notReadySince`, and never while the detector
suspects itself. -/
theorem evictable_needs_grace (cfg : Config) (s : State) (now : Nat) (a : Action)
    (ha : a ∈ (tick cfg s now).2) (hk : a.kind = .proposeEvictable) :
    ∃ tr rec, (a.node, tr) ∈ s.tracks ∧ assocLookup a.node s.known = some rec ∧
      rec.state = .notready ∧ rec.evictable = false ∧ a.modRev = rec.modRev ∧
      tr.notReadySince + cfg.evictionGrace ≤ (tick cfg s now).1.now ∧
      (tick cfg s now).1.suspended = false := by
  obtain ⟨p, seq, hp, hmem⟩ := tick_action_source ha
  obtain ⟨rec, hrec, hnode, hrev, hsusp, hstate, hev, hgrace⟩ := tickNode_evictable hmem hk
  refine ⟨p.2, rec, ?_, ?_, hstate, hev, hrev, hgrace, hsusp⟩
  · rw [hnode]; exact hp
  · rw [hnode]; exact hrec

/-- Observing a NotReady record restarts the eviction grace at the detector's current time, unless the
detector already knew the node as NotReady at the same revision. -/
theorem notready_observation_restarts_grace (cfg : Config) (s : State) (now : Nat) (node : String)
    (rec : Record) (tr : Track) (hrec : rec.state = .notready)
    (hprev : ∀ prev, assocLookup node s.known = some prev →
      prev.state ≠ .notready ∨ prev.modRev ≠ rec.modRev)
    (ht : assocLookup node (observed cfg s now node rec).tracks = some tr) :
    tr.notReadySince = (observed cfg s now node rec).now := by
  simp only [observed] at ht ⊢
  cases hl : assocLookup node s.tracks with
  | none =>
    simp only [hl] at ht
    split at ht
    · rw [assocLookup_assocInsert_self] at ht
      cases ht
      rfl
    · rw [hl] at ht
      cases ht
  | some tr0 =>
    simp only [hl] at ht
    rw [assocLookup_assocInsert_self] at ht
    cases ht
    cases hk : assocLookup node s.known with
    | none => simp [observeTrack, hrec]
    | some prev =>
      rcases hprev prev hk with hne | hne
      · simp [observeTrack, hrec, hne]
      · simp [observeTrack, hrec, hne]

/-- An accepted heartbeat ends the node's round. -/
theorem heartbeat_ends_round (cfg : Config) (s : State) (now : Nat) (node : String) (tr : Track)
    (hs : (heartbeat cfg s now node).2.1 = .accepted)
    (ht : assocLookup node (heartbeat cfg s now node).1.tracks = some tr) : tr.round = none := by
  cases hk : assocLookup node s.known with
  | none => simp [heartbeat, hk] at hs
  | some rec =>
    cases ho : owns cfg node with
    | false => simp [heartbeat, hk, ho] at hs
    | true =>
      simp only [heartbeat, hk, ho, Bool.not_true, Bool.false_eq_true, ite_false] at ht
      rw [assocLookup_assocInsert_self] at ht
      cases ht
      rfl

/-- A successful indirect probe from a prober that the node's round asked ends the round, even if
that prober reported a failure before. -/
theorem ok_probe_ends_round (cfg : Config) (s : State) (now : Nat) (fromId : String) (fromNode : Bool)
    (node : String) (roundId : Nat) (tr : Track) (rnd : Round)
    (ht : assocLookup node s.tracks = some tr) (hr : tr.round = some rnd) (hid : rnd.id = roundId)
    (hp : ({ id := fromId, isNode := fromNode } : Prober) ∈ rnd.probers) :
    ∃ tr', assocLookup node (probeResult cfg s now fromId fromNode node roundId true).tracks = some tr' ∧
      tr'.round = none := by
  refine ⟨bump cfg { tr with round := none } (natMax now s.now), ?_, rfl⟩
  simp [probeResult, ht, hr, hid, hp, assocLookup_assocInsert_self]

/-- A vote from another owner that vouches the node is alive ends the round, whatever it says about
suspicion and even if that owner voted before. -/
theorem alive_vote_ends_round (cfg : Config) (s : State) (now : Nat) (fromId node : String)
    (roundId : Nat) (suspect : Bool) (tr : Track) (rnd : Round)
    (ht : assocLookup node s.tracks = some tr) (hr : tr.round = some rnd) (hid : rnd.id = roundId)
    (hv : fromId ∈ rnd.voters) :
    ∃ tr', assocLookup node (voteReceived cfg s now fromId node roundId suspect true).tracks = some tr' ∧
      tr'.round = none := by
  refine ⟨bump cfg { tr with round := none } (natMax now s.now), ?_, rfl⟩
  simp [voteReceived, ht, hr, hid, hv, assocLookup_assocInsert_self]

/-- What a round has collected so far, relative to the node it is about. -/
private def RoundOk (cfg : Config) (node : String) (rnd : Round) : Prop :=
  rnd.voters = voters cfg node ∧ rnd.voted.Nodup ∧ (∀ v ∈ rnd.voted, v ∈ rnd.voters) ∧
    rnd.suspectVotes ≤ rnd.voted.length ∧ rnd.failed.Nodup ∧ (∀ p ∈ rnd.failed, p ∈ rnd.probers)

private def Inv (cfg : Config) (s : State) : Prop :=
  ∀ node tr rnd, (node, tr) ∈ s.tracks → tr.round = some rnd → RoundOk cfg node rnd

private theorem observeRound_some {rec : Record} {o : Option Round} {rnd : Round}
    (h : observeRound rec o = some rnd) : o = some rnd := by
  cases o with
  | none => simp [observeRound] at h
  | some r =>
    simp only [observeRound] at h
    split at h
    · cases h
    · exact h

private theorem observeTrack_round (cfg : Config) (now : Nat) (prev : Option Record) (rec : Record)
    (tr : Track) : (observeTrack cfg now prev rec tr).round = observeRound rec tr.round := by
  simp only [observeTrack]
  repeat' split
  all_goals rfl

private theorem tickNode_round {cfg : Config} {s : State} {now : Nat} {susp : Bool} {seq : Nat}
    {node : String} {tr : Track} {rnd : Round}
    (h : (tickNode cfg s now susp seq node tr).1.round = some rnd) :
    tr.round = some rnd ∨
      (∃ r, tr.round = some r ∧ rnd = { r with doubted := r.doubted || susp }) ∨
      (rnd.voters = voters cfg node ∧ rnd.voted = [] ∧ rnd.suspectVotes = 0 ∧ rnd.failed = []) := by
  cases hrec : assocLookup node s.known with
  | none =>
    simp only [tickNode, hrec] at h
    exact Or.inl h
  | some rec =>
    simp only [tickNode, hrec] at h
    have hev : ∀ t : Track, (tickEvict cfg now susp node rec t).1.round = t.round := by
      intro t
      simp only [tickEvict]
      split <;> rfl
    rw [hev] at h
    cases hr : tr.round with
    | none =>
      simp only [tickRound, hr] at h
      split at h
      · simp only [Option.some.injEq] at h
        subst h
        exact Or.inr (Or.inr ⟨rfl, rfl, rfl, rfl⟩)
      · rw [hr] at h
        cases h
    | some r =>
      simp only [tickRound, hr] at h
      split at h
      · cases h
      · simp only [Option.some.injEq] at h
        exact Or.inr (Or.inl ⟨r, rfl, h.symm⟩)

private theorem roundOk_fresh {cfg : Config} {node : String} {rnd : Round}
    (h : rnd.voters = voters cfg node ∧ rnd.voted = [] ∧ rnd.suspectVotes = 0 ∧ rnd.failed = []) :
    RoundOk cfg node rnd := by
  obtain ⟨h1, h2, h3, h4⟩ := h
  refine ⟨h1, ?_, ?_, ?_, ?_, ?_⟩ <;> simp [h2, h3, h4]

private theorem inv_step {cfg : Config} {s : State} (hI : Inv cfg s) (op : Op) :
    Inv cfg (step cfg s op).1 := by
  intro n tr rnd hmem hr
  cases op with
  | observed now node rec =>
    simp only [step, observed] at hmem
    cases hl : assocLookup node s.tracks with
    | none =>
      simp only [hl] at hmem
      split at hmem
      · rcases mem_assocInsert hmem with h | h
        · cases h
          cases hr
        · exact hI n tr rnd h hr
      · exact hI n tr rnd hmem hr
    | some tr0 =>
      simp only [hl] at hmem
      rcases mem_assocInsert hmem with h | h
      · cases h
        rw [observeTrack_round] at hr
        exact hI _ tr0 rnd (assocLookup_mem hl) (observeRound_some hr)
      · exact hI n tr rnd h hr
  | forgotten now node =>
    simp only [step, forgotten] at hmem
    exact hI n tr rnd (mem_assocErase hmem) hr
  | heartbeat now node =>
    simp only [step] at hmem
    cases hk : assocLookup node s.known with
    | none =>
      simp only [heartbeat, hk] at hmem
      exact hI n tr rnd hmem hr
    | some rec =>
      cases ho : owns cfg node with
      | false =>
        simp only [heartbeat, hk, ho, Bool.not_false, ite_true] at hmem
        exact hI n tr rnd hmem hr
      | true =>
        simp only [heartbeat, hk, ho, Bool.not_true, Bool.false_eq_true, ite_false] at hmem
        rcases mem_assocInsert hmem with h | h
        · cases h
          cases hr
        · exact hI n tr rnd h hr
  | tick now =>
    simp only [step, tick, List.mem_reverse] at hmem
    rcases tickFold_tracks _ _ _ _ _ _ _ hmem with h | ⟨p, seq, hp, hq⟩
    · simp at h
    · cases hq
      rcases tickNode_round hr with h | ⟨r, hr0, rfl⟩ | h
      · exact hI p.1 p.2 rnd hp h
      · exact hI p.1 p.2 r hp hr0
      · exact roundOk_fresh h
  | voteRequested now fromId node roundId =>
    simp only [step, voteRequested] at hmem
    exact hI n tr rnd hmem hr
  | voteReceived now fromId node roundId suspect alive =>
    simp only [step, voteReceived] at hmem
    cases hl : assocLookup node s.tracks with
    | none =>
      simp only [hl] at hmem
      exact hI n tr rnd hmem hr
    | some tr0 =>
      cases hr0 : tr0.round with
      | none =>
        simp only [hl, hr0] at hmem
        exact hI n tr rnd hmem hr
      | some rnd0 =>
        simp only [hl, hr0] at hmem
        split at hmem
        · rename_i hcond
          simp only [Bool.and_eq_true, beq_iff_eq, List.contains_eq_mem, decide_eq_true_eq] at hcond
          obtain ⟨_, hv⟩ := hcond
          split at hmem
          · rcases mem_assocInsert hmem with h | h
            · cases h
              simp [bump] at hr
            · exact hI n tr rnd h hr
          · split at hmem
            · rename_i hnot
              simp only [Bool.not_eq_true', List.contains_eq_mem, decide_eq_false_iff_not] at hnot
              rcases mem_assocInsert hmem with h | h
              · cases h
                obtain ⟨g1, g2, g3, g4, g5, g6⟩ := hI _ tr0 rnd0 (assocLookup_mem hl) hr0
                simp only [Option.some.injEq] at hr
                subst hr
                refine ⟨g1, ?_, ?_, ?_, g5, g6⟩
                · rw [List.nodup_append]
                  refine ⟨g2, by simp, ?_⟩
                  intro a ha b hb
                  simp only [List.mem_singleton] at hb
                  subst hb
                  intro hab
                  exact hnot (hab ▸ ha)
                · intro v hv'
                  simp only [List.mem_append, List.mem_singleton] at hv'
                  rcases hv' with hv' | hv'
                  · exact g3 v hv'
                  · rw [hv']; exact hv
                · simp only [List.length_append, List.length_singleton]
                  split <;> omega
              · exact hI n tr rnd h hr
            · exact hI n tr rnd hmem hr
        · exact hI n tr rnd hmem hr
  | probeResult now fromId fromNode node roundId ok =>
    simp only [step, probeResult] at hmem
    cases hl : assocLookup node s.tracks with
    | none =>
      simp only [hl] at hmem
      exact hI n tr rnd hmem hr
    | some tr0 =>
      cases hr0 : tr0.round with
      | none =>
        simp only [hl, hr0] at hmem
        exact hI n tr rnd hmem hr
      | some rnd0 =>
        simp only [hl, hr0] at hmem
        split at hmem
        · rename_i hcond
          simp only [Bool.and_eq_true, beq_iff_eq, List.contains_eq_mem, decide_eq_true_eq] at hcond
          obtain ⟨_, hp⟩ := hcond
          split at hmem
          · rcases mem_assocInsert hmem with h | h
            · cases h
              simp [bump] at hr
            · exact hI n tr rnd h hr
          · split at hmem
            · rename_i hnot
              simp only [Bool.not_eq_true', List.contains_eq_mem, decide_eq_false_iff_not] at hnot
              rcases mem_assocInsert hmem with h | h
              · cases h
                obtain ⟨g1, g2, g3, g4, g5, g6⟩ := hI _ tr0 rnd0 (assocLookup_mem hl) hr0
                simp only [Option.some.injEq] at hr
                subst hr
                refine ⟨g1, g2, g3, g4, ?_, ?_⟩
                · rw [List.nodup_append]
                  refine ⟨g5, by simp, ?_⟩
                  intro a ha b hb
                  simp only [List.mem_singleton] at hb
                  subst hb
                  intro hab
                  exact hnot (hab ▸ ha)
                · intro q hq
                  simp only [List.mem_append, List.mem_singleton] at hq
                  rcases hq with hq | hq
                  · exact g6 q hq
                  · rw [hq]; exact hp
              · exact hI n tr rnd h hr
            · exact hI n tr rnd hmem hr
        · exact hI n tr rnd hmem hr

private theorem inv_foldl {cfg : Config} (ops : List Op) (s : State) (hI : Inv cfg s) :
    Inv cfg (ops.foldl (fun s op => (step cfg s op).1) s) := by
  induction ops generalizing s with
  | nil => exact hI
  | cons op rest ih => exact ih _ (inv_step hI op)

/-- In every state the detector can reach, the votes a round has counted came from distinct owners
of the node other than the detector itself, there are no more suspecting votes than votes, and the
failures it has counted came from distinct probers that the round asked. -/
theorem round_evidence_is_sound (cfg : Config) (ops : List Op) (node : String) (tr : Track)
    (rnd : Round) (ht : (node, tr) ∈ (run cfg ops).tracks) (hr : tr.round = some rnd) :
    rnd.voted.Nodup ∧ (∀ v ∈ rnd.voted, v ∈ owners cfg node ∧ v ≠ cfg.self) ∧
      rnd.suspectVotes ≤ rnd.voted.length ∧ rnd.failed.Nodup ∧
      (∀ p ∈ rnd.failed, p ∈ rnd.probers) := by
  have hinit : Inv cfg State.init := by
    intro n t r hmem _
    simp [State.init] at hmem
  obtain ⟨g1, g2, g3, g4, g5, g6⟩ := inv_foldl ops State.init hinit node tr rnd ht hr
  refine ⟨g2, ?_, g4, g5, g6⟩
  intro v hv
  have hv' := g3 v hv
  rw [g1] at hv'
  simp only [voters, List.mem_filter, bne_iff_ne, ne_eq] at hv'
  exact hv'

/-- A granted eviction leaves at most `limit` recorded grants, and every one of them is the new
grant or lies inside the window that ends at `now`. -/
theorem budget_grant_is_bounded (granted : List Nat) (now window limit : Nat)
    (h : (Budget.grant granted now window limit).2 = true) :
    (Budget.grant granted now window limit).1.length ≤ limit ∧
      ∀ e ∈ (Budget.grant granted now window limit).1, e = now ∨ now < e + window := by
  simp only [Budget.grant] at h ⊢
  split at h
  · cases h
  · rename_i hlt
    simp only [hlt, ite_false]
    refine ⟨?_, ?_⟩
    · rw [insertAsc_length]
      omega
    · intro e he
      rcases mem_insertAsc he with he | he
      · exact Or.inl he
      · right
        simp only [List.mem_filter, decide_eq_true_eq, Int.ofNat_eq_natCast] at he
        omega

end Nayutar.Liveness
