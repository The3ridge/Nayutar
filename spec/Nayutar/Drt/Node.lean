import Lean.Data.Json
import Nayutar.Node

set_option autoImplicit false

open Lean
open Nayutar.Node

namespace Nayutar.Drt.Node

/-- Wire operation for the `node` DRT suite. -/
inductive DrtOp where
  | event (ev : Event)
  | nextStep (nowMs : Nat)
  | backoff (baseMs maxMs attempt : Nat)

/-- Parsed test case for the `node` DRT suite. -/
structure Case where
  id : Nat
  spec : Spec
  ops : List DrtOp

/-- Result and post-step snapshot for one wire operation. -/
structure Step where
  action : Action := .none
  state : State
  wellFormed : Bool
  backoffMs : Nat := 0

private def str (j : Json) (field : String) : Except String String := do
  (← j.getObjVal? field).getStr?

private def nat (j : Json) (field : String) : Except String Nat := do
  (← j.getObjVal? field).getNat?

private def bool (j : Json) (field : String) : Except String Bool := do
  (← j.getObjVal? field).getBool?

private def list {α : Type} (j : Json) (field : String) (parse : Json → Except String α) :
    Except String (List α) := do
  let arr ← (← j.getObjVal? field).getArr?
  arr.toList.mapM parse

private def parseRestartPolicy : String → Except String RestartPolicy
  | "never" => pure .never
  | "onFailure" => pure .onFailure
  | "always" => pure .always
  | other => throw s!"unknown restart policy: {other}"

private def parseSpec (j : Json) : Except String Spec := do
  return {
    restartPolicy := ← parseRestartPolicy (← str j "restart_policy")
    stopGraceMs := ← nat j "stop_grace_ms"
    backoffBaseMs := ← nat j "backoff_base_ms"
    backoffMaxMs := ← nat j "backoff_max_ms"
    backoffResetMs := ← nat j "backoff_reset_ms"
    cniCount := ← nat j "cni_count"
  }

private def parseEventKind : String → Except String EventKind
  | "start_requested" => pure .startRequested
  | "stop_requested" => pure .stopRequested
  | "netns_created" => pure .netnsCreated
  | "netns_create_failed" => pure .netnsCreateFailed
  | "cni_added" => pure .cniAdded
  | "cni_add_failed" => pure .cniAddFailed
  | "image_ready" => pure .imageReady
  | "image_failed" => pure .imageFailed
  | "container_created" => pure .containerCreated
  | "container_create_failed" => pure .containerCreateFailed
  | "task_started" => pure .taskStarted
  | "task_start_failed" => pure .taskStartFailed
  | "term_sent" => pure .termSent
  | "kill_sent" => pure .killSent
  | "task_exited" => pure .taskExited
  | "task_deleted" => pure .taskDeleted
  | "container_deleted" => pure .containerDeleted
  | "cni_deleted" => pure .cniDeleted
  | "netns_removed" => pure .netnsRemoved
  | other => throw s!"unknown event kind: {other}"

private def parseOp (j : Json) : Except String DrtOp := do
  match ← str j "op" with
  | "event" =>
    return .event {
      kind := ← parseEventKind (← str j "kind")
      nowMs := ← nat j "now_ms"
      cniIndex := ← nat j "cni_index"
      exitCode := ← nat j "exit_code"
      ok := ← bool j "ok"
    }
  | "next_step" =>
    return .nextStep (← nat j "now_ms")
  | "backoff" =>
    return .backoff (← nat j "base_ms") (← nat j "max_ms") (← nat j "attempt")
  | other =>
    throw s!"unknown op: {other}"

/-- Parse one JSON line into a `node` DRT `Case`. -/
def parseCase (line : String) : Except String Case := do
  let j ← Json.parse line
  let id ← nat j "id"
  let spec ← parseSpec (← j.getObjVal? "spec")
  let ops ← list j "ops" parseOp
  return { id := id, spec := spec, ops := ops }

