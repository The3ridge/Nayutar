#!/usr/bin/env python3
"""Tests for scripts/agent-harness/ guards, post-edit checks, and self-check.

Run:
    python3 scripts/tests/test-agent-harness.py
"""

from __future__ import annotations

import json
import os
import pathlib
import shutil
import stat
import subprocess
import sys
import tempfile

ROOT = pathlib.Path(__file__).resolve().parents[2]
HARNESS_DIR = ROOT / "scripts" / "agent-harness"
PROTECTED_SCRIPT = HARNESS_DIR / "guard_protected_paths.py"
PUSH_SCRIPT = HARNESS_DIR / "guard_push_commands.py"
POST_EDIT_SCRIPT = HARNESS_DIR / "post_edit_checks.py"
SELFCHECK_SCRIPT = HARNESS_DIR / "harness_selfcheck.py"

if str(HARNESS_DIR) not in sys.path:
    sys.path.insert(0, str(HARNESS_DIR))

import harness_selfcheck as selfcheck  # noqa: E402
from hook_payload import HookPayloadError, parse_hook_stdin  # noqa: E402

PASS = 0
FAIL = 0


def check(name: str, condition: bool, detail: str = "") -> None:
    global PASS, FAIL
    if condition:
        print(f"  PASS  {name}")
        PASS += 1
    else:
        suffix = f" ({detail})" if detail else ""
        print(f"  FAIL  {name}{suffix}")
        FAIL += 1


def run_script(
    script: pathlib.Path,
    stdin_text: str = "",
    *,
    args: list[str] | None = None,
    env: dict[str, str] | None = None,
    cwd: pathlib.Path | None = None,
) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [sys.executable, str(script), *(args or [])],
        input=stdin_text,
        capture_output=True,
        text=True,
        env={**os.environ, **(env or {})},
        cwd=cwd,
        check=False,
    )


def claude_edit(tool: str, path: str, event: str = "PreToolUse") -> str:
    field = "notebook_path" if tool == "NotebookEdit" else "file_path"
    return json.dumps({"hook_event_name": event, "tool_name": tool, "tool_input": {field: path}})


def claude_multiedit(
    file_path: str | None,
    edits: list[dict[str, str]],
    event: str = "PreToolUse",
) -> str:
    tool_input: dict[str, object] = {"edits": edits}
    if file_path is not None:
        tool_input["file_path"] = file_path
    return json.dumps({"hook_event_name": event, "tool_name": "MultiEdit", "tool_input": tool_input})


def codex_patch(*paths: str) -> str:
    body = ["*** Begin Patch"]
    for p in paths:
        body += [f"*** Update File: {p}", "@@", "+x"]
    body.append("*** End Patch")
    return json.dumps({"tool_name": "apply_patch", "tool_input": {"command": "\n".join(body)}})


def codex_add_patch(*paths: str) -> str:
    body = ["*** Begin Patch"]
    for p in paths:
        body += [f"*** Add File: {p}", "+x"]
    body.append("*** End Patch")
    return json.dumps({"tool_name": "apply_patch", "tool_input": {"command": "\n".join(body)}})


def shell(command: str | list[str], tool: str = "Bash") -> str:
    return json.dumps({"tool_name": tool, "tool_input": {"command": command}})


def decision(res: subprocess.CompletedProcess[str]) -> str | None:
    if not res.stdout.strip():
        return None
    return json.loads(res.stdout)["hookSpecificOutput"]["permissionDecision"]


def decision_reason(res: subprocess.CompletedProcess[str]) -> str:
    if not res.stdout.strip():
        return ""
    return json.loads(res.stdout)["hookSpecificOutput"].get("permissionDecisionReason", "")


def test_hook_payload() -> None:
    print("hook_payload.py")
    parsed = parse_hook_stdin(
        claude_multiedit(
            "config/dev.lkal",
            [
                {"old_string": "a", "new_string": "alpha"},
                {"old_string": "b", "new_string": "beta"},
            ],
        )
    )
    check(
        "MultiEdit extracts top-level file_path and concatenates new_string",
        parsed is not None
        and parsed.paths == ("config/dev.lkal",)
        and parsed.added_text == {"config/dev.lkal": "alpha\nbeta"},
    )

    nested = parse_hook_stdin(
        claude_multiedit(
            None,
            [
                {"file_path": "a.lkal", "new_string": "one"},
                {"file_path": "b.lkal", "new_string": "two"},
                {"file_path": "a.lkal", "new_string": "three"},
            ],
        )
    )
    check(
        "MultiEdit supports nested edits carrying file_path",
        nested is not None
        and nested.paths == ("a.lkal", "b.lkal")
        and nested.added_text == {"a.lkal": "one\nthree", "b.lkal": "two"},
    )

    raised = False
    try:
        parse_hook_stdin(claude_multiedit(None, [{"old_string": "x", "new_string": "y"}]))
    except HookPayloadError:
        raised = True
    check("MultiEdit without any target path raises HookPayloadError", raised)


