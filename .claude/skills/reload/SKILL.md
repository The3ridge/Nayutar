---
name: reload
description: Re-reads Nayutar's authoritative project context — CLAUDE.md and AGENTS.md, the path-scoped rules in .claude/rules/, ADR 0001 and the decisions currently in force — and restates which of them bear on the work in progress. Use when invoked manually after a long session, after context compaction, or when the working assumptions have drifted from what the repo actually says (「コンテキスト再読み込み」).
allowed-tools: Read, Glob, Bash(docdag query:*)
disable-model-invocation: true
---

# Reload Project Context

Read the following and treat every line as authoritative for the remainder of the session:

1. `./CLAUDE.md` and `./AGENTS.md` — the working rules shared by every agent
2. `.claude/rules/*.md` — path-scoped standing constraints. Glob the directory rather than assuming
   which rule files exist
3. ADR 0001 (`docs/ADR/0001-*.md`) — the premises that must not move
4. `docdag query --binding --fields id,title,status,path` — the decisions in force. ADRs still
   `proposed` are not in force; say so when the work leans on one

Skills are discovered from their own frontmatter and do not need to be read here.

## Output Checklist

When summarizing the reloaded context, output a short checklist structured as follows:

- [ ] **Working rules (`AGENTS.md`)**: Restate the rules that apply to the ongoing task — LKAL instead of YAML, gate-by-gate progress with an oracle, no unverified work reported as done, ADR procedure, and the git limits (no push, PR or workflow operation without an explicit request).
- [ ] **Path-scoped rules (`.claude/rules/`)**: Match active files against the glob patterns in `.claude/rules/*.md` and list the constraints that apply to those files.
- [ ] **Decisions (`docs/ADR/`)**: List the binding ADRs and the proposed ones the current work depends on, and flag any step of the work that would contradict ADR 0001.
