#!/usr/bin/env python3
"""Re-check repository structure for accepted foundational and store-contract ADRs."""

from __future__ import annotations

import json
import pathlib
import shutil
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parents[2]


def read(rel: str) -> str:
    return (ROOT / rel).read_text(encoding="utf-8")


def frontmatter_status(rel: str) -> str:
    lines = read(rel).splitlines()
    if not lines or lines[0].strip() != "---":
        return ""
    for line in lines[1:]:
        if line.strip() == "---":
            break
        if line.startswith("status:"):
            return line.split(":", 1)[1].strip()
    return ""


def tracked_files() -> list[str]:
    res = subprocess.run(
        ["git", "-C", str(ROOT), "ls-files", "-z"],
        capture_output=True,
        check=True,
    )
    entries = [entry for entry in res.stdout.decode("utf-8").split("\x00") if entry]
    return sorted(entry for entry in entries if (ROOT / entry).is_file())


def query_binding_adrs() -> tuple[bool, list[str], str]:
    docdag_bin = shutil.which("docdag")
    if docdag_bin is None:
        return False, [], "docdag executable not found"
    res = subprocess.run(
        [docdag_bin, "query", "--binding"],
        cwd=ROOT,
        capture_output=True,
        text=True,
        check=False,
    )
    if res.returncode != 0:
        return False, [], res.stderr.strip() or f"exit {res.returncode}"
    ids = [line.strip() for line in res.stdout.splitlines() if line.strip()]
    return True, ids, ", ".join(ids)