def test_protected_paths(tmp: pathlib.Path) -> None:
    print("guard_protected_paths.py")
    host = tmp / "kisage" / "host.json"
    store = tmp / "state" / "store"
    managed = tmp / "managed"
    host.parent.mkdir(parents=True)
    host.write_text(
        json.dumps(
            {
                "storeDirectory": str(store),
                "lockDirectory": str(tmp / "state" / "locks"),
                "resources": [{"managementRoot": str(managed), "targetPath": str(managed / "dev.lkal")}],
            }
        ),
        encoding="utf-8",
    )
    env = {"KISAGE_HOST_CONFIG": str(host)}

    def blocked(
        payload: str,
        run_env: dict[str, str] = env,
        cwd: pathlib.Path | None = None,
    ) -> tuple[bool, str]:
        res = run_script(PROTECTED_SCRIPT, payload, env=run_env, cwd=cwd)
        return res.returncode == 2, res.stderr

    cases_blocked = {
        "dotenv": claude_edit("Write", str(ROOT / ".env")),
        "dotenv with suffix": claude_edit("Edit", str(ROOT / ".env.local")),
        "private key": claude_edit("Write", str(ROOT / "tls" / "server.key")),
        "certificate": claude_edit("Write", str(ROOT / "tls" / "ca.pem")),
        "secrets dir": claude_edit("Write", str(ROOT / "secrets" / "token")),
        "git internals": claude_edit("Edit", str(ROOT / ".git" / "config")),
        "notebook in .git": claude_edit("NotebookEdit", str(ROOT / ".git" / "x.ipynb")),
        "kisage host config": claude_edit("Edit", str(host)),
        "kisage store": claude_edit("Write", str(store / "nayutar" / "rev")),
        "kisage locks": claude_edit("Write", str(tmp / "state" / "locks" / "a.lock")),
        "kisage managed target": claude_edit("Edit", str(managed / "dev.lkal")),
        "path that resolves into the store": claude_edit("Write", str(store / ".." / "store" / "x")),
        "codex patch touching one protected path": codex_patch("config/dev.lkal", str(host)),
    }
    for name, payload in cases_blocked.items():
        ok, stderr = blocked(payload)
        check(f"blocks {name}", ok, stderr.strip())

    _, stderr = blocked(claude_edit("Edit", str(managed / "dev.lkal")))
    check("kisage block names the declaration file", "config/dev.lkal" in stderr, stderr.strip())

    theorem_blocked = {
        "comparator config": claude_edit("Edit", "spec/comparator.json"),
        "statements root": claude_edit("Edit", "spec/Nayutar/Statements.lean"),
        "statements file": claude_edit("Edit", "spec/Nayutar/Statements/Register.lean"),
        "nested statements file": claude_edit("Edit", "spec/Nayutar/Statements/Store/Watch.lean"),
        "absolute statements file": claude_edit("Edit", str(tmp / "spec" / "Nayutar" / "Statements" / "Register.lean")),
        "worktree statements file": claude_edit("Edit", "/x/.claude/worktrees/w/spec/Nayutar/Statements/Register.lean"),
        "fixture file": claude_edit("Edit", "spec/Nayutar/Fixture/Register.lean"),
        "nested fixture file": claude_edit("Write", "spec/Nayutar/Fixture/Deep/Model.lean"),
        "fixture root": claude_edit("Edit", "spec/Nayutar/Fixture.lean"),
        "lakefile": claude_edit("Edit", "spec/lakefile.toml"),
        "new lakefile.lean": claude_edit("Write", "spec/lakefile.lean"),
        "lake manifest": claude_edit("Edit", "spec/lake-manifest.json"),
        "lean toolchain": claude_edit("Write", "spec/lean-toolchain"),
        "absolute fixture file": claude_edit("Edit", str(tmp / "spec" / "Nayutar" / "Fixture" / "Register.lean")),
        "absolute fixture root": claude_edit("Edit", str(tmp / "spec" / "Nayutar" / "Fixture.lean")),
        "absolute lakefile": claude_edit("Edit", str(tmp / "spec" / "lakefile.toml")),
        "absolute new lakefile.lean": claude_edit("Write", str(tmp / "spec" / "lakefile.lean")),
        "absolute lake manifest": claude_edit("Edit", str(tmp / "spec" / "lake-manifest.json")),
        "absolute lean toolchain": claude_edit("Edit", str(tmp / "spec" / "lean-toolchain")),
        "worktree fixture file": claude_edit("Edit", "/x/.claude/worktrees/w/spec/Nayutar/Fixture/Register.lean"),
        "worktree fixture root": claude_edit("Edit", "/x/.claude/worktrees/w/spec/Nayutar/Fixture.lean"),
        "worktree lakefile": claude_edit("Edit", "/x/.claude/worktrees/w/spec/lakefile.toml"),
        "worktree new lakefile.lean": claude_edit("Write", "/x/.claude/worktrees/w/spec/lakefile.lean"),
        "worktree lake manifest": claude_edit("Edit", "/x/.claude/worktrees/w/spec/lake-manifest.json"),
        "worktree lean toolchain": claude_edit("Edit", "/x/.claude/worktrees/w/spec/lean-toolchain"),
        "fixture file via ..": claude_edit("Edit", "spec/Nayutar/Proofs/../Fixture/Register.lean"),
        "lakefile via ..": claude_edit("Edit", "spec/Nayutar/../lakefile.toml"),
        "lakefile.lean via ..": claude_edit("Write", "spec/Nayutar/../lakefile.lean"),
        "notebook edit of the fixture": claude_edit("NotebookEdit", "spec/Nayutar/Fixture/Register.lean"),
    }
    for name, payload in theorem_blocked.items():
        ok, stderr = blocked(payload)
        check(f"blocks {name} with theorem statements message", ok and "theorem statements" in stderr, stderr.strip())

    repo = tmp / "repo"
    spec = repo / "spec"
    fixture = spec / "Nayutar" / "Fixture"
    other = repo / "other"
    fixture.mkdir(parents=True)
    (spec / "Nayutar" / "Statements").mkdir(parents=True)
    other.mkdir(parents=True)

    relative_theorem_blocked = [
        ("statements root from spec", claude_edit("Edit", "Nayutar/Statements.lean"), spec),
        ("comparator config from spec", claude_edit("Edit", "comparator.json"), spec),
        ("statements file via .. from fixture", claude_edit("Edit", "../Statements/Register.lean"), fixture),
        ("codex patch statements root from spec", codex_patch("Nayutar/Statements.lean"), spec),
        ("fixture file from spec", claude_edit("Edit", "Nayutar/Fixture/Register.lean"), spec),
        ("fixture root from spec", claude_edit("Edit", "Nayutar/Fixture.lean"), spec),
        ("lakefile from spec", claude_edit("Edit", "lakefile.toml"), spec),
        ("new lakefile.lean from spec", claude_edit("Write", "lakefile.lean"), spec),
        ("new lakefile.lean via .. from fixture", claude_edit("Write", "../../lakefile.lean"), fixture),
        ("codex add file lakefile.lean from spec", codex_add_patch("lakefile.lean"), spec),
        ("lake manifest from spec", claude_edit("Edit", "lake-manifest.json"), spec),
        ("lean toolchain from spec", claude_edit("Edit", "lean-toolchain"), spec),
        ("fixture file via .. from statements", claude_edit("Edit", "../Fixture/Register.lean"), spec / "Nayutar" / "Statements"),
        ("fixture root via .. from fixture", claude_edit("Edit", "../Fixture.lean"), fixture),
        ("lakefile via .. from fixture", claude_edit("Edit", "../../lakefile.toml"), fixture),
        ("fixture file from fixture", claude_edit("Edit", "Register.lean"), fixture),
        ("codex patch fixture file from spec", codex_patch("Nayutar/Fixture/Register.lean"), spec),
        ("codex patch lakefile from spec", codex_patch("lakefile.toml"), spec),
    ]
    for name, payload, cwd in relative_theorem_blocked:
        ok, stderr = blocked(payload, cwd=cwd)
        check(f"blocks {name} with theorem statements message", ok and "theorem statements" in stderr, stderr.strip())

    sym_dir = tmp / "symlinks"
    sym_dir.mkdir(parents=True)
    (sym_dir / "to_env.txt").symlink_to(tmp / "real_target" / ".env")
    (sym_dir / "to_env_local.txt").symlink_to(tmp / "real_target" / ".env.production")
    (sym_dir / "to_key.txt").symlink_to(tmp / "real_target" / "server.key")
    (sym_dir / "to_secret.txt").symlink_to(tmp / "real_target" / "secrets" / "api_token")
    (sym_dir / "to_git_cfg.txt").symlink_to(tmp / "real_target" / ".git" / "config")
    (sym_dir / "to_stmt.txt").symlink_to(ROOT / "spec" / "Nayutar" / "Statements" / "Register.lean")
    (sym_dir / "to_comp.txt").symlink_to(ROOT / "spec" / "comparator.json")
    (sym_dir / "stmt_dir").symlink_to(ROOT / "spec" / "Nayutar" / "Statements")
    (sym_dir / "to_fixture.txt").symlink_to(ROOT / "spec" / "Nayutar" / "Fixture" / "Register.lean")
    (sym_dir / "to_fixture_root.txt").symlink_to(ROOT / "spec" / "Nayutar" / "Fixture.lean")
    (sym_dir / "to_lakefile.txt").symlink_to(ROOT / "spec" / "lakefile.toml")
    (sym_dir / "to_lakefile_lean.txt").symlink_to(ROOT / "spec" / "lakefile.lean")
    (sym_dir / "to_manifest.txt").symlink_to(ROOT / "spec" / "lake-manifest.json")
    (sym_dir / "to_toolchain.txt").symlink_to(ROOT / "spec" / "lean-toolchain")
    (sym_dir / "fixture_dir").symlink_to(ROOT / "spec" / "Nayutar" / "Fixture")

    symlink_blocked = {
        "symlink to .env": claude_edit("Edit", str(sym_dir / "to_env.txt")),
        "symlink to .env.production": claude_edit("Write", str(sym_dir / "to_env_local.txt")),
        "symlink to private key": claude_edit("Edit", str(sym_dir / "to_key.txt")),
        "symlink to secrets file": claude_edit("Write", str(sym_dir / "to_secret.txt")),
        "symlink to .git/config": claude_edit("Edit", str(sym_dir / "to_git_cfg.txt")),
        "symlink to statements file": claude_edit("Edit", str(sym_dir / "to_stmt.txt")),
        "symlink to comparator.json": claude_edit("Edit", str(sym_dir / "to_comp.txt")),
        "file through symlinked Statements dir": claude_edit("Edit", str(sym_dir / "stmt_dir" / "Register.lean")),
        "symlink to fixture file": claude_edit("Edit", str(sym_dir / "to_fixture.txt")),
        "symlink to fixture root": claude_edit("Edit", str(sym_dir / "to_fixture_root.txt")),
        "symlink to lakefile": claude_edit("Edit", str(sym_dir / "to_lakefile.txt")),
        "symlink to lakefile.lean": claude_edit("Write", str(sym_dir / "to_lakefile_lean.txt")),
        "symlink to lake manifest": claude_edit("Edit", str(sym_dir / "to_manifest.txt")),
        "symlink to lean toolchain": claude_edit("Edit", str(sym_dir / "to_toolchain.txt")),
        "file through symlinked Fixture dir": claude_edit("Edit", str(sym_dir / "fixture_dir" / "Register.lean")),
    }
    for name, payload in symlink_blocked.items():
        ok, stderr = blocked(payload)
        check(f"blocks {name}", ok, stderr.strip())

    multiedit_blocked = {
        "MultiEdit on comparator.json": claude_multiedit(
            "spec/comparator.json",
            [{"old_string": "a", "new_string": "b"}],
        ),
        "MultiEdit with nested protected file_path": claude_multiedit(
            None,
            [
                {"file_path": "README.md", "new_string": "ok"},
                {"file_path": "spec/Nayutar/Statements/Register.lean", "new_string": "bad"},
            ],
        ),
        "MultiEdit on the fixture": claude_multiedit(
            "spec/Nayutar/Fixture/Register.lean",
            [{"old_string": "a", "new_string": "b"}],
        ),
        "MultiEdit on the lakefile": claude_multiedit("spec/lakefile.toml", [{"old_string": "a", "new_string": "b"}]),
        "MultiEdit with nested lean-toolchain file_path": claude_multiedit(
            None,
            [
                {"file_path": "README.md", "new_string": "ok"},
                {"file_path": "spec/lean-toolchain", "new_string": "bad"},
            ],
        ),
        "codex patch on the fixture": codex_patch("README.md", "spec/Nayutar/Fixture/Register.lean"),
        "codex patch on the fixture root": codex_patch("spec/Nayutar/Fixture.lean"),
        "codex patch on the lakefile": codex_patch("spec/lakefile.toml"),
        "codex add file lakefile.lean": codex_add_patch("README.md", "spec/lakefile.lean"),
        "codex add file lakefile.lean via ..": codex_add_patch("spec/Nayutar/../lakefile.lean"),
        "MultiEdit on lakefile.lean": claude_multiedit("spec/lakefile.lean", [{"old_string": "a", "new_string": "b"}]),
        "codex patch on the lake manifest": codex_patch("spec/lake-manifest.json"),
        "codex patch on the lean toolchain": codex_patch("spec/lean-toolchain"),
    }
    for name, payload in multiedit_blocked.items():
        ok, stderr = blocked(payload)
        check(f"blocks {name}", ok, stderr.strip())

    model_or_build_message = {
        "lakefile": claude_edit("Edit", "spec/lakefile.toml"),
        "lakefile.lean": claude_edit("Write", "spec/lakefile.lean"),
        "fixture file": claude_edit("Edit", "spec/Nayutar/Fixture/Register.lean"),
        "fixture root": claude_edit("Edit", "spec/Nayutar/Fixture.lean"),
        "lake manifest": claude_edit("Edit", "spec/lake-manifest.json"),
        "lean toolchain": claude_edit("Edit", "spec/lean-toolchain"),
    }
    for name, payload in model_or_build_message.items():
        ok, stderr = blocked(payload)
        check(
            f"model and build block for the {name} explains that it changes the statements",
            ok and "determine what the protected theorem statements mean" in stderr and "belongs to the user" in stderr,
            stderr.strip(),
        )
    ok, stderr = blocked(claude_edit("Write", "spec/lakefile.lean"))
    check("model and build block lists spec/lakefile.lean", ok and "spec/lakefile.lean," in stderr, stderr.strip())
    ok, stderr = blocked(claude_edit("Edit", "spec/Nayutar/Fixture/Register.lean"))
    check(
        "model and build block names the two explicit model paths",
        ok and "spec/Nayutar/Fixture/ and spec/Nayutar/Fixture.lean" in stderr and "Fixture*" not in stderr,
        stderr.strip(),
    )
    ok, stderr = blocked(claude_edit("Edit", "spec/Nayutar/Statements/Register.lean"))
    check(
        "statement block does not use the model and build message",
        ok and "determine what the protected theorem statements mean" not in stderr,
        stderr.strip(),
    )

    (sym_dir / "to_repo_yaml.txt").symlink_to(ROOT / "config" / "sneaky.yaml")
    yaml_blocked = {
        "repo-relative .yaml": claude_edit("Write", "config/cluster.yaml"),
        "repo-relative .yml": claude_edit("Edit", "deploy/service.yml"),
        "absolute repo .yaml": claude_edit("Write", str(ROOT / "manifest.yaml")),
        "MultiEdit repo .yaml": claude_multiedit("config/app.yaml", [{"old_string": "a", "new_string": "b"}]),
        "codex patch repo .yml": codex_patch("config/app.yml"),
        "non-workflow .github yaml": claude_edit("Write", ".github/labeler.yml"),
        "dependabot .yaml spelling": claude_edit("Write", ".github/dependabot.yaml"),
        "dependabot.yml in a subdirectory of .github": claude_edit("Write", ".github/sub/dependabot.yml"),
        "dependabot.yml outside .github": claude_edit("Write", "sub/.github/dependabot.yml"),
        "dependabot.yml at the root": claude_edit("Write", "dependabot.yml"),
        "absolute nested dependabot.yml": claude_edit("Write", str(ROOT / "sub" / ".github" / "dependabot.yml")),
        "codex patch dependabot.yaml": codex_patch(".github/dependabot.yaml"),
        "nested workflow subdir yaml": claude_edit("Write", ".github/workflows/sub/extra.yml"),
        "symlink targeting repo .yaml": claude_edit("Write", str(sym_dir / "to_repo_yaml.txt")),
    }
    for name, payload in yaml_blocked.items():
        ok, stderr = blocked(payload)
        check(f"blocks {name} with LKAL guidance", ok and ".lkal" in stderr, stderr.strip())

    cases_allowed = {
        "env example": claude_edit("Write", str(ROOT / ".env.example")),
        "env template": claude_edit("Write", str(ROOT / "deploy" / ".env.template")),
        "tracked declaration": claude_edit("Edit", str(ROOT / "config" / "dev.lkal")),
        "ADR": claude_edit("Edit", str(ROOT / "docs" / "ADR" / "0002-x.md")),
        "sibling of the host config": claude_edit("Write", str(tmp / "kisage" / "notes.md")),
        "codex patch of ordinary files": codex_patch("README.md", "config/dev.lkal"),
        "MultiEdit of ordinary files": claude_multiedit(
            "config/dev.lkal",
            [{"old_string": "a", "new_string": "b"}],
        ),
        "proofs file": claude_edit("Edit", "spec/Nayutar/Proofs/Register.lean"),
        "proofs root": claude_edit("Edit", "spec/Nayutar/Proofs.lean"),
        "drt main": claude_edit("Edit", "spec/Nayutar/Drt.lean"),
        "drt file": claude_edit("Edit", "spec/Nayutar/Drt/Store.lean"),
        "store model": claude_edit("Edit", "spec/Nayutar/Store.lean"),
        "spec root module": claude_edit("Edit", "spec/Nayutar/Spec.lean"),
        "library root module": claude_edit("Edit", "spec/Nayutar.lean"),
        "proofs root file": claude_edit("Edit", "spec/Nayutar/Proofs.lean"),
        "FixtureHelper": claude_edit("Write", "spec/Nayutar/FixtureHelper.lean"),
        "docs Fixture notes": claude_edit("Write", "docs/Fixture/notes.md"),
        "other lakefile": claude_edit("Write", "other/lakefile.toml"),
        "other lakefile.lean": claude_edit("Write", "other/lakefile.lean"),
        "root lakefile.lean": claude_edit("Write", "lakefile.lean"),
        "spec subdirectory lakefile.lean": claude_edit("Write", "spec/sub/lakefile.lean"),
        "codex add file other lakefile.lean": codex_add_patch("other/lakefile.lean"),
        "other lean-toolchain": claude_edit("Write", "other/lean-toolchain"),
        "other lake-manifest": claude_edit("Write", "other/lake-manifest.json"),
        "root lakefile": claude_edit("Write", "lakefile.toml"),
        "dependabot.yml": claude_edit("Write", ".github/dependabot.yml"),
        "absolute dependabot.yml": claude_edit("Write", str(ROOT / ".github" / "dependabot.yml")),
        "codex patch dependabot.yml": codex_patch(".github/dependabot.yml"),
        "docs Statements notes": claude_edit("Write", "docs/Statements/notes.md"),
        "other comparator.json": claude_edit("Write", "other/comparator.json"),
        "StatementsHelper": claude_edit("Write", "spec/Nayutar/StatementsHelper.lean"),
        "docdag.yaml": claude_edit("Edit", "docdag.yaml"),
        "absolute docdag.yaml": claude_edit("Edit", str(ROOT / "docdag.yaml")),
        "GitHub workflow .yml": claude_edit("Edit", ".github/workflows/ci.yml"),
        "GitHub workflow .yaml": claude_edit("Write", ".github/workflows/release.yaml"),
        "temporary yaml outside repo": claude_edit("Write", str(tmp / "scratch.yaml")),
    }
    for name, payload in cases_allowed.items():
        ok, stderr = blocked(payload)
        check(f"allows {name}", not ok, stderr.strip())

    relative_allowed = [
        ("proofs file from spec", claude_edit("Edit", "Nayutar/Proofs/Register.lean"), spec),
        ("lakefile from other", claude_edit("Edit", "lakefile.toml"), other),
        ("lakefile.lean from other", claude_edit("Write", "lakefile.lean"), other),
        ("lean-toolchain from other", claude_edit("Edit", "lean-toolchain"), other),
        ("drt file from spec", claude_edit("Edit", "Nayutar/Drt/Store.lean"), spec),
        ("fixture helper from spec", claude_edit("Edit", "Nayutar/FixtureHelper.lean"), spec),
        ("comparator config from other", claude_edit("Edit", "comparator.json"), other),
    ]
    for name, payload, cwd in relative_allowed:
        ok, stderr = blocked(payload, cwd=cwd)
        check(f"allows {name}", not ok, stderr.strip())

    missing = {"KISAGE_HOST_CONFIG": str(tmp / "absent" / "host.json")}
    ok, _ = blocked(claude_edit("Write", str(tmp / "absent" / "host.json")), missing)
    check("still blocks an unreadable host config path itself", ok)
    ok, _ = blocked(claude_edit("Write", str(store / "x")), missing)
    check("unreadable host config protects nothing else", not ok)

    res = run_script(PROTECTED_SCRIPT, json.dumps({"tool_name": "Read", "tool_input": {"file_path": str(host)}}), env=env)
    check("ignores unwatched tools", res.returncode == 0)
    res = run_script(PROTECTED_SCRIPT, "{not json", env=env)
    check("fails closed on malformed payload", res.returncode == 2)
    res = run_script(PROTECTED_SCRIPT, json.dumps({"tool_name": "Write", "tool_input": {}}), env=env)
    check("fails closed when a watched tool names no path", res.returncode == 2)


