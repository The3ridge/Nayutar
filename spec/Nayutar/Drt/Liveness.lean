import Lean.Data.Json
import Nayutar.Liveness

open Lean
open Nayutar.Liveness

namespace Nayutar.Drt.Liveness

/-- Wire operation for the `liveness` DRT suite, including the standalone `budget` op. -/
inductive DrtOp where
  | detector (op : Op)
  | budget (granted : List Nat) (now : Nat) (window : Nat) (limit : Nat)

/-- Parsed test case for the `liveness` DRT suite. -/
structure Case where
  id : Nat
  config : Config
  ops : List DrtOp

/-- Snapshot of one tracked node's state after a step. -/
structure NodeSnapshot where
  node : String
  streak : Nat
  round : Nat
  samples : Nat
  last : Nat
  backoff : Nat
  heard : Bool
  nextRoundAt : Nat
  nextReadyAt : Nat
  nextEvictAt : Nat
  notReadySince : Nat
  doubted : Bool

/-- Result and post-step snapshot for one wire operation. -/
structure Step where
  status : String := ""
  actions : List Action := []
  suspended : Bool := false
  nodes : List NodeSnapshot := []
  granted : List Nat := []
  ok : Bool := false

private def str (j : Json) (field : String) : Except String String := do
  (← j.getObjVal? field).getStr?

private def nat (j : Json) (field : String) : Except String Nat := do
  (← j.getObjVal? field).getNat?

private def bool (j : Json) (field : String) : Except String Bool := do
  (← j.getObjVal? field).getBool?

private def list (j : Json) (field : String) (parse : Json → Except String α) : Except String (List α) := do
  let arr ← (← j.getObjVal? field).getArr?
  arr.toList.mapM parse

private def parsePhiConfig (j : Json) : Except String PhiConfig := do
  return {
    threshold := ← nat j "threshold"
    maxSamples := ← nat j "max_samples"
    minStdDev := ← nat j "min_std_dev"
    acceptablePause := ← nat j "acceptable_pause"
    firstInterval := ← nat j "first_interval"
    maxInterval := ← nat j "max_interval"
  }

private def parseRankedEntry (j : Json) : Except String (String × List String) := do
  let node ← str j "node"
  let aggs ← list j "aggregators" (fun elem => elem.getStr?)
  return (node, aggs)

private def parseConfig (j : Json) (ranked : List (String × List String)) : Except String Config := do
  return {
    self := ← str j "self"
    owners := ← nat j "owners"
    peerProbers := ← nat j "peer_probers"
    rackProbers := ← nat j "rack_probers"
    witnessProbers := ← nat j "witness_probers"
    minFailedProbes := ← nat j "min_failed_probes"
    roundTimeout := ← nat j "round_timeout"
    retryInterval := ← nat j "retry_interval"
    maxRoundBackoff := ← nat j "max_round_backoff"
    evictionRetryInterval := ← nat j "eviction_retry_interval"
    selfSuspectPercent := ← nat j "self_suspect_percent"
    selfSuspectMinNodes := ← nat j "self_suspect_min_nodes"
    readyHeartbeats := ← nat j "ready_heartbeats"
    evictionGrace := ← nat j "eviction_grace"
    roundBase := ← nat j "round_base"
    phi := ← parsePhiConfig (← j.getObjVal? "phi")
    ranked := ranked
  }

private def parseNodeState : String → Except String NodeState
  | "pending" => pure .pending
  | "ready" => pure .ready
  | "notready" => pure .notready
  | other => throw s!"unknown state: {other}"

private def parseOp (j : Json) : Except String DrtOp := do
  match ← str j "op" with
  | "observed" =>
    let now ← nat j "now"
    let node ← str j "node"
    let state ← parseNodeState (← str j "state")
    let since ← nat j "since"
    let rack ← str j "rack"
    let evictable ← bool j "evictable"
    let modRev ← nat j "mod_rev"
    return .detector (.observed now node {
      state := state
      since := since
      rack := rack
      evictable := evictable
      modRev := modRev
    })
  | "forgotten" =>
    return .detector (.forgotten (← nat j "now") (← str j "node"))
  | "heartbeat" =>
    return .detector (.heartbeat (← nat j "now") (← str j "node"))
  | "tick" =>
    return .detector (.tick (← nat j "now"))
  | "vote_requested" =>
    return .detector (.voteRequested (← nat j "now") (← str j "from") (← str j "node") (← nat j "round"))
  | "vote_received" =>
    return .detector (.voteReceived (← nat j "now") (← str j "from") (← str j "node") (← nat j "round")
      (← bool j "suspect") (← bool j "alive"))
  | "probe_result" =>
    return .detector (.probeResult (← nat j "now") (← str j "from") (← bool j "from_node")
      (← str j "node") (← nat j "round") (← bool j "ok"))
  | "budget" =>
    let granted ← list j "granted" (fun elem => elem.getNat?)
    return .budget granted (← nat j "now") (← nat j "window") (← nat j "limit")
  | other => throw s!"unknown op: {other}"

