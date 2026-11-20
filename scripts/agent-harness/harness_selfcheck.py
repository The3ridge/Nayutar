#!/usr/bin/env python3
"""Self-check for Nayutar coding-agent harness wiring, guards, rules and instruction files."""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path, PurePosixPath
from typing import Any

BAD_ENV_VARS: tuple[str, ...] = ("CLAUDE_TOOL_INPUT", "CLAUDE_FILE_PATHS")
REPO_SCRIPT_REF_RE = re.compile(
    r"(?:\$CLAUDE_PROJECT_DIR|\$\{CLAUDE_PROJECT_DIR\}|\$\(git rev-parse --show-toplevel\))/([^\s'\"]+)"
)
CLAUDE_MD_IMPORT_RE = re.compile(r"^@AGENTS\.md\s*$", re.MULTILINE)
NESTED_AGENTS_REF_RE = re.compile(r"`([^`\s]+/AGENTS\.md)`")
MAX_CLAUDE_MD_LINES = 200
MAX_AGENTS_MD_BYTES = 32768
MAX_ROOT_AGENTS_MD_BYTES = 16384
MAX_ROOT_AGENTS_MD_LINES = 200
IGNORED_INSTRUCTION_DIRS: frozenset[str] = frozenset(
    {".git", ".claude", ".agents", ".codex", ".lake", "node_modules"}
)

CLAUDE_SETTINGS_REL = ".claude/settings.json"
CODEX_HOOKS_REL = ".codex/hooks.json"

REQUIRED_GUARDS: dict[str, tuple[tuple[str, tuple[str, ...]], ...]] = {
    CLAUDE_SETTINGS_REL: (
        ("guard_protected_paths.py", ("Edit", "MultiEdit", "Write", "NotebookEdit")),
        ("guard_push_commands.py", ("Bash",)),
    ),
    CODEX_HOOKS_REL: (
        ("guard_protected_paths.py", ("apply_patch", "Edit", "Write")),
        ("guard_push_commands.py", ("Bash",)),
    ),
}

REQUIRED_POST_HOOKS: dict[str, tuple[tuple[str, tuple[str, ...]], ...]] = {
    CLAUDE_SETTINGS_REL: (
        ("post_edit_checks.py", ("Edit", "MultiEdit", "Write")),
    ),
    CODEX_HOOKS_REL: (
        ("post_edit_checks.py", ("apply_patch", "Edit", "Write")),
    ),
}

