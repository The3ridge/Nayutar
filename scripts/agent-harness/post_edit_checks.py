#!/usr/bin/env python3
"""PostToolUse checks running lkal fmt/check on .lkal files and docdag immutability checks on ADR files."""

from __future__ import annotations

import os
import subprocess
import sys
from pathlib import Path, PurePosixPath

_SELF_DIR = str(Path(__file__).resolve().parent)
if _SELF_DIR not in sys.path:
    sys.path.insert(0, _SELF_DIR)

from hook_payload import read_hook_stdin_or_exit  # noqa: E402

FALLBACK_REPO_ROOT = Path(__file__).resolve().parents[2]
IMMUTABLE_GUIDANCE = (
    "This ADR was settled at HEAD and is append-only: undo the rewrite, "
    "then record the new decision in an ADR that supersedes it (docdag new --supersedes)."
)


def _inside(target: Path, root: Path) -> bool:
    return target == root or root in target.parents


def _find_repo_root(target: Path) -> Path:
    probe = target.parent
    while not probe.is_dir() and probe.parent != probe:
        probe = probe.parent
    for start in (probe, Path.cwd()):
        for candidate in (start, *start.parents):
            if (candidate / ".git").exists():
                try:
                    return candidate.resolve()
                except (OSError, RuntimeError):
                    return candidate
    return FALLBACK_REPO_ROOT


def _combine_output(res: subprocess.CompletedProcess[str]) -> str:
    parts = [part.strip() for part in (res.stdout, res.stderr) if part and part.strip()]
    return "\n".join(parts)


def _check_lkal_file(target: Path, raw_path: str) -> str | None:
    if not target.is_file():
        return None
    for subcmd in (["lkal", "fmt", "-w", str(target)], ["lkal", "check", str(target)]):
        try:
            res = subprocess.run(subcmd, capture_output=True, text=True, check=False)
        except FileNotFoundError:
            return (
                f"post_edit_checks: 'lkal' executable not found on PATH while checking {raw_path}. "
                "Install lkal so edited .lkal files can be formatted and validated."
            )
        if res.returncode != 0:
            detail = _combine_output(res)
            return detail or f"post_edit_checks: {' '.join(subcmd[:2])} failed for {raw_path}"
    return None


def _check_adr_immutability(repo_root: Path, rel_posix: str, raw_path: str) -> str | None:
    try:
        res = subprocess.run(
            ["docdag", "validate", "--immutable-since", "HEAD", "--touching", rel_posix],
            cwd=repo_root,
            capture_output=True,
            text=True,
            check=False,
        )
    except FileNotFoundError:
        return (
            f"post_edit_checks: 'docdag' executable not found on PATH while checking {raw_path}. "
            "Install docdag so settled ADR immutability can be verified."
        )

    combined = _combine_output(res)
    violations = [line for line in combined.splitlines() if "immutable_violation" in line]
    if violations:
        return "\n".join([*violations, IMMUTABLE_GUIDANCE])
    return None


def check_edited_path(raw_path: str) -> str | None:
    lexical = Path(os.path.abspath(os.path.expanduser(raw_path)))
    try:
        resolved = lexical.resolve()
    except (OSError, RuntimeError):
        resolved = lexical

    if resolved.suffix.lower() == ".lkal" or lexical.suffix.lower() == ".lkal":
        target_lkal = resolved if resolved.is_file() else lexical
        return _check_lkal_file(target_lkal, raw_path)

    repo_root = _find_repo_root(resolved)
    for cand in (resolved, lexical):
        if _inside(cand, repo_root) and cand != repo_root:
            rel_posix = cand.relative_to(repo_root).as_posix()
            rel = PurePosixPath(rel_posix)
            if rel.parent == PurePosixPath("docs/ADR") and rel.suffix.lower() == ".md":
                return _check_adr_immutability(repo_root, rel_posix, raw_path)

    return None


def main() -> int:
    payload = read_hook_stdin_or_exit()
    if payload is None:
        return 0

    errors = [err for path in payload.paths if (err := check_edited_path(path)) is not None]
    if errors:
        print("\n".join(errors), file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
