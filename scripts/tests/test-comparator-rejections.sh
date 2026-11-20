#!/usr/bin/env bash
# Deleting a protected theorem must fail as a missing theorem, exit 1.
# On Lean 4.35.0, lake comparator's child panics with exit 134 and lake
# itself then exits 1 with "Child exited with 134". That panic is not the
# deletion diagnostic, so this script does not run comparator for it.
# Weakened statements, sorry, and extra axioms still have the theorem
# declared; lake comparator must reject each of them with exit 1.
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$root"
proof="$root/spec/Nayutar/Proofs/Register.lean"
backup=$(mktemp)
bin=$(mktemp)
cp "$proof" "$backup"
restore() {
  cp "$backup" "$proof"
  rm -f "$backup" "$bin"
}
trap restore EXIT
go build -o "$bin" ./cmd/check-protected-theorems

mutate() {
  cp "$backup" "$proof"
  python3 - "$proof" "$1" <<'PY'
import pathlib, sys
path, kind = pathlib.Path(sys.argv[1]), sys.argv[2]
text = path.read_text()
monotone = """theorem Register.step_revision_monotone (r : Register) (op : Op) : r.revision ≤ (r.step op).state.revision := by
  cases op with
  | put v => simp [Register.step]
  | delete =>
    cases h : r.value with
    | none => simp [Register.step, h]
    | some _ => simp [Register.step, h]
  | cas e v =>
    by_cases h : r.revision = e <;> simp [Register.step, h]
"""
if kind == "delete":
    if monotone not in text:
        raise SystemExit("delete: monotone theorem not found")
    text = text.replace(monotone, "", 1)
elif kind == "weakened":
    old = "r.revision ≤ (r.step op).state.revision := by"
    new = "r.revision < (r.step op).state.revision := by"
    if old not in text:
        raise SystemExit("weakened: statement not found")
    text = text.replace(old, new, 1)
elif kind == "sorry":
    replacement = """theorem Register.step_revision_monotone (r : Register) (op : Op) : r.revision ≤ (r.step op).state.revision := by
  sorry
"""
    if monotone not in text:
        raise SystemExit("sorry: monotone theorem not found")
    text = text.replace(monotone, replacement, 1)
elif kind == "axiom":
    needle = "namespace Nayutar.Fixture\n"
    axiom = """namespace Nayutar.Fixture

axiom nayutarGuardAx (r : Register) (op : Op) : r.revision ≤ (r.step op).state.revision

"""
    replacement = """theorem Register.step_revision_monotone (r : Register) (op : Op) : r.revision ≤ (r.step op).state.revision := by
  exact nayutarGuardAx r op
"""
    if needle not in text or monotone not in text:
        raise SystemExit("axiom: proof shape not found")
    text = text.replace(needle, axiom, 1).replace(monotone, replacement, 1)
else:
    raise SystemExit(f"unknown mutation {kind}")
path.write_text(text)
PY
}

run_captured() {
  local want=$1
  shift
  set +e
  local out
  out=$("$@" 2>&1)
  local code=$?
  set -e
  if [[ "$code" -ne "$want" ]]; then
    printf '%s\nexit %s, want %s\n' "$out" "$code" "$want" >&2
    exit 1
  fi
  printf '%s' "$out"
}

mutate delete
deletion=$(run_captured 1 "$bin" -root "$root")
want="missing protected theorem: Nayutar.Fixture.Register.step_revision_monotone in solution module Nayutar.Proofs"
if [[ "$deletion" != "$want" ]]; then
  printf 'deletion diagnostic =\n%s\nwant =\n%s\n' "$deletion" "$want" >&2
  exit 1
fi
printf 'deletion rejected: %s\n' "$want"

reject_comparator() {
  local label=$1
  local needle=$2
  mutate "$label"
  local out
  out=$(run_captured 1 lake comparator)
  if ! grep -F -q -- "$needle" <<<"$out"; then
    printf '%s: lake comparator output missing %q\n%s\n' "$label" "$needle" "$out" >&2
    exit 1
  fi
  printf '%s rejected\n' "$label"
}

# lake comparator's working directory is the Lake project.
cd "$root/spec"
reject_comparator weakened "unsolved goals"
reject_comparator sorry "Illegal axiom detected: 'sorryAx'"
reject_comparator axiom "Illegal axiom detected"