REQUIRED_CLAUDE_DENY: tuple[str, ...] = (
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


@dataclass(frozen=True)
class Finding:
    finding_id: str
    path: str
    message: str


def _resolve_default_root() -> Path:
    res = subprocess.run(
        ["git", "rev-parse", "--show-toplevel"],
        capture_output=True,
        text=True,
        check=False,
    )
    if res.returncode == 0 and res.stdout.strip():
        return Path(res.stdout.strip()).resolve()
    return Path(__file__).resolve().parents[2]


def _git_ls_files(root: Path, *, include_untracked: bool = False) -> list[str]:
    cmd = ["git", "-C", str(root), "ls-files", "-z"]
    if include_untracked:
        cmd.extend(["--cached", "--others", "--exclude-standard"])
    res = subprocess.run(cmd, capture_output=True, check=False)
    if res.returncode != 0:
        return []
    raw = res.stdout.decode("utf-8", errors="replace")
    return [entry for entry in raw.split("\x00") if entry]


def _iter_hooks_with_matchers(entries: Any) -> list[tuple[Any, dict[str, Any]]]:
    result: list[tuple[Any, dict[str, Any]]] = []
    if not isinstance(entries, list):
        return result
    for group in entries:
        if not isinstance(group, dict):
            continue
        nested = group.get("hooks")
        if isinstance(nested, list):
            result.extend((group.get("matcher"), h) for h in nested if isinstance(h, dict))
    return result


def _matcher_matches_tool(matcher: Any, tool_name: str) -> bool:
    if matcher is None:
        return True
    if not isinstance(matcher, str):
        return False
    pat = matcher.strip()
    if pat in ("", "*"):
        return True
    try:
        return re.fullmatch(pat, tool_name) is not None
    except re.error:
        return pat == tool_name


def _check_hook_file(root: Path, rel_label: str, findings: list[Finding]) -> int:
    path = root / rel_label
    if not path.is_file():
        findings.append(Finding("H4", rel_label, f"{rel_label} is missing"))
        return 0
    try:
        cfg = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError) as exc:
        findings.append(Finding("H4", rel_label, f"could not parse hook config: {exc}"))
        return 0
    hooks_map = cfg.get("hooks") if isinstance(cfg, dict) else None
    if not isinstance(hooks_map, dict):
        findings.append(Finding("H4", rel_label, "missing 'hooks' object"))
        return 0

    is_claude = rel_label == CLAUDE_SETTINGS_REL
    if is_claude:
        perms = cfg.get("permissions")
        deny = perms.get("deny") if isinstance(perms, dict) else None
        deny_set = {item for item in deny if isinstance(item, str)} if isinstance(deny, list) else set()
        for rule in REQUIRED_CLAUDE_DENY:
            if rule not in deny_set:
                findings.append(Finding("H9", rel_label, f"permissions.deny is missing '{rule}'"))

    total = 0
    pre_tool: list[tuple[Any, str]] = []
    post_tool: list[tuple[Any, str]] = []
    for event_name, entries in hooks_map.items():
        for matcher, hook in _iter_hooks_with_matchers(entries):
            if hook.get("type") != "command":
                continue
            total += 1
            cmd = hook.get("command")
            cmd_str = cmd if isinstance(cmd, str) else ""

            if event_name == "PreToolUse":
                pre_tool.append((matcher, cmd_str))
                if is_claude and hook.get("onFailure") != "block":
                    findings.append(
                        Finding("H3", rel_label, 'PreToolUse command hook lacks "onFailure": "block"')
                    )
            elif event_name == "PostToolUse":
                post_tool.append((matcher, cmd_str))

            for var in BAD_ENV_VARS:
                if var in cmd_str:
                    findings.append(
                        Finding(
                            "H1",
                            rel_label,
                            f"hook command references non-existent {var}; read JSON from stdin instead",
                        )
                    )

            for match in REPO_SCRIPT_REF_RE.finditer(cmd_str):
                rel_script = match.group(1)
                if not (root / rel_script).exists():
                    findings.append(
                        Finding("H2", rel_label, f"hook command references missing repo script '{rel_script}'")
                    )

    for script, tools in REQUIRED_GUARDS[rel_label]:
        for tool in tools:
            if not any(script in cmd and _matcher_matches_tool(m, tool) for m, cmd in pre_tool):
                findings.append(
                    Finding(
                        "H4",
                        rel_label,
                        f"no PreToolUse hook runs {script} for the {tool} tool",
                    )
                )

    for script, tools in REQUIRED_POST_HOOKS[rel_label]:
        for tool in tools:
            if not any(script in cmd and _matcher_matches_tool(m, tool) for m, cmd in post_tool):
                findings.append(
                    Finding(
                        "H4",
                        rel_label,
                        f"no PostToolUse hook runs {script} for the {tool} tool",
                    )
                )
    return total


def _expand_braces(pattern: str) -> list[str]:
    """Expand brace alternatives such as {a,b,c} (including nested braces)."""
    start = -1
    depth = 0
    for idx, ch in enumerate(pattern):
        if ch == "{":
            if depth == 0:
                start = idx
            depth += 1
        elif ch == "}":
            depth -= 1
            if depth == 0 and start != -1:
                prefix, inner, suffix = pattern[:start], pattern[start + 1 : idx], pattern[idx + 1 :]
                options: list[str] = []
                buf: list[str] = []
                inner_depth = 0
                for c in inner:
                    if c == "{":
                        inner_depth += 1
                    elif c == "}":
                        inner_depth -= 1
                    elif c == "," and inner_depth == 0:
                        options.append("".join(buf))
                        buf = []
                        continue
                    buf.append(c)
                options.append("".join(buf))
                expanded: list[str] = []
                for opt in options:
                    expanded.extend(_expand_braces(prefix + opt + suffix))
                return expanded
    return [pattern]


