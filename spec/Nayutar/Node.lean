set_option autoImplicit false

namespace Nayutar.Node

/-- Restart policy controlling whether a container unit restarts after its task exits. -/
inductive RestartPolicy where
  | never
  | onFailure
  | always
  deriving Repr, DecidableEq

/-- Whether a task exiting with `exitCode` restarts under `policy`. -/
def shouldRestart (policy : RestartPolicy) (exitCode : Nat) : Bool :=
  match policy with
  | .always => true
  | .onFailure => exitCode != 0
  | .never => false

/-- Immutable lifecycle parameters for one container unit in integer milliseconds. -/
structure Spec where
  restartPolicy : RestartPolicy
  stopGraceMs : Nat
  backoffBaseMs : Nat
  backoffMaxMs : Nat
  backoffResetMs : Nat
  cniCount : Nat
  deriving Repr, DecidableEq

/-- Minimum of two natural numbers. -/
def natMin (a b : Nat) : Nat :=
  if a ≤ b then a else b

/-- Saturating exponential doubling loop bounded by `maxMs`. -/
def backoffLoop (maxMs : Nat) : Nat → Nat → Nat
  | 0, d => d
  | k + 1, d =>
    if d ≥ maxMs then maxMs
    else backoffLoop maxMs k (natMin maxMs (d * 2))

/-- Saturating integer exponential backoff `min(maxMs, baseMs * 2^attempt)`. -/
def backoffMs (baseMs maxMs attempt : Nat) : Nat :=
  if baseMs == 0 || maxMs == 0 then 0
  else backoffLoop maxMs attempt (natMin baseMs maxMs)

/-- Lifecycle phase of one container unit. -/
inductive Phase where
  | init
  | starting
  | running
  | backoff
  | rollingBack
  | stopping
  | stopped
  deriving Repr, DecidableEq

/-- Whether `p` is a teardown phase (`stopping` or `rollingBack`). -/
def isTeardownPhase : Phase → Bool
  | .stopping | .rollingBack => true
  | _ => false

/-- External resources currently held (or attempted for CNI) by one unit. -/
structure Resources where
  netnsCreated : Bool := false
  cniAdded : Nat := 0
  imageReady : Bool := false
  containerCreated : Bool := false
  taskCreated : Bool := false
  taskRunning : Bool := false
  stopSent : Bool := false
  killSent : Bool := false
  deriving Repr, DecidableEq

/-- Whether zero external resources are held. -/
def Resources.empty (r : Resources) : Bool :=
  !r.netnsCreated &&
  r.cniAdded == 0 &&
  !r.imageReady &&
  !r.containerCreated &&
  !r.taskCreated &&
  !r.taskRunning &&
  !r.stopSent &&
  !r.killSent

/-- Complete pure lifecycle state of one container unit. -/
structure State where
  phase : Phase := .init
  desiredRunning : Bool := false
  resources : Resources := {}
  failed : Bool := false
  cleanupErrors : Nat := 0
  restartCount : Nat := 0
  backoffAttempt : Nat := 0
  hasExited : Bool := false
  lastExitCode : Nat := 0
  startedAtMs : Nat := 0
  stopSentAtMs : Nat := 0
  nextStartAtMs : Nat := 0
  deriving Repr, DecidableEq

/-- Initial idle unit state with zero held resources. -/
def State.init : State := {}

/-- Decidable check of the resource dependency hierarchy and phase invariant. -/
def wellFormed (spec : Spec) (s : State) : Bool :=
  let r := s.resources
  let hier :=
    (!r.killSent || r.stopSent) &&
    (!r.stopSent || r.taskCreated) &&
    (!r.taskRunning || r.taskCreated) &&
    (!r.taskCreated || r.containerCreated) &&
    (!r.containerCreated || r.imageReady) &&
    (!r.imageReady || (r.netnsCreated && r.cniAdded == spec.cniCount)) &&
    decide (r.cniAdded ≤ spec.cniCount) &&
    (r.cniAdded == 0 || r.netnsCreated)
  let phaseOk :=
    match s.phase with
    | .init | .stopped => r.empty && !s.desiredRunning
    | .starting => s.desiredRunning && !r.taskRunning && !r.taskCreated
    | .running => r.taskRunning
    | .backoff => s.desiredRunning && !r.taskRunning && r.imageReady
    | .stopping | .rollingBack => !s.desiredRunning && r.netnsCreated
  hier && phaseOk

