#!/usr/bin/env python3
"""PreToolUse guard denying push-class commands, hook/sandbox bypasses, and shell mutations of protected paths."""

from __future__ import annotations

import json
import os
import re
import shlex
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Any

_SELF_DIR = str(Path(__file__).resolve().parent)
if _SELF_DIR not in sys.path:
    sys.path.insert(0, _SELF_DIR)

from guard_protected_paths import (  # noqa: E402
    _inside,
    _is_protected_env_name,
    _is_protected_statement_path,
    kisage_trust_boundary,
)

WATCHED_SHELL_TOOLS = frozenset({"Bash", "shell"})

_GIT_GLOBAL_OPTS_PAT = r"""
    (?:\s+(?:
        -[cC](?:\s+(?:"[^"]*"|'[^']*'|\S+)|\S+)
      | --(?:git-dir|work-tree|namespace|exec-path|config-env)\s+(?:"[^"]*"|'[^']*'|\S+)
      | --?[A-Za-z][\w.=/:-]*
    ))*
"""

_WRAPPER_PREFIX_PAT = r"""
    (?:
        (?:
            [A-Za-z_]\w*=\S*
          | (?:/\S+/)?env(?:\s+(?:-[A-Za-z0-9-]+|[A-Za-z_]\w*=\S*))*
          | command(?:\s+-[p]+)*
          | nohup
          | (?:/\S+/)?timeout(?:\s+(?:-[ks]\s+\S+|--(?:kill-after|signal)(?:=|\s+)\S+|--[\w-]+))*\s+\S+
        )
        \s+
    )*
"""

PUSH_CLASS_RE = re.compile(
    rf"""
    (?:^|[\s&;|`(]|\$\()
    {_WRAPPER_PREFIX_PAT}
    (?:
        (?:/\S+/)?git{_GIT_GLOBAL_OPTS_PAT}\s+(?:
            push
          | remote\s+(?:update|set-url|add)
          | fetch\s+[^\n;|&`)]*push
        )
      | (?:/\S+/)?gh(?:\s+(?:-[R]\s+\S+|--repo(?:=|\s+)\S+|--?[A-Za-z][\w.=/:-]*))*\s+(?:
            repo\s+sync
          | release\s+(?:upload|create)
          | pr\s+(?:create|merge)
          | workflow\s+run
          | run\s+(?:rerun|cancel)
        )
    )
    (?=\s|$|[;&|`)])
    """,
    re.IGNORECASE | re.VERBOSE,
)

DENY_REASON = (
    "BLOCKED: pushing, creating or merging pull requests, publishing releases and running, "
    "re-running or cancelling workflows need an explicit request from the user (AGENTS.md). "
    "Ask the user to run the command themselves."
)
HOOK_BYPASS_REASON = (
    "BLOCKED: bypassing git hooks or commit signatures (--no-verify, -n, --no-gpg-sign, "
    "core.hooksPath, or commit.gpgsign=false) is prohibited (AGENTS.md). "
    "Fix the underlying hook failure instead of skipping verification."
)
COMPARATOR_SANDBOX_REASON = (
    "BLOCKED: running 'lake comparator' with '--inadvisably-no-sandbox' is prohibited (AGENTS.md). "
    "Run comparator inside its bubblewrap sandbox."
)
LAKE_UPDATE_REASON = (
    "BLOCKED: 'lake update' and the lake '--update' (-U) option rewrite spec/lake-manifest.json, which is part "
    "of the Lean build configuration that the user owns (AGENTS.md). Ask the user to run it."
)
LEAN_BUILD_CONFIG_DESC = (
    "the Lean build configuration (spec/lakefile.toml, spec/lakefile.lean, "
    "spec/lake-manifest.json, spec/lean-toolchain)"
)
LAKE_ARG_OPTIONS = frozenset({"-d", "--dir", "-f", "--file", "-K", "--packages", "--lean"})
LAKE_UPDATE_SUBCOMMANDS = frozenset({"update", "upgrade"})
LAKE_UPDATE_OPTIONS = frozenset({"--update", "-U"})
LAKE_PROGRAM_SUBCOMMANDS = frozenset({"exe", "exec", "env", "lean", "script", "run"})
UNREADABLE_REASON = (
    "BLOCKED: the push guard could not read this shell command, so it fails closed. "
    "Report the payload problem to the user."
)