def _glob_to_regex(pattern: str) -> re.Pattern[str]:
    pat = pattern.strip().removeprefix("./")
    out: list[str] = ["^"]
    i = 0
    n = len(pat)
    while i < n:
        if pat[i : i + 3] == "**/":
            out.append("(?:.+/)?")
            i += 3
        elif pat[i : i + 3] == "/**" and i + 3 == n:
            out.append("/.+")
            i += 3
        elif pat[i : i + 2] == "**":
            out.append(".*")
            i += 2
        elif pat[i] == "*":
            out.append("[^/]*")
            i += 1
        elif pat[i] == "?":
            out.append("[^/]")
            i += 1
        else:
            out.append(re.escape(pat[i]))
            i += 1
    out.append("$")
    return re.compile("".join(out))


def _matches_rule_glob(pattern: str, tracked_files: list[str]) -> bool:
    regexes = [_glob_to_regex(alt) for alt in _expand_braces(pattern)]
    return any(rx.match(f) for rx in regexes for f in tracked_files)


def _strip_yaml_scalar(raw: str) -> str:
    val = raw.strip()
    if len(val) >= 2 and val[0] == val[-1] and val[0] in ('"', "'"):
        return val[1:-1]
    if " #" in val:
        val = val.split(" #", 1)[0].rstrip()
    return val


def _split_inline_yaml_list(inner: str) -> list[str]:
    items: list[str] = []
    buf: list[str] = []
    in_quote: str | None = None
    brace_depth = 0
    for i, ch in enumerate(inner):
        if in_quote is not None:
            if ch == in_quote and inner[i - 1] != "\\":
                in_quote = None
        elif ch in ('"', "'"):
            in_quote = ch
        elif ch == "{":
            brace_depth += 1
        elif ch == "}":
            brace_depth = max(0, brace_depth - 1)
        elif ch == "," and brace_depth == 0:
            if token := _strip_yaml_scalar("".join(buf)):
                items.append(token)
            buf = []
            continue
        buf.append(ch)
    if tail := _strip_yaml_scalar("".join(buf)):
        items.append(tail)
    return items


def extract_frontmatter_paths(text: str) -> list[str]:
    """Return the `paths:` entries of a rule file's frontmatter (block or inline list)."""
    lines = text.splitlines()
    if not lines or lines[0].lstrip("﻿").strip() != "---":
        return []
    try:
        end_idx = next(i for i in range(1, len(lines)) if lines[i].strip() == "---")
    except StopIteration:
        return []

    fm = lines[1:end_idx]
    paths: list[str] = []
    for idx, line in enumerate(fm):
        if not re.match(r"^paths\s*:", line):
            continue
        rest = line.split(":", 1)[1].strip()
        if rest.startswith("[") and rest.endswith("]"):
            paths.extend(_split_inline_yaml_list(rest[1:-1]))
        elif rest:
            paths.append(_strip_yaml_scalar(rest))
        else:
            for sub in fm[idx + 1 :]:
                if not sub.strip() or sub.lstrip().startswith("#"):
                    continue
                if not sub[0].isspace():
                    break
                m_item = re.match(r"^\s*-\s+(.*)$", sub)
                if m_item is not None and (item := _strip_yaml_scalar(m_item.group(1))):
                    paths.append(item)
    return paths


def _is_model_invocable_skill(skill_md: Path) -> bool:
    text = skill_md.read_text(encoding="utf-8", errors="replace")
    lines = text.splitlines()
    if not lines or lines[0].lstrip("\ufeff").strip() != "---":
        return True
    try:
        end_idx = next(i for i in range(1, len(lines)) if lines[i].strip() == "---")
    except StopIteration:
        return True
    for line in lines[1:end_idx]:
        m = re.match(r"^disable-model-invocation\s*:\s*(.+)$", line)
        if m is not None and _strip_yaml_scalar(m.group(1)).lower() == "true":
            return False
    return True


def _is_nested_instruction(rel: str, filename: str) -> bool:
    parts = PurePosixPath(rel).parts
    return (
        len(parts) > 1
        and parts[-1] == filename
        and not any(part in IGNORED_INSTRUCTION_DIRS for part in parts[:-1])
    )


def _instruction_chain_bytes(root: Path, dir_rel: str) -> int:
    total = 0
    root_agents = root / "AGENTS.md"
    if root_agents.is_file():
        total += root_agents.stat().st_size
    cur = root
    for part in PurePosixPath(dir_rel).parts:
        cur = cur / part
        candidate = cur / "AGENTS.md"
        if candidate.is_file():
            total += candidate.stat().st_size
    return total


