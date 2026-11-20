namespace Nayutar.Fixture

structure Register where
  value : Option Nat
  revision : Nat
  deriving Repr, DecidableEq

def Register.empty : Register := ⟨none, 0⟩

inductive Op where
  | put (value : Nat)
  | delete
  | cas (expect : Nat) (value : Nat)
  deriving Repr, DecidableEq

structure Outcome where
  ok : Bool
  state : Register
  deriving Repr, DecidableEq

def Register.step (r : Register) : Op → Outcome
  | .put v => { ok := true, state := { value := some v, revision := r.revision + 1 } }
  | .delete =>
    match r.value with
    | some _ => { ok := true, state := { value := none, revision := r.revision + 1 } }
    | none => { ok := false, state := r }
  | .cas expect v =>
    if r.revision = expect then
      { ok := true, state := { value := some v, revision := r.revision + 1 } }
    else
      { ok := false, state := r }

private def runFrom (r : Register) : List Op → List Outcome
  | [] => []
  | op :: ops =>
    let out := r.step op
    out :: runFrom out.state ops

def Register.run (ops : List Op) : List Outcome :=
  runFrom Register.empty ops

end Nayutar.Fixture
