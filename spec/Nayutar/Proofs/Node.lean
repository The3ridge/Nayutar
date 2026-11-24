import Nayutar.Node

set_option autoImplicit false

namespace Nayutar.Node

private theorem and_assoc_r {a b c : Prop} : ((a ∧ b) ∧ c) ↔ (a ∧ (b ∧ c)) :=
  ⟨fun ⟨⟨ha, hb⟩, hc⟩ => ⟨ha, hb, hc⟩, fun ⟨ha, hb, hc⟩ => ⟨⟨ha, hb⟩, hc⟩⟩

private theorem empty_eq_true_iff (r : Resources) :
    r.empty = true ↔
      r.netnsCreated = false ∧ r.cniAdded = 0 ∧ r.imageReady = false ∧
      r.containerCreated = false ∧ r.taskCreated = false ∧ r.taskRunning = false ∧
      r.stopSent = false ∧ r.killSent = false := by
  simp only [Resources.empty, Bool.and_eq_true, Bool.not_eq_true', beq_iff_eq, and_assoc_r]

private theorem not_empty_implies_netns {spec : Spec} {r : Resources}
    (hh : Hierarchy spec r) (hne : r.empty = false) : r.netnsCreated = true := by
  obtain ⟨hk, hs, htr, htc, hci, hin, _, hcn⟩ := hh
  cases hnet : r.netnsCreated
  · exfalso
    have hcni : r.cniAdded = 0 := by
      by_cases hpos : r.cniAdded > 0
      · have := hcn hpos
        rw [hnet] at this
        cases this
      · omega
    have himg : r.imageReady = false := by
      cases hi : r.imageReady
      · rfl
      · have := (hin hi).1
        rw [hnet] at this
        cases this
    have hcon : r.containerCreated = false := by
      cases hc : r.containerCreated
      · rfl
      · have := hci hc
        rw [himg] at this
        cases this
    have htcr : r.taskCreated = false := by
      cases ht : r.taskCreated
      · rfl
      · have := htc ht
        rw [hcon] at this
        cases this
    have htrun : r.taskRunning = false := by
      cases htr' : r.taskRunning
      · rfl
      · have := htr htr'
        rw [htcr] at this
        cases this
    have hstop : r.stopSent = false := by
      cases hst : r.stopSent
      · rfl
      · have := hs hst
        rw [htcr] at this
        cases this
    have hkill : r.killSent = false := by
      cases hkl : r.killSent
      · rfl
      · have := hk hkl
        rw [hstop] at this
        cases this
    have hemp : r.empty = true :=
      (empty_eq_true_iff r).mpr ⟨hnet, hcni, himg, hcon, htcr, htrun, hstop, hkill⟩
    rw [hemp] at hne
    cases hne
  · rfl

private theorem graceElapsed_iff (nowMs sentAtMs graceMs : Nat) :
    graceElapsed nowMs sentAtMs graceMs = true ↔ sentAtMs + graceMs ≤ nowMs := by
  simp only [graceElapsed, decide_eq_true_eq]
  omega

private theorem isTeardownPhase_iff (p : Phase) :
    isTeardownPhase p = true ↔ p = .stopping ∨ p = .rollingBack := by
  cases p <;> simp [isTeardownPhase]

private theorem init_wellFormed (spec : Spec) : WellFormed spec State.init := by
  refine ⟨⟨?_, ?_, ?_, ?_, ?_, ?_, ?_, ?_⟩, ?_⟩ <;> simp [State.init, Resources.empty]

private theorem canCreateContainer_spec {spec : Spec} {s : State} {nowMs : Nat}
    (h : canCreateContainer spec s nowMs = true) :
    s.resources.netnsCreated = true ∧ s.resources.cniAdded = spec.cniCount ∧
    s.resources.imageReady = true ∧ s.resources.containerCreated = false ∧
    s.resources.taskCreated = false ∧ s.resources.taskRunning = false ∧
    (s.phase = .starting ∨ (s.phase = .backoff ∧ s.nextStartAtMs ≤ nowMs)) := by
  simp only [canCreateContainer] at h
  split at h
  · cases h
  · rename_i hres
    simp [and_assoc_r] at hres
    obtain ⟨h1, h2, h3, h4, h5, h6⟩ := hres
    split at h
    · rename_i hst
      simp at hst
      exact ⟨h1, h2, h3, h4, h5, h6, Or.inl hst⟩
    · simp at h
      exact ⟨h1, h2, h3, h4, h5, h6, Or.inr h⟩

/-- Every single lifecycle event preserves the resource dependency hierarchy and phase invariant. -/
theorem step_preserves_hierarchy (spec : Spec) (s : State) (ev : Event)
    (h : WellFormed spec s) :
    WellFormed spec (applyEvent spec s ev) := by
  by_cases hst : s.phase = .stopped
  · simp [applyEvent, hst, h]
  · have hwf : WellFormed spec s := h
    obtain ⟨hh, hp⟩ := h
    obtain ⟨hk, hs, htr, htc, hci, hin, hle, hcn⟩ := hh
    have hh' : Hierarchy spec s.resources := ⟨hk, hs, htr, htc, hci, hin, hle, hcn⟩
    rcases ev with ⟨kind, nowMs, cniIndex, exitCode, ok⟩
    have hns : (s.phase == .stopped) = false := by
      cases hph : s.phase <;> simp [hph] at hst ⊢
    cases kind with
    | startRequested =>
      simp only [applyEvent, hns, Bool.false_eq_true, ite_false]
      split
      · exact hwf
      · rename_i hne
        simp at hne
        simp only [hne] at hp
        obtain ⟨hemp, _⟩ := hp
        obtain ⟨_, _, _, _, h5, h6, _, _⟩ := (empty_eq_true_iff s.resources).mp hemp
        exact ⟨hh', rfl, h6, h5⟩
    | stopRequested =>
      simp only [applyEvent, hns, Bool.false_eq_true, ite_false]
      cases hph : s.phase <;> simp only [hph] at hp ⊢
      · obtain ⟨hemp, _⟩ := hp
        exact ⟨hh', hemp, rfl⟩
      · split
        · rename_i hemp
          exact ⟨hh', hemp, rfl⟩
        · rename_i hne
          have hne' : s.resources.empty = false := by
            cases he : s.resources.empty
            · rfl
            · exact absurd he hne
          have hnet : s.resources.netnsCreated = true := not_empty_implies_netns hh' hne'
          split
          · rename_i hnc
            simp at hnc
            refine ⟨⟨hk, hs, htr, htc, ?_, ?_, hle, hcn⟩, rfl, hnet⟩
            · intro hc; rw [hnc] at hc; cases hc
            · intro hi; cases hi
          · exact ⟨hh', rfl, hnet⟩
      · have hnet : s.resources.netnsCreated = true := (hin (hci (htc (htr hp)))).1
        exact ⟨hh', rfl, hnet⟩
      · obtain ⟨_, _, himg⟩ := hp
        have hnet : s.resources.netnsCreated = true := (hin himg).1
        split
        · rename_i hnc
          simp at hnc
          refine ⟨⟨hk, hs, htr, htc, ?_, ?_, hle, hcn⟩, rfl, hnet⟩
          · intro hc; rw [hnc] at hc; cases hc
          · intro hi; cases hi
        · exact ⟨hh', rfl, hnet⟩
      · exact hwf
      · exact hwf
      · exact hwf
    | netnsCreated =>
      simp only [applyEvent, hns, Bool.false_eq_true, ite_false]
      split
      · exact hwf
      · rename_i hg
        simp at hg
        obtain ⟨hph, hemp⟩ := hg
        simp only [hph] at hp
        obtain ⟨hdes, _, _⟩ := hp
        obtain ⟨_, hcni, himg, hcon, htcr, htrun, hstop, hkill⟩ := (empty_eq_true_iff s.resources).mp hemp
        refine ⟨⟨?_, ?_, ?_, ?_, ?_, ?_, ?_, ?_⟩, ?_⟩
        · intro h1; rw [hkill] at h1; cases h1
        · intro h1; rw [hstop] at h1; cases h1
        · intro h1; rw [htrun] at h1; cases h1
        · intro h1; rw [htcr] at h1; cases h1
        · intro h1; rw [hcon] at h1; cases h1
        · intro h1; rw [himg] at h1; cases h1
        · omega
        · intro _; rfl
        · simp only [hph]; exact ⟨hdes, htrun, htcr⟩
    | netnsCreateFailed =>
      simp only [applyEvent, hns, Bool.false_eq_true, ite_false]
      split
      · exact hwf
      · rename_i hg
        simp at hg
        exact ⟨hh', hg.2, rfl⟩
    | cniAdded =>
      simp only [applyEvent, hns, Bool.false_eq_true, ite_false]
      split
      · exact hwf
      · split
        · exact hwf
        · rename_i hg1 hg2
          simp [and_assoc_r] at hg1
          simp at hg2
          obtain ⟨hph, hnet, himg, hcon, htcr, htrun⟩ := hg1
          obtain ⟨hlt, _⟩ := hg2
          simp only [hph] at hp
          refine ⟨⟨hk, hs, htr, htc, hci, ?_, ?_, ?_⟩, ?_⟩
          · intro h1; rw [himg] at h1; cases h1
          · dsimp only; omega
          · intro _; exact hnet
          · simp only [hph]; exact hp
    | cniAddFailed =>
      simp only [applyEvent, hns, Bool.false_eq_true, ite_false]
      split
      · exact hwf
      · split
        · exact hwf
        · rename_i hg1 hg2
          simp [and_assoc_r] at hg1
          simp at hg2
          obtain ⟨_, hnet, himg, _, _, _⟩ := hg1
          obtain ⟨hlt, _⟩ := hg2
          refine ⟨⟨hk, hs, htr, htc, hci, ?_, ?_, ?_⟩, rfl, hnet⟩
          · intro h1; rw [himg] at h1; cases h1
          · dsimp only; omega
          · intro _; exact hnet
    | imageReady =>
      simp only [applyEvent, hns, Bool.false_eq_true, ite_false]
      split
      · exact hwf
      · rename_i hg
        simp [and_assoc_r] at hg
        obtain ⟨hph, hnet, hcni, _, _, _, _⟩ := hg
        simp only [hph] at hp
        refine ⟨⟨hk, hs, htr, htc, ?_, ?_, hle, hcn⟩, ?_⟩
        · intro _; rfl
        · intro _; exact ⟨hnet, hcni⟩
        · simp only [hph]; exact hp
    | imageFailed =>
      simp only [applyEvent, hns, Bool.false_eq_true, ite_false]
      split
      · exact hwf
      · rename_i hg
        simp [and_assoc_r] at hg
        obtain ⟨_, hnet, _, _, _, _, _⟩ := hg
        exact ⟨hh', rfl, hnet⟩
    | containerCreated =>
      simp only [applyEvent, hns, Bool.false_eq_true, ite_false]
      split
      · exact hwf
      · rename_i hg
        simp at hg
        obtain ⟨_, _, himg, _, htcr, htrun, hph⟩ := canCreateContainer_spec hg
        have hdes : s.desiredRunning = true := by
          rcases hph with hph | ⟨hph, _⟩
          · simp only [hph] at hp; exact hp.1
          · simp only [hph] at hp; exact hp.1
        refine ⟨⟨hk, hs, htr, ?_, ?_, hin, hle, hcn⟩, hdes, htrun, htcr⟩
        · intro _; rfl
        · intro _; exact himg
    | containerCreateFailed =>
      simp only [applyEvent, hns, Bool.false_eq_true, ite_false]
      split
      · exact hwf
      · rename_i hg
        simp at hg
        obtain ⟨hnet, _, _, hcon, _, _, _⟩ := canCreateContainer_spec hg
        refine ⟨⟨hk, hs, htr, htc, ?_, ?_, hle, hcn⟩, rfl, hnet⟩
        · intro h1; rw [hcon] at h1; cases h1
        · intro h1; cases h1
    | taskStarted =>
      simp only [applyEvent, hns, Bool.false_eq_true, ite_false]
      split
      · exact hwf
      · rename_i hg
        simp [and_assoc_r] at hg
        obtain ⟨_, hcon, _, _⟩ := hg
        refine ⟨⟨hk, ?_, ?_, ?_, hci, hin, hle, hcn⟩, rfl⟩
        · intro _; rfl
        · intro _; rfl
        · intro _; exact hcon
    | taskStartFailed =>
      simp only [applyEvent, hns, Bool.false_eq_true, ite_false]
      split
      · exact hwf
      · rename_i hg
        simp [and_assoc_r] at hg
        obtain ⟨_, hcon, _, _⟩ := hg
        exact ⟨hh', rfl, (hin (hci hcon)).1⟩
    | termSent =>
      simp only [applyEvent, hns, Bool.false_eq_true, ite_false]
      split
      · exact hwf
      · rename_i hg
        simp [and_assoc_r] at hg
        obtain ⟨htd, hrun, _⟩ := hg
        have htcr : s.resources.taskCreated = true := htr hrun
        refine ⟨⟨?_, ?_, htr, htc, hci, hin, hle, hcn⟩, ?_⟩
        · intro _; rfl
        · intro _; exact htcr
        · rcases (isTeardownPhase_iff s.phase).mp htd with hph | hph <;>
            (simp only [hph] at hp ⊢; exact hp)
    | killSent =>
      simp only [applyEvent, hns, Bool.false_eq_true, ite_false]
      split
      · exact hwf
      · split
        · exact hwf
        · rename_i hg1 _
          simp [and_assoc_r] at hg1
          obtain ⟨htd, _, hstop, _⟩ := hg1
          refine ⟨⟨?_, hs, htr, htc, hci, hin, hle, hcn⟩, ?_⟩
          · intro _; exact hstop
          · rcases (isTeardownPhase_iff s.phase).mp htd with hph | hph <;>
              (simp only [hph] at hp ⊢; exact hp)
    | taskExited =>
      simp only [applyEvent, hns, Bool.false_eq_true, ite_false]
      split
      · exact hwf
      · split
        · exact hwf
        · rename_i hrun hphg
          simp at hrun hphg
          have hhNew : Hierarchy spec { s.resources with taskRunning := false } :=
            ⟨hk, hs, (fun h1 => by cases h1), htc, hci, hin, hle, hcn⟩
          have himg : s.resources.imageReady = true := hci (htc (htr hrun))
          have hnet : s.resources.netnsCreated = true := (hin himg).1
          by_cases hrunPh : s.phase = .running
          · simp only [hrunPh, beq_self_eq_true, ite_true]
            by_cases hres : (s.desiredRunning && shouldRestart spec.restartPolicy exitCode) = true
            · simp only [hres, ite_true]
              simp only [Bool.and_eq_true] at hres
              exact ⟨hhNew, hres.1, rfl, himg⟩
            · simp only [hres]
              exact ⟨hhNew, rfl, hnet⟩
          · have hnrp : (s.phase == .running) = false := by
              cases hph : s.phase <;> simp [hph] at hrunPh ⊢
            simp only [hnrp, Bool.false_eq_true, ite_false]
            have htd : isTeardownPhase s.phase = true := by
              cases htd' : isTeardownPhase s.phase
              · simp [hrunPh, htd'] at hphg
              · rfl
            rcases (isTeardownPhase_iff s.phase).mp htd with hph | hph
            · simp only [hph] at hp
              simp only [WellFormed, hph]
              exact ⟨hhNew, hp⟩
            · simp only [hph] at hp
              simp only [WellFormed, hph]
              exact ⟨hhNew, hp⟩
    | taskDeleted =>
      simp only [applyEvent, hns, Bool.false_eq_true, ite_false]
      split
      · exact hwf
      · split
        · exact hwf
        · rename_i hphg hrg
          simp at hphg hrg
          obtain ⟨htrun, _⟩ := hrg
          have hhNew : Hierarchy spec
              { s.resources with taskCreated := false, stopSent := false, killSent := false } := by
            refine ⟨?_, ?_, ?_, ?_, hci, hin, hle, hcn⟩
            · intro h1; cases h1
            · intro h1; cases h1
            · intro h1; rw [htrun] at h1; cases h1
            · intro h1; cases h1
          refine ⟨hhNew, ?_⟩
          cases hph : s.phase <;> simp [hph, isTeardownPhase] at hphg hp ⊢ <;> exact hp
    | containerDeleted =>
      simp only [applyEvent, hns, Bool.false_eq_true, ite_false]
      split
      · exact hwf
      · split
        · exact hwf
        · rename_i hphg hrg
          simp at hphg
          simp [and_assoc_r] at hrg
          obtain ⟨_, htcr, _⟩ := hrg
          cases hph : s.phase <;> simp [hph, isTeardownPhase] at hphg hp ⊢
          · refine ⟨⟨hk, hs, htr, ?_, ?_, hin, hle, hcn⟩, hp⟩
            · intro h1; rw [htcr] at h1; cases h1
            · intro h1; cases h1
          · refine ⟨⟨hk, hs, htr, ?_, ?_, ?_, hle, hcn⟩, hp⟩
            · intro h1; rw [htcr] at h1; cases h1
            · intro h1; cases h1
            · intro h1; cases h1
          · refine ⟨⟨hk, hs, htr, ?_, ?_, ?_, hle, hcn⟩, hp⟩
            · intro h1; rw [htcr] at h1; cases h1
            · intro h1; cases h1
            · intro h1; cases h1
    | cniDeleted =>
      simp only [applyEvent, hns, Bool.false_eq_true, ite_false]
      split
      · exact hwf
      · split
        · exact hwf
        · rename_i htd hrg
          simp at htd
          simp [and_assoc_r] at hrg
          obtain ⟨_, _, _, himg, hcni, _⟩ := hrg
          have hhNew : Hierarchy spec { s.resources with cniAdded := s.resources.cniAdded - 1 } := by
            refine ⟨hk, hs, htr, htc, hci, ?_, ?_, ?_⟩
            · intro h1; rw [himg] at h1; cases h1
            · dsimp only; omega
            · intro _; exact hcn (by omega)
          refine ⟨hhNew, ?_⟩
          rcases (isTeardownPhase_iff s.phase).mp htd with hph | hph <;>
            (simp only [hph] at hp ⊢; exact hp)
    | netnsRemoved =>
      simp only [applyEvent, hns, Bool.false_eq_true, ite_false]
      split
      · exact hwf
      · split
        · exact hwf
        · rename_i htd hrg
          simp at htd
          simp [and_assoc_r] at hrg
          obtain ⟨htrun, htcr, hstop, hkill, hcon, himg, hcni, _⟩ := hrg
          have hdes : s.desiredRunning = false := by
            rcases (isTeardownPhase_iff s.phase).mp htd with hph | hph <;>
              (simp only [hph] at hp; exact hp.1)
          have hemp : ({ s.resources with netnsCreated := false } : Resources).empty = true :=
            (empty_eq_true_iff _).mpr ⟨rfl, hcni, himg, hcon, htcr, htrun, hstop, hkill⟩
          refine ⟨⟨?_, ?_, ?_, ?_, ?_, ?_, ?_, ?_⟩, hemp, hdes⟩
          · intro h1; rw [hkill] at h1; cases h1
          · intro h1; rw [hstop] at h1; cases h1
          · intro h1; rw [htrun] at h1; cases h1
          · intro h1; rw [htcr] at h1; cases h1
          · intro h1; rw [hcon] at h1; cases h1
          · intro h1; rw [himg] at h1; cases h1
          · omega
          · intro h1; dsimp only at h1; omega

private theorem wellFormed_foldl (spec : Spec) (evs : List Event) (s : State)
    (h : WellFormed spec s) : WellFormed spec (evs.foldl (applyEvent spec) s) := by
  induction evs generalizing s with
  | nil => exact h
  | cons ev rest ih => exact ih _ (step_preserves_hierarchy spec s ev h)

private theorem stopTaskAction_ne_deleteTask (spec : Spec) (s : State) (nowMs : Nat) :
    stopTaskAction spec s nowMs ≠ .deleteTask := by
  simp only [stopTaskAction]
  split
  · intro h; cases h
  · split
    · split <;> (intro h; cases h)
    · intro h; cases h

private theorem stopTaskAction_ne_deleteContainer (spec : Spec) (s : State) (nowMs : Nat) :
    stopTaskAction spec s nowMs ≠ .deleteContainer := by
  simp only [stopTaskAction]
  split
  · intro h; cases h
  · split
    · split <;> (intro h; cases h)
    · intro h; cases h

private theorem stopTaskAction_ne_delCNI (spec : Spec) (s : State) (nowMs idx : Nat) :
    stopTaskAction spec s nowMs ≠ .delCNI idx := by
  simp only [stopTaskAction]
  split
  · intro h; cases h
  · split
    · split <;> (intro h; cases h)
    · intro h; cases h

private theorem stopTaskAction_ne_removeNetns (spec : Spec) (s : State) (nowMs : Nat) :
    stopTaskAction spec s nowMs ≠ .removeNetns := by
  simp only [stopTaskAction]
  split
  · intro h; cases h
  · split
    · split <;> (intro h; cases h)
    · intro h; cases h

/-- Neither the task nor the container can be deleted while the task is still running, and the
container cannot be deleted while the task still exists. -/
theorem delete_container_requires_task_stopped (spec : Spec) (s : State) (nowMs : Nat) (ev : Event) :
    (nextStep spec s nowMs = .deleteTask →
      s.resources.taskRunning = false ∧ s.resources.taskCreated = true) ∧
    (nextStep spec s nowMs = .deleteContainer →
      s.resources.taskRunning = false ∧ s.resources.taskCreated = false ∧
      s.resources.containerCreated = true) ∧
    (s.resources.taskRunning = true → ev.kind = .taskDeleted → applyEvent spec s ev = s) ∧
    ((s.resources.taskRunning = true ∨ s.resources.taskCreated = true) →
      ev.kind = .containerDeleted → applyEvent spec s ev = s) := by
  refine ⟨?_, ?_, ?_, ?_⟩
  · intro h
    cases hph : s.phase <;> simp only [nextStep, hph] at h
    · cases h
    · repeat (split at h; try cases h)
      cases h
    · cases h
    · split at h
      · exact absurd h (stopTaskAction_ne_deleteTask spec s nowMs)
      · split at h
        · rename_i hnr htc
          simp at hnr
          exact ⟨hnr, htc⟩
        · repeat (split at h; try cases h)
          cases h
    · split at h
      · exact absurd h (stopTaskAction_ne_deleteTask spec s nowMs)
      · split at h
        · rename_i hnr htc
          simp at hnr
          exact ⟨hnr, htc⟩
        · repeat (split at h; try cases h)
          cases h
    · split at h
      · exact absurd h (stopTaskAction_ne_deleteTask spec s nowMs)
      · split at h
        · rename_i hnr htc
          simp at hnr
          exact ⟨hnr, htc⟩
        · repeat (split at h; try cases h)
          cases h
    · cases h
  · intro h
    cases hph : s.phase <;> simp only [nextStep, hph] at h
    · cases h
    · repeat (split at h; try cases h)
      cases h
    · cases h
    · split at h
      · exact absurd h (stopTaskAction_ne_deleteContainer spec s nowMs)
      · split at h
        · cases h
        · split at h
          · rename_i hnr hnt hcon
            simp at hnr hnt
            exact ⟨hnr, hnt, hcon⟩
          · split at h <;> cases h
    · split at h
      · exact absurd h (stopTaskAction_ne_deleteContainer spec s nowMs)
      · split at h
        · cases h
        · split at h
          · rename_i hnr hnt hcon
            simp at hnr hnt
            exact ⟨hnr, hnt, hcon⟩
          · repeat (split at h; try cases h)
            cases h
    · split at h
      · exact absurd h (stopTaskAction_ne_deleteContainer spec s nowMs)
      · split at h
        · cases h
        · split at h
          · rename_i hnr hnt hcon
            simp at hnr hnt
            exact ⟨hnr, hnt, hcon⟩
          · repeat (split at h; try cases h)
            cases h
    · cases h
  · intro hrun hev
    simp only [applyEvent, hev]
    split
    · rfl
    · split
      · rfl
      · simp [hrun]
  · intro hrc hev
    simp only [applyEvent, hev]
    split
    · rfl
    · split
      · rfl
      · rcases hrc with hrun | htcr
        · simp [hrun]
        · simp [htcr]

/-- CNI `DEL` requires no running task, task object, or container, and strictly targets and
decrements `cniAdded` in reverse order `(k - 1)`. -/
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
  constructor
  · intro h
    cases hph : s.phase <;> simp only [nextStep, hph] at h
    · cases h
    · repeat (split at h; try cases h)
      cases h
    · cases h
    · split at h
      · exact absurd h (stopTaskAction_ne_delCNI spec s nowMs idx)
      · repeat (split at h; try cases h)
        cases h
    · split at h
      · exact absurd h (stopTaskAction_ne_delCNI spec s nowMs idx)
      · split at h
        · cases h
        · split at h
          · cases h
          · split at h
            · rename_i hnr hnt hnc hpos
              simp at hnr hnt hnc
              cases h
              exact ⟨hnr, hnt, hnc, hpos, rfl⟩
            · split at h <;> cases h
    · split at h
      · exact absurd h (stopTaskAction_ne_delCNI spec s nowMs idx)
      · split at h
        · cases h
        · split at h
          · cases h
          · split at h
            · rename_i hnr hnt hnc hpos
              simp at hnr hnt hnc
              cases h
              exact ⟨hnr, hnt, hnc, hpos, rfl⟩
            · split at h <;> cases h
    · cases h
  · intro hev hne
    simp only [applyEvent, hev] at hne ⊢
    split at hne
    · exact absurd rfl hne
    · rename_i hnotStop
      simp only [hnotStop]
      split at hne
      · exact absurd rfl hne
      · rename_i htd
        simp only [htd]
        split at hne
        · exact absurd rfl hne
        · rename_i hrg
          simp only [hrg]
          simp [and_assoc_r] at hrg
          obtain ⟨h1, h2, h3, h4, h5, h6⟩ := hrg
          exact ⟨h1, h2, h3, h4, by omega, h6, rfl⟩

/-- Removing the network namespace requires zero CNI steps, zero container, and zero task. -/
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
  constructor
  · intro h
    cases hph : s.phase <;> simp only [nextStep, hph] at h
    · cases h
    · repeat (split at h; try cases h)
      cases h
    · cases h
    · split at h
      · exact absurd h (stopTaskAction_ne_removeNetns spec s nowMs)
      · repeat (split at h; try cases h)
        cases h
    · split at h
      · exact absurd h (stopTaskAction_ne_removeNetns spec s nowMs)
      · split at h
        · cases h
        · split at h
          · cases h
          · split at h
            · cases h
            · split at h
              · rename_i hnr hnt hnc hcni hnet
                simp at hnr hnt hnc
                exact ⟨hnr, hnt, hnc, by omega, hnet⟩
              · cases h
    · split at h
      · exact absurd h (stopTaskAction_ne_removeNetns spec s nowMs)
      · split at h
        · cases h
        · split at h
          · cases h
          · split at h
            · cases h
            · split at h
              · rename_i hnr hnt hnc hcni hnet
                simp at hnr hnt hnc
                exact ⟨hnr, hnt, hnc, by omega, hnet⟩
              · cases h
    · cases h
  · intro hev hne
    simp only [applyEvent, hev] at hne ⊢
    split at hne
    · exact absurd rfl hne
    · rename_i hnotStop
      simp only [hnotStop]
      split at hne
      · exact absurd rfl hne
      · rename_i htd
        simp only [htd]
        split at hne
        · exact absurd rfl hne
        · rename_i hrg
          simp only [hrg]
          simp [and_assoc_r] at hrg
          obtain ⟨h1, h2, h3, h4, h5, h6, h7, h8⟩ := hrg
          have hemp : ({ s.resources with netnsCreated := false } : Resources).empty = true :=
            (empty_eq_true_iff _).mpr ⟨rfl, h7, h6, h5, h2, h1, h3, h4⟩
          exact ⟨h1, h2, h3, h4, h5, h6, h7, h8, hemp, rfl⟩

/-- Any reachable state in the terminal `stopped` phase holds zero external resources. -/
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
  have hwf := wellFormed_foldl spec evs State.init (init_wellFormed spec)
  have hph := hwf.2
  simp only [run] at h ⊢
  rw [h] at hph
  exact (empty_eq_true_iff _).mp hph.1

private theorem stopTaskAction_killTask {spec : Spec} {s : State} {nowMs : Nat}
    (h : stopTaskAction spec s nowMs = .killTask) :
    s.resources.stopSent = true ∧ s.resources.killSent = false ∧
    s.stopSentAtMs + spec.stopGraceMs ≤ nowMs := by
  simp only [stopTaskAction] at h
  split at h
  · cases h
  · rename_i hstop
    simp at hstop
    split at h
    · rename_i hnk
      simp at hnk
      split at h
      · rename_i hgr
        exact ⟨hstop, hnk, (graceElapsed_iff nowMs s.stopSentAtMs spec.stopGraceMs).mp hgr⟩
      · cases h
    · cases h

/-- `SIGKILL` escalation requires `SIGTERM` already sent and at least `stopGraceMs` elapsed. -/
theorem kill_requires_grace_elapsed (spec : Spec) (s : State) (nowMs : Nat) (ev : Event) :
    (nextStep spec s nowMs = .killTask →
      s.resources.taskRunning = true ∧ s.resources.stopSent = true ∧
      s.resources.killSent = false ∧ s.stopSentAtMs + spec.stopGraceMs ≤ nowMs) ∧
    (ev.kind = .killSent → applyEvent spec s ev ≠ s →
      s.resources.taskRunning = true ∧ s.resources.stopSent = true ∧
      s.resources.killSent = false ∧ s.stopSentAtMs + spec.stopGraceMs ≤ ev.nowMs) := by
  constructor
  · intro h
    cases hph : s.phase <;> simp only [nextStep, hph] at h
    · cases h
    · repeat (split at h; try cases h)
      cases h
    · cases h
    · split at h
      · rename_i hrun
        obtain ⟨h1, h2, h3⟩ := stopTaskAction_killTask h
        exact ⟨hrun, h1, h2, h3⟩
      · repeat (split at h; try cases h)
        cases h
    · split at h
      · rename_i hrun
        obtain ⟨h1, h2, h3⟩ := stopTaskAction_killTask h
        exact ⟨hrun, h1, h2, h3⟩
      · repeat (split at h; try cases h)
        cases h
    · split at h
      · rename_i hrun
        obtain ⟨h1, h2, h3⟩ := stopTaskAction_killTask h
        exact ⟨hrun, h1, h2, h3⟩
      · repeat (split at h; try cases h)
        cases h
    · cases h
  · intro hev hne
    simp only [applyEvent, hev] at hne
    split at hne
    · exact absurd rfl hne
    · split at hne
      · exact absurd rfl hne
      · split at hne
        · exact absurd rfl hne
        · rename_i _ hrg hgr
          simp [and_assoc_r] at hrg
          simp at hgr
          obtain ⟨_, hrun, hstop, hnk⟩ := hrg
          exact ⟨hrun, hstop, hnk, (graceElapsed_iff ev.nowMs s.stopSentAtMs spec.stopGraceMs).mp hgr⟩

/-- Restart policy soundness: `never` never restarts, `onFailure` restarts only on non-zero exit
codes, and `always` restarts on any exit while desired running. -/
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
  refine ⟨?_, ?_, ?_, ?_⟩
  · intro hp
    simp [applyEvent, hph, hrun, hev, shouldRestart, hp]
  · intro hp h0
    simp [applyEvent, hph, hrun, hev, shouldRestart, hp, h0]
  · intro hp hne hdes
    simp [applyEvent, hph, hrun, hev, shouldRestart, hp, hne, hdes]
  · intro hp hdes
    simp [applyEvent, hph, hrun, hev, shouldRestart, hp, hdes]

private theorem one_le_two_pow : ∀ n : Nat, 1 ≤ 2 ^ n
  | 0 => by decide
  | n + 1 => by
    have ih := one_le_two_pow n
    simp only [Nat.pow_succ]
    omega

private theorem two_pow_mono : ∀ {a b : Nat}, a ≤ b → 2 ^ a ≤ 2 ^ b
  | a, 0, h => by
    have ha : a = 0 := by omega
    subst ha
    exact Nat.le_refl 1
  | a, b + 1, h => by
    by_cases hle : a ≤ b
    · have ih := two_pow_mono hle
      simp only [Nat.pow_succ]
      omega
    · have ha : a = b + 1 := by omega
      subst ha
      exact Nat.le_refl _

private theorem backoffLoop_eq (maxMs : Nat) : ∀ (k d : Nat), d ≤ maxMs →
    backoffLoop maxMs k d = natMin maxMs (d * 2 ^ k)
  | 0, d, hle => by
    simp only [backoffLoop, natMin, Nat.pow_zero, Nat.mul_one]
    split <;> omega
  | k + 1, d, hle => by
    simp only [backoffLoop]
    split
    · rename_i hge
      have hdeq : d = maxMs := by omega
      subst hdeq
      have hpow : 1 ≤ 2 ^ (k + 1) := one_le_two_pow (k + 1)
      have hmul : d * 1 ≤ d * 2 ^ (k + 1) := Nat.mul_le_mul_left d hpow
      simp only [Nat.mul_one] at hmul
      simp only [natMin]
      split <;> omega
    · have hle' : natMin maxMs (d * 2) ≤ maxMs := by
        simp only [natMin]
        split <;> omega
      rw [backoffLoop_eq maxMs k (natMin maxMs (d * 2)) hle']
      have hmul : d * 2 ^ (k + 1) = (d * 2) * 2 ^ k := by
        rw [Nat.pow_succ, Nat.mul_comm (2 ^ k) 2, ← Nat.mul_assoc]
      rw [hmul]
      have hpow : 1 ≤ 2 ^ k := one_le_two_pow k
      simp only [natMin]
      split
      · rename_i hcap
        have hm1 : maxMs * 1 ≤ maxMs * 2 ^ k := Nat.mul_le_mul_left maxMs hpow
        have hm2 : maxMs * 2 ^ k ≤ (d * 2) * 2 ^ k := Nat.mul_le_mul_right (2 ^ k) hcap
        simp only [Nat.mul_one] at hm1
        split <;> split <;> omega
      · rfl

private theorem backoffMs_eq (baseMs maxMs attempt : Nat) :
    backoffMs baseMs maxMs attempt = natMin maxMs (baseMs * 2 ^ attempt) := by
  by_cases hb : baseMs = 0
  · subst hb
    simp [backoffMs, natMin]
    omega
  · by_cases hm : maxMs = 0
    · subst hm
      simp [backoffMs, natMin]
    · have hcond : (baseMs == 0 || maxMs == 0) = false := by
        simp [hb, hm]
      simp only [backoffMs, hcond, Bool.false_eq_true, ite_false]
      have hle : natMin baseMs maxMs ≤ maxMs := by
        simp only [natMin]
        split <;> omega
      rw [backoffLoop_eq maxMs attempt (natMin baseMs maxMs) hle]
      have hpow : 1 ≤ 2 ^ attempt := one_le_two_pow attempt
      simp only [natMin]
      split
      · rfl
      · rename_i hgt
        have hm1 : maxMs * 1 ≤ maxMs * 2 ^ attempt := Nat.mul_le_mul_left maxMs hpow
        have hm2 : maxMs * 2 ^ attempt ≤ baseMs * 2 ^ attempt :=
          Nat.mul_le_mul_right (2 ^ attempt) (by omega)
        simp only [Nat.mul_one] at hm1
        split <;> split <;> omega

/-- Backoff is bounded within `[baseMs, maxMs]` whenever `baseMs ≤ maxMs`, and is monotone in the
consecutive restart attempt counter. -/
theorem backoff_bounded_and_monotone (baseMs maxMs a b : Nat) :
    (baseMs ≤ maxMs →
      baseMs ≤ backoffMs baseMs maxMs a ∧ backoffMs baseMs maxMs a ≤ maxMs) ∧
    (a ≤ b → backoffMs baseMs maxMs a ≤ backoffMs baseMs maxMs b) := by
  rw [backoffMs_eq baseMs maxMs a, backoffMs_eq baseMs maxMs b]
  constructor
  · intro hle
    have hmul : baseMs * 1 ≤ baseMs * 2 ^ a := Nat.mul_le_mul_left baseMs (one_le_two_pow a)
    simp only [Nat.mul_one] at hmul
    simp only [natMin]
    split <;> omega
  · intro hab
    have hmul : baseMs * 2 ^ a ≤ baseMs * 2 ^ b := Nat.mul_le_mul_left baseMs (two_pow_mono hab)
    simp only [natMin]
    split <;> split <;> omega

/-- Exiting after a continuous healthy run of at least `backoffResetMs` resets `backoffAttempt` to
`0` and schedules any restart at the base backoff delay. -/
theorem backoff_resets_after_healthy_run (spec : Spec) (s : State) (ev : Event)
    (hrun : s.resources.taskRunning = true)
    (hph : s.phase = .running ∨ s.phase = .stopping ∨ s.phase = .rollingBack)
    (hev : ev.kind = .taskExited)
    (hhealthy : s.startedAtMs + spec.backoffResetMs ≤ ev.nowMs) :
    (applyEvent spec s ev).backoffAttempt = 0 ∧
    (s.phase = .running → s.desiredRunning = true →
      shouldRestart spec.restartPolicy ev.exitCode = true →
      (applyEvent spec s ev).nextStartAtMs = ev.nowMs + backoffMs spec.backoffBaseMs spec.backoffMaxMs 0) := by
  have hgr : graceElapsed ev.nowMs s.startedAtMs spec.backoffResetMs = true :=
    (graceElapsed_iff ev.nowMs s.startedAtMs spec.backoffResetMs).mpr hhealthy
  constructor
  · rcases hph with hph | hph | hph
    · simp [applyEvent, hph, hrun, hev, hgr]
      split <;> rfl
    · simp [applyEvent, hph, hrun, hev, hgr, isTeardownPhase]
    · simp [applyEvent, hph, hrun, hev, hgr, isTeardownPhase]
  · intro hrunPh hdes hsr
    simp [applyEvent, hrunPh, hrun, hev, hgr, hdes, hsr]

end Nayutar.Node