def _check_nested_instructions(
    root: Path,
    repo_files: list[str],
    agents_text: str,
    findings: list[Finding],
) -> int:
    listed_paths = list(dict.fromkeys(NESTED_AGENTS_REF_RE.findall(agents_text)))
    listed_set = set(listed_paths)

    for listed in listed_paths:
        parts = PurePosixPath(listed).parts
        if listed.startswith("/") or ".." in parts or not (root / listed).is_file():
            findings.append(Finding("H10", "AGENTS.md", f"lists '{listed}', which does not exist"))

    nested_agents = [p for p in repo_files if _is_nested_instruction(p, "AGENTS.md") and (root / p).is_file()]
    for rel_path in nested_agents:
        dir_rel = PurePosixPath(rel_path).parent.as_posix()
        chain_bytes = _instruction_chain_bytes(root, dir_rel)
        if chain_bytes > MAX_AGENTS_MD_BYTES:
            findings.append(
                Finding(
                    "H7",
                    rel_path,
                    f"instruction chain down to {dir_rel} is {chain_bytes} bytes (limit is {MAX_AGENTS_MD_BYTES})",
                )
            )
        if rel_path not in listed_set:
            findings.append(Finding("H10", rel_path, "nested AGENTS.md is not listed in the root AGENTS.md"))

        sibling_claude_rel = f"{dir_rel}/CLAUDE.md"
        sibling_claude = root / sibling_claude_rel
        sibling_text = sibling_claude.read_text(encoding="utf-8", errors="replace") if sibling_claude.is_file() else ""
        if not sibling_claude.is_file() or not CLAUDE_MD_IMPORT_RE.search(sibling_text):
            findings.append(
                Finding(
                    "H10",
                    sibling_claude_rel,
                    "does not exist or does not import the sibling AGENTS.md with an '@AGENTS.md' line",
                )
            )

    for rel_path in repo_files:
        if not _is_nested_instruction(rel_path, "CLAUDE.md") or not (root / rel_path).is_file():
            continue
        dir_rel = PurePosixPath(rel_path).parent.as_posix()
        if not (root / dir_rel / "AGENTS.md").is_file():
            findings.append(
                Finding(
                    "H10",
                    rel_path,
                    "nested CLAUDE.md has no sibling AGENTS.md, so other agents would not see these instructions",
                )
            )

    return len(nested_agents)


