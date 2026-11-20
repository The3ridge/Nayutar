import Nayutar.Fixture.Register

namespace Nayutar.Fixture

theorem Register.step_revision_monotone (r : Register) (op : Op) : r.revision ≤ (r.step op).state.revision := by
  cases op with
  | put v => simp [Register.step]
  | delete =>
    cases h : r.value with
    | none => simp [Register.step, h]
    | some _ => simp [Register.step, h]
  | cas e v =>
    by_cases h : r.revision = e <;> simp [Register.step, h]

theorem Register.step_rejected_unchanged (r : Register) (op : Op) : (r.step op).ok = false → (r.step op).state = r := by
  cases op with
  | put v => simp [Register.step]
  | delete =>
    cases h : r.value with
    | none => simp [Register.step, h]
    | some _ => simp [Register.step, h]
  | cas e v =>
    by_cases h : r.revision = e <;> simp [Register.step, h]

theorem Register.step_accepted_bumps (r : Register) (op : Op) : (r.step op).ok = true → (r.step op).state.revision = r.revision + 1 := by
  cases op with
  | put v => simp [Register.step]
  | delete =>
    cases h : r.value with
    | none => simp [Register.step, h]
    | some _ => simp [Register.step, h]
  | cas e v =>
    by_cases h : r.revision = e <;> simp [Register.step, h]

end Nayutar.Fixture