WRITE_REDIR_OPS = frozenset({">", ">>", ">|", "&>", "&>>", "<>"})
SHELL_BINS = frozenset({"sh", "bash", "zsh", "dash", "ksh"})
PYTHON_BIN_RE = re.compile(r"^python(?:\d+(?:\.\d+)*)?$")
GIT_COMMIT_ARG_SHORT_FLAGS = frozenset({"m", "F", "c", "C", "t", "u", "S"})
GIT_COMMIT_ARG_LONG_FLAGS = frozenset(
    {
        "--message",
        "--file",
        "--reuse-message",
        "--reedit-message",
        "--fixup",
        "--squash",
        "--author",
        "--date",
        "--template",
        "--cleanup",
        "--untracked-files",
        "--pathspec-from-file",
        "--trailer",
    }
)
GIT_GLOBAL_ARG_FLAGS = frozenset(
    {"-C", "-c", "--git-dir", "--work-tree", "--namespace", "--exec-path", "--config-env", "--super-prefix"}
)
PYTHON_WRITE_RE = re.compile(
    r"""
    \.write(?:_text|_bytes|lines)?\s*\(
    | \bshutil\.(?:copy\w*|move|rmtree)\b
    | \bos\.(?:remove|unlink|rename|replace|symlink|link|truncate|open)\b
    | \.(?:unlink|rename|replace|symlink_to|hardlink_to)\s*\(
    | \bopen\s*\([^)]*,\s*(?:mode\s*=\s*)?['"][wax+rbBtU]*[wax+]
    """,
    re.VERBOSE,
)


@dataclass
class _Tok:
    kind: str
    value: str
    heredoc: str = ""


def _decision(reason: str) -> dict[str, Any]:
    return {
        "hookSpecificOutput": {
            "hookEventName": "PreToolUse",
            "permissionDecision": "deny",
            "permissionDecisionReason": reason,
        }
    }


def _extract_dollar_paren(text: str, start: int) -> tuple[str, int]:
    depth = 1
    i = start + 2
    n = len(text)
    in_sq = False
    in_dq = False
    while i < n and depth > 0:
        ch = text[i]
        if in_sq:
            if ch == "'":
                in_sq = False
        elif in_dq:
            if ch == "\\" and i + 1 < n:
                i += 2
                continue
            if ch == '"':
                in_dq = False
        else:
            if ch == "\\" and i + 1 < n:
                i += 2
                continue
            if ch == "'":
                in_sq = True
            elif ch == '"':
                in_dq = True
            elif ch == "(":
                depth += 1
            elif ch == ")":
                depth -= 1
                if depth == 0:
                    return text[start + 2 : i], i + 1
        i += 1
    return text[start + 2 : i], i


def _extract_backtick(text: str, start: int) -> tuple[str, int]:
    i = start + 1
    n = len(text)
    buf: list[str] = []
    while i < n:
        ch = text[i]
        if ch == "\\" and i + 1 < n and text[i + 1] in ("\\", "`", "$"):
            buf.append(text[i + 1])
            i += 2
            continue
        if ch == "`":
            return "".join(buf), i + 1
        buf.append(ch)
        i += 1
    return "".join(buf), i


def _read_heredoc_delim(text: str, pos: int) -> tuple[str, int]:
    n = len(text)
    while pos < n and text[pos] in (" ", "\t"):
        pos += 1
    delim: list[str] = []
    while pos < n and text[pos] not in (" ", "\t", "\r", "\n", ";", "&", "|", "(", ")"):
        ch = text[pos]
        if ch in ("'", '"'):
            quote = ch
            pos += 1
            while pos < n and text[pos] != quote:
                delim.append(text[pos])
                pos += 1
            if pos < n:
                pos += 1
        elif ch == "\\" and pos + 1 < n:
            delim.append(text[pos + 1])
            pos += 2
        else:
            delim.append(ch)
            pos += 1
    return "".join(delim), pos