def run_selfcheck(root: Path) -> tuple[list[Finding], dict[str, Any]]:
    findings: list[Finding] = []
    tracked_files = _git_ls_files(root)

    # H1-H4: hook configs for both agents
    claude_hooks = _check_hook_file(root, CLAUDE_SETTINGS_REL, findings)
    codex_hooks = _check_hook_file(root, CODEX_HOOKS_REL, findings)

    # H5: .claude/rules frontmatter paths: entries match at least one tracked file
    rules_dir = root / ".claude" / "rules"
    rule_count = 0
    if rules_dir.is_dir():
        for rule_file in sorted(rules_dir.rglob("*.md")):
            rule_count += 1
            rel_rule = rule_file.relative_to(root).as_posix()
            text = rule_file.read_text(encoding="utf-8", errors="replace")
            for pat in extract_frontmatter_paths(text):
                if not _matches_rule_glob(pat, tracked_files):
                    findings.append(Finding("H5", rel_rule, f"paths entry '{pat}' matches no tracked file"))

    # H6: CLAUDE.md imports AGENTS.md so both agents read one rule set
    claude_md = root / "CLAUDE.md"
    agents_md = root / "AGENTS.md"
    for doc in (claude_md, agents_md):
        if not doc.is_file():
            findings.append(Finding("H6", doc.name, f"{doc.name} does not exist"))
    if claude_md.is_file():
        claude_text = claude_md.read_text(encoding="utf-8", errors="replace")
        if not CLAUDE_MD_IMPORT_RE.search(claude_text):
            findings.append(Finding("H6", "CLAUDE.md", "CLAUDE.md does not import AGENTS.md with an '@AGENTS.md' line"))

        # H7: size limits
        line_count = len(claude_text.splitlines())
        if line_count > MAX_CLAUDE_MD_LINES:
            findings.append(
                Finding("H7", "CLAUDE.md", f"CLAUDE.md has {line_count} lines (limit is {MAX_CLAUDE_MD_LINES})")
            )
    agents_text = ""
    if agents_md.is_file():
        byte_size = agents_md.stat().st_size
        if byte_size > MAX_ROOT_AGENTS_MD_BYTES:
            findings.append(
                Finding("H7", "AGENTS.md", f"AGENTS.md is {byte_size} bytes (limit is {MAX_ROOT_AGENTS_MD_BYTES})")
            )
        agents_text = agents_md.read_text(encoding="utf-8", errors="replace")
        agents_lines = len(agents_text.splitlines())
        if agents_lines > MAX_ROOT_AGENTS_MD_LINES:
            findings.append(
                Finding("H7", "AGENTS.md", f"AGENTS.md has {agents_lines} lines (limit is {MAX_ROOT_AGENTS_MD_LINES})")
            )

    repo_files = sorted(set(_git_ls_files(root, include_untracked=True)))
    nested_instructions = _check_nested_instructions(root, repo_files, agents_text, findings)

    skills_dir = root / ".agents" / "skills"
    skill_links = 0
    if skills_dir.is_dir():
        for entry in sorted(skills_dir.iterdir(), key=lambda p: p.name):
            skill_links += 1
            if not entry.is_dir() or not (entry / "SKILL.md").is_file():
                findings.append(
                    Finding(
                        "H8",
                        entry.relative_to(root).as_posix(),
                        "entry under .agents/skills does not resolve to a directory containing SKILL.md",
                    )
                )

    claude_skills_dir = root / ".claude" / "skills"
    if claude_skills_dir.is_dir():
        for claude_skill in sorted(claude_skills_dir.iterdir(), key=lambda p: p.name):
            if not claude_skill.is_dir():
                continue
            skill_md = claude_skill / "SKILL.md"
            if not skill_md.is_file():
                findings.append(
                    Finding(
                        "H8",
                        claude_skill.relative_to(root).as_posix(),
                        "directory under .claude/skills is missing SKILL.md",
                    )
                )
                continue
            if not _is_model_invocable_skill(skill_md):
                continue
            agent_entry = skills_dir / claude_skill.name
            if not agent_entry.exists() and not agent_entry.is_symlink():
                findings.append(
                    Finding(
                        "H8",
                        claude_skill.relative_to(root).as_posix(),
                        f"model-invocable skill '{claude_skill.name}' is missing from .agents/skills/{claude_skill.name}",
                    )
                )
            elif agent_entry.is_dir() and (agent_entry / "SKILL.md").is_file():
                try:
                    same_target = agent_entry.resolve() == claude_skill.resolve()
                except (OSError, RuntimeError):
                    same_target = False
                if not same_target:
                    findings.append(
                        Finding(
                            "H8",
                            agent_entry.relative_to(root).as_posix(),
                            f"skill entry does not resolve to .claude/skills/{claude_skill.name}",
                        )
                    )

    meta = {
        "claude_hooks": claude_hooks,
        "codex_hooks": codex_hooks,
        "rules": rule_count,
        "codex_skills": skill_links,
        "nested_instructions": nested_instructions,
    }
    return findings, meta


def _summary(meta: dict[str, Any]) -> str:
    return (
        "agent_harness_selfcheck: OK "
        f"(claude_hooks={meta['claude_hooks']}, codex_hooks={meta['codex_hooks']}, "
        f"rules={meta['rules']}, codex_skills={meta['codex_skills']}, "
        f"nested_instructions={meta['nested_instructions']})"
    )


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Verify Nayutar coding-agent harness health.")
    parser.add_argument("--root", type=Path, default=None, help="Repository root directory")
    parser.add_argument(
        "--hook",
        action="store_true",
        help="Emit a single-line SessionStart summary and always exit 0",
    )
    args = parser.parse_args(argv)

    root = args.root.resolve() if args.root is not None else _resolve_default_root()
    findings, meta = run_selfcheck(root)

    if args.hook:
        if findings:
            ids = ", ".join(sorted({f.finding_id for f in findings}))
            print(
                f"agent_harness_selfcheck_FAILED: {len(findings)} finding(s): {ids}; "
                "run python3 scripts/agent-harness/harness_selfcheck.py"
            )
        else:
            print(_summary(meta))
        return 0

    if findings:
        for finding in findings:
            print(f"{finding.finding_id} {finding.path}: {finding.message}")
        return 1
    print(_summary(meta))
    return 0


if __name__ == "__main__":
    sys.exit(main())
