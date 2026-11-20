namespace Nayutar.Liveness

/-- Configuration parameters for a phi accrual failure detector window. -/
structure PhiConfig where
  threshold : Nat
  maxSamples : Nat
  minStdDev : Nat
  acceptablePause : Nat
  firstInterval : Nat
  maxInterval : Nat
  deriving Repr, DecidableEq

/-- Sliding window of inter-arrival heartbeat intervals in milliseconds. -/
structure Window where
  samples : List Nat
  last : Nat
  started : Bool
  deriving Repr, DecidableEq

/-- An unstarted heartbeat window. -/
def Window.empty : Window :=
  { samples := [], last := 0, started := false }

/-- Upper-tail normal quantile table `ceil(1000 * z_k^2)` for threshold `1..16`, or `0` out of range. -/
def zTable : Nat → Nat
  | 1 => 1643
  | 2 => 5412
  | 3 => 9550
  | 4 => 13832
  | 5 => 18190
  | 6 => 22596
  | 7 => 27034
  | 8 => 31495
  | 9 => 35974
  | 10 => 40467
  | 11 => 44971
  | 12 => 49484
  | 13 => 54005
  | 14 => 58533
  | 15 => 63065
  | 16 => 67603
  | _ => 0

/-- Sum of a list of natural numbers. -/
def sumNat : List Nat → Nat
  | [] => 0
  | x :: xs => x + sumNat xs

/-- Sum of squares of a list of natural numbers. -/
def sumSqNat : List Nat → Nat
  | [] => 0
  | x :: xs => x * x + sumSqNat xs

/-- Append one interval sample, keeping at most `maxSamples` newest samples. -/
def pushSample (maxSamples : Nat) (samples : List Nat) (x : Nat) : List Nat :=
  let next := samples ++ [x]
  if next.length > maxSamples then next.drop (next.length - maxSamples) else next

/-- Evaluate the exact integer phi threshold test at monotonic time `now`. -/
def Window.suspect (w : Window) (cfg : PhiConfig) (now : Nat) : Bool :=
  if !w.started || now < w.last then false
  else
    let t : Int := Int.ofNat (now - w.last)
    let n : Int := Int.ofNat w.samples.length
    let s : Int := Int.ofNat (sumNat w.samples)
    let q : Int := Int.ofNat (sumSqNat w.samples)
    let a : Int := n * (t - Int.ofNat cfg.acceptablePause) - s
    if a ≤ 0 then false
    else
      let var : Int := n * q - s * s
      let minVar : Int := n * n * Int.ofNat cfg.minStdDev * Int.ofNat cfg.minStdDev
      let v : Int := if var < minVar then minVar else var
      decide (1000 * a * a > Int.ofNat (zTable cfg.threshold) * v)