def test_push_commands(tmp: pathlib.Path) -> None:
    print("guard_push_commands.py")
    denied = [
        "git push",
        "git push origin main",
        "git push --force-with-lease",
        "git -C ../LKAL push",
        "git -c user.name=x push",
        "git --no-pager push",
        "cd repo && git push",
        "make test; git push",
        "echo $(git push)",
        "git remote add upstream https://example.invalid/x.git",
        "git remote set-url origin x",
        "gh pr create --fill",
        "gh pr merge 3 --squash",
        "gh workflow run ci.yml",
        "gh run rerun 42",
        "gh run cancel 42",
        "gh release create v0.1.0",
        "gh repo sync",
        "GIT_PUSH=1 git  push",
        "env git push origin main",
        "env GIT_SSH_COMMAND=ssh FOO=1 git push",
        "command git push",
        "nohup git push",
        "timeout 30s git push",
        "/usr/bin/git push",
        "/usr/local/bin/gh pr create --fill",
        "git -C ../LKAL remote add upstream https://example.invalid/x.git",
        "git -C ../LKAL remote set-url origin https://example.invalid/x.git",
        "git -c credential.helper=store remote update",
        "git --git-dir=.git --work-tree=. remote add up https://example.invalid/x.git",
        "git -C ../LKAL fetch origin +HEAD:refs/heads/push",
    ]
    for cmd in denied:
        check(f"denies `{cmd}`", decision(run_script(PUSH_SCRIPT, shell(cmd))) == "deny")
    check(
        "denies a Codex argv command",
        decision(run_script(PUSH_SCRIPT, shell(["bash", "-lc", "git push"], tool="shell"))) == "deny",
    )

    hook_bypasses = [
        'git commit --no-verify -m "wip"',
        'git commit -m "wip" --no-verify',
        'git commit -n -m "wip"',
        'git commit -an -m "wip"',
        'git commit --no-gpg-sign -m "wip"',
        'git -c core.hooksPath=/dev/null commit -m "wip"',
        'git -ccore.hooksPath=/tmp commit -m "wip"',
        'git -c commit.gpgsign=false commit -m "wip"',
        "git config core.hooksPath /dev/null",
    ]
    for cmd in hook_bypasses:
        res = run_script(PUSH_SCRIPT, shell(cmd))
        check(
            f"denies hook/signature bypass `{cmd}`",
            decision(res) == "deny" and "hooks" in decision_reason(res).lower(),
            decision_reason(res),
        )

    comparator_bypasses = [
        "lake comparator --inadvisably-no-sandbox",
        "(cd spec && lake comparator --inadvisably-no-sandbox)",
        "lake --dir spec comparator --inadvisably-no-sandbox",
    ]
    for cmd in comparator_bypasses:
        res = run_script(PUSH_SCRIPT, shell(cmd))
        check(
            f"denies comparator sandbox bypass `{cmd}`",
            decision(res) == "deny" and "sandbox" in decision_reason(res).lower(),
            decision_reason(res),
        )

    lake_update_denied = [
        "lake update",
        "lake update kisage",
        "(cd spec && lake update)",
        "lake -d spec update",
        "lake --dir spec update",
        "lake --dir=spec update",
        "lake -q update",
        "lake -K foo=bar update",
        "lake upgrade",
        "FOO=1 lake update",
        "env FOO=1 lake update",
        "/root/.elan/bin/lake update",
        "cd spec; lake update",
        "echo ok && lake update",
        "bash -c 'lake update'",
        "xargs lake update",
        "echo $(lake update)",
        "lake build --update",
        "lake --update build",
        "lake -U build",
        "(cd spec && lake build nayutar-drt --update)",
        "lake -d spec --update build",
        "FOO=1 lake test --update",
    ]
    for cmd in lake_update_denied:
        res = run_script(PUSH_SCRIPT, shell(cmd))
        reason = decision_reason(res)
        check(
            f"denies lake update `{cmd}`",
            decision(res) == "deny" and "manifest" in reason and "user" in reason,
            reason,
        )

    shell_mutations = [
        "echo '{}' > spec/comparator.json",
        "echo '{}' >> spec/comparator.json",
        "echo '{}' &> spec/Nayutar/Statements.lean",
        "echo '{}' 1> spec/Nayutar/Statements/Register.lean",
        "> spec/comparator.json",
        "echo '#!/bin/sh' > .git/hooks/pre-commit",
        "echo '{}' > ~/.config/kisage/nayutar.json",
        'echo "{}" > "$KISAGE_HOST_CONFIG"',
        "sed -i 's/a/b/' spec/comparator.json",
        "sed -i.bak 's/a/b/' spec/Nayutar/Statements/Register.lean",
        "perl -pi -e 's/a/b/' spec/comparator.json",
        "echo '{}' | tee spec/comparator.json",
        "echo '{}' | tee -a spec/Nayutar/Statements.lean",
        "cp /tmp/evil.json spec/comparator.json",
        "mv /tmp/evil.lean spec/Nayutar/Statements.lean",
        "rm spec/comparator.json",
        "rm -rf spec/Nayutar/Statements",
        "dd if=/dev/zero of=spec/comparator.json",
        "truncate -s 0 spec/comparator.json",
        "install -m 644 /tmp/x spec/comparator.json",
        "ln -sf /tmp/x spec/comparator.json",
        "ln spec/comparator.json /tmp/hardlink.json",
        'python3 -c \'open("spec/comparator.json", "w").write("{}")\'',
        'python -c \'import pathlib; pathlib.Path("spec/Nayutar/Statements.lean").write_text("")\'',
        "(cd spec && sed -i 's/a/b/' comparator.json)",
        "echo x > spec/Nayutar/Fixture/Register.lean",
        "echo x >> spec/Nayutar/Fixture.lean",
        "echo x > spec/lakefile.toml",
        "echo x >> spec/lake-manifest.json",
        "echo x &> spec/lean-toolchain",
        "echo x > ./spec/lakefile.toml",
        "echo x > /x/.claude/worktrees/w/spec/lakefile.toml",
        "echo x > /x/.claude/worktrees/w/spec/Nayutar/Fixture/Register.lean",
        "> spec/lean-toolchain",
        "sed -i 's/a/b/' spec/Nayutar/Fixture/Register.lean",
        "sed -i.bak 's/a/b/' spec/Nayutar/Fixture.lean",
        "sed -i 's/a/b/' spec/lakefile.toml",
        "perl -pi -e 's/a/b/' spec/lake-manifest.json",
        "echo x | tee spec/lean-toolchain",
        "echo x | tee -a spec/Nayutar/Fixture/Register.lean",
        "cp /tmp/evil.lean spec/Nayutar/Fixture/Register.lean",
        "cp /tmp/evil.toml spec/lakefile.toml",
        "install -m 644 /tmp/x spec/lean-toolchain",
        "mv /tmp/evil.lean spec/Nayutar/Fixture.lean",
        "mv /tmp/evil.json spec/lake-manifest.json",
        "rm spec/lakefile.toml",
        "rm -rf spec/Nayutar/Fixture",
        "rm spec/Nayutar/Fixture.lean",
        "dd if=/dev/zero of=spec/lean-toolchain",
        "truncate -s 0 spec/lake-manifest.json",
        "ln -sf /tmp/x spec/lakefile.toml",
        'python3 -c \'open("spec/lakefile.toml", "w").write("")\'',
        'python -c \'import pathlib; pathlib.Path("spec/Nayutar/Fixture/Register.lean").write_text("")\'',
        "(cd spec && sed -i 's/a/b/' lakefile.toml)",
        "(cd spec && echo x > lean-toolchain)",
        "(cd spec && echo x > Nayutar/Fixture/Register.lean)",
        "(cd spec/Nayutar/Fixture && sed -i 's/a/b/' Register.lean)",
        "(cd spec/Nayutar/Fixture && rm -rf .)",
        "echo x > spec/lakefile.lean",
        "echo x >> spec/lakefile.lean",
        "echo x > ./spec/lakefile.lean",
        "echo x > /x/.claude/worktrees/w/spec/lakefile.lean",
        "cp /tmp/x $PWD/spec/lakefile.lean",
        'echo x > "$PWD/spec/lakefile.lean"',
        "cp /tmp/x ${PWD}/spec/lakefile.lean",
        "echo x > 'spec/lakefile.lean'",
        "sed -i 's/a/b/' spec/lakefile.lean",
        "echo x | tee spec/lakefile.lean",
        "cp /tmp/evil.lean spec/lakefile.lean",
        "mv /tmp/evil.lean spec/lakefile.lean",
        "rm spec/lakefile.lean",
        "install -m 644 /tmp/x spec/lakefile.lean",
        "ln -sf /tmp/x spec/lakefile.lean",
        'python3 -c \'open("spec/lakefile.lean", "w").write("")\'',
        "(cd spec && echo x > lakefile.lean)",
        "(cd spec && cp /tmp/x lakefile.lean)",
        "(cd spec && mv /tmp/x lakefile.lean)",
        "echo x > spec/Nayutar/Fixture.lean",
        "echo x > 'spec/Nayutar/Fixture.lean'",
        'echo x > "spec/Nayutar/Fixture.lean"',
        "(echo x > spec/Nayutar/Fixture.lean)",
        "true; echo x > spec/Nayutar/Fixture.lean; true",
        "echo x | tee spec/Nayutar/Fixture.lean",
        "echo x > spec/Nayutar/Fixture.lean&& true",
        'python3 -c \'open("spec/Nayutar/Fixture.lean", "w").write("")\'',
        "python3 -c 'p=\"spec/Nayutar/Fixture.lean\"; open(p, \"w\")'",
        "python3 -c \"open('spec/Nayutar/Fixture.lean', 'w')\"",
    ]
    for cmd in shell_mutations:
        res = run_script(PUSH_SCRIPT, shell(cmd))
        check(
            f"denies protected-path shell mutation `{cmd}`",
            decision(res) == "deny" and "protected" in decision_reason(res).lower(),
            decision_reason(res),
        )

    allowed = [
        "git status",
        "git stash push -m wip",
        "git commit -m 'feat: add push guard'",
        'git commit -m "fix -n flag"',
        "git commit -m 'fix -n flag'",
        'git commit -m "mention --no-verify in docs"',
        'git commit -am "handle -n and --no-verify"',
        'git commit -m"attached -n flag"',
        'git commit --message="mention -n and --no-verify"',
        "git log --grep=push",
        "git log -n 5",
        "git config --get core.hooksPath",
        "(cd spec && lake comparator)",
        'git commit -m "forbid lake comparator --inadvisably-no-sandbox"',
        "cat spec/comparator.json",
        "rg foo spec/Nayutar/Statements",
        "git diff spec/comparator.json",
        "sed -n '1,20p' spec/comparator.json",
        "cat spec/comparator.json 2>/dev/null",
        "git diff spec/comparator.json > /tmp/comparator.diff",
        "sed -i 's/a/b/' spec/Nayutar/Proofs/Register.lean",
        "sed -i 's/a/b/' spec/Nayutar/Drt/Store.lean",
        "echo x > spec/Nayutar/FixtureHelper.lean",
        "echo x > other/lakefile.toml",
        "echo x > docs/Fixture/notes.md",
        "echo x > other/lakefile.lean",
        "echo x > spec/Nayutar/Fixture.lean.bak",
        "echo x > spec/Nayutar/Fixture.leanx",
        "cp /tmp/x spec/Nayutar/Fixture.lean.bak",
        "echo x > other/lakefile.lean.bak",
        "cat spec/lakefile.lean",
        "cat spec/Nayutar/Fixture.lean",
        "grep -n kisage spec/lake-manifest.json",
        "cp /tmp/x other/lean-toolchain",
        "cat spec/lakefile.toml",
        "cat spec/Nayutar/Fixture/Register.lean",
        "rg foo spec/Nayutar/Fixture",
        "git diff spec/lean-toolchain spec/lake-manifest.json",
        "lake build",
        "(cd spec && lake build)",
        "lake env",
        "lake exe nayutar-drt",
        "lake exe nayutar-drt update",
        "lake comparator",
        "lake --version",
        "lake -d spec build",
        "lake --dir spec env",
        "lake build update",
        "lake -K foo=update build",
        "lake exe nayutar-drt --update",
        "lake env printenv --update",
        "lake -K update=1 build",
        "echo lake build --update",
        'git commit -m "mention lake build --update"',
        "grep -rn -- '--update' docs",
        "(cd spec && lake comparator)",
        'git commit -m "docs: mention lake update and spec/lakefile.lean"',
        'echo "run lake update to refresh the manifest"',
        "echo lake update",
        "grep -rn 'lake update' docs",
        "grep -n 'lake update' AGENTS.md",
        "git log --grep='lake update'",
        "lake-update-helper",
        "bash scripts/tests/test-comparator-rejections.sh",
        'python3 -c \'import json; print(json.load(open("spec/comparator.json")))\'',
        "gh pr view 3",
        "gh pr list",
        "gh run view 42",
        "gh run list",
        "kisage plan config/dev.lkal -detailed-exitcode",
    ]
    for cmd in allowed:
        res = run_script(PUSH_SCRIPT, shell(cmd))
        check(f"allows `{cmd}`", res.returncode == 0 and decision(res) is None, res.stdout.strip())

    shell_repo = tmp / "shell_repo"
    shell_spec = shell_repo / "spec"
    shell_nayutar = shell_spec / "Nayutar"
    shell_fixture = shell_nayutar / "Fixture"
    shell_other = shell_repo / "other"
    for d in (shell_fixture, shell_nayutar / "Statements", shell_nayutar / "Proofs", shell_other):
        d.mkdir(parents=True, exist_ok=True)
    (shell_fixture / "Register.lean").write_text("", encoding="utf-8")
    (shell_nayutar / "Proofs" / "Register.lean").write_text("", encoding="utf-8")
    links = shell_other / "links"
    links.mkdir()
    (links / "fixture_dir").symlink_to(shell_fixture)
    (links / "statements_dir").symlink_to(shell_nayutar / "Statements")
    (links / "fixture_root").symlink_to(shell_nayutar / "Fixture.lean")
    (links / "statements_root").symlink_to(shell_nayutar / "Statements.lean")
    (links / "comparator").symlink_to(shell_spec / "comparator.json")
    (links / "lakefile_toml").symlink_to(shell_spec / "lakefile.toml")
    (links / "lakefile_lean").symlink_to(shell_spec / "lakefile.lean")
    (links / "manifest").symlink_to(shell_spec / "lake-manifest.json")
    (links / "toolchain").symlink_to(shell_spec / "lean-toolchain")
    (links / "proofs_dir").symlink_to(shell_nayutar / "Proofs")

    cwd_denied = [
        ("spec/Nayutar", "rm Fixture.lean"),
        ("spec/Nayutar", "echo x > Fixture.lean"),
        ("spec/Nayutar", "rm Fixture/Register.lean"),
        ("spec/Nayutar", "echo x > Fixture/New.lean"),
        ("spec/Nayutar", "rm Statements.lean"),
        ("spec/Nayutar", "rm Statements/Register.lean"),
        ("spec/Nayutar", "rm ../comparator.json"),
        ("spec/Nayutar", "rm ../lakefile.toml"),
        ("spec/Nayutar", "rm ../lakefile.lean"),
        ("spec/Nayutar", "echo x > ../lakefile.lean"),
        ("spec/Nayutar", "rm ../lake-manifest.json"),
        ("spec/Nayutar", "rm ../lean-toolchain"),
        ("spec/Nayutar/Proofs", "rm ../Fixture.lean"),
        ("spec/Nayutar/Proofs", "rm ../Fixture/Register.lean"),
        ("spec/Nayutar/Proofs", "rm ../../lakefile.lean"),
        ("other", "rm links/fixture_dir/Register.lean"),
        ("other", "echo x > links/fixture_dir/New.lean"),
        ("other", "rm links/statements_dir/Register.lean"),
        ("other", "rm links/fixture_root"),
        ("other", "rm links/statements_root"),
        ("other", "rm links/comparator"),
        ("other", "rm links/lakefile_toml"),
        ("other", "rm links/lakefile_lean"),
        ("other", "rm links/manifest"),
        ("other", "rm links/toolchain"),
        ("other", "echo x > links/lakefile_lean"),
    ]
    for rel_cwd, cmd in cwd_denied:
        res = run_script(PUSH_SCRIPT, shell(cmd), cwd=shell_repo / rel_cwd)
        check(
            f"denies `{cmd}` run from {rel_cwd}",
            decision(res) == "deny" and "protected" in decision_reason(res).lower(),
            decision_reason(res),
        )

    cwd_allowed = [
        ("spec/Nayutar", "rm FixtureHelper.lean"),
        ("spec/Nayutar", "rm Proofs/Register.lean"),
        ("spec/Nayutar", "echo x > Fixture.lean.bak"),
        ("spec/Nayutar", "cat Fixture.lean"),
        ("spec/Nayutar/Proofs", "rm Register.lean"),
        ("other", "rm links/proofs_dir/Register.lean"),
        ("other", "echo x > lakefile.lean"),
    ]
    for rel_cwd, cmd in cwd_allowed:
        res = run_script(PUSH_SCRIPT, shell(cmd), cwd=shell_repo / rel_cwd)
        check(f"allows `{cmd}` run from {rel_cwd}", res.returncode == 0 and decision(res) is None, decision_reason(res))

    argv_commit = run_script(PUSH_SCRIPT, shell(["git", "commit", "-m", "fix -n flag"], tool="shell"))
    check(
        "allows Codex argv git commit -m 'fix -n flag'",
        argv_commit.returncode == 0 and decision(argv_commit) is None,
        argv_commit.stdout.strip(),
    )

    res = run_script(PUSH_SCRIPT, json.dumps({"tool_name": "Edit", "tool_input": {"file_path": "x"}}))
    check("ignores non-shell tools", res.returncode == 0 and decision(res) is None)
    check("denies a malformed payload", decision(run_script(PUSH_SCRIPT, "{not json")) == "deny")
    check(
        "denies a shell payload without a command",
        decision(run_script(PUSH_SCRIPT, json.dumps({"tool_name": "Bash", "tool_input": {}}))) == "deny",
    )


