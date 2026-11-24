import Nayutar.Node

set_option autoImplicit false

namespace Nayutar.Node

theorem step_preserves_hierarchy (spec : Spec) (s : State) (ev : Event)
    (h : WellFormed spec s) :
    WellFormed spec (applyEvent spec s ev) := by
  sorry

theorem delete_container_requires_task_stopped (spec : Spec) (s : State) (nowMs : Nat) (ev : Event) :
    (nextStep spec s nowMs = .deleteTask →
      s.resources.taskRunning = false ∧ s.resources.taskCreated = true) ∧
    (nextStep spec s nowMs = .deleteContainer →
      s.resources.taskRunning = false ∧ s.resources.taskCreated = false ∧
      s.resources.containerCreated = true) ∧
    (s.resources.taskRunning = true → ev.kind = .taskDeleted → applyEvent spec s ev = s) ∧
    ((s.resources.taskRunning = true ∨ s.resources.taskCreated = true) →
      ev.kind = .containerDeleted → applyEvent spec s ev = s) := by
  sorry

theorem del_cni_reverse_and_safe (spec : Spec) (s : State) (nowMs idx : Nat) (ev : Event) :
    (nextStep spec s nowMs = .delCNI idx →
      s.resources.taskRunning = false ∧ s.resources.taskCreated = false ∧
      s.resources.containerCreated = false ∧ s.resources.cniAdded > 0 ∧
      idx = s.resources.cniAdded - 1) ∧
    (ev.kind = .cniDeleted → applyEvent spec s ev ≠ s →
      s.resources.taskRunning = false ∧ s.resources.taskCreated = false ∧
      s.resources.containerCreated = false ∧ s.resources.imageReady = false ∧
      s.resources.cniAdded > 0 ∧ ev.cniIndex = s.resources.cniAdded - 1 ∧
      (applyEvent spec s ev).resources.cniAdded = s.resources.cniAdded - 1) := by
  sorry

theorem remove_netns_requires_clean_stack (spec : Spec) (s : State) (nowMs : Nat) (ev : Event) :
    (nextStep spec s nowMs = .removeNetns →
      s.resources.taskRunning = false ∧ s.resources.taskCreated = false ∧
      s.resources.containerCreated = false ∧ s.resources.cniAdded = 0 ∧
      s.resources.netnsCreated = true) ∧
    (ev.kind = .netnsRemoved → applyEvent spec s ev ≠ s →
      s.resources.taskRunning = false ∧ s.resources.taskCreated = false ∧
      s.resources.stopSent = false ∧ s.resources.killSent = false ∧
      s.resources.containerCreated = false ∧ s.resources.imageReady = false ∧
      s.resources.cniAdded = 0 ∧ s.resources.netnsCreated = true ∧
      (applyEvent spec s ev).resources.empty = true ∧
      (applyEvent spec s ev).phase = .stopped) := by
  sorry

theorem stopped_has_no_leaks (spec : Spec) (evs : List Event)
    (h : (run spec evs).phase = .stopped) :
    (run spec evs).resources.netnsCreated = false ∧
    (run spec evs).resources.cniAdded = 0 ∧
    (run spec evs).resources.imageReady = false ∧
    (run spec evs).resources.containerCreated = false ∧
    (run spec evs).resources.taskCreated = false ∧
    (run spec evs).resources.taskRunning = false ∧
    (run spec evs).resources.stopSent = false ∧
    (run spec evs).resources.killSent = false := by
  sorry

theorem kill_requires_grace_elapsed (spec : Spec) (s : State) (nowMs : Nat) (ev : Event) :
    (nextStep spec s nowMs = .killTask →
      s.resources.taskRunning = true ∧ s.resources.stopSent = true ∧
      s.resources.killSent = false ∧ s.stopSentAtMs + spec.stopGraceMs ≤ nowMs) ∧
    (ev.kind = .killSent → applyEvent spec s ev ≠ s →
      s.resources.taskRunning = true ∧ s.resources.stopSent = true ∧
      s.resources.killSent = false ∧ s.stopSentAtMs + spec.stopGraceMs ≤ ev.nowMs) := by
  sorry

theorem restart_policy_sound (spec : Spec) (s : State) (ev : Event)
    (hph : s.phase = .running) (hrun : s.resources.taskRunning = true)
    (hev : ev.kind = .taskExited) :
    (spec.restartPolicy = .never →
      (applyEvent spec s ev).phase = .stopping ∧
      (applyEvent spec s ev).restartCount = s.restartCount) ∧
    (spec.restartPolicy = .onFailure → ev.exitCode = 0 →
      (applyEvent spec s ev).phase = .stopping ∧
      (applyEvent spec s ev).restartCount = s.restartCount) ∧
    (spec.restartPolicy = .onFailure → ev.exitCode ≠ 0 → s.desiredRunning = true →
      (applyEvent spec s ev).phase = .backoff ∧
      (applyEvent spec s ev).restartCount = s.restartCount + 1) ∧
    (spec.restartPolicy = .always → s.desiredRunning = true →
      (applyEvent spec s ev).phase = .backoff ∧
      (applyEvent spec s ev).restartCount = s.restartCount + 1) := by
  sorry

theorem backoff_bounded_and_monotone (baseMs maxMs a b : Nat) :
    (baseMs ≤ maxMs →
      baseMs ≤ backoffMs baseMs maxMs a ∧ backoffMs baseMs maxMs a ≤ maxMs) ∧
    (a ≤ b → backoffMs baseMs maxMs a ≤ backoffMs baseMs maxMs b) := by
  sorry

theorem backoff_resets_after_healthy_run (spec : Spec) (s : State) (ev : Event)
    (hrun : s.resources.taskRunning = true)
    (hph : s.phase = .running ∨ s.phase = .stopping ∨ s.phase = .rollingBack)
    (hev : ev.kind = .taskExited)
    (hhealthy : s.startedAtMs + spec.backoffResetMs ≤ ev.nowMs) :
    (applyEvent spec s ev).backoffAttempt = 0 ∧
    (s.phase = .running → s.desiredRunning = true →
      shouldRestart spec.restartPolicy ev.exitCode = true →
      (applyEvent spec s ev).nextStartAtMs = ev.nowMs + backoffMs spec.backoffBaseMs spec.backoffMaxMs 0) := by
  sorry

end Nayutar.Node