/-- Record a heartbeat arrival at monotonic time `now`. -/
def Window.heartbeat (w : Window) (cfg : PhiConfig) (now : Nat) : Window :=
  if !w.started then
    let f := cfg.firstInterval
    let s1 := pushSample cfg.maxSamples w.samples (f - f / 4)
    let s2 := pushSample cfg.maxSamples s1 (f + f / 4)
    { samples := s2, last := now, started := true }
  else if now < w.last then
    w
  else
    let d := now - w.last
    let samples' :=
      if !w.suspect cfg now && d ≤ cfg.maxInterval then
        pushSample cfg.maxSamples w.samples d
      else
        w.samples
    { samples := samples', last := now, started := true }

/-- Maximum of two natural numbers. -/
def natMax (a b : Nat) : Nat :=
  if a < b then b else a

/-- Minimum of two natural numbers. -/
def natMin (a b : Nat) : Nat :=
  if a < b then a else b

/-- Insert `x` into a list before the first element strictly greater than `x`. -/
def insertAsc (x : Nat) : List Nat → List Nat
  | [] => [x]
  | e :: es => if x < e then x :: e :: es else e :: insertAsc x es

/-- Sliding-window eviction budget limit `max(1, ceil(totalNodes * percent / 100))`. -/
def Budget.limit (totalNodes percent : Nat) : Nat :=
  natMax 1 ((totalNodes * percent + 99) / 100)

/-- Prune entries not newer than `now - window` and record `now` if under `limit`. -/
def Budget.grant (granted : List Nat) (now window limit : Nat) : List Nat × Bool :=
  let cutoff : Int := Int.ofNat now - Int.ofNat window
  let kept := granted.filter (fun e => decide (Int.ofNat e > cutoff))
  if kept.length ≥ limit then (kept, false)
  else (insertAsc now kept, true)

/-- Node liveness state stored in the cluster registry. -/
inductive NodeState where
  | pending
  | ready
  | notready
  deriving Repr, DecidableEq

/-- Aggregator's local copy of a node record from the store. -/
structure Record where
  state : NodeState
  since : Nat
  rack : String
  evictable : Bool
  modRev : Nat
  deriving Repr, DecidableEq

/-- Configuration for a node-liveness detector instance. -/
structure Config where
  self : String
  owners : Nat
  peerProbers : Nat
  rackProbers : Nat
  witnessProbers : Nat
  minFailedProbes : Nat
  roundTimeout : Nat
  retryInterval : Nat
  maxRoundBackoff : Nat
  evictionRetryInterval : Nat
  selfSuspectPercent : Nat
  selfSuspectMinNodes : Nat
  readyHeartbeats : Nat
  evictionGrace : Nat
  roundBase : Nat
  phi : PhiConfig
  ranked : List (String × List String)
  deriving Repr, DecidableEq

/-- Target of an indirect probe request or sender of a probe result. -/
structure Prober where
  id : String
  isNode : Bool
  deriving Repr, DecidableEq

/-- Active confirmation round for a suspected Ready node. -/
structure Round where
  id : Nat
  deadline : Nat
  modRev : Nat
  voters : List String
  voted : List String
  suspectVotes : Nat
  probers : List Prober
  failed : List Prober
  doubted : Bool
  deriving Repr, DecidableEq

/-- Per-node tracking state maintained by an owner aggregator. -/
structure Track where
  window : Window
  streak : Nat
  heard : Bool
  backoff : Nat
  notReadySince : Nat
  round : Option Round
  nextRoundAt : Nat
  nextReadyAt : Nat
  nextEvictAt : Nat
  deriving Repr, DecidableEq

/-- Complete state of one detector instance. -/
structure State where
  now : Nat
  known : List (String × Record)
  tracks : List (String × Track)
  seq : Nat
  suspended : Bool
  deriving Repr, DecidableEq

/-- Kind of side-effect action emitted by the detector. -/
inductive ActionKind where
  | voteRequest
  | vote
  | probeRequest
  | proposeReady
  | proposeNotReady
  | proposeEvictable
  deriving Repr, DecidableEq

/-- Action emitted by a detector step; unused fields keep default zero values. -/
structure Action where
  kind : ActionKind
  to : String := ""
  toNode : Bool := false
  node : String := ""
  round : Nat := 0
  suspect : Bool := false
  alive : Bool := false
  modRev : Nat := 0
  deriving Repr, DecidableEq

/-- Outcome returned by `heartbeat`. -/
inductive HeartbeatStatus where
  | accepted
  | unknownNode
  | notOwner
  deriving Repr, DecidableEq

/-- Look up `key` in a string-keyed association list. -/
def assocLookup {α : Type} (key : String) : List (String × α) → Option α
  | [] => none
  | (k, v) :: rest =>
    if k == key then some v
    else assocLookup key rest

/-- Insert or replace `(key, val)` while keeping ascending key order. -/
def assocInsert {α : Type} (key : String) (val : α) : List (String × α) → List (String × α)
  | [] => [(key, val)]
  | (k, v) :: rest =>
    if key < k then (key, val) :: (k, v) :: rest
    else if k == key then (key, val) :: rest
    else (k, v) :: assocInsert key val rest

/-- Remove `key` from a string-keyed association list. -/
def assocErase {α : Type} (key : String) : List (String × α) → List (String × α)
  | [] => []
  | (k, v) :: rest =>
    if k == key then rest
    else (k, v) :: assocErase key rest

/-- Deduplicate strings preserving the first occurrence of each element. -/
def dedupStringsAcc (seen : List String) : List String → List String
  | [] => []
  | x :: xs =>
    if seen.contains x then dedupStringsAcc seen xs
    else x :: dedupStringsAcc (x :: seen) xs

/-- Initial empty detector state. -/
def State.init : State :=
  { now := 0, known := [], tracks := [], seq := 0, suspended := false }

/-- Deduplicated rendezvous ranking of aggregators for `node`. -/
def ranked (cfg : Config) (node : String) : List String :=
  match assocLookup node cfg.ranked with
  | some aggs => dedupStringsAcc [] aggs
  | none => []

/-- Owner aggregators responsible for `node`. -/
def owners (cfg : Config) (node : String) : List String :=
  (ranked cfg node).take cfg.owners

/-- Whether `cfg.self` is an owner of `node`. -/
def owns (cfg : Config) (node : String) : Bool :=
  (owners cfg node).contains cfg.self

/-- Number of owner votes required to confirm a node as NotReady. -/
def quorum (cfg : Config) (node : String) : Nat :=
  (owners cfg node).length / 2 + 1

/-- Peer owners asked to vote on `node`, excluding `cfg.self`. -/
def voters (cfg : Config) (node : String) : List String :=
  (owners cfg node).filter (fun id => id != cfg.self)

/-- Peer aggregator probers selected for `node`, falling back to `voters` if needed. -/
def peerProbers (cfg : Config) (node : String) : List String :=
  let r := ranked cfg node
  let ow := r.take cfg.owners
  let rest := (r.drop ow.length).filter (fun id => id != cfg.self)
  let fromRest := rest.take cfg.peerProbers
  let need := cfg.peerProbers - fromRest.length
  fromRest ++ (voters cfg node).take need

/-- Rotate an ascending list of candidate names so entries greater than `node` precede smaller ones. -/
def rotate (cands : List String) (node : String) : List String :=
  cands.filter (fun m => decide (node < m)) ++ cands.filter (fun m => decide (m < node))

/-- Ready nodes in the same rack as `node` that this detector does not itself suspect, rotated past
`node` and truncated to `rackProbers`. -/
def rackProbers (cfg : Config) (s : State) (node : String) (now : Nat) : List String :=
  match assocLookup node s.known with
  | none => []
  | some rec =>
    let candidates :=
      (s.known.filter fun p =>
        p.1 != node && p.2.rack == rec.rack && p.2.state == .ready &&
        match assocLookup p.1 s.tracks with
        | some tr => !tr.window.suspect cfg.phi now
        | none => true).map (fun p => p.1)
    (rotate candidates node).take cfg.rackProbers

/-- Ready nodes in other racks, skipping those this detector tracks and has not heard from or
suspects, rotated past `node` and truncated to `witnessProbers`. A node this detector does not track
is a candidate. -/
def witnessProbers (cfg : Config) (s : State) (node : String) (now : Nat) : List String :=
  match assocLookup node s.known with
  | none => []
  | some rec =>
    let candidates :=
      (s.known.filter fun p =>
        p.1 != node && p.2.state == .ready && p.2.rack != rec.rack &&
        match assocLookup p.1 s.tracks with
        | some tr => tr.heard && !tr.window.suspect cfg.phi now
        | none => true).map (fun p => p.1)
    (rotate candidates node).take cfg.witnessProbers

/-- Combined list of peer, rack, and witness probers for `node`. -/
def probers (cfg : Config) (s : State) (node : String) (now : Nat) : List Prober :=
  (peerProbers cfg node).map (fun id => ({ id := id, isNode := false } : Prober)) ++
    (rackProbers cfg s node now).map (fun id => ({ id := id, isNode := true } : Prober)) ++
    (witnessProbers cfg s node now).map (fun id => ({ id := id, isNode := true } : Prober))

/-- Exponential round backoff delay capped at `maxRoundBackoff`. -/
def delay (cfg : Config) (k : Nat) : Nat :=
  natMin (cfg.retryInterval * 2 ^ natMin k 16) cfg.maxRoundBackoff

/-- Increase round backoff and push `nextRoundAt` forward after evidence of life. -/
def bump (cfg : Config) (tr : Track) (now : Nat) : Track :=
  let backoff' := natMin (tr.backoff + 1) 16
  { tr with backoff := backoff', nextRoundAt := natMax tr.nextRoundAt (now + delay cfg backoff') }