def _lex_shell(command: str) -> tuple[list[_Tok], list[str]]:
    tokens: list[_Tok] = []
    subcommands: list[str] = []
    buf: list[str] = []
    quoted = False
    pending_heredocs: list[tuple[str, bool, int]] = []
    i = 0
    n = len(command)

    def flush_word() -> None:
        nonlocal quoted
        if buf or quoted:
            tokens.append(_Tok("word", "".join(buf)))
            buf.clear()
            quoted = False

    while i < n:
        ch = command[i]

        if ch == "\n":
            flush_word()
            tokens.append(_Tok("op", "\n"))
            i += 1
            if pending_heredocs:
                for delim, strip_tabs, tok_idx in pending_heredocs:
                    body_lines: list[str] = []
                    while i < n:
                        nl = command.find("\n", i)
                        if nl == -1:
                            line = command[i:]
                            i = n
                        else:
                            line = command[i:nl]
                            i = nl + 1
                        line_clean = line.rstrip("\r")
                        cmp_line = line_clean.lstrip("\t") if strip_tabs else line_clean
                        if delim and cmp_line == delim:
                            break
                        body_lines.append(line_clean)
                    tokens[tok_idx].heredoc = "\n".join(body_lines)
                pending_heredocs.clear()
            continue

        if ch in (" ", "\t", "\r"):
            flush_word()
            i += 1
            continue

        if ch == "#" and not buf and not quoted:
            nl = command.find("\n", i)
            i = n if nl == -1 else nl
            continue

        if ch == "\\":
            if i + 1 < n:
                if command[i + 1] == "\n":
                    i += 2
                    continue
                buf.append(command[i + 1])
                quoted = True
                i += 2
            else:
                i += 1
            continue

        if ch == "'":
            quoted = True
            i += 1
            end = command.find("'", i)
            if end == -1:
                buf.append(command[i:])
                i = n
            else:
                buf.append(command[i:end])
                i = end + 1
            continue

        if ch == '"':
            quoted = True
            i += 1
            while i < n and command[i] != '"':
                c = command[i]
                if c == "\\" and i + 1 < n and command[i + 1] in ('"', "\\", "$", "`", "\n"):
                    if command[i + 1] != "\n":
                        buf.append(command[i + 1])
                    i += 2
                    continue
                if command[i : i + 2] == "$(":
                    inner, next_i = _extract_dollar_paren(command, i)
                    subcommands.append(inner)
                    buf.append(command[i:next_i])
                    i = next_i
                    continue
                if c == "`":
                    inner, next_i = _extract_backtick(command, i)
                    subcommands.append(inner)
                    buf.append(command[i:next_i])
                    i = next_i
                    continue
                buf.append(c)
                i += 1
            if i < n and command[i] == '"':
                i += 1
            continue

        if command[i : i + 2] == "$(":
            inner, next_i = _extract_dollar_paren(command, i)
            subcommands.append(inner)
            buf.append(command[i:next_i])
            i = next_i
            continue

        if ch == "`":
            inner, next_i = _extract_backtick(command, i)
            subcommands.append(inner)
            buf.append(command[i:next_i])
            i = next_i
            continue

        if ch == "&" and not buf and not quoted and i + 1 < n and command[i + 1] == ">":
            if command[i : i + 3] == "&>>":
                tokens.append(_Tok("redir", "&>>"))
                i += 3
            else:
                tokens.append(_Tok("redir", "&>"))
                i += 2
            continue

        if ch in (">", "<") and not quoted and (not buf or "".join(buf).isdigit()):
            fd_prefix = "".join(buf)
            buf.clear()
            if command[i : i + 3] == "<<<":
                tokens.append(_Tok("redir", fd_prefix + "<<<"))
                i += 3
            elif command[i : i + 2] == "<<":
                strip_tabs = command[i : i + 3] == "<<-"
                op_str = "<<-" if strip_tabs else "<<"
                i += len(op_str)
                delim, i = _read_heredoc_delim(command, i)
                pending_heredocs.append((delim, strip_tabs, len(tokens)))
                tokens.append(_Tok("redir", fd_prefix + op_str))
            elif command[i : i + 2] in (">>", ">|", ">&", "<>", "<&"):
                tokens.append(_Tok("redir", fd_prefix + command[i : i + 2]))
                i += 2
            else:
                tokens.append(_Tok("redir", fd_prefix + ch))
                i += 1
            continue

        if ch in (";", "|", "&", "(", ")"):
            flush_word()
            if command[i : i + 2] in ("&&", "||", "|&"):
                tokens.append(_Tok("op", command[i : i + 2]))
                i += 2
            else:
                tokens.append(_Tok("op", ch))
                i += 1
            continue

        buf.append(ch)
        i += 1

    flush_word()
    return tokens, subcommands


def _is_write_redir(op: str, target: str) -> bool:
    base_op = op.lstrip("0123456789")
    if base_op in WRITE_REDIR_OPS:
        return True
    if op == ">&" and target != "-" and not target.isdigit():
        return True
    return False


