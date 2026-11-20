---
name: grill-with-docs
description: >-
  Interrogates a plan or design the user already has — one question at a time, walking every
  branch of the decision tree — and checks each answer against Nayutar's recorded decisions, namely
  the ADRs in `docs/ADR/` (above all ADR 0001, the premises that must not move), and the code once
  it exists. Surfaces contradictions with a binding ADR, plans that quietly shrink the goal or
  bring YAML back to the human-facing surface, and terms too fuzzy to build on. Use whenever the
  user wants a plan stress-tested, grilled, or thought through out loud — "grill me", "poke holes
  in this design", "stress-test this plan", 「このプランを詰めて」 「設計を叩いて」
  「ADR と矛盾してないか見て」 — even if they never mention the docs.
allowed-tools: Bash, Read, Glob, Grep
---

<what-to-do>

Interview the user relentlessly about this plan until you reach a shared understanding. Walk down each
branch of the design tree, resolving dependencies between decisions one by one. Ask **one question at a
time** and wait for the answer before continuing. For each question, give your own recommended answer.

If a question can be answered by exploring the repository or the decision graph, explore instead of asking.

</what-to-do>

<supporting-info>

## Where the documented model lives

Read from these before challenging anything:

| Authority | Where |
|---|---|
| Premises that must not move | ADR 0001 (`docs/ADR/0001-*.md`); changing one needs an ADR that supersedes it |
| Decisions in force | `docdag query --binding --fields id,title,status,path` |
| Decisions still open | ADRs with `status: proposed`; treat them as undecided, not as binding |
| Declaration format | `../LKAL/docs/PRINCIPLE.md` and `../LKAL/docs/ADR/` |
| Ownership and apply | `../kisage/docs/PRINCIPLE.md` and `../kisage/docs/ADR/` |
| Working rules | `AGENTS.md` |

DocDag commands for traversing decisions:

- `docdag context <id> --budget 400`: the decision, its resolution, and its typed neighbourhood (`supersedes` and `depends-on`) with Decision excerpts.
- `docdag resolve <id>`: the current binding successor. It stops at a non-binding successor (e.g. `status: proposed`) and says so; treat that successor as not yet decided.
- `docdag query <id>`: what a decision rests on (its `depends-on` and `supersedes` targets). Trace prerequisites this way, not by grepping prose.
- `docdag query <id> --ancestors`: what depends on or supersedes a decision — the blast radius of a challenge.

## During the session

**Challenge against ADR 0001 first.** The usual drift is quiet: a plan that settles for "small and
used" instead of a full Kubernetes replacement, a human-written surface that goes back to YAML instead
of LKAL, or an ownership scheme that sidesteps kisage's leaf-path model. Name the premise and ask
whether the plan means to supersede it.

**Ask for the oracle.** Nayutar advances by verification gates, and a stage counts only when a machine
can judge it (a Kubernetes differential test, a deterministic simulation, a formal spec). For each
claimed capability, ask which oracle proves it and at which gate. "We'll test it later" is an open
branch, not an answer.

**Challenge against the documented language.** When a term conflicts with how an ADR uses it,
say so immediately: "ADR NNNN uses '<term>' for X, but you seem to mean Y — which is it?"

**Sharpen fuzzy terms.** Propose a precise canonical term when one is overloaded.

**Stress-test with concrete scenarios.** Invent cases that probe the edges — a conflicting apply, a
watch that falls behind, a tenant reference across a boundary — and force precision about where one
concept ends and the next begins.

**Cross-reference with code** once it exists. When the user states how something works, check whether
the code agrees, and surface the contradiction.

**Capture resolved terms as you go**, in a running summary rather than batching them to the end. Writing
a term into an ADR is a separate, user-approved edit: ask first. An unrequested glossary file
becomes a second source of truth nobody maintains.

## Offer an ADR sparingly

Only offer one when all three hold:

1. **Hard to reverse** — changing your mind later has a real cost
2. **Surprising without context** — a future reader will ask "why did they do it this way?"
3. **The result of a real trade-off** — there were genuine alternatives and one was picked for reasons

If any is missing, skip the ADR. When one is warranted, follow the ADR procedure in `AGENTS.md`
(`docdag new "<English title>"`, the `docs/ADR/template.md` headings in Japanese, `status: proposed`)
instead of inventing a format here. Whether it becomes `accepted` is the user's call.

</supporting-info>