/-- Resource dependency hierarchy invariant for a unit resource stack. -/
def Hierarchy (spec : Spec) (r : Resources) : Prop :=
  (r.killSent = true → r.stopSent = true) ∧
  (r.stopSent = true → r.taskCreated = true) ∧
  (r.taskRunning = true → r.taskCreated = true) ∧
  (r.taskCreated = true → r.containerCreated = true) ∧
  (r.containerCreated = true → r.imageReady = true) ∧
  (r.imageReady = true → r.netnsCreated = true ∧ r.cniAdded = spec.cniCount) ∧
  r.cniAdded ≤ spec.cniCount ∧
  (r.cniAdded > 0 → r.netnsCreated = true)

/-- Full well-formedness invariant combining the resource hierarchy and phase constraints. -/
def WellFormed (spec : Spec) (s : State) : Prop :=
  Hierarchy spec s.resources ∧
  match s.phase with
  | .init | .stopped => s.resources.empty = true ∧ s.desiredRunning = false
  | .starting => s.desiredRunning = true ∧ s.resources.taskRunning = false ∧ s.resources.taskCreated = false
  | .running => s.resources.taskRunning = true
  | .backoff => s.desiredRunning = true ∧ s.resources.taskRunning = false ∧ s.resources.imageReady = true
  | .stopping | .rollingBack => s.desiredRunning = false ∧ s.resources.netnsCreated = true

/-- Next external action selected by `nextStep`. -/
inductive Action where
  | none
  | wait (waitUntilMs : Nat)
  | createNetns
  | addCNI (cniIndex : Nat)
  | ensureImage
  | createContainer
  | startTask
  | termTask
  | killTask
  | deleteTask
  | deleteContainer
  | delCNI (cniIndex : Nat)
  | removeNetns
  deriving Repr, DecidableEq

/-- Whether `nowMs` has reached or passed `sentAtMs + graceMs` without underflow. -/
def graceElapsed (nowMs sentAtMs graceMs : Nat) : Bool :=
  decide (nowMs ≥ sentAtMs ∧ nowMs - sentAtMs ≥ graceMs)

/-- Stop-task escalation sub-step (`SIGTERM` -> grace wait -> `SIGKILL`). -/
def stopTaskAction (spec : Spec) (s : State) (nowMs : Nat) : Action :=
  if !s.resources.stopSent then
    .termTask
  else if !s.resources.killSent then
    if graceElapsed nowMs s.stopSentAtMs spec.stopGraceMs then
      .killTask
    else
      .wait (s.stopSentAtMs + spec.stopGraceMs)
  else
    .wait 0

/-- Next external action required for `s` at monotonic timestamp `nowMs`. -/
def nextStep (spec : Spec) (s : State) (nowMs : Nat) : Action :=
  let r := s.resources
  match s.phase with
  | .init | .stopped => .none
  | .running => .wait 0
  | .starting =>
    if !r.netnsCreated then .createNetns
    else if r.cniAdded < spec.cniCount then .addCNI r.cniAdded
    else if !r.imageReady then .ensureImage
    else if !r.containerCreated then .createContainer
    else if !r.taskCreated then .startTask
    else .wait 0
  | .backoff =>
    if r.taskRunning then stopTaskAction spec s nowMs
    else if r.taskCreated then .deleteTask
    else if r.containerCreated then .deleteContainer
    else if nowMs < s.nextStartAtMs then .wait s.nextStartAtMs
    else .createContainer
  | .stopping | .rollingBack =>
    if r.taskRunning then stopTaskAction spec s nowMs
    else if r.taskCreated then .deleteTask
    else if r.containerCreated then .deleteContainer
    else if r.cniAdded > 0 then .delCNI (r.cniAdded - 1)
    else if r.netnsCreated then .removeNetns
    else .none

/-- Kind of discrete observation fed into `applyEvent`. -/
inductive EventKind where
  | startRequested
  | stopRequested
  | netnsCreated
  | netnsCreateFailed
  | cniAdded
  | cniAddFailed
  | imageReady
  | imageFailed
  | containerCreated
  | containerCreateFailed
  | taskStarted
  | taskStartFailed
  | termSent
  | killSent
  | taskExited
  | taskDeleted
  | containerDeleted
  | cniDeleted
  | netnsRemoved
  deriving Repr, DecidableEq