def _unwrap_wrappers(words: list[str]) -> tuple[list[str], bool, list[str]]:
    """Strip env vars and command wrappers; return (remaining_words, via_xargs, nested_scripts)."""
    i = 0
    n = len(words)
    via_xargs = False
    nested: list[str] = []

    while i < n:
        w = words[i]
        if re.match(r"^[A-Za-z_]\w*=", w):
            i += 1
            continue
        base = os.path.basename(w)
        if base == "env":
            i += 1
            while i < n:
                cur = words[i]
                if re.match(r"^[A-Za-z_]\w*=", cur):
                    i += 1
                elif cur in ("-u", "-C", "--unset", "--chdir") and i + 1 < n:
                    i += 2
                elif cur in ("-S", "--split-string") and i + 1 < n:
                    nested.append(words[i + 1])
                    return [], via_xargs, nested
                elif cur.startswith("--split-string="):
                    nested.append(cur.split("=", 1)[1])
                    return [], via_xargs, nested
                elif cur == "--":
                    i += 1
                    break
                elif cur.startswith("-"):
                    i += 1
                else:
                    break
            continue
        if base == "command":
            i += 1
            while i < n and words[i].startswith("-"):
                flag = words[i]
                if flag == "--":
                    i += 1
                    break
                if "v" in flag or "V" in flag:
                    return [], via_xargs, nested
                i += 1
            continue
        if base in ("nohup", "time"):
            i += 1
            continue
        if base == "nice":
            i += 1
            if i < n and words[i] in ("-n", "--adjustment") and i + 1 < n:
                i += 2
            elif i < n and words[i].startswith("-"):
                i += 1
            continue
        if base == "timeout":
            i += 1
            while i < n and words[i].startswith("-"):
                cur = words[i]
                if cur == "--":
                    i += 1
                    break
                if cur in ("-k", "-s", "--kill-after", "--signal") and i + 1 < n:
                    i += 2
                else:
                    i += 1
            if i < n:
                i += 1
            continue
        if base == "xargs":
            via_xargs = True
            i += 1
            while i < n and words[i].startswith("-"):
                cur = words[i]
                if cur == "--":
                    i += 1
                    break
                if cur in ("-n", "-L", "-P", "-I", "-d", "-a", "-s", "-E") and i + 1 < n:
                    i += 2
                else:
                    i += 1
            continue
        break

    return words[i:], via_xargs, nested


def _check_protected_token(
    raw_token: str,
    cwd_in_spec: bool,
    cwd_in_statements: bool,
    boundary: list[Path],
) -> str | None:
    tok = raw_token.strip()
    if not tok or tok in ("/dev/null", "-"):
        return None
    if "$KISAGE_HOST_CONFIG" in tok or "${KISAGE_HOST_CONFIG}" in tok:
        return "kisage host configuration ($KISAGE_HOST_CONFIG)"

    norm = tok.replace("\\", "/")
    while norm.startswith("./"):
        norm = norm[2:]
    norm_stripped = norm.rstrip("/") if norm != "/" else norm

    if norm_stripped == "spec/comparator.json" or norm_stripped.endswith("/spec/comparator.json"):
        return "spec/comparator.json"
    if cwd_in_spec and norm_stripped == "comparator.json":
        return "spec/comparator.json"
    if re.search(r"(?:^|/)spec/(?:lakefile\.(?:toml|lean)|lake-manifest\.json|lean-toolchain)$", norm_stripped):
        return LEAN_BUILD_CONFIG_DESC
    if cwd_in_spec and norm_stripped in ("lakefile.toml", "lakefile.lean", "lake-manifest.json", "lean-toolchain"):
        return LEAN_BUILD_CONFIG_DESC
    if re.search(r"(?:^|/)(?:spec/)?Nayutar/Statements(?:\.lean|/|$)", norm_stripped):
        return "spec/Nayutar/Statements"
    if re.search(r"(?:^|/)(?:spec/)?Nayutar/Fixture(?:\.lean)?(?:/|$)", norm_stripped):
        return "spec/Nayutar/Fixture"
    if cwd_in_statements and (norm_stripped in (".", "..") or norm_stripped.endswith(".lean")):
        return "spec/Nayutar/Statements or spec/Nayutar/Fixture"
    if re.search(r"(?:^|/)\.git(?:/|$)", norm_stripped):
        return ".git internals"
    if re.search(r"(?:^|/)\.(?:config|local/state)/kisage(?:/|$)", norm_stripped):
        return "the kisage trust boundary"

    if "$" not in tok:
        try:
            lexical = Path(os.path.abspath(os.path.expanduser(tok)))
            resolved = lexical.resolve()
        except (OSError, RuntimeError):
            return None
        for cand in (lexical, resolved):
            if _is_protected_statement_path(cand.parts):
                return "the theorem statements, the fixture model, the comparator configuration or the Lean build configuration"
            for root in boundary:
                if _inside(cand, root):
                    return f"the kisage trust boundary ({root})"
            if _is_protected_env_name(cand.name) or "secrets" in cand.parts[:-1] or ".git" in cand.parts:
                return str(tok)

    return None


