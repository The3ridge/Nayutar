import Nayutar.Fixture.Register

namespace Nayutar.Fixture

theorem Register.step_revision_monotone (r : Register) (op : Op) : r.revision ≤ (r.step op).state.revision := by
  sorry

theorem Register.step_rejected_unchanged (r : Register) (op : Op) : (r.step op).ok = false → (r.step op).state = r := by
  sorry

theorem Register.step_accepted_bumps (r : Register) (op : Op) : (r.step op).ok = true → (r.step op).state.revision = r.revision + 1 := by
  sorry

end Nayutar.Fixture