def _write(root: pathlib.Path, rel: str, text: str) -> None:
    path = root / rel
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")


def _write_executable(path: pathlib.Path, text: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")
    path.chmod(path.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)


def _git_add_all(root: pathlib.Path) -> None:
    subprocess.run(["git", "init", "-q", str(root)], check=True)
    subprocess.run(["git", "-C", str(root), "add", "-A"], check=True)


def test_post_edit_checks(tmp: pathlib.Path) -> None:
    print("post_edit_checks.py")
    fake_bin = tmp / "fake_bin"
    empty_bin = tmp / "empty_bin"
    empty_bin.mkdir(parents=True)
    log_file = tmp / "tool_invocations.log"

    _write_executable(
        fake_bin / "lkal",
        f"""#!{sys.executable}
import pathlib, sys
args = sys.argv[1:]
pathlib.Path({str(log_file)!r}).open("a", encoding="utf-8").write("lkal " + " ".join(args) + "\\n")
if len(args) >= 3 and args[0] == "fmt" and args[1] == "-w":
    p = pathlib.Path(args[2])
    text = p.read_text(encoding="utf-8")
    if "INVALID_SYNTAX" in text:
        sys.stderr.write("lkal fmt: syntax error\\n")
        sys.exit(1)
    p.write_text(text.strip() + "\\n", encoding="utf-8")
    sys.exit(0)
if len(args) >= 3 and args[0] == "schema" and args[1] == "check":
    p = pathlib.Path(args[2])
    if "INVALID_SCHEMA" in p.read_text(encoding="utf-8"):
        sys.stderr.write("lkal schema check: L601 invalid schema\\n")
        sys.exit(1)
    sys.exit(0)
if len(args) >= 4 and args[0] == "check" and args[1] == "--schema":
    schema_p = pathlib.Path(args[2])
    doc_p = pathlib.Path(args[3])
    if "INVALID_SCHEMA" in schema_p.read_text(encoding="utf-8"):
        sys.stderr.write("lkal check --schema: L601 invalid schema\\n")
        sys.exit(1)
    if "SCHEMA_VIOLATION" in doc_p.read_text(encoding="utf-8"):
        sys.stderr.write("lkal check --schema: L501 type mismatch\\n")
        sys.exit(1)
    sys.exit(0)
if len(args) >= 2 and args[0] == "check":
    p = pathlib.Path(args[1])
    if "INVALID_CHECK" in p.read_text(encoding="utf-8"):
        sys.stderr.write("lkal check: validation error\\n")
        sys.exit(1)
    sys.exit(0)
sys.exit(1)
""",
    )

    _write_executable(
        fake_bin / "docdag",
        f"""#!{sys.executable}
import pathlib, sys
args = sys.argv[1:]
pathlib.Path({str(log_file)!r}).open("a", encoding="utf-8").write("docdag " + " ".join(args) + "\\n")
touching = args[args.index("--touching") + 1] if "--touching" in args else ""
target = pathlib.Path.cwd() / touching
text = target.read_text(encoding="utf-8") if target.is_file() else ""
if "REWRITTEN_SETTLED_BODY" in text:
    sys.stdout.write(f"{{touching}}:8: ERROR immutable_violation: body changed after settlement\\n")
    sys.exit(1)
if "DRAFT_INCOMPLETE" in text:
    sys.stdout.write(f"{{touching}}:4: ERROR missing_section: draft missing section\\n")
    sys.exit(1)
sys.exit(0)
""",
    )

    post_repo = tmp / "post_repo"
    _write(post_repo, ".git/HEAD", "ref: refs/heads/main\n")
    valid_lkal = post_repo / "config" / "sample.lkal"
    _write(post_repo, "config/sample.lkal", "  let x = 1  \n\n")
    fake_env = {"PATH": f"{fake_bin}{os.pathsep}{os.environ.get('PATH', '')}"}

    res = run_script(POST_EDIT_SCRIPT, claude_edit("Edit", str(valid_lkal), event="PostToolUse"), env=fake_env, cwd=post_repo)
    invocations = log_file.read_text(encoding="utf-8") if log_file.is_file() else ""
    check(
        "runs lkal fmt -w and lkal check on edited .lkal file",
        res.returncode == 0
        and valid_lkal.read_text(encoding="utf-8") == "let x = 1\n"
        and "lkal fmt -w" in invocations
        and "lkal check" in invocations,
        res.stderr.strip(),
    )

    log_file.write_text("", encoding="utf-8")
    res = run_script(
        POST_EDIT_SCRIPT,
        codex_patch("config/sample.lkal"),
        env=fake_env,
        cwd=post_repo,
    )
    check(
        "runs lkal checks for Codex apply_patch on .lkal file",
        res.returncode == 0 and "lkal check" in log_file.read_text(encoding="utf-8"),
        res.stderr.strip(),
    )

    bad_lkal = post_repo / "config" / "broken.lkal"
    _write(post_repo, "config/broken.lkal", "INVALID_CHECK\n")
    res = run_script(
        POST_EDIT_SCRIPT,
        claude_multiedit(str(bad_lkal), [{"old_string": "a", "new_string": "b"}], event="PostToolUse"),
        env=fake_env,
        cwd=post_repo,
    )
    check(
        "exits 2 when lkal check fails on MultiEdit",
        res.returncode == 2 and "lkal check: validation error" in res.stderr,
        res.stderr.strip(),
    )

    paired_schema = post_repo / "config" / "paired.schema.lkal"
    paired_doc = post_repo / "config" / "paired.lkal"
    _write(post_repo, "config/paired.schema.lkal", 'lkal-schema = "0.2"\nroot {}\n')
    _write(post_repo, "config/paired.lkal", "x = 1\n")

    log_file.write_text("", encoding="utf-8")
    res = run_script(
        POST_EDIT_SCRIPT,
        claude_edit("Edit", str(paired_schema), event="PostToolUse"),
        env=fake_env,
        cwd=post_repo,
    )
    schema_invocations = log_file.read_text(encoding="utf-8")
    check(
        "runs lkal schema check and paired lkal check --schema on edited .schema.lkal file",
        res.returncode == 0
        and f"lkal schema check {paired_schema}" in schema_invocations
        and f"lkal check --schema {paired_schema} {paired_doc}" in schema_invocations,
        res.stderr.strip(),
    )

    log_file.write_text("", encoding="utf-8")
    res = run_script(
        POST_EDIT_SCRIPT,
        claude_edit("Edit", str(paired_doc), event="PostToolUse"),
        env=fake_env,
        cwd=post_repo,
    )
    doc_invocations = log_file.read_text(encoding="utf-8")
    check(
        "runs lkal check --schema when sibling .schema.lkal exists",
        res.returncode == 0 and f"lkal check --schema {paired_schema} {paired_doc}" in doc_invocations,
        res.stderr.strip(),
    )

    _write(post_repo, "config/paired.lkal", "SCHEMA_VIOLATION\n")
    res = run_script(
        POST_EDIT_SCRIPT,
        claude_edit("Edit", str(paired_doc), event="PostToolUse"),
        env=fake_env,
        cwd=post_repo,
    )
    check(
        "exits 2 when paired lkal check --schema fails",
        res.returncode == 2 and "L501 type mismatch" in res.stderr,
        res.stderr.strip(),
    )

    bad_schema = post_repo / "config" / "invalid.schema.lkal"
    _write(post_repo, "config/invalid.schema.lkal", "INVALID_SCHEMA\n")
    res = run_script(
        POST_EDIT_SCRIPT,
        claude_edit("Write", str(bad_schema), event="PostToolUse"),
        env=fake_env,
        cwd=post_repo,
    )
    check(
        "exits 2 when lkal schema check fails on .schema.lkal",
        res.returncode == 2 and "L601 invalid schema" in res.stderr,
        res.stderr.strip(),
    )

    res = run_script(
        POST_EDIT_SCRIPT,
        claude_edit("Write", str(valid_lkal), event="PostToolUse"),
        env={"PATH": str(empty_bin)},
        cwd=post_repo,
    )
    check(
        "fails closed with exit 2 when lkal is missing from PATH",
        res.returncode == 2 and "'lkal' executable not found" in res.stderr,
        res.stderr.strip(),
    )

    res = run_script(
        POST_EDIT_SCRIPT,
        claude_edit("Write", str(post_repo / "config" / "deleted.lkal"), event="PostToolUse"),
        env={"PATH": str(empty_bin)},
        cwd=post_repo,
    )
    check("skips deleted .lkal file cleanly", res.returncode == 0, res.stderr.strip())

    settled_adr = post_repo / "docs" / "ADR" / "0099-settled.md"
    _write(post_repo, "docs/ADR/0099-settled.md", "REWRITTEN_SETTLED_BODY\n")
    res = run_script(
        POST_EDIT_SCRIPT,
        claude_edit("Edit", str(settled_adr), event="PostToolUse"),
        env=fake_env,
        cwd=post_repo,
    )
    check(
        "exits 2 with append-only guidance on settled ADR rewrite",
        res.returncode == 2 and "immutable_violation" in res.stderr and "--supersedes" in res.stderr,
        res.stderr.strip(),
    )

    res = run_script(
        POST_EDIT_SCRIPT,
        codex_patch("docs/ADR/0099-settled.md"),
        env=fake_env,
        cwd=post_repo,
    )
    check(
        "exits 2 on Codex apply_patch rewriting a settled ADR",
        res.returncode == 2 and "immutable_violation" in res.stderr,
        res.stderr.strip(),
    )

    draft_adr = post_repo / "docs" / "ADR" / "0100-draft.md"
    _write(post_repo, "docs/ADR/0100-draft.md", "DRAFT_INCOMPLETE\n")
    res = run_script(
        POST_EDIT_SCRIPT,
        claude_edit("Edit", str(draft_adr), event="PostToolUse"),
        env=fake_env,
        cwd=post_repo,
    )
    check("allows draft ADR edit without immutable_violation", res.returncode == 0, res.stderr.strip())

    res = run_script(
        POST_EDIT_SCRIPT,
        claude_edit("Edit", str(settled_adr), event="PostToolUse"),
        env={"PATH": str(empty_bin)},
        cwd=post_repo,
    )
    check(
        "fails closed with exit 2 when docdag is missing from PATH",
        res.returncode == 2 and "'docdag' executable not found" in res.stderr,
        res.stderr.strip(),
    )

    res = run_script(POST_EDIT_SCRIPT, json.dumps({"tool_name": "Read", "tool_input": {"file_path": "x"}}))
    check("post_edit_checks ignores unwatched tools", res.returncode == 0)
    res = run_script(POST_EDIT_SCRIPT, "{bad json")
    check("post_edit_checks fails closed on malformed JSON", res.returncode == 2)

    if shutil.which("lkal") is not None:
        real_lkal_dir = tmp / "real_lkal"
        sample_src = (ROOT / "config" / "dev.lkal").read_text(encoding="utf-8")
        _write(real_lkal_dir, "sample.lkal", sample_src + "\n\n\n")
        real_lkal_res = run_script(
            POST_EDIT_SCRIPT,
            claude_edit("Write", str(real_lkal_dir / "sample.lkal"), event="PostToolUse"),
            cwd=real_lkal_dir,
        )
        check(
            "real lkal integration formats and checks .lkal file",
            real_lkal_res.returncode == 0
            and (real_lkal_dir / "sample.lkal").read_text(encoding="utf-8") == sample_src,
            real_lkal_res.stderr.strip(),
        )
        _write(real_lkal_dir, "broken.lkal", "=== invalid lkal ===\n")
        broken_lkal_res = run_script(
            POST_EDIT_SCRIPT,
            claude_edit("Write", str(real_lkal_dir / "broken.lkal"), event="PostToolUse"),
            cwd=real_lkal_dir,
        )
        check(
            "real lkal integration rejects invalid .lkal syntax",
            broken_lkal_res.returncode == 2 and bool(broken_lkal_res.stderr.strip()),
            broken_lkal_res.stderr.strip(),
        )
        ver_probe = subprocess.run(["lkal", "version"], capture_output=True, text=True, check=False)
        if ver_probe.returncode == 0:
            dev_schema_src = (ROOT / "config" / "dev.schema.lkal").read_text(encoding="utf-8")
            _write(real_lkal_dir, "dev.schema.lkal", dev_schema_src)
            _write(real_lkal_dir, "dev.lkal", sample_src)
            real_schema_ok = run_script(
                POST_EDIT_SCRIPT,
                claude_edit("Write", str(real_lkal_dir / "dev.lkal"), event="PostToolUse"),
                cwd=real_lkal_dir,
            )
            check(
                "real lkal v0.2 integration validates paired dev.lkal against dev.schema.lkal",
                real_schema_ok.returncode == 0,
                real_schema_ok.stderr.strip(),
            )
            _write(real_lkal_dir, "dev.lkal", '@lkal "0.1"\netcd { extra = 1 }\n')
            real_schema_bad = run_script(
                POST_EDIT_SCRIPT,
                claude_edit("Write", str(real_lkal_dir / "dev.lkal"), event="PostToolUse"),
                cwd=real_lkal_dir,
            )
            check(
                "real lkal v0.2 integration rejects schema violation in paired dev.lkal",
                real_schema_bad.returncode == 2 and "L5" in real_schema_bad.stderr,
                real_schema_bad.stderr.strip(),
            )

    if shutil.which("docdag") is not None:
        real_repo = tmp / "real_docdag_repo"
        _write(real_repo, "docdag.yaml", "preset: adr\ndir: docs/ADR\nid_width: 4\n")
        _write(
            real_repo,
            "docs/ADR/0001-record.md",
            "---\ntitle: Sample\nstatus: accepted\ndate: 2026-01-01\n---\n\nOriginal settled text.\n",
        )
        subprocess.run(["git", "init", "-q", str(real_repo)], check=True)
        subprocess.run(["git", "-C", str(real_repo), "config", "user.email", "test@example.invalid"], check=True)
        subprocess.run(["git", "-C", str(real_repo), "config", "user.name", "Test"], check=True)
        subprocess.run(["git", "-C", str(real_repo), "add", "-A"], check=True)
        subprocess.run(["git", "-C", str(real_repo), "commit", "-q", "-m", "initial"], check=True)
        _write(
            real_repo,
            "docs/ADR/0001-record.md",
            "---\ntitle: Sample\nstatus: accepted\ndate: 2026-01-01\n---\n\nMutated settled text.\n",
        )
        real_res = run_script(
            POST_EDIT_SCRIPT,
            codex_patch("docs/ADR/0001-record.md"),
            cwd=real_repo,
        )
        check(
            "real docdag integration blocks settled ADR rewrite",
            real_res.returncode == 2 and "immutable_violation" in real_res.stderr,
            real_res.stderr.strip(),
        )


def test_selfcheck(tmp: pathlib.Path) -> None:
    print("harness_selfcheck.py")
    res = run_script(SELFCHECK_SCRIPT, args=["--root", str(ROOT)])
    check("this repository passes", res.returncode == 0, res.stdout.strip())

    broken = tmp / "broken"
    copied = [".claude/settings.json", ".codex/hooks.json", "CLAUDE.md", "AGENTS.md"]
    for rel in copied:
        _write(broken, rel, (ROOT / rel).read_text(encoding="utf-8"))

    settings = json.loads((broken / ".claude/settings.json").read_text(encoding="utf-8"))
    pre = settings["hooks"]["PreToolUse"]
    pre[0]["hooks"][0].pop("onFailure")
    pre[1]["matcher"] = "Edit|Write"
    pre.append(
        {
            "matcher": "Bash",
            "hooks": [
                {
                    "type": "command",
                    "command": 'echo "$CLAUDE_TOOL_INPUT"; python3 "$CLAUDE_PROJECT_DIR/scripts/agent-harness/gone.py"',
                    "onFailure": "block",
                }
            ],
        }
    )
    settings["hooks"]["PostToolUse"][0]["matcher"] = "Edit|Write"
    settings["permissions"]["deny"] = [
        rule
        for rule in settings["permissions"]["deny"]
        if rule not in ("Edit(spec/comparator.json)", "Edit(spec/Nayutar/Fixture/**)", "Edit(spec/lakefile.toml)")
    ]
    _write(broken, ".claude/settings.json", json.dumps(settings))
    intact_settings_text = (ROOT / ".claude/settings.json").read_text(encoding="utf-8")

    codex = json.loads((broken / ".codex/hooks.json").read_text(encoding="utf-8"))
    codex["hooks"]["PreToolUse"] = [g for g in codex["hooks"]["PreToolUse"] if g["matcher"] != "Bash"]
    codex["hooks"].pop("PostToolUse", None)
    _write(broken, ".codex/hooks.json", json.dumps(codex))

    _write(broken, ".claude/rules/stale.md", '---\npaths: ["nowhere/**", "CLAUDE.{md,txt}"]\n---\n# x\n')
    _write(broken, "CLAUDE.md", "# CLAUDE.md\n" + "line\n" * 205)
    _write(broken, "AGENTS.md", "x" * 40000)
    _write(broken, "a/AGENTS.md", "# a\n")
    _write(broken, "b/AGENTS.md", "# b\n")
    _write(broken, "b/CLAUDE.md", "# b without import\n")
    _write(broken, "c/CLAUDE.md", "@AGENTS.md\n")
    (broken / ".agents" / "skills").mkdir(parents=True)
    (broken / ".agents" / "skills" / "dangling").symlink_to("../../.claude/skills/dangling")
    _write(broken, ".claude/skills/unlinked-skill/SKILL.md", "---\nname: unlinked-skill\n---\n# Skill\n")
    _write(
        broken,
        ".claude/skills/manual-only/SKILL.md",
        "---\nname: manual-only\ndisable-model-invocation: true\n---\n# Manual\n",
    )
    _git_add_all(broken)

    res = run_script(SELFCHECK_SCRIPT, args=["--root", str(broken)])
    out = res.stdout
    check("broken harness exits 1", res.returncode == 1, out)
    expectations = {
        "H1 flags CLAUDE_TOOL_INPUT": "H1 .claude/settings.json: hook command references non-existent CLAUDE_TOOL_INPUT",
        "H2 flags a missing hook script": "missing repo script 'scripts/agent-harness/gone.py'",
        "H3 flags a PreToolUse hook without onFailure": 'H3 .claude/settings.json: PreToolUse command hook lacks "onFailure": "block"',
        "H4 flags a matcher that skips MultiEdit": "no PreToolUse hook runs guard_protected_paths.py for the MultiEdit tool",
        "H4 flags a matcher that skips NotebookEdit": "no PreToolUse hook runs guard_protected_paths.py for the NotebookEdit tool",
        "H4 flags Claude PostToolUse skipping MultiEdit": "no PostToolUse hook runs post_edit_checks.py for the MultiEdit tool",
        "H4 flags Codex without the push guard": "H4 .codex/hooks.json: no PreToolUse hook runs guard_push_commands.py for the Bash tool",
        "H4 flags Codex without post_edit_checks.py": "H4 .codex/hooks.json: no PostToolUse hook runs post_edit_checks.py for the apply_patch tool",
        "H5 flags a rule path matching nothing": "paths entry 'nowhere/**' matches no tracked file",
        "H6 flags CLAUDE.md without the AGENTS.md import": "H6 CLAUDE.md",
        "H7 flags CLAUDE.md over 200 lines": "H7 CLAUDE.md",
        "H7 flags root AGENTS.md over 16384 bytes": "H7 AGENTS.md: AGENTS.md is 40000 bytes (limit is 16384)",
        "H8 flags a dangling .agents/skills entry": "H8 .agents/skills/dangling",
        "H8 flags a model-invocable skill missing from .agents/skills": "model-invocable skill 'unlinked-skill' is missing from .agents/skills/unlinked-skill",
        "H9 flags missing comparator deny rule": "H9 .claude/settings.json: permissions.deny is missing 'Edit(spec/comparator.json)'",
        "H9 flags missing fixture deny rule": "permissions.deny is missing 'Edit(spec/Nayutar/Fixture/**)'",
        "H9 flags missing lakefile deny rule": "permissions.deny is missing 'Edit(spec/lakefile.toml)'",
        "H10 flags unlisted nested a/AGENTS.md": "H10 a/AGENTS.md: nested AGENTS.md is not listed in the root AGENTS.md",
        "H10 flags missing a/CLAUDE.md": "H10 a/CLAUDE.md: does not exist or does not import the sibling AGENTS.md with an '@AGENTS.md' line",
        "H10 flags b/CLAUDE.md lacking import": "H10 b/CLAUDE.md: does not exist or does not import the sibling AGENTS.md with an '@AGENTS.md' line",
        "H10 flags orphan c/CLAUDE.md": "H10 c/CLAUDE.md: nested CLAUDE.md has no sibling AGENTS.md, so other agents would not see these instructions",
    }
    for name, needle in expectations.items():
        check(name, needle in out, needle)
    check("H5 accepts a brace pattern that matches", "CLAUDE.{md,txt}" not in out)
    check("H8 allows manual-only skill with disable-model-invocation", "manual-only" not in out)

    expected_deny_rules = (
        "Edit(spec/comparator.json)",
        "Edit(spec/Nayutar/Statements.lean)",
        "Edit(spec/Nayutar/Statements/**)",
        "Edit(spec/Nayutar/Fixture.lean)",
        "Edit(spec/Nayutar/Fixture/**)",
        "Edit(spec/lakefile.toml)",
        "Edit(spec/lakefile.lean)",
        "Edit(spec/lake-manifest.json)",
        "Edit(spec/lean-toolchain)",
    )
    check(
        "the self-check requires exactly the statement, model and build deny rules",
        set(selfcheck.REQUIRED_CLAUDE_DENY) == set(expected_deny_rules),
        str(selfcheck.REQUIRED_CLAUDE_DENY),
    )
    intact_deny = json.loads(intact_settings_text)["permissions"]["deny"]
    for rule in expected_deny_rules:
        check(f"settings.json denies {rule}", rule in intact_deny)
        single = tmp / "single_rule" / rule.replace("/", "_").replace("*", "S")
        for rel in copied:
            _write(single, rel, (ROOT / rel).read_text(encoding="utf-8"))
        one = json.loads(intact_settings_text)
        one["permissions"]["deny"] = [r for r in one["permissions"]["deny"] if r != rule]
        _write(single, ".claude/settings.json", json.dumps(one))
        _git_add_all(single)
        res = run_script(SELFCHECK_SCRIPT, args=["--root", str(single)])
        check(
            f"the self-check reports a missing {rule}",
            res.returncode == 1 and f"permissions.deny is missing '{rule}'" in res.stdout,
            res.stdout.strip(),
        )

    res = run_script(SELFCHECK_SCRIPT, args=["--root", str(broken), "--hook"])
    check("hook mode always exits 0", res.returncode == 0)
    check(
        "hook mode reports failure on one line",
        res.stdout.startswith("agent_harness_selfcheck_FAILED:") and res.stdout.count("\n") == 1,
        res.stdout,
    )

    empty = tmp / "empty"
    empty.mkdir()
    _git_add_all(empty)
    res = run_script(SELFCHECK_SCRIPT, args=["--root", str(empty)])
    check(
        "missing hook configs are findings",
        ".claude/settings.json is missing" in res.stdout and ".codex/hooks.json is missing" in res.stdout,
        res.stdout,
    )

    nested = tmp / "nested_instructions"
    for rel in (".claude/settings.json", ".codex/hooks.json", "CLAUDE.md"):
        _write(nested, rel, (ROOT / rel).read_text(encoding="utf-8"))
    _write(
        nested,
        "AGENTS.md",
        "# Map\n- `big/AGENTS.md`\n- `gone/AGENTS.md`\n- `ok/AGENTS.md`\n",
    )
    _write(nested, "big/AGENTS.md", "x" * 33000)
    _write(nested, "big/CLAUDE.md", "@AGENTS.md\n")
    _write(nested, "ok/AGENTS.md", "# ok\n")
    _write(nested, "ok/CLAUDE.md", "@AGENTS.md\n")
    subprocess.run(["git", "init", "-q", str(nested)], check=True)

    res = run_script(SELFCHECK_SCRIPT, args=["--root", str(nested)])
    nested_out = res.stdout
    check(
        "nested fixture exits 1 with chain and missing-target findings",
        res.returncode == 1
        and "H7 big/AGENTS.md: instruction chain down to big is " in nested_out
        and "limit is 32768)" in nested_out
        and "H10 AGENTS.md: lists 'gone/AGENTS.md', which does not exist" in nested_out,
        nested_out,
    )
    check(
        "paired and listed ok/AGENTS.md produces no H10 or H7 finding for ok/",
        "ok/" not in nested_out,
        nested_out,
    )

    _write(nested, "AGENTS.md", "line\n" * 201)
    res = run_script(SELFCHECK_SCRIPT, args=["--root", str(nested)])
    check(
        "H7 flags root AGENTS.md over 200 lines",
        res.returncode == 1
        and "H7 AGENTS.md: AGENTS.md has 201 lines (limit is 200)" in res.stdout
        and "limit is 16384" not in res.stdout,
        res.stdout,
    )


def main() -> int:
    with tempfile.TemporaryDirectory() as raw_tmp:
        tmp = pathlib.Path(raw_tmp).resolve()
        test_hook_payload()
        test_protected_paths(tmp)
        test_push_commands(tmp)
        test_post_edit_checks(tmp)
        test_selfcheck(tmp)
    print(f"\n{PASS} passed, {FAIL} failed")
    return 1 if FAIL else 0


if __name__ == "__main__":
    sys.exit(main())