def _text_mentions_protected_path(text: str, boundary: list[Path]) -> str | None:
    if "KISAGE_HOST_CONFIG" in text:
        return "kisage host configuration (KISAGE_HOST_CONFIG)"
    if re.search(r"(?:^|[\s'\"/])spec/comparator\.json(?:$|[\s'\"])", text):
        return "spec/comparator.json"
    if re.search(r"(?:^|[\s'\"/])spec/(?:lakefile\.(?:toml|lean)|lake-manifest\.json|lean-toolchain)(?:$|[\s'\"])", text):
        return "the Lean build configuration"
    if re.search(r"(?:^|[\s'\"/])(?:spec/)?Nayutar/Statements(?:\.lean|/|(?:$|[\s'\"]))", text):
        return "spec/Nayutar/Statements"
    if re.search(r"(?:^|[\s'\"/])(?:spec/)?Nayutar/Fixture(?:\.lean)?(?:/|(?=$|[\s'\";)|&>]))", text):
        return "spec/Nayutar/Fixture"
    if re.search(r"(?:^|[\s'\"/])\.git(?:/hooks|/config|/|$)", text):
        return ".git internals"
    if re.search(r"(?:^|[\s'\"/])\.(?:config|local/state)/kisage(?:/|(?:$|[\s'\"]))", text):
        return "the kisage trust boundary"
    for root in boundary:
        root_str = str(root)
        if root_str and root_str in text:
            return f"the kisage trust boundary ({root})"
    return None


def _is_hook_bypass_config(kv: str) -> bool:
    key = kv.split("=", 1)[0].strip().lower()
    if key == "core.hookspath":
        return True
    if "=" in kv:
        val = kv.split("=", 1)[1].strip().lower()
        if key in ("commit.gpgsign", "tag.gpgsign") and val in ("false", "0", "no", "off"):
            return True
    return False


def _check_git_invocation(args: list[str]) -> str | None:
    i = 0
    n = len(args)
    while i < n:
        tok = args[i]
        if tok == "--":
            i += 1
            break
        if tok == "-c" and i + 1 < n:
            if _is_hook_bypass_config(args[i + 1]):
                return HOOK_BYPASS_REASON
            i += 2
            continue
        if tok.startswith("-c") and len(tok) > 2:
            if _is_hook_bypass_config(tok[2:]):
                return HOOK_BYPASS_REASON
            i += 1
            continue
        if tok in GIT_GLOBAL_ARG_FLAGS and i + 1 < n:
            i += 2
            continue
        if tok.startswith("-"):
            i += 1
            continue
        break

    if i >= n:
        return None

    sub = args[i].lower()
    rest = args[i + 1 :]

    if sub == "push":
        return DENY_REASON

    if sub == "remote":
        for tok in rest:
            if tok.startswith("-"):
                continue
            if tok.lower() in ("add", "set-url", "update"):
                return DENY_REASON
            break
        return None

    if sub == "fetch":
        if any("push" in tok.lower() for tok in rest):
            return DENY_REASON
        return None

    if sub == "config":
        read_flags = {"--get", "--get-all", "--get-regexp", "--list", "-l"}
        if any(tok in read_flags for tok in rest):
            return None
        pos = [tok for tok in rest if not tok.startswith("-")]
        if pos and pos[0].strip().lower() == "core.hookspath" and len(pos) >= 2:
            return HOOK_BYPASS_REASON
        if len(pos) >= 2 and pos[0].strip().lower() in ("commit.gpgsign", "tag.gpgsign"):
            if pos[1].strip().lower() in ("false", "0", "no", "off"):
                return HOOK_BYPASS_REASON
        return None

    if sub == "commit":
        j = 0
        while j < len(rest):
            tok = rest[j]
            if tok == "--":
                break
            if tok in ("--no-verify", "--no-gpg-sign"):
                return HOOK_BYPASS_REASON
            if tok in GIT_COMMIT_ARG_LONG_FLAGS:
                j += 2
                continue
            if tok.startswith("--"):
                j += 1
                continue
            if tok.startswith("-") and len(tok) > 1:
                cluster = tok[1:]
                for idx_ch, ch in enumerate(cluster):
                    if ch == "n":
                        return HOOK_BYPASS_REASON
                    if ch in GIT_COMMIT_ARG_SHORT_FLAGS:
                        if idx_ch == len(cluster) - 1 and ch != "S":
                            j += 1
                        break
                j += 1
                continue
            j += 1

    return None


def _check_gh_invocation(args: list[str]) -> str | None:
    i = 0
    n = len(args)
    while i < n:
        tok = args[i]
        if tok in ("-R", "--repo", "--hostname") and i + 1 < n:
            i += 2
            continue
        if tok.startswith("-"):
            i += 1
            continue
        break
    if i >= n:
        return None
    sub = args[i].lower()
    sub_args = [t for t in args[i + 1 :] if not t.startswith("-")]
    action = sub_args[0].lower() if sub_args else ""
    if (
        (sub == "repo" and action == "sync")
        or (sub == "release" and action in ("upload", "create"))
        or (sub == "pr" and action in ("create", "merge"))
        or (sub == "workflow" and action == "run")
        or (sub == "run" and action in ("rerun", "cancel"))
    ):
        return DENY_REASON
    return None