/-- Keep a round only while the record is Ready at the revision the round started on. -/
def observeRound (rec : Record) : Option Round → Option Round
  | some rnd => if rec.state != .ready || rec.modRev != rnd.modRev then none else some rnd
  | none => none

/-- Update an existing track for a newly observed record; `prev` is the record known before. -/
def observeTrack (cfg : Config) (now : Nat) (prev : Option Record) (rec : Record) (tr : Track) : Track :=
  let trA := { tr with round := observeRound rec tr.round }
  let becameNotReady :=
    match prev with
    | none => true
    | some p => p.state != .notready || p.modRev != rec.modRev
  let trB :=
    if rec.state == .notready && becameNotReady then
      { trA with streak := 0, notReadySince := now }
    else
      trA
  let wasNotReady :=
    match prev with
    | none => false
    | some p => p.state == .notready
  if rec.state == .ready && wasNotReady && trB.window.suspect cfg.phi now then
    bump cfg trB now
  else
    trB

/-- Track created for an owned node first seen as Ready or NotReady: the observation counts as a heartbeat. -/
def bootstrapTrack (cfg : Config) (now : Nat) : Track :=
  { window := Window.empty.heartbeat cfg.phi now
    streak := 0
    heard := false
    backoff := 0
    notReadySince := now
    round := none
    nextRoundAt := 0
    nextReadyAt := 0
    nextEvictAt := 0 }

