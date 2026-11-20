import Nayutar.WatchCache

namespace Nayutar.WatchCache

open Nayutar.Store (Rev Event)

/-- `omega` does not look through the `Rev` abbreviation. -/
local macro "rev_omega" : tactic => `(tactic| ((try simp only [Rev] at *); omega))

private theorem evictOne_same {c c' : Cache} {now : Nat} (h : c.evictOne now = some c') :
    c'.log = c.log ∧ c'.rev = c.rev ∧ c'.isOpen = c.isOpen := by
  unfold Cache.evictOne at h
  split at h
  · simp at h
  · dsimp only at h
    repeat' split at h
    all_goals first
      | (simp at h; done)
      | (simp only [Option.some.injEq] at h; subst h; exact ⟨rfl, rfl, rfl⟩)

private theorem evictLoop_same (fuel : Nat) (c : Cache) (now : Nat) :
    (Cache.evictLoop fuel c now).log = c.log ∧ (Cache.evictLoop fuel c now).rev = c.rev
      ∧ (Cache.evictLoop fuel c now).isOpen = c.isOpen := by
  induction fuel generalizing c with
  | zero => simp [Cache.evictLoop]
  | succ fuel ih =>
    unfold Cache.evictLoop
    cases h : c.evictOne now with
    | none => simp
    | some c' =>
      have h1 := evictOne_same h
      have h2 := ih c'
      simp only
      exact ⟨h2.1.trans h1.1, h2.2.1.trans h1.2.1, h2.2.2.trans h1.2.2⟩

private theorem feed_event {c c' : Cache} {e : Event} {now : Nat} (h : c.feed (.event e now) = some c') :
    c.rev < e.rev ∧ c'.log = c.log ++ [{ event := e, time := now }] ∧ c'.rev = e.rev - 1 ∧ c'.isOpen = true := by
  simp only [Cache.feed] at h
  split at h
  · rename_i hlt
    simp only [Option.some.injEq] at h
    subst h
    have hs := evictLoop_same
      ({ c with log := c.log ++ [{ event := e, time := now }], rev := e.rev - 1, isOpen := true } : Cache).log.length
      { c with log := c.log ++ [{ event := e, time := now }], rev := e.rev - 1, isOpen := true } now
    exact ⟨hlt, hs.1, hs.2.1, hs.2.2⟩
  · simp at h

private theorem feed_progress {c c' : Cache} {p : Rev} (h : c.feed (.progress p) = some c') :
    (if c.isOpen then c.rev + 1 else c.rev) ≤ p ∧ c'.log = c.log ∧ c'.rev = p ∧ c'.isOpen = false := by
  simp only [Cache.feed] at h
  by_cases hle : (if c.isOpen then c.rev + 1 else c.rev) ≤ p
  · simp only [hle, ↓reduceIte, Option.some.injEq] at h
    subst h
    exact ⟨hle, rfl, rfl, rfl⟩
  · simp [hle] at h

/-- What every cache fed by a store satisfies: its changes are in revision order, none is above
the open revision, and none is above the closed revision when nothing is open. -/
private structure WF (c : Cache) : Prop where
  sorted : (c.log.map (fun e => e.event)).Pairwise (fun a b => a.rev ≤ b.rev)
  bound : ∀ e ∈ c.log, e.event.rev ≤ c.rev + 1
  closed : c.isOpen = false → ∀ e ∈ c.log, e.event.rev ≤ c.rev

private theorem init_wf (config : Config) (listRev : Rev) (kvs : List Nayutar.Store.KV) :
    WF (Cache.init config listRev kvs) := by
  constructor <;> simp [Cache.init]

private theorem feed_wf {c c' : Cache} {input : Input} (hwf : WF c) (h : c.feed input = some c') : WF c' := by
  cases input with
  | event e now =>
    obtain ⟨hlt, hlog, hrev, hopen⟩ := feed_event h
    constructor
    · rw [hlog, List.map_append, List.pairwise_append]
      refine ⟨hwf.sorted, by simp, ?_⟩
      intro a ha b hb
      obtain ⟨x, hx, rfl⟩ := List.mem_map.1 ha
      simp at hb
      subst hb
      have := hwf.bound x hx
      rev_omega
    · intro x hx
      rw [hlog] at hx
      rw [hrev]
      rcases List.mem_append.1 hx with hx | hx
      · have := hwf.bound x hx
        rev_omega
      · simp at hx
        subst hx
        simp
        rev_omega
    · intro hclosed
      rw [hopen] at hclosed
      cases hclosed
  | progress p =>
    obtain ⟨hle, hlog, hrev, hopen⟩ := feed_progress h
    have hall : ∀ e ∈ c.log, e.event.rev ≤ p := by
      intro x hx
      cases hopen' : c.isOpen with
      | true =>
        rw [hopen'] at hle
        have := hwf.bound x hx
        simp at hle
        rev_omega
      | false =>
        rw [hopen'] at hle
        have := hwf.closed hopen' x hx
        simp at hle
        rev_omega
    constructor
    · rw [hlog]
      exact hwf.sorted
    · intro x hx
      rw [hlog] at hx
      rw [hrev]
      have := hall x hx
      rev_omega
    · intro _ x hx
      rw [hlog] at hx
      rw [hrev]
      exact hall x hx

private theorem feedAll_wf {inputs : List Input} {c c' : Cache} (hwf : WF c) (h : c.feedAll inputs = some c') :
    WF c' := by
  induction inputs generalizing c with
  | nil =>
    simp [Cache.feedAll] at h
    subst h
    exact hwf
  | cons input inputs ih =>
    unfold Cache.feedAll at h
    cases hf : c.feed input with
    | none => simp [hf] at h
    | some c1 =>
      simp only [hf] at h
      exact ih (feed_wf hwf hf) h

private theorem take_append_drop_length {α : Type} (l : List α) (n : Nat) :
    l.take n ++ l.drop (l.take n).length = l := by
  induction l generalizing n with
  | nil => simp
  | cons a l ih =>
    cases n with
    | zero => simp
    | succ n => simpa [List.length_take] using ih n

private theorem poll_spec {c : Cache} {w w1 : Watcher} {limit : Nat} {events : List Event}
    (hw : w.next ≤ c.log.length) (h : c.poll w limit = .ok (events, w1)) :
    ∃ taken : List Event,
      c.remaining w = taken ++ c.remaining w1 ∧ events = taken.filter w.matches
        ∧ w1.keyPrefix = w.keyPrefix ∧ w1.next ≤ c.log.length := by
  unfold Cache.poll at h
  split at h
  · cases h
  · simp only [Except.ok.injEq, Prod.mk.injEq] at h
    obtain ⟨hev, hw1⟩ := h
    subst hw1
    have hrem : ∀ k, c.remaining { w with next := w.next + k } = (c.remaining w).drop k := by
      intro k
      simp [Cache.remaining, List.map_drop, List.drop_drop]
    have hlen : (c.remaining w).length + w.next = c.log.length := by
      simp [Cache.remaining]
      omega
    by_cases hl : limit = 0
    · refine ⟨c.remaining w, ?_, ?_, rfl, ?_⟩
      · simp [hl, hrem]
      · simp [hl] at hev
        exact hev.symm
      · simp [hl]
        rev_omega
    · refine ⟨(c.remaining w).take limit, ?_, ?_, rfl, ?_⟩
      · simp only [hl, ite_false, hrem]
        exact (take_append_drop_length _ _).symm
      · simp [hl] at hev
        exact hev.symm
      · simp only [hl, ite_false]
        have := List.length_take_le' limit (c.remaining w)
        rev_omega

private theorem events_map_append (events : List Event) (outs : List Out) :
    Out.events (events.map Out.event ++ outs) = events ++ Out.events outs := by
  induction events with
  | nil => rfl
  | cons e events ih => simp [Out.events, ih]

/-- What one watch is sent during a session: a prefix `d` of the changes it had not passed plus
the changes fed since, filtered by its key prefix. -/
private theorem session_spec (steps : List Step) :
    ∀ (c : Cache) (w : Watcher) (c' : Cache) (w' : Watcher) (outs : List Out),
      w.next ≤ c.log.length → session c w steps = some (c', w', outs) →
      ∃ d : List Event,
        c.remaining w ++ Step.fedEvents steps = d ++ c'.remaining w'
          ∧ Out.events outs = d.filter w.matches
          ∧ w'.keyPrefix = w.keyPrefix
          ∧ w'.next ≤ c'.log.length
          ∧ c'.log.map (fun e => e.event) = c.log.map (fun e => e.event) ++ Step.fedEvents steps := by
  induction steps with
  | nil =>
    intro c w c' w' outs hw h
    simp [session] at h
    obtain ⟨rfl, rfl, rfl⟩ := h
    exact ⟨[], by simp [Step.fedEvents], by simp [Out.events], rfl, hw, by simp [Step.fedEvents]⟩
  | cons step steps ih =>
    intro c w c' w' outs hw h
    cases step with
    | feed input =>
      unfold session at h
      cases hf : c.feed input with
      | none => simp [hf] at h
      | some c1 =>
        simp only [hf] at h
        cases input with
        | event e now =>
          obtain ⟨_, hlog, _, _⟩ := feed_event hf
          have hw1 : w.next ≤ c1.log.length := by
            rw [hlog]
            simp
            rev_omega
          obtain ⟨d, h1, h2, h3, h4, h5⟩ := ih c1 w c' w' outs hw1 h
          have hrem : c1.remaining w = c.remaining w ++ [e] := by
            simp [Cache.remaining, hlog, List.drop_append_of_le_length hw]
          refine ⟨d, ?_, h2, h3, h4, ?_⟩
          · rw [← h1, hrem]
            simp [Step.fedEvents]
          · rw [h5, hlog]
            simp [Step.fedEvents]
        | progress p =>
          obtain ⟨_, hlog, _, _⟩ := feed_progress hf
          have hw1 : w.next ≤ c1.log.length := by
            rw [hlog]
            exact hw
          obtain ⟨d, h1, h2, h3, h4, h5⟩ := ih c1 w c' w' outs hw1 h
          have hrem : c1.remaining w = c.remaining w := by
            simp [Cache.remaining, hlog]
          refine ⟨d, ?_, h2, h3, h4, ?_⟩
          · rw [← h1, hrem]
            simp [Step.fedEvents]
          · rw [h5, hlog]
            simp [Step.fedEvents]
    | poll limit =>
      unfold session at h
      cases hp : c.poll w limit with
      | error err => simp [hp] at h
      | ok res =>
        obtain ⟨events, w1⟩ := res
        simp only [hp] at h
        cases hs : session c w1 steps with
        | none => simp [hs] at h
        | some res =>
          obtain ⟨c2, w2, outs2⟩ := res
          simp only [hs, Option.some.injEq, Prod.mk.injEq] at h
          obtain ⟨rfl, rfl, rfl⟩ := h
          obtain ⟨taken, ht1, ht2, ht3, ht4⟩ := poll_spec hw hp
          obtain ⟨d, h1, h2, h3, h4, h5⟩ := ih c w1 c2 w2 outs2 ht4 hs
          have hm : w1.matches = w.matches := by
            funext x
            simp [Watcher.matches, ht3]
          refine ⟨taken ++ d, ?_, ?_, h3.trans ht3, h4, ?_⟩
          · rw [ht1, List.append_assoc, List.append_assoc, ← h1]
            simp [Step.fedEvents]
          · rw [events_map_append, h2, ht2, hm, List.filter_append]
          · rw [h5]
            simp [Step.fedEvents]
    | bookmark =>
      unfold session at h
      cases hs : session c w steps with
      | none => simp [hs] at h
      | some res =>
        obtain ⟨c2, w2, outs2⟩ := res
        simp only [hs] at h
        obtain ⟨d, h1, h2, h3, h4, h5⟩ := ih c w c2 w2 outs2 hw hs
        cases hb : c.bookmark w with
        | none =>
          simp only [hb, Option.some.injEq, Prod.mk.injEq] at h
          obtain ⟨rfl, rfl, rfl⟩ := h
          exact ⟨d, by simpa [Step.fedEvents] using h1, h2, h3, h4, by simpa [Step.fedEvents] using h5⟩
        | some r =>
          simp only [hb, Option.some.injEq, Prod.mk.injEq] at h
          obtain ⟨rfl, rfl, rfl⟩ := h
          exact ⟨d, by simpa [Step.fedEvents] using h1, by simpa [Out.events] using h2, h3, h4,
            by simpa [Step.fedEvents] using h5⟩

private theorem session_wf (steps : List Step) :
    ∀ (c : Cache) (w : Watcher) (c' : Cache) (w' : Watcher) (outs : List Out),
      WF c → session c w steps = some (c', w', outs) → WF c' := by
  induction steps with
  | nil =>
    intro c w c' w' outs hwf h
    simp [session] at h
    obtain ⟨rfl, _, _⟩ := h
    exact hwf
  | cons step steps ih =>
    intro c w c' w' outs hwf h
    cases step with
    | feed input =>
      unfold session at h
      cases hf : c.feed input with
      | none => simp [hf] at h
      | some c1 =>
        simp only [hf] at h
        exact ih c1 w c' w' outs (feed_wf hwf hf) h
    | poll limit =>
      unfold session at h
      cases hp : c.poll w limit with
      | error err => simp [hp] at h
      | ok res =>
        obtain ⟨events, w1⟩ := res
        simp only [hp] at h
        cases hs : session c w1 steps with
        | none => simp [hs] at h
        | some res =>
          obtain ⟨c2, w2, outs2⟩ := res
          simp only [hs, Option.some.injEq, Prod.mk.injEq] at h
          obtain ⟨rfl, _, _⟩ := h
          exact ih c w1 c2 w2 outs2 hwf hs
    | bookmark =>
      unfold session at h
      cases hs : session c w steps with
      | none => simp [hs] at h
      | some res =>
        obtain ⟨c2, w2, outs2⟩ := res
        simp only [hs] at h
        cases hb : c.bookmark w with
        | none =>
          simp only [hb, Option.some.injEq, Prod.mk.injEq] at h
          obtain ⟨rfl, _, _⟩ := h
          exact ih c w c2 w2 outs2 hwf hs
        | some r =>
          simp only [hb, Option.some.injEq, Prod.mk.injEq] at h
          obtain ⟨rfl, _, _⟩ := h
          exact ih c w c2 w2 outs2 hwf hs

/-- Every bookmark of a session is at or above the closed revision and every change the cache
held when the session began, and every change sent was either already waiting or is above the
closed revision the session began with. -/
private theorem session_bounds (steps : List Step) :
    ∀ (c : Cache) (w : Watcher) (c' : Cache) (w' : Watcher) (outs : List Out),
      WF c → w.next ≤ c.log.length → session c w steps = some (c', w', outs) →
      (∀ b, Out.bookmark b ∈ outs → c.rev ≤ b ∧ ∀ x ∈ c.log, x.event.rev ≤ b)
        ∧ (∀ e, Out.event e ∈ outs → e ∈ c.remaining w ∨ c.rev < e.rev) := by
  induction steps with
  | nil =>
    intro c w c' w' outs _ _ h
    simp [session] at h
    obtain ⟨_, _, rfl⟩ := h
    simp
  | cons step steps ih =>
    intro c w c' w' outs hwf hw h
    cases step with
    | feed input =>
      unfold session at h
      cases hf : c.feed input with
      | none => simp [hf] at h
      | some c1 =>
        simp only [hf] at h
        have hwf1 := feed_wf hwf hf
        cases input with
        | event e now =>
          obtain ⟨hlt, hlog, hrev, _⟩ := feed_event hf
          have hw1 : w.next ≤ c1.log.length := by
            rw [hlog]
            simp
            rev_omega
          obtain ⟨hb, he⟩ := ih c1 w c' w' outs hwf1 hw1 h
          have hrem : c1.remaining w = c.remaining w ++ [e] := by
            simp [Cache.remaining, hlog, List.drop_append_of_le_length hw]
          constructor
          · intro b hbm
            obtain ⟨h1, h2⟩ := hb b hbm
            refine ⟨by rev_omega, ?_⟩
            intro x hx
            exact h2 x (by rw [hlog]; exact List.mem_append_left _ hx)
          · intro x hx
            rcases he x hx with h1 | h1
            · rw [hrem] at h1
              rcases List.mem_append.1 h1 with h1 | h1
              · exact Or.inl h1
              · simp at h1
                subst h1
                exact Or.inr hlt
            · exact Or.inr (by rev_omega)
        | progress p =>
          obtain ⟨hle, hlog, hrev, _⟩ := feed_progress hf
          have hw1 : w.next ≤ c1.log.length := by
            rw [hlog]
            exact hw
          obtain ⟨hb, he⟩ := ih c1 w c' w' outs hwf1 hw1 h
          have hrem : c1.remaining w = c.remaining w := by
            simp [Cache.remaining, hlog]
          have hmono : c.rev ≤ c1.rev := by
            rw [hrev]
            cases hopen : c.isOpen <;> simp [hopen] at hle <;> rev_omega
          constructor
          · intro b hbm
            obtain ⟨h1, h2⟩ := hb b hbm
            refine ⟨by rev_omega, ?_⟩
            intro x hx
            exact h2 x (by rw [hlog]; exact hx)
          · intro x hx
            rcases he x hx with h1 | h1
            · exact Or.inl (by rw [← hrem]; exact h1)
            · exact Or.inr (by rev_omega)
    | poll limit =>
      unfold session at h
      cases hp : c.poll w limit with
      | error err => simp [hp] at h
      | ok res =>
        obtain ⟨events, w1⟩ := res
        simp only [hp] at h
        cases hs : session c w1 steps with
        | none => simp [hs] at h
        | some res =>
          obtain ⟨c2, w2, outs2⟩ := res
          simp only [hs, Option.some.injEq, Prod.mk.injEq] at h
          obtain ⟨rfl, rfl, rfl⟩ := h
          obtain ⟨taken, ht1, ht2, _, ht4⟩ := poll_spec hw hp
          obtain ⟨hb, he⟩ := ih c w1 c2 w2 outs2 hwf ht4 hs
          constructor
          · intro b hbm
            rcases List.mem_append.1 hbm with h1 | h1
            · obtain ⟨x, _, hx⟩ := List.mem_map.1 h1
              cases hx
            · exact hb b h1
          · intro x hx
            rcases List.mem_append.1 hx with h1 | h1
            · obtain ⟨y, hy, hxy⟩ := List.mem_map.1 h1
              cases hxy
              rw [ht2] at hy
              exact Or.inl (by rw [ht1]; exact List.mem_append_left _ (List.mem_filter.1 hy).1)
            · rcases he x h1 with h2 | h2
              · exact Or.inl (by rw [ht1]; exact List.mem_append_right _ h2)
              · exact Or.inr h2
    | bookmark =>
      unfold session at h
      cases hs : session c w steps with
      | none => simp [hs] at h
      | some res =>
        obtain ⟨c2, w2, outs2⟩ := res
        simp only [hs] at h
        obtain ⟨hb, he⟩ := ih c w c2 w2 outs2 hwf hw hs
        cases hbm : c.bookmark w with
        | none =>
          simp only [hbm, Option.some.injEq, Prod.mk.injEq] at h
          obtain ⟨rfl, rfl, rfl⟩ := h
          exact ⟨hb, he⟩
        | some r =>
          simp only [hbm, Option.some.injEq, Prod.mk.injEq] at h
          obtain ⟨rfl, rfl, rfl⟩ := h
          unfold Cache.bookmark at hbm
          split at hbm
          · rename_i hcond
            simp only [Option.some.injEq] at hbm
            subst hbm
            constructor
            · intro b hmem
              rcases List.mem_cons.1 hmem with h1 | h1
              · cases h1
                exact ⟨Nat.le_refl _, hwf.closed hcond.2⟩
              · exact hb b h1
            · intro x hx
              rcases List.mem_cons.1 hx with h1 | h1
              · cases h1
              · exact he x h1
          · cases hbm

private theorem events_pairwise (events : List Event) : (events.map Out.event).Pairwise Out.ordered := by
  induction events with
  | nil => simp
  | cons e events ih =>
    rw [List.map_cons, List.pairwise_cons]
    refine ⟨?_, ih⟩
    intro o ho
    obtain ⟨x, _, rfl⟩ := List.mem_map.1 ho
    trivial

theorem watch_no_loss (c : Cache) (w : Watcher) (steps : List Step) (c' : Cache) (w' : Watcher)
    (outs : List Out) (hw : w.next ≤ c.log.length) (hs : session c w steps = some (c', w', outs)) :
    c.pending w ++ (Step.fedEvents steps).filter w.matches = Out.events outs ++ c'.pending w' := by
  obtain ⟨d, h1, h2, h3, _, _⟩ := session_spec steps c w c' w' outs hw hs
  have hm : w'.matches = w.matches := by
    funext x
    simp [Watcher.matches, h3]
  unfold Cache.pending
  rw [h2, hm, ← List.filter_append, ← List.filter_append, h1]

theorem watch_no_duplicates (c : Cache) (w : Watcher) (steps : List Step) (c' : Cache) (w' : Watcher)
    (outs : List Out) (hw : w.next ≤ c.log.length) (hs : session c w steps = some (c', w', outs)) :
    (Out.events outs).Sublist (c.pending w ++ (Step.fedEvents steps).filter w.matches) := by
  rw [watch_no_loss c w steps c' w' outs hw hs]
  exact List.sublist_append_left _ _

theorem watch_order_preserved (config : Config) (listRev : Rev) (kvs : List Nayutar.Store.KV)
    (inputs : List Input) (c : Cache) (w : Watcher) (steps : List Step) (c' : Cache) (w' : Watcher)
    (outs : List Out) (hc : (Cache.init config listRev kvs).feedAll inputs = some c)
    (hw : w.next ≤ c.log.length) (hs : session c w steps = some (c', w', outs)) :
    (Out.events outs).Pairwise (fun a b => a.rev ≤ b.rev) := by
  have hwf := feedAll_wf (init_wf config listRev kvs) hc
  have hwf' := session_wf steps c w c' w' outs hwf hs
  obtain ⟨d, h1, h2, _, _, h5⟩ := session_spec steps c w c' w' outs hw hs
  have hdrop : c.remaining w ++ Step.fedEvents steps = (c'.log.map (fun e => e.event)).drop w.next := by
    rw [h5, List.drop_append_of_le_length (by simpa using hw)]
    simp [Cache.remaining, List.map_drop]
  have hsub : d.Sublist (c'.log.map (fun e => e.event)) := by
    have h3 : d.Sublist (d ++ c'.remaining w') := List.sublist_append_left _ _
    rw [← h1, hdrop] at h3
    exact h3.trans (List.drop_sublist _ _)
  rw [h2]
  exact (hwf'.sorted.sublist hsub).sublist List.filter_sublist

theorem bookmark_monotone (config : Config) (listRev : Rev) (kvs : List Nayutar.Store.KV)
    (inputs : List Input) (c : Cache) (w : Watcher) (steps : List Step) (c' : Cache) (w' : Watcher)
    (outs : List Out) (hc : (Cache.init config listRev kvs).feedAll inputs = some c)
    (hw : w.next ≤ c.log.length) (hs : session c w steps = some (c', w', outs)) :
    outs.Pairwise Out.ordered := by
  have hwf := feedAll_wf (init_wf config listRev kvs) hc
  clear hc
  induction steps generalizing c w c' w' outs with
  | nil =>
    simp [session] at hs
    obtain ⟨_, _, rfl⟩ := hs
    simp
  | cons step steps ih =>
    cases step with
    | feed input =>
      unfold session at hs
      cases hf : c.feed input with
      | none => simp [hf] at hs
      | some c1 =>
        simp only [hf] at hs
        have hw1 : w.next ≤ c1.log.length := by
          cases input with
          | event e now =>
            rw [(feed_event hf).2.1]
            simp
            rev_omega
          | progress p =>
            rw [(feed_progress hf).2.1]
            exact hw
        exact ih c1 w c' w' outs hw1 hs (feed_wf hwf hf)
    | poll limit =>
      unfold session at hs
      cases hp : c.poll w limit with
      | error err => simp [hp] at hs
      | ok res =>
        obtain ⟨events, w1⟩ := res
        simp only [hp] at hs
        cases hs2 : session c w1 steps with
        | none => simp [hs2] at hs
        | some res =>
          obtain ⟨c2, w2, outs2⟩ := res
          simp only [hs2, Option.some.injEq, Prod.mk.injEq] at hs
          obtain ⟨rfl, rfl, rfl⟩ := hs
          obtain ⟨taken, ht1, ht2, _, ht4⟩ := poll_spec hw hp
          obtain ⟨hb, _⟩ := session_bounds steps c w1 c2 w2 outs2 hwf ht4 hs2
          rw [List.pairwise_append]
          refine ⟨events_pairwise events, ih c w1 c2 w2 outs2 ht4 hs2 hwf, ?_⟩
          intro a ha o ho
          obtain ⟨e, he, rfl⟩ := List.mem_map.1 ha
          cases o with
          | event e2 => trivial
          | bookmark b =>
            have hrem : e ∈ c.remaining w := by
              rw [ht2] at he
              rw [ht1]
              exact List.mem_append_left _ (List.mem_filter.1 he).1
            unfold Cache.remaining at hrem
            obtain ⟨x, hx, rfl⟩ := List.mem_map.1 hrem
            exact (hb b ho).2 x (List.mem_of_mem_drop hx)
    | bookmark =>
      unfold session at hs
      cases hs2 : session c w steps with
      | none => simp [hs2] at hs
      | some res =>
        obtain ⟨c2, w2, outs2⟩ := res
        simp only [hs2] at hs
        have hrest := ih c w c2 w2 outs2 hw hs2 hwf
        obtain ⟨hb, he⟩ := session_bounds steps c w c2 w2 outs2 hwf hw hs2
        cases hbm : c.bookmark w with
        | none =>
          simp only [hbm, Option.some.injEq, Prod.mk.injEq] at hs
          obtain ⟨rfl, rfl, rfl⟩ := hs
          exact hrest
        | some r =>
          simp only [hbm, Option.some.injEq, Prod.mk.injEq] at hs
          obtain ⟨rfl, rfl, rfl⟩ := hs
          unfold Cache.bookmark at hbm
          split at hbm
          · rename_i hcond
            simp only [Option.some.injEq] at hbm
            subst hbm
            rw [List.pairwise_cons]
            refine ⟨?_, hrest⟩
            intro o ho
            cases o with
            | bookmark b => exact (hb b ho).1
            | event e =>
              rcases he e ho with h1 | h1
              · simp [Cache.remaining, hcond.1] at h1
              · exact h1
          · cases hbm

end Nayutar.WatchCache