def _lake_subcommand(args: list[str]) -> str | None:
    i = 0
    while i < len(args):
        tok = args[i]
        if tok.startswith("-"):
            i += 2 if tok in LAKE_ARG_OPTIONS else 1
            continue
        return tok
    return None


def _lake_has_update_option(args: list[str]) -> bool:
    i = 0
    after_subcommand = False
    while i < len(args):
        tok = args[i]
        if tok.startswith("-"):
            if tok in LAKE_UPDATE_OPTIONS:
                return True
            i += 2 if tok in LAKE_ARG_OPTIONS else 1
            continue
        if after_subcommand:
            i += 1
            continue
        if tok in LAKE_PROGRAM_SUBCOMMANDS:
            return False
        after_subcommand = True
        i += 1
    return False


def _check_lake_or_comparator(base: str, args: list[str]) -> str | None:
    if base == "lake" and (
        _lake_subcommand(args) in LAKE_UPDATE_SUBCOMMANDS or _lake_has_update_option(args)
    ):
        return LAKE_UPDATE_REASON
    has_no_sandbox = any(
        tok == "--inadvisably-no-sandbox" or tok.startswith("--inadvisably-no-sandbox=")
        for tok in args
    )
    if not has_no_sandbox:
        return None
    if base == "comparator" or (base == "lake" and any(tok == "comparator" for tok in args)):
        return COMPARATOR_SANDBOX_REASON
    return None


def _has_inplace_flag(args: list[str], script_flags: set[str]) -> tuple[bool, list[str]]:
    inplace = False
    positional: list[str] = []
    after_dd = False
    i = 0
    while i < len(args):
        tok = args[i]
        if after_dd:
            positional.append(tok)
            i += 1
            continue
        if tok == "--":
            after_dd = True
            i += 1
            continue
        if tok == "--in-place" or tok.startswith("--in-place="):
            inplace = True
            i += 1
            continue
        if tok in script_flags:
            i += 2
            continue
        if tok.startswith("--"):
            if tok in ("--expression", "--file"):
                i += 2
            else:
                i += 1
            continue
        if tok.startswith("-") and len(tok) > 1:
            cluster = tok[1:]
            for idx_ch, ch in enumerate(cluster):
                if ch == "i":
                    inplace = True
                    break
                if f"-{ch}" in script_flags:
                    if idx_ch == len(cluster) - 1:
                        i += 1
                    break
            i += 1
            continue
        positional.append(tok)
        i += 1
    return inplace, positional


def _extract_cp_destinations(args: list[str], is_install: bool = False) -> list[str]:
    targets: list[str] = []
    positional: list[str] = []
    dir_mode = False
    after_dd = False
    i = 0
    while i < len(args):
        tok = args[i]
        if after_dd:
            positional.append(tok)
            i += 1
            continue
        if tok == "--":
            after_dd = True
            i += 1
            continue
        if tok in ("-t", "--target-directory") and i + 1 < len(args):
            targets.append(args[i + 1])
            i += 2
            continue
        if tok.startswith("--target-directory="):
            targets.append(tok.split("=", 1)[1])
            i += 1
            continue
        if is_install and tok in ("-m", "-o", "-g", "-S", "--mode", "--owner", "--group", "--suffix"):
            i += 2
            continue
        if is_install and (tok == "-d" or (tok.startswith("-") and not tok.startswith("--") and "d" in tok[1:])):
            dir_mode = True
            i += 1
            continue
        if tok.startswith("-"):
            i += 1
            continue
        positional.append(tok)
        i += 1

    if dir_mode:
        targets.extend(positional)
    elif targets:
        pass
    elif len(positional) >= 2:
        targets.append(positional[-1])
    return targets


def _protected_mutation_reason(target_desc: str) -> str:
    return (
        f"BLOCKED: shell command attempts to modify a protected path ({target_desc}). "
        "Protected files must not be edited or overwritten via shell redirections or utilities; "
        "ask the user if a protected file needs to change."
    )


