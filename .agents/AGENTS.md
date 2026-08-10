# Adaptive LLM Gateway agent instructions

## Authority and scope

- Fetch the live GitHub Issue before editing. It is the source of truth for task
  scope, dependencies, acceptance criteria, discussion, and status.
- Fetch the live [Adaptive LLM Gateway Notion design](https://app.notion.com/p/3b46d181813e8194bb0bd5ba1c7d73ca)
  before editing. It is the continuously synchronized source of truth for the
  product and system design.
- Do not copy issue bodies or the Notion design into `.agents`; stale snapshots
  defeat the shared-context workflow.
- Treat checked-in `docs/` files as implementation records and accepted ADRs.
  Reconcile them with the live issue and Notion source when starting work.
- Work on one issue per conversation and one dedicated branch unless the user
  explicitly groups issues.
- Do not implement a dependent issue by assumption. Record the blocker or use an
  accepted proposed default from the relevant contract.
- Machine-local installation and editor configuration are contributor concerns,
  not project backlog work.

## Product invariants

- Never automatically retry after client-visible model output or a potentially
  actionable tool call.
- Never combine client-visible bytes from multiple provider attempts.
- Reject unsupported semantics explicitly instead of dropping them.
- Treat missing, stale, or insufficient health evidence as `unknown`.
- Keep routing deterministic for a fixed policy and evidence snapshot.
- Propagate cancellation and downstream disconnects upstream.
- Do not retain prompts, completions, or tool arguments by default.
- Keep identity, request, conversation, run, and credential values out of metric
  labels.

## Engineering workflow

1. Start from `main` on a dedicated branch. Codex branches use `codex/`.
2. Preserve unrelated worktree changes.
3. Implement in small reviewable increments.
4. Run all checks relevant to the changed files before every commit.
5. Update `docs/TODO.md` and affected contracts.
6. Sign every commit with `git commit -S` and use Conventional Commits.
7. Do not add an AI co-author trailer.
8. Ask before pushing. After pushing, ask before creating a PR; use a draft PR
   unless the user explicitly requests ready-for-review.
9. Never merge locally into protected `main`.

## Context refresh

At the start of every issue conversation:

1. Read the live issue and its comments.
2. Read linked blocking issues and accepted decisions.
3. Read the relevant sections of the live Notion design.
4. Inspect the current branch, repository state, and affected files.
5. If GitHub, Notion, and checked-in code disagree materially, stop and present
   the conflict instead of silently choosing an older snapshot.

## Go expectations

- Prefer standard-library primitives and narrow consumer-owned interfaces.
- Document goroutine, channel, timer, response-body, and cancellation ownership.
- Keep provider wire models inside provider adapters.
- Unit tests live beside packages; root `tests/` is for cross-service, end-to-end,
  load, replay, and fault assets.
- New runtime dependencies need a written reason and must be pinned in `go.mod`.
- Run formatting, vet/static analysis, unit tests, race tests, builds, and
  vulnerability checks when the relevant code exists.

## Evidence required at handoff

- Branch and signed commit IDs.
- Files and behaviors changed.
- Exact validation commands and results.
- Acceptance criteria satisfied or still open.
- Security, privacy, reliability, observability, compatibility, migration, and
  rollback impact.
- Any decision that still requires the maintainer.