/-- Parse one JSON line into a `liveness` DRT `Case`. -/
def parseCase (line : String) : Except String Case := do
  let j ← Json.parse line
  let id ← nat j "id"
  let ranked ← list j "ranked" parseRankedEntry
  let config ← parseConfig (← j.getObjVal? "config") ranked
  let ops ← list j "ops" parseOp
  return { id := id, config := config, ops := ops }

private def statusName : HeartbeatStatus → String
  | .accepted => "accepted"
  | .unknownNode => "unknown"
  | .notOwner => "notowner"

private def snapshotNodes (s : State) : List NodeSnapshot :=
  s.tracks.map fun p =>
    {
      node := p.1
      streak := p.2.streak
      round := match p.2.round with | some r => r.id | none => 0
      samples := p.2.window.samples.length
      last := p.2.window.last
      backoff := p.2.backoff
      heard := p.2.heard
      nextRoundAt := p.2.nextRoundAt
      nextReadyAt := p.2.nextReadyAt
      nextEvictAt := p.2.nextEvictAt
      notReadySince := p.2.notReadySince
      doubted := match p.2.round with | some r => r.doubted | none => false
    }

private def drtStep (cfg : Config) (s : State) : DrtOp → Step × State
  | .detector (.heartbeat now node) =>
    let (s', st, acts) := heartbeat cfg s now node
    ({ status := statusName st, actions := acts, suspended := s'.suspended, nodes := snapshotNodes s' }, s')
  | .detector op =>
    let (s', acts) := Nayutar.Liveness.step cfg s op
    ({ actions := acts, suspended := s'.suspended, nodes := snapshotNodes s' }, s')
  | .budget granted now window limit =>
    let (granted', ok') := Budget.grant granted now window limit
    ({ suspended := s.suspended, nodes := snapshotNodes s, granted := granted', ok := ok' }, s)

/-- Run every operation in `c` and return the step results in order. -/
def run (c : Case) : List Step :=
  let rec go (s : State) : List DrtOp → List Step
    | [] => []
    | op :: ops =>
      let (out, s') := drtStep c.config s op
      out :: go s' ops
  go State.init c.ops

private def actionKindName : ActionKind → String
  | .voteRequest => "vote_request"
  | .vote => "vote"
  | .probeRequest => "probe_request"
  | .proposeReady => "propose_ready"
  | .proposeNotReady => "propose_notready"
  | .proposeEvictable => "propose_evictable"

private def actionToJson (a : Action) : Json :=
  Json.mkObj [
    ("kind", toJson (actionKindName a.kind)),
    ("to", toJson a.to),
    ("to_node", toJson a.toNode),
    ("node", toJson a.node),
    ("round", toJson a.round),
    ("suspect", toJson a.suspect),
    ("alive", toJson a.alive),
    ("mod_rev", toJson a.modRev)
  ]

private def nodeToJson (n : NodeSnapshot) : Json :=
  Json.mkObj [
    ("node", toJson n.node),
    ("streak", toJson n.streak),
    ("round", toJson n.round),
    ("samples", toJson n.samples),
    ("last", toJson n.last),
    ("backoff", toJson n.backoff),
    ("heard", toJson n.heard),
    ("next_round_at", toJson n.nextRoundAt),
    ("next_ready_at", toJson n.nextReadyAt),
    ("next_evict_at", toJson n.nextEvictAt),
    ("not_ready_since", toJson n.notReadySince),
    ("doubted", toJson n.doubted)
  ]

private def stepToJson (out : Step) : Json :=
  Json.mkObj [
    ("status", toJson out.status),
    ("actions", Json.arr (out.actions.map actionToJson).toArray),
    ("suspended", toJson out.suspended),
    ("nodes", Json.arr (out.nodes.map nodeToJson).toArray),
    ("granted", Json.arr (out.granted.map toJson).toArray),
    ("ok", toJson out.ok)
  ]

/-- Evaluate `c` and render the DRT output JSON object. -/
def caseToJson (c : Case) : Json :=
  Json.mkObj [
    ("id", toJson c.id),
    ("steps", Json.arr ((run c).map stepToJson).toArray)
  ]

end Nayutar.Drt.Liveness
