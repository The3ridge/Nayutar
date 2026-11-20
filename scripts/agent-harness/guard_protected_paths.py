#!/usr/bin/env python3
"""PreToolUse guard blocking edits to git internals, env, key and secret files, theorem statements, comparator config, human-facing YAML, and the kisage trust boundary."""

from __future__ import annotations

import json
import os
import sys
from pathlib import Path, PurePosixPath

_SELF_DIR = str(Path(__file__).resolve().parent)
if _SELF_DIR not in sys.path:
    sys.path.insert(0, _SELF_DIR)

from hook_payload import read_hook_stdin_or_exit  # noqa: E402

ENV_ALLOWED_SUFFIXES: tuple[str, ...] = (".example", ".template", ".sample")
KEY_SUFFIXES: tuple[str, ...] = (".key", ".pem")
YAML_SUFFIXES: tuple[str, ...] = (".yaml", ".yml")
PROTECTED_DIRS = frozenset({"secrets", ".git"})
DEFAULT_KISAGE_HOST_CONFIG = "~/.config/kisage/nayutar.json"
HOST_CONFIG_PATH_KEYS: tuple[str, ...] = ("storeDirectory", "lockDirectory")
RESOURCE_PATH_KEYS: tuple[str, ...] = ("managementRoot", "targetPath")
FALLBACK_REPO_ROOT = Path(__file__).resolve().parents[2]


def _is_protected_env_name(name: str) -> bool:
    is_env = name == ".env" or name.startswith(".env.") or name.endswith(".env")
    return is_env and not name.endswith(ENV_ALLOWED_SUFFIXES)


def _lexical_abs(raw: str) -> Path:
    return Path(os.path.abspath(os.path.expanduser(raw)))


def _resolve(raw: str) -> Path:
    return _lexical_abs(raw).resolve()


def _git_toplevel_from(start: Path) -> Path | None:
    cur = start
    for candidate in (cur, *cur.parents):
        if (candidate / ".git").exists():
            try:
                return candidate.resolve()
            except (OSError, RuntimeError):
                return candidate
    return None


def _discover_repo_roots() -> tuple[Path, ...]:
    roots: list[Path] = []
    cwd_root = _git_toplevel_from(Path.cwd())
    if cwd_root is not None:
        roots.append(cwd_root)
    if FALLBACK_REPO_ROOT not in roots:
        roots.append(FALLBACK_REPO_ROOT)
    return tuple(roots)