def _check_segment_mutation(
    base: str,
    args: list[str],
    heredocs: list[str],
    via_xargs: bool,
    pipeline_protected: str | None,
    cwd_in_spec: bool,
    cwd_in_statements: bool,
    boundary: list[Path],
) -> str | None:
    def check_tok(tok: str) -> str | None:
        return _check_protected_token(tok, cwd_in_spec, cwd_in_statements, boundary)

    if base in ("rm", "unlink", "shred", "mv", "truncate", "ln", "chmod", "chown", "touch", "patch"):
        for tok in args:
            if not tok.startswith("-") and (hit := check_tok(tok)) is not None:
                return _protected_mutation_reason(hit)
        if via_xargs and pipeline_protected is not None:
            return _protected_mutation_reason(pipeline_protected)
        return None

    if base == "tee":
        for tok in args:
            if not tok.startswith("-") and (hit := check_tok(tok)) is not None:
                return _protected_mutation_reason(hit)
        if via_xargs and pipeline_protected is not None:
            return _protected_mutation_reason(pipeline_protected)
        return None

    if base in ("cp", "install"):
        for dst in _extract_cp_destinations(args, is_install=(base == "install")):
            if (hit := check_tok(dst)) is not None:
                return _protected_mutation_reason(hit)
        return None

    if base == "dd":
        for tok in args:
            if tok.startswith("of=") and (hit := check_tok(tok[3:])) is not None:
                return _protected_mutation_reason(hit)
        return None

    if base in ("sed", "gsed"):
        inplace, pos = _has_inplace_flag(args, {"-e", "-f"})
        if inplace:
            for tok in pos[1:] if pos else []:
                if (hit := check_tok(tok)) is not None:
                    return _protected_mutation_reason(hit)
            for tok in pos:
                if (hit := check_tok(tok)) is not None:
                    return _protected_mutation_reason(hit)
            if via_xargs and pipeline_protected is not None:
                return _protected_mutation_reason(pipeline_protected)
        return None

    if base in ("perl", "ruby"):
        inplace, pos = _has_inplace_flag(args, {"-e", "-E"})
        if inplace:
            for tok in pos:
                if (hit := check_tok(tok)) is not None:
                    return _protected_mutation_reason(hit)
            if via_xargs and pipeline_protected is not None:
                return _protected_mutation_reason(pipeline_protected)
        return None

    if base == "find":
        mutating_find = any(tok in ("-delete", "-exec", "-execdir") for tok in args)
        if mutating_find:
            for tok in args:
                if not tok.startswith("-") and (hit := check_tok(tok)) is not None:
                    return _protected_mutation_reason(hit)
        return None

    if PYTHON_BIN_RE.match(base):
        inline_codes: list[str] = list(heredocs)
        extra_args: list[str] = []
        for idx, tok in enumerate(args):
            if tok == "-c" and idx + 1 < len(args):
                inline_codes.append(args[idx + 1])
                extra_args.extend(args[idx + 2 :])
                break
            if tok.startswith("-c") and len(tok) > 2:
                inline_codes.append(tok[2:])
                extra_args.extend(args[idx + 1 :])
                break
        if inline_codes:
            combined_code = "\n".join(inline_codes)
            if PYTHON_WRITE_RE.search(combined_code):
                if (hit := _text_mentions_protected_path(combined_code, boundary)) is not None:
                    return _protected_mutation_reason(hit)
                for tok in extra_args:
                    if (hit := check_tok(tok)) is not None:
                        return _protected_mutation_reason(hit)
        return None

    return None


def _extract_shell_c_script(args: list[str]) -> str | None:
    i = 0
    while i < len(args):
        tok = args[i]
        if tok == "--":
            break
        if tok.startswith("-") and not tok.startswith("--") and "c" in tok[1:]:
            if i + 1 < len(args):
                return args[i + 1]
            return None
        if tok in ("-o", "+o") and i + 1 < len(args):
            i += 2
            continue
        if tok.startswith("-") or tok.startswith("+"):
            i += 1
            continue
        break
    return None