private def drtStep (spec : Spec) (s : State) : DrtOp → Step × State
  | .event ev =>
    let s' := applyEvent spec s ev
    ({ action := .none, state := s', wellFormed := wellFormed spec s', backoffMs := 0 }, s')
  | .nextStep nowMs =>
    let act := nextStep spec s nowMs
    ({ action := act, state := s, wellFormed := wellFormed spec s, backoffMs := 0 }, s)
  | .backoff baseMs maxMs attempt =>
    let d := backoffMs baseMs maxMs attempt
    ({ action := .none, state := s, wellFormed := wellFormed spec s, backoffMs := d }, s)

/-- Run every operation in `c` and return the step snapshots in order. -/
def run (c : Case) : List Step :=
  let rec go (s : State) : List DrtOp → List Step
    | [] => []
    | op :: ops =>
      let (out, s') := drtStep c.spec s op
      out :: go s' ops
  go State.init c.ops

private def phaseName : Phase → String
  | .init => "init"
  | .starting => "starting"
  | .running => "running"
  | .backoff => "backoff"
  | .rollingBack => "rolling_back"
  | .stopping => "stopping"
  | .stopped => "stopped"

private def mkActionJson (kind : String) (cniIndex waitUntilMs : Nat) : Json :=
  Json.mkObj [
    ("kind", toJson kind),
    ("cni_index", toJson cniIndex),
    ("wait_until_ms", toJson waitUntilMs)
  ]

private def actionToJson : Action → Json
  | .none => mkActionJson "none" 0 0
  | .wait u => mkActionJson "wait" 0 u
  | .createNetns => mkActionJson "create_netns" 0 0
  | .addCNI i => mkActionJson "add_cni" i 0
  | .ensureImage => mkActionJson "ensure_image" 0 0
  | .createContainer => mkActionJson "create_container" 0 0
  | .startTask => mkActionJson "start_task" 0 0
  | .termTask => mkActionJson "term_task" 0 0
  | .killTask => mkActionJson "kill_task" 0 0
  | .deleteTask => mkActionJson "delete_task" 0 0
  | .deleteContainer => mkActionJson "delete_container" 0 0
  | .delCNI i => mkActionJson "del_cni" i 0
  | .removeNetns => mkActionJson "remove_netns" 0 0

private def resourcesToJson (r : Resources) : Json :=
  Json.mkObj [
    ("netns_created", toJson r.netnsCreated),
    ("cni_added", toJson r.cniAdded),
    ("image_ready", toJson r.imageReady),
    ("container_created", toJson r.containerCreated),
    ("task_created", toJson r.taskCreated),
    ("task_running", toJson r.taskRunning),
    ("stop_sent", toJson r.stopSent),
    ("kill_sent", toJson r.killSent)
  ]

private def stateToJson (s : State) : Json :=
  Json.mkObj [
    ("phase", toJson (phaseName s.phase)),
    ("desired_running", toJson s.desiredRunning),
    ("resources", resourcesToJson s.resources),
    ("failed", toJson s.failed),
    ("cleanup_errors", toJson s.cleanupErrors),
    ("restart_count", toJson s.restartCount),
    ("backoff_attempt", toJson s.backoffAttempt),
    ("has_exited", toJson s.hasExited),
    ("last_exit_code", toJson s.lastExitCode),
    ("started_at_ms", toJson s.startedAtMs),
    ("stop_sent_at_ms", toJson s.stopSentAtMs),
    ("next_start_at_ms", toJson s.nextStartAtMs)
  ]

private def stepToJson (out : Step) : Json :=
  Json.mkObj [
    ("action", actionToJson out.action),
    ("state", stateToJson out.state),
    ("well_formed", toJson out.wellFormed),
    ("backoff_ms", toJson out.backoffMs)
  ]

/-- Evaluate `c` and render the DRT output JSON object. -/
def caseToJson (c : Case) : Json :=
  Json.mkObj [
    ("id", toJson c.id),
    ("steps", Json.arr ((run c).map stepToJson).toArray)
  ]

end Nayutar.Drt.Node