/-- Record or update a node's registry state in the detector. -/
def observed (cfg : Config) (s : State) (nowArg : Nat) (node : String) (rec : Record) : State :=
  let now := natMax nowArg s.now
  let tracks :=
    match assocLookup node s.tracks with
    | none =>
      if owns cfg node && (rec.state == .ready || rec.state == .notready) then
        assocInsert node (bootstrapTrack cfg now) s.tracks
      else
        s.tracks
    | some tr => assocInsert node (observeTrack cfg now (assocLookup node s.known) rec tr) s.tracks
  { s with now := now, known := assocInsert node rec s.known, tracks := tracks }

/-- Remove a node from `known` and `tracks`. -/
def forgotten (_cfg : Config) (s : State) (nowArg : Nat) (node : String) : State :=
  let now := natMax nowArg s.now
  { s with now := now, known := assocErase node s.known, tracks := assocErase node s.tracks }

/-- Track of a node that has not been heard or bootstrapped yet. -/
def Track.fresh : Track :=
  { window := Window.empty
    streak := 0
    heard := false
    backoff := 0
    notReadySince := 0
    round := none
    nextRoundAt := 0
    nextReadyAt := 0
    nextEvictAt := 0 }

/-- Process a heartbeat from `node` at monotonic time `nowArg`. -/
def heartbeat (cfg : Config) (s : State) (nowArg : Nat) (node : String) :
    State × HeartbeatStatus × List Action :=
  let now := natMax nowArg s.now
  match assocLookup node s.known with
  | none => ({ s with now := now }, .unknownNode, [])
  | some rec =>
    if !owns cfg node then
      ({ s with now := now }, .notOwner, [])
    else
      let tr0 : Track := (assocLookup node s.tracks).getD Track.fresh
      let was := tr0.window.suspect cfg.phi now
      let streak' := if was then 1 else natMin (tr0.streak + 1) cfg.readyHeartbeats
      let propose := rec.state != .ready && decide (streak' ≥ cfg.readyHeartbeats) &&
        decide (now ≥ tr0.nextReadyAt)
      let tr' : Track := {
        window := tr0.window.heartbeat cfg.phi now
        streak := streak'
        heard := true
        backoff := 0
        notReadySince := tr0.notReadySince
        round := none
        nextRoundAt := 0
        nextReadyAt := if propose then now + cfg.retryInterval else tr0.nextReadyAt
        nextEvictAt := tr0.nextEvictAt
      }
      ({ s with now := now, tracks := assocInsert node tr' s.tracks }, .accepted,
        if propose then [{ kind := .proposeReady, node := node, modRev := rec.modRev }] else [])

/-- Whether a round that reached its deadline confirms the node as NotReady. A round that asked any
node needs failure reports from nodes; a round that could ask aggregators only confirms on their
reports, and never while the detector suspects itself. -/
def confirms (cfg : Config) (suspended : Bool) (node : String) (rec : Record) (rnd : Round) : Bool :=
  let base := rec.state == .ready && rec.modRev == rnd.modRev &&
    decide (1 + rnd.suspectVotes ≥ quorum cfg node)
  let nodeAsked := (rnd.probers.filter (fun p => p.isNode)).length
  let nodeFailures := (rnd.failed.filter (fun p => p.isNode)).length
  if nodeAsked ≥ 1 then
    base && decide (nodeFailures ≥ natMin cfg.minFailedProbes nodeAsked)
  else if suspended then
    false
  else
    base && decide (rnd.probers.length ≥ 1) &&
      decide (rnd.failed.length ≥ natMin cfg.minFailedProbes rnd.probers.length)

/-- Close a round that reached its deadline, or open one for a suspected Ready node. -/
def tickRound (cfg : Config) (s : State) (now : Nat) (suspended : Bool) (seq : Nat) (node : String)
    (rec : Record) (tr : Track) : Track × Nat × List Action :=
  match tr.round with
  | some rnd =>
    if now ≥ rnd.deadline then
      ({ tr with round := none, nextRoundAt := now + cfg.retryInterval }, seq,
        if confirms cfg (rnd.doubted || suspended) node rec rnd then
          [{ kind := .proposeNotReady, node := node, modRev := rnd.modRev }]
        else [])
    else
      ({ tr with round := some { rnd with doubted := rnd.doubted || suspended } }, seq, [])
  | none =>
    if rec.state == .ready && tr.window.suspect cfg.phi now && now ≥ tr.nextRoundAt then
      let id := cfg.roundBase + seq + 1
      let vs := voters cfg node
      let ps := probers cfg s node now
      let rnd : Round := {
        id := id
        deadline := now + cfg.roundTimeout
        modRev := rec.modRev
        voters := vs
        voted := []
        suspectVotes := 0
        probers := ps
        failed := []
        doubted := suspended
      }
      ({ tr with round := some rnd }, seq + 1,
        vs.map (fun v => ({ kind := .voteRequest, to := v, node := node, round := id } : Action)) ++
        ps.map (fun p =>
          ({ kind := .probeRequest, to := p.id, toNode := p.isNode, node := node, round := id } : Action)))
    else
      (tr, seq, [])

/-- Whether the eviction permit for a NotReady node is due at `now`. -/
def evictionDue (cfg : Config) (now : Nat) (suspended : Bool) (rec : Record) (tr : Track) : Bool :=
  !suspended && rec.state == .notready && !rec.evictable &&
    decide (now ≥ tr.notReadySince + cfg.evictionGrace) &&
    tr.window.suspect cfg.phi now && decide (now ≥ tr.nextEvictAt)

/-- Propose the eviction permit for a NotReady node once the grace has passed. -/
def tickEvict (cfg : Config) (now : Nat) (suspended : Bool) (node : String) (rec : Record)
    (tr : Track) : Track × List Action :=
  if evictionDue cfg now suspended rec tr then
    ({ tr with nextEvictAt := now + cfg.evictionRetryInterval },
      [{ kind := .proposeEvictable, node := node, modRev := rec.modRev }])
  else
    (tr, [])

/-- Evaluate one tracked node during `tick`. -/
def tickNode (cfg : Config) (s : State) (now : Nat) (suspended : Bool) (seq : Nat) (node : String)
    (tr : Track) : Track × Nat × List Action :=
  match assocLookup node s.known with
  | none => (tr, seq, [])
  | some rec =>
    let r := tickRound cfg s now suspended seq node rec tr
    let e := tickEvict cfg now suspended node rec r.1
    (e.1, r.2.1, r.2.2 ++ e.2)

/-- Whether the detector doubts its own vantage point: it suspects at least `selfSuspectMinNodes` and
at least `selfSuspectPercent` percent of the Ready nodes it tracks, or it tracks two or more and
suspects them all. -/
def selfSuspected (cfg : Config) (s : State) (now : Nat) : Bool :=
  let alive :=
    s.tracks.filter fun p =>
      match assocLookup p.1 s.known with
      | some rec => rec.state == .ready
      | none => false
  let suspected := alive.filter (fun p => p.2.window.suspect cfg.phi now)
  (decide (suspected.length ≥ cfg.selfSuspectMinNodes) &&
    decide (suspected.length * 100 ≥ cfg.selfSuspectPercent * alive.length)) ||
  (decide (alive.length ≥ 2) && decide (suspected.length = alive.length))

/-- Accumulator of the per-node pass of `tick`: reversed tracks, last round id, reversed action chunks. -/
abbrev TickAcc := List (String × Track) × Nat × List (List Action)

/-- Evaluate one tracked node and push the result onto the accumulator. -/
def tickStep (cfg : Config) (s : State) (now : Nat) (suspended : Bool) (acc : TickAcc)
    (p : String × Track) : TickAcc :=
  let r := tickNode cfg s now suspended acc.2.1 p.1 p.2
  ((p.1, r.1) :: acc.1, r.2.1, r.2.2 :: acc.2.2)

/-- Advance the detector by one tick at monotonic time `nowArg`. -/
def tick (cfg : Config) (s : State) (nowArg : Nat) : State × List Action :=
  let now := natMax nowArg s.now
  let suspended := selfSuspected cfg s now
  let acc := s.tracks.foldl (tickStep cfg s now suspended) ([], s.seq, [])
  ({ s with now := now, tracks := acc.1.reverse, seq := acc.2.1, suspended := suspended },
    acc.2.2.reverse.flatMap (fun xs => xs))

/-- This detector's vote on `node`: whether it suspects the node, and whether it vouches that the node is alive. -/
def voteOf (cfg : Config) (s : State) (now : Nat) (node : String) : Bool × Bool :=
  match assocLookup node s.tracks, assocLookup node s.known with
  | some tr, some rec =>
    if rec.state == .ready then
      let sus := tr.window.suspect cfg.phi now
      (sus, tr.heard && !sus)
    else
      (false, false)
  | _, _ => (false, false)

/-- Answer a peer's vote request for `node` in `roundId`. -/
def voteRequested (cfg : Config) (s : State) (nowArg : Nat) (fromId node : String) (roundId : Nat) :
    State × List Action :=
  let now := natMax nowArg s.now
  let v := voteOf cfg s now node
  ({ s with now := now },
    [{ kind := .vote, to := fromId, node := node, round := roundId, suspect := v.1, alive := v.2 }])

/-- Record a peer owner's vote for `node` in `roundId`. -/
def voteReceived (cfg : Config) (s : State) (nowArg : Nat) (fromId node : String) (roundId : Nat)
    (suspect alive : Bool) : State :=
  let now := natMax nowArg s.now
  match assocLookup node s.tracks with
  | none => { s with now := now }
  | some tr =>
    match tr.round with
    | none => { s with now := now }
    | some rnd =>
      if rnd.id == roundId && rnd.voters.contains fromId then
        if alive then
          { s with now := now, tracks := assocInsert node (bump cfg { tr with round := none } now) s.tracks }
        else if !rnd.voted.contains fromId then
          let rnd' := {
            rnd with
            voted := rnd.voted ++ [fromId]
            suspectVotes := if suspect then rnd.suspectVotes + 1 else rnd.suspectVotes
          }
          { s with now := now, tracks := assocInsert node { tr with round := some rnd' } s.tracks }
        else
          { s with now := now }
      else
        { s with now := now }

/-- Record an indirect probe result for `node` in `roundId`. -/
def probeResult (cfg : Config) (s : State) (nowArg : Nat) (fromId : String) (fromNode : Bool)
    (node : String) (roundId : Nat) (ok : Bool) : State :=
  let now := natMax nowArg s.now
  let prober : Prober := { id := fromId, isNode := fromNode }
  match assocLookup node s.tracks with
  | none => { s with now := now }
  | some tr =>
    match tr.round with
    | none => { s with now := now }
    | some rnd =>
      if rnd.id == roundId && rnd.probers.contains prober then
        if ok then
          { s with now := now, tracks := assocInsert node (bump cfg { tr with round := none } now) s.tracks }
        else if !rnd.failed.contains prober then
          let rnd' := { rnd with failed := rnd.failed ++ [prober] }
          { s with now := now, tracks := assocInsert node { tr with round := some rnd' } s.tracks }
        else
          { s with now := now }
      else
        { s with now := now }

/-- Detector operation sequence element (excluding the standalone `budget` op). -/
inductive Op where
  | observed (now : Nat) (node : String) (rec : Record)
  | forgotten (now : Nat) (node : String)
  | heartbeat (now : Nat) (node : String)
  | tick (now : Nat)
  | voteRequested (now : Nat) (fromId : String) (node : String) (round : Nat)
  | voteReceived (now : Nat) (fromId : String) (node : String) (round : Nat) (suspect : Bool) (alive : Bool)
  | probeResult (now : Nat) (fromId : String) (fromNode : Bool) (node : String) (round : Nat) (ok : Bool)
  deriving Repr, DecidableEq

/-- Execute one detector operation, dropping `heartbeat` status. -/
def step (cfg : Config) (s : State) : Op → State × List Action
  | .observed now node rec => (observed cfg s now node rec, [])
  | .forgotten now node => (forgotten cfg s now node, [])
  | .heartbeat now node =>
    let (s', _, acts) := heartbeat cfg s now node
    (s', acts)
  | .tick now => tick cfg s now
  | .voteRequested now fromId node roundId => voteRequested cfg s now fromId node roundId
  | .voteReceived now fromId node roundId suspect alive =>
    (voteReceived cfg s now fromId node roundId suspect alive, [])
  | .probeResult now fromId fromNode node roundId ok =>
    (probeResult cfg s now fromId fromNode node roundId ok, [])

/-- Fold a list of detector operations starting from `State.init`. -/
def run (cfg : Config) (ops : List Op) : State :=
  ops.foldl (fun s op => (step cfg s op).1) State.init

end Nayutar.Liveness