def check_shell_command(command: str, _depth: int = 0) -> str | None:
    """Return a specific deny reason if command violates shell guard rules, else None."""
    if _depth > 8:
        return None

    boundary = kisage_trust_boundary()
    tokens, subcommands = _lex_shell(command)

    cwd_norm = os.getcwd().replace("\\", "/").rstrip("/")
    cwd_in_spec = cwd_norm.endswith("/spec")
    cwd_in_statements = bool(re.search(r"/spec/Nayutar/(?:Statements|Fixture)(?:/|$)", cwd_norm))

    segments: list[tuple[list[_Tok], str]] = []
    cur_seg: list[_Tok] = []
    for tok in tokens:
        if tok.kind == "op":
            if cur_seg:
                segments.append((cur_seg, tok.value))
                cur_seg = []
        else:
            cur_seg.append(tok)
    if cur_seg:
        segments.append((cur_seg, ""))

    pipeline_protected: str | None = None

    for seg_tokens, sep_op in segments:
        words: list[str] = []
        heredocs: list[str] = []
        idx = 0
        while idx < len(seg_tokens):
            t = seg_tokens[idx]
            if t.kind == "redir":
                if t.heredoc:
                    heredocs.append(t.heredoc)
                if t.value.lstrip("0123456789") in ("<<", "<<-"):
                    idx += 1
                    continue
                next_word = (
                    seg_tokens[idx + 1].value
                    if idx + 1 < len(seg_tokens) and seg_tokens[idx + 1].kind == "word"
                    else ""
                )
                if next_word and _is_write_redir(t.value, next_word):
                    hit = _check_protected_token(next_word, cwd_in_spec, cwd_in_statements, boundary)
                    if hit is not None:
                        return _protected_mutation_reason(hit)
                if idx + 1 < len(seg_tokens) and seg_tokens[idx + 1].kind == "word":
                    idx += 2
                    continue
                idx += 1
                continue
            words.append(t.value)
            idx += 1

        seg_protected: str | None = None
        for w in words[1:]:
            if not w.startswith("-"):
                seg_protected = _check_protected_token(w, cwd_in_spec, cwd_in_statements, boundary)
                if seg_protected is not None:
                    break

        unwrapped, via_xargs, wrapper_scripts = _unwrap_wrappers(words)
        for script in wrapper_scripts:
            if (reason := check_shell_command(script, _depth + 1)) is not None:
                return reason

        if unwrapped:
            base = os.path.basename(unwrapped[0])
            args = unwrapped[1:]

            if base == "cd" and args:
                dest = args[0].replace("\\", "/").rstrip("/")
                if dest == "spec" or dest.endswith("/spec"):
                    cwd_in_spec = True
                elif re.search(r"(?:^|/)(?:spec/)?Nayutar/(?:Statements|Fixture)(?:/|$)", dest):
                    cwd_in_statements = True
                else:
                    cwd_in_spec = False
                    cwd_in_statements = False

            if base == "eval" and args:
                if (reason := check_shell_command(" ".join(args), _depth + 1)) is not None:
                    return reason

            if base in SHELL_BINS:
                c_script = _extract_shell_c_script(args)
                if c_script is not None:
                    if (reason := check_shell_command(c_script, _depth + 1)) is not None:
                        return reason
                for hd in heredocs:
                    if (reason := check_shell_command(hd, _depth + 1)) is not None:
                        return reason

            if base == "git":
                if (reason := _check_git_invocation(args)) is not None:
                    return reason

            if base == "gh":
                if (reason := _check_gh_invocation(args)) is not None:
                    return reason

            if base in ("lake", "comparator"):
                if (reason := _check_lake_or_comparator(base, args)) is not None:
                    return reason

            if (
                reason := _check_segment_mutation(
                    base,
                    args,
                    heredocs,
                    via_xargs,
                    pipeline_protected,
                    cwd_in_spec,
                    cwd_in_statements,
                    boundary,
                )
            ) is not None:
                return reason

        if sep_op in ("|", "|&"):
            pipeline_protected = pipeline_protected or seg_protected
        else:
            pipeline_protected = None

    for subcmd in subcommands:
        if (reason := check_shell_command(subcmd, _depth + 1)) is not None:
            return reason

    return None


def is_push_class(command: str) -> bool:
    return check_shell_command(command) is not None or PUSH_CLASS_RE.search(command) is not None


def read_shell_command(raw: str) -> str | None:
    """Return the shell command text, None for an unwatched tool; raise ValueError when unreadable."""
    payload = json.loads(raw)
    if not isinstance(payload, dict):
        raise ValueError("expected a JSON object")
    tool_name = payload.get("tool_name")
    if not isinstance(tool_name, str) or not tool_name:
        raise ValueError("missing tool_name")
    if tool_name not in WATCHED_SHELL_TOOLS:
        return None
    tool_input = payload.get("tool_input")
    if not isinstance(tool_input, dict):
        raise ValueError(f"missing tool_input for {tool_name}")
    command = tool_input.get("command")
    if isinstance(command, str):
        return command
    if isinstance(command, list) and command and all(isinstance(part, str) for part in command):
        return shlex.join(command)
    raise ValueError(f"missing or invalid command for {tool_name}")


def main() -> int:
    try:
        command = read_shell_command(sys.stdin.buffer.read().decode("utf-8"))
    except (ValueError, UnicodeDecodeError):
        print(json.dumps(_decision(UNREADABLE_REASON)))
        return 0
    if command is not None and (reason := check_shell_command(command)) is not None:
        print(json.dumps(_decision(reason)))
    return 0


if __name__ == "__main__":
    sys.exit(main())