/-- One discrete observation fed into `applyEvent`. -/
structure Event where
  kind : EventKind
  nowMs : Nat := 0
  cniIndex : Nat := 0
  exitCode : Nat := 0
  ok : Bool := false
  deriving Repr, DecidableEq

/-- Guard for container creation in initial startup or after backoff expires. -/
def canCreateContainer (spec : Spec) (s : State) (nowMs : Nat) : Bool :=
  let r := s.resources
  if !r.netnsCreated || r.cniAdded != spec.cniCount || !r.imageReady ||
    r.containerCreated || r.taskCreated || r.taskRunning then
    false
  else if s.phase == .starting then
    true
  else
    s.phase == .backoff && decide (nowMs ≥ s.nextStartAtMs)

/-- Deterministic single-event transition; invalid or stale events are no-ops. -/
def applyEvent (spec : Spec) (s : State) (ev : Event) : State :=
  if s.phase == .stopped then
    s
  else
    match ev.kind with
    | .startRequested =>
      if s.phase != .init then s
      else { s with desiredRunning := true, phase := .starting }
    | .stopRequested =>
      match s.phase with
      | .init =>
        { s with desiredRunning := false, phase := .stopped }
      | .starting =>
        if s.resources.empty then
          { s with desiredRunning := false, phase := .stopped }
        else
          let r' :=
            if !s.resources.containerCreated then { s.resources with imageReady := false }
            else s.resources
          { s with desiredRunning := false, resources := r', phase := .stopping }
      | .running =>
        { s with desiredRunning := false, phase := .stopping }
      | .backoff =>
        let r' :=
          if !s.resources.containerCreated then { s.resources with imageReady := false }
          else s.resources
        { s with desiredRunning := false, resources := r', phase := .stopping }
      | _ => s
    | .netnsCreated =>
      if s.phase != .starting || !s.resources.empty then s
      else { s with resources := { s.resources with netnsCreated := true } }
    | .netnsCreateFailed =>
      if s.phase != .starting || !s.resources.empty then s
      else { s with failed := true, desiredRunning := false, phase := .stopped }
    | .cniAdded =>
      if s.phase != .starting || !s.resources.netnsCreated || s.resources.imageReady ||
        s.resources.containerCreated || s.resources.taskCreated || s.resources.taskRunning then s
      else if decide (s.resources.cniAdded ≥ spec.cniCount) || ev.cniIndex != s.resources.cniAdded then s
      else { s with resources := { s.resources with cniAdded := s.resources.cniAdded + 1 } }
    | .cniAddFailed =>
      if s.phase != .starting || !s.resources.netnsCreated || s.resources.imageReady ||
        s.resources.containerCreated || s.resources.taskCreated || s.resources.taskRunning then s
      else if decide (s.resources.cniAdded ≥ spec.cniCount) || ev.cniIndex != s.resources.cniAdded then s
      else
        { s with
          resources := { s.resources with cniAdded := s.resources.cniAdded + 1 }
          failed := true
          desiredRunning := false
          phase := .rollingBack
        }
    | .imageReady =>
      if s.phase != .starting || !s.resources.netnsCreated || s.resources.cniAdded != spec.cniCount ||
        s.resources.imageReady || s.resources.containerCreated || s.resources.taskCreated || s.resources.taskRunning then s
      else { s with resources := { s.resources with imageReady := true } }
    | .imageFailed =>
      if s.phase != .starting || !s.resources.netnsCreated || s.resources.cniAdded != spec.cniCount ||
        s.resources.imageReady || s.resources.containerCreated || s.resources.taskCreated || s.resources.taskRunning then s
      else { s with failed := true, desiredRunning := false, phase := .rollingBack }
    | .containerCreated =>
      if !canCreateContainer spec s ev.nowMs then s
      else { s with resources := { s.resources with containerCreated := true }, phase := .starting }
    | .containerCreateFailed =>
      if !canCreateContainer spec s ev.nowMs then s
      else
        { s with
          resources := { s.resources with imageReady := false }
          failed := true
          desiredRunning := false
          phase := .rollingBack
        }
    | .taskStarted =>
      if s.phase != .starting || !s.resources.containerCreated || s.resources.taskCreated || s.resources.taskRunning then s
      else
        let attempt' := if s.hasExited then s.backoffAttempt + 1 else s.backoffAttempt
        { s with
          backoffAttempt := attempt'
          resources := { s.resources with taskCreated := true, taskRunning := true }
          startedAtMs := ev.nowMs
          phase := .running
        }
    | .taskStartFailed =>
      if s.phase != .starting || !s.resources.containerCreated || s.resources.taskCreated || s.resources.taskRunning then s
      else { s with failed := true, desiredRunning := false, phase := .rollingBack }
    | .termSent =>
      if !isTeardownPhase s.phase || !s.resources.taskRunning || s.resources.stopSent then s
      else { s with resources := { s.resources with stopSent := true }, stopSentAtMs := ev.nowMs }
    | .killSent =>
      if !isTeardownPhase s.phase || !s.resources.taskRunning || !s.resources.stopSent || s.resources.killSent then s
      else if !graceElapsed ev.nowMs s.stopSentAtMs spec.stopGraceMs then s
      else { s with resources := { s.resources with killSent := true } }
    | .taskExited =>
      if !s.resources.taskRunning then s
      else if s.phase != .running && !isTeardownPhase s.phase then s
      else
        let attempt' :=
          if graceElapsed ev.nowMs s.startedAtMs spec.backoffResetMs then 0
          else s.backoffAttempt
        let base :=
          { s with
            resources := { s.resources with taskRunning := false }
            hasExited := true
            lastExitCode := ev.exitCode
            backoffAttempt := attempt'
          }
        if s.phase == .running then
          if s.desiredRunning && shouldRestart spec.restartPolicy ev.exitCode then
            let delay := backoffMs spec.backoffBaseMs spec.backoffMaxMs attempt'
            { base with
              nextStartAtMs := ev.nowMs + delay
              restartCount := s.restartCount + 1
              phase := .backoff
            }
          else
            { base with desiredRunning := false, phase := .stopping }
        else
          base
    | .taskDeleted =>
      if s.phase != .backoff && !isTeardownPhase s.phase then s
      else if s.resources.taskRunning || !s.resources.taskCreated then s
      else
        { s with
          resources := { s.resources with taskCreated := false, stopSent := false, killSent := false }
          cleanupErrors := if ev.ok then s.cleanupErrors else s.cleanupErrors + 1
        }
    | .containerDeleted =>
      if s.phase != .backoff && !isTeardownPhase s.phase then s
      else if s.resources.taskRunning || s.resources.taskCreated || !s.resources.containerCreated then s
      else
        { s with
          resources :=
            { s.resources with
              containerCreated := false
              imageReady := if isTeardownPhase s.phase then false else s.resources.imageReady
            }
          cleanupErrors := if ev.ok then s.cleanupErrors else s.cleanupErrors + 1
        }
    | .cniDeleted =>
      if !isTeardownPhase s.phase then s
      else if s.resources.taskRunning || s.resources.taskCreated || s.resources.containerCreated ||
        s.resources.imageReady || s.resources.cniAdded == 0 || ev.cniIndex != s.resources.cniAdded - 1 then s
      else
        { s with
          resources := { s.resources with cniAdded := s.resources.cniAdded - 1 }
          cleanupErrors := if ev.ok then s.cleanupErrors else s.cleanupErrors + 1
        }
    | .netnsRemoved =>
      if !isTeardownPhase s.phase then s
      else if s.resources.taskRunning || s.resources.taskCreated || s.resources.stopSent || s.resources.killSent ||
        s.resources.containerCreated || s.resources.imageReady || s.resources.cniAdded != 0 || !s.resources.netnsCreated then s
      else
        { s with
          resources := { s.resources with netnsCreated := false }
          cleanupErrors := if ev.ok then s.cleanupErrors else s.cleanupErrors + 1
          phase := .stopped
        }

/-- Fold a list of events starting from `State.init`. -/
def run (spec : Spec) (evs : List Event) : State :=
  evs.foldl (applyEvent spec) State.init

end Nayutar.Node