def main() -> int:
    checks: list[tuple[str, bool, str]] = []

    def add(name: str, ok: bool, detail: str) -> None:
        checks.append((name, ok, detail))

    tracked = tracked_files()
    tracked_set = set(tracked)

    gomod = read("go.mod")
    add("go.mod floor 1.27.3", "\ngo 1.27.3\n" in f"\n{gomod}", "go 1.27.3")
    add("go.mod has no toolchain pin", "\ntoolchain " not in f"\n{gomod}", "no toolchain directive")

    lake = read("spec/lakefile.toml")
    add(
        "spec is the Lake project and depends on kisage v0.1.2",
        'name = "nayutar"' in lake
        and "https://github.com/Kaikei-e/kisage.git" in lake
        and 'rev = "v0.1.2"' in lake
        and 'name = "nayutar-drt"' in lake
        and "lakefile.toml" not in tracked_set,
        "lakefile.toml",
    )
    add(
        "lean toolchain is v4.35.0",
        read("spec/lean-toolchain").strip() == "leanprover/lean4:v4.35.0",
        read("spec/lean-toolchain").strip(),
    )

    ci = read(".github/workflows/ci.yml")
    drt = read(".github/workflows/drt.yml")
    lean = read(".github/workflows/lean.yml")
    add("Go CI workflow does not run lake", "lake" not in ci, "ci.yml")
    add(
        "Lean workflow checks protected theorems, builds spec, and runs comparator",
        "check-protected-theorems" in lean and "lake build" in lean and "lake comparator" in lean,
        "lean.yml",
    )
    add(
        "DRT workflow builds Lean on every change and tests etcd",
        "lake build nayutar-drt" in drt
        and "paths:" not in drt
        and '-tags "drt etcd" ./core/drt/...' in drt,
        "drt.yml",
    )
    add(
        "DRT case counts are 10000 on push and 1000 otherwise",
        "github.event_name == 'push' && '10000' || '1000'" in drt,
        "NAYUTAR_DRT_CASES",
    )

    yaml_files = [rel for rel in tracked if pathlib.PurePosixPath(rel).suffix in {".yml", ".yaml"}]
    allowed_yaml = {
        ".github/dependabot.yml",
        ".github/workflows/ci.yml",
        ".github/workflows/drt.yml",
        ".github/workflows/lean.yml",
        "docdag.yaml",
    }
    add("tracked YAML files are only tool files", set(yaml_files) == allowed_yaml, ", ".join(yaml_files))

    banned_names = {"Cargo.toml", "Cargo.lock", "quint.toml", "rust-toolchain", "rust-toolchain.toml"}
    banned_suffixes = {".rs", ".tla", ".qnt", ".vpr"}
    banned_found = [
        rel
        for rel in tracked
        if not rel.startswith("docs/")
        and (
            pathlib.PurePosixPath(rel).name in banned_names
            or pathlib.PurePosixPath(rel).suffix in banned_suffixes
        )
    ]
    add("no Rust, TLA+, Quint, or Viper project files", banned_found == [], " ".join(banned_found) or "none")

    accepted_adrs = [
        ("ADR 0001", "docs/ADR/0001-aim-for-a-full-kubernetes-replacement-on-fixed-premises.md"),
        ("ADR 0002", "docs/ADR/0002-implement-the-core-in-go-and-write-specifications-in-lean-4.md"),
        ("ADR 0003", "docs/ADR/0003-assure-the-implementation-by-differential-testing-against-lean-models-instead-of-proving-it.md"),
        ("ADR 0004", "docs/ADR/0004-fix-a-subset-of-the-etcd-v3-api-without-leases-as-the-store-contract.md"),
        ("ADR 0017", "docs/ADR/0017-build-the-drt-harness-and-the-theorem-statement-guard-first-and-defer-the-models-the-esr-proof-and-the-kubernetes-oracle.md"),
        ("ADR 0018", "docs/ADR/0018-define-the-g0-verification-gate-pass-criteria.md"),
    ]
    for label, rel in accepted_adrs:
        status = frontmatter_status(rel)
        add(f"{label} is accepted", status == "accepted", status)

    binding_ok, binding_ids, binding_detail = query_binding_adrs()
    add(
        "docdag binding ADRs are 0001, 0002, 0003, 0004, 0017, and 0018",
        binding_ok and binding_ids == ["0001", "0002", "0003", "0004", "0017", "0018"],
        binding_detail,
    )

    supersedes = []
    for rel in tracked:
        if not rel.startswith("docs/ADR/") or not rel.endswith(".md") or rel == "docs/ADR/template.md":
            continue
        for line in read(rel).splitlines():
            if line.startswith("supersedes:"):
                supersedes.append(f"{pathlib.PurePosixPath(rel).name}:{line}")
    add("no ADR supersedes another", supersedes == [], "; ".join(supersedes) or "none")

    lean_rels = [rel for rel in tracked if rel.startswith("spec/") and rel.endswith(".lean")]
    expected_lean = {
        "spec/Nayutar.lean",
        "spec/Nayutar/Drt.lean",
        "spec/Nayutar/Drt/Store.lean",
        "spec/Nayutar/Fixture/Register.lean",
        "spec/Nayutar/Proofs.lean",
        "spec/Nayutar/Proofs/Register.lean",
        "spec/Nayutar/Spec.lean",
        "spec/Nayutar/Statements.lean",
        "spec/Nayutar/Statements/Register.lean",
        "spec/Nayutar/Store.lean",
    }
    add(
        "Lean sources cover register fixture, store model, statements, and proofs without watch or ownership",
        set(lean_rels) == expected_lean
        and not any("Watch" in rel or "Ownership" in rel for rel in lean_rels),
        f"{len(lean_rels)} lean files",
    )
    add("statements use sorry", "sorry" in read("spec/Nayutar/Statements/Register.lean"), "Statements/Register.lean")
    non_statement_lean = [
        rel for rel in lean_rels if rel != "spec/Nayutar/Statements.lean" and not rel.startswith("spec/Nayutar/Statements/")
    ]
    add(
        "proofs and models do not use sorry",
        not any("sorry" in read(rel) for rel in non_statement_lean),
        "non-statement Lean files",
    )
    add("no ESR text in Lean sources", not any("ESR" in read(rel) for rel in lean_rels), "spec/**/*.lean")

    reg = read("core/drt/register_drt_test.go")
    store_drt = read("core/drt/store_drt_test.go")
    store_etcd_drt = read("core/drt/store_etcd_drt_test.go")
    add(
        "DRT comparisons are behind the drt build tag",
        reg.startswith("//go:build drt\n")
        and store_drt.startswith("//go:build drt\n")
        and store_etcd_drt.startswith("//go:build drt && etcd\n"),
        "core/drt/*_drt_test.go",
    )
    add(
        "DRT comparisons call RunModel for register and store",
        'RunModel("register"' in reg and 'RunModel("store"' in store_drt,
        "core/drt",
    )
    add(
        "DRT tests are not skipped",
        all("t.Skip" not in read(rel) for rel in [
            "core/drt/register_drt_test.go",
            "core/drt/store_drt_test.go",
            "core/drt/store_etcd_drt_test.go",
            "core/drt/config_test.go",
        ]),
        "core/drt",
    )

    workflows = ci + "\n" + drt + "\n" + lean
    add("workflows do not name kind or kwok", "kind" not in workflows and "kwok" not in workflows, "workflows")

    product_go = [rel for rel in tracked if rel.endswith(".go") and not rel.endswith("_test.go")]
    expected_product_go = {
        "cmd/check-protected-theorems/main.go",
        "core/doc.go",
        "core/drt/drt.go",
        "core/store/etcdstore/etcdstore.go",
        "core/store/etcdstore/etcdtest/etcdtest.go",
        "core/store/memstore/memstore.go",
        "core/store/store.go",
        "core/store/storetest/contract.go",
        "core/store/storetest/linearizability.go",
        "core/store/storetest/scope.go",
        "core/store/validate.go",
        "core/theoremguard.go",
    }
    add(
        "non-test Go files match theorem guard, DRT runner, and store packages",
        set(product_go) == expected_product_go,
        f"{len(product_go)} non-test Go files",
    )

    agents = read("AGENTS.md")
    add(
        "AGENTS.md says plain go test does not run the Lean DRT",
        "通常の `go test ./...` では走らず" in agents and "go test -tags drt" in agents,
        "AGENTS.md",
    )
    add(
        "lean workflow runs lake comparator without disabling the sandbox",
        "lake comparator" in lean and "--inadvisably-no-sandbox" not in lean,
        "lean.yml",
    )
    comparator = json.loads(read("spec/comparator.json"))
    add(
        "comparator.json permits only the three standard axioms",
        set(comparator["permitted_axioms"]) == {"propext", "Quot.sound", "Classical.choice"}
        and comparator["theorem_names"]
        == [
            "Nayutar.Fixture.Register.step_revision_monotone",
            "Nayutar.Fixture.Register.step_rejected_unchanged",
            "Nayutar.Fixture.Register.step_accepted_bumps",
        ],
        "spec/comparator.json",
    )
    add(
        "Go register implementation lives in the drt test",
        "func stepRegister(" in reg and "func stepRegister(" not in read("core/drt/drt.go"),
        "register_drt_test.go",
    )
    drt_go = read("core/drt/drt.go")
    add(
        "missing lake is an error, not a skip",
        "lake executable not found" in drt_go and "t.Skip" not in drt_go,
        "core/drt/drt.go",
    )

    failed = 0
    for name, ok, detail in checks:
        mark = "PASS" if ok else "FAIL"
        if not ok:
            failed += 1
        print(f"{mark}\t{name}\t{detail}")
    print(f"{len(checks) - failed} passed, {failed} failed")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
