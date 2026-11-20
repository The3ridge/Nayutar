import Nayutar.Liveness

namespace Nayutar.Liveness

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
  sorry

/-- After a tick in which the detector suspects itself, every round that is open for a node with a
known record is marked as doubted, whether the round was already open or opened in that tick. -/
theorem suspected_tick_marks_rounds (cfg : Config) (s : State) (now : Nat) (node : String)
    (tr : Track) (rnd : Round) (rec : Record)
    (hs : (tick cfg s now).1.suspended = true) (hk : assocLookup node s.known = some rec)
    (hmem : (node, tr) ∈ (tick cfg s now).1.tracks) (hr : tr.round = some rnd) :
    rnd.doubted = true := by
  sorry

/-- An eviction proposal comes only for a record that is NotReady and not yet evictable, after the
grace has passed on the detector's own clock since `notReadySince`, and never while the detector
suspects itself. -/
theorem evictable_needs_grace (cfg : Config) (s : State) (now : Nat) (a : Action)
    (ha : a ∈ (tick cfg s now).2) (hk : a.kind = .proposeEvictable) :
    ∃ tr rec, (a.node, tr) ∈ s.tracks ∧ assocLookup a.node s.known = some rec ∧
      rec.state = .notready ∧ rec.evictable = false ∧ a.modRev = rec.modRev ∧
      tr.notReadySince + cfg.evictionGrace ≤ (tick cfg s now).1.now ∧
      (tick cfg s now).1.suspended = false := by
  sorry

/-- Observing a NotReady record restarts the eviction grace at the detector's current time, unless the
detector already knew the node as NotReady at the same revision. -/
theorem notready_observation_restarts_grace (cfg : Config) (s : State) (now : Nat) (node : String)
    (rec : Record) (tr : Track) (hrec : rec.state = .notready)
    (hprev : ∀ prev, assocLookup node s.known = some prev →
      prev.state ≠ .notready ∨ prev.modRev ≠ rec.modRev)
    (ht : assocLookup node (observed cfg s now node rec).tracks = some tr) :
    tr.notReadySince = (observed cfg s now node rec).now := by
  sorry

/-- An accepted heartbeat ends the node's round. -/
theorem heartbeat_ends_round (cfg : Config) (s : State) (now : Nat) (node : String) (tr : Track)
    (hs : (heartbeat cfg s now node).2.1 = .accepted)
    (ht : assocLookup node (heartbeat cfg s now node).1.tracks = some tr) : tr.round = none := by
  sorry

/-- A successful indirect probe from a prober that the node's round asked ends the round, even if
that prober reported a failure before. -/
theorem ok_probe_ends_round (cfg : Config) (s : State) (now : Nat) (fromId : String) (fromNode : Bool)
    (node : String) (roundId : Nat) (tr : Track) (rnd : Round)
    (ht : assocLookup node s.tracks = some tr) (hr : tr.round = some rnd) (hid : rnd.id = roundId)
    (hp : ({ id := fromId, isNode := fromNode } : Prober) ∈ rnd.probers) :
    ∃ tr', assocLookup node (probeResult cfg s now fromId fromNode node roundId true).tracks = some tr' ∧
      tr'.round = none := by
  sorry

/-- A vote from another owner that vouches the node is alive ends the round, whatever it says about
suspicion and even if that owner voted before. -/
theorem alive_vote_ends_round (cfg : Config) (s : State) (now : Nat) (fromId node : String)
    (roundId : Nat) (suspect : Bool) (tr : Track) (rnd : Round)
    (ht : assocLookup node s.tracks = some tr) (hr : tr.round = some rnd) (hid : rnd.id = roundId)
    (hv : fromId ∈ rnd.voters) :
    ∃ tr', assocLookup node (voteReceived cfg s now fromId node roundId suspect true).tracks = some tr' ∧
      tr'.round = none := by
  sorry

/-- In every state the detector can reach, the votes a round has counted came from distinct owners
of the node other than the detector itself, there are no more suspecting votes than votes, and the
failures it has counted came from distinct probers that the round asked. -/
theorem round_evidence_is_sound (cfg : Config) (ops : List Op) (node : String) (tr : Track)
    (rnd : Round) (ht : (node, tr) ∈ (run cfg ops).tracks) (hr : tr.round = some rnd) :
    rnd.voted.Nodup ∧ (∀ v ∈ rnd.voted, v ∈ owners cfg node ∧ v ≠ cfg.self) ∧
      rnd.suspectVotes ≤ rnd.voted.length ∧ rnd.failed.Nodup ∧
      (∀ p ∈ rnd.failed, p ∈ rnd.probers) := by
  sorry

/-- A granted eviction leaves at most `limit` recorded grants, and every one of them is the new
grant or lies inside the window that ends at `now`. -/
theorem budget_grant_is_bounded (granted : List Nat) (now window limit : Nat)
    (h : (Budget.grant granted now window limit).2 = true) :
    (Budget.grant granted now window limit).1.length ≤ limit ∧
      ∀ e ∈ (Budget.grant granted now window limit).1, e = now ∨ now < e + window := by
  sorry

end Nayutar.Liveness