def kisage_trust_boundary() -> list[Path]:
    """Return the host config and every store, lock and managed path it names."""
    host_config = _resolve(os.environ.get("KISAGE_HOST_CONFIG") or DEFAULT_KISAGE_HOST_CONFIG)
    boundary = [host_config]
    try:
        data = json.loads(host_config.read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return boundary
    if not isinstance(data, dict):
        return boundary

    candidates = [data.get(key) for key in HOST_CONFIG_PATH_KEYS]
    resources = data.get("resources")
    if isinstance(resources, list):
        for resource in resources:
            if isinstance(resource, dict):
                candidates.extend(resource.get(key) for key in RESOURCE_PATH_KEYS)
    for candidate in candidates:
        if isinstance(candidate, str) and candidate.strip():
            try:
                boundary.append(_resolve(candidate))
            except (OSError, RuntimeError):
                continue
    return boundary


def _inside(target: Path, root: Path) -> bool:
    return target == root or root in target.parents


STATEMENT_TREES: tuple[tuple[str, ...], ...] = (
    ("spec", "Nayutar", "Statements"),
    ("spec", "Nayutar", "Fixture"),
)
STATEMENT_FILES: tuple[tuple[str, ...], ...] = (
    ("spec", "comparator.json"),
    ("spec", "Nayutar", "Statements.lean"),
    ("spec", "Nayutar", "Fixture.lean"),
    ("spec", "lakefile.toml"),
    ("spec", "lakefile.lean"),
    ("spec", "lake-manifest.json"),
    ("spec", "lean-toolchain"),
)
MODEL_OR_BUILD_PATHS: tuple[tuple[str, ...], ...] = (
    ("spec", "Nayutar", "Fixture"),
    ("spec", "Nayutar", "Fixture.lean"),
    ("spec", "lakefile.toml"),
    ("spec", "lakefile.lean"),
    ("spec", "lake-manifest.json"),
    ("spec", "lean-toolchain"),
)


def _is_protected_statement_path(parts: tuple[str, ...]) -> bool:
    if any(parts[-len(f) :] == f for f in STATEMENT_FILES):
        return True
    return any(
        parts[i : i + len(tree)] == tree
        for tree in STATEMENT_TREES
        for i in range(len(parts) - len(tree) + 1)
    )


def _is_model_or_build_path(parts: tuple[str, ...]) -> bool:
    if any(parts[-len(f) :] == f for f in MODEL_OR_BUILD_PATHS):
        return True
    return any(parts[i : i + 3] == ("spec", "Nayutar", "Fixture") for i in range(len(parts) - 2))


def _is_allowed_repo_yaml(rel_posix: str) -> bool:
    if rel_posix == "docdag.yaml":
        return True
    rel = PurePosixPath(rel_posix)
    if rel_posix == ".github/dependabot.yml":
        return True
    return rel.parent == PurePosixPath(".github/workflows") and rel.suffix.lower() in YAML_SUFFIXES


def _check_name_and_dirs(parts: tuple[str, ...], raw_path: str) -> str | None:
    name = parts[-1] if parts else ""

    if _is_protected_env_name(name):
        return (
            f"Blocked edit to {raw_path}: environment files matching .env are protected "
            "unless they end in .example, .template, or .sample. "
            "Edit a tracked template instead, or ask the user to change the real file."
        )

    if name.endswith(KEY_SUFFIXES):
        return (
            f"Blocked edit to {raw_path}: private key and certificate files (*.key, *.pem) "
            "must not be modified directly. "
            "Keys and certificates are issued by tooling, not written by agents; "
            "ask the user if one has to change."
        )

    if name == ".git":
        return (
            f"Blocked edit to {raw_path}: '.git' metadata is protected. "
            "Use git commands instead of editing .git internals directly."
        )

    for seg in parts[:-1]:
        if seg in PROTECTED_DIRS:
            if seg == "secrets":
                next_step = "Secret files are written by the host operator; ask the user to change them."
            else:
                next_step = "Use git commands instead of editing .git internals directly."
            return (
                f"Blocked edit to {raw_path}: files inside a '{seg}' directory are protected. "
                f"{next_step}"
            )

    return None


def check_target_path(
    raw_path: str,
    trust_boundary: list[Path],
    repo_roots: tuple[Path, ...] | None = None,
) -> str | None:
    """Return a 1-2 sentence block message if raw_path is protected, else None."""
    p = Path(os.path.normpath(raw_path))
    if (msg := _check_name_and_dirs(p.parts, raw_path)) is not None:
        return msg

    lexical_target = _lexical_abs(raw_path)
    try:
        target = lexical_target.resolve()
    except (OSError, RuntimeError):
        return f"Blocked edit to {raw_path}: target path could not be safely resolved."

    if (msg := _check_name_and_dirs(target.parts, raw_path)) is not None:
        return msg

    if _is_model_or_build_path(lexical_target.parts) or _is_model_or_build_path(target.parts):
        return (
            f"Blocked edit to {raw_path}: the model that the Lean theorem statements import "
            "(spec/Nayutar/Fixture/ and spec/Nayutar/Fixture.lean) and the Lean build configuration "
            "(spec/lakefile.toml, spec/lakefile.lean, spec/lake-manifest.json, spec/lean-toolchain) "
            "determine what the protected theorem statements mean, "
            "so changing them is a change to the statements and belongs to the user. "
            "Write proofs under spec/Nayutar/Proofs/ instead, and ask the user to change the model or the build."
        )

    if _is_protected_statement_path(lexical_target.parts) or _is_protected_statement_path(target.parts):
        return (
            f"Blocked edit to {raw_path}: Lean theorem statements and the comparator configuration "
            "are owned by the user. "
            "Write proofs under spec/Nayutar/Proofs/ instead, and ask the user to change a statement."
        )

    for root in trust_boundary:
        if _inside(lexical_target, root) or _inside(target, root):
            return (
                f"Blocked edit to {raw_path}: it lies inside the kisage trust boundary ({root}). "
                "Change the declaration in config/dev.lkal and reconcile with kisage_plan and "
                "kisage_apply; drift and ownership conflicts go to the user."
            )

    roots = repo_roots if repo_roots is not None else _discover_repo_roots()
    for root in roots:
        for candidate in (lexical_target, target):
            if candidate != root and _inside(candidate, root) and candidate.suffix.lower() in YAML_SUFFIXES:
                rel_posix = candidate.relative_to(root).as_posix()
                if not _is_allowed_repo_yaml(rel_posix):
                    return (
                        f"Blocked edit to {raw_path}: human-written configuration and declarations in this "
                        "repository must be written in LKAL (.lkal), not YAML. "
                        "Only docdag.yaml, .github/dependabot.yml and .github/workflows/*.{yml,yaml} are permitted YAML files."
                    )

    return None


def main() -> int:
    payload = read_hook_stdin_or_exit()
    if payload is None:
        return 0

    boundary = kisage_trust_boundary()
    roots = _discover_repo_roots()
    violations = [
        msg for path in payload.paths if (msg := check_target_path(path, boundary, roots)) is not None
    ]
    if violations:
        print("\n".join(violations), file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
