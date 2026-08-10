# Contributing

Adaptive LLM Gateway treats reproducible engineering evidence as part of the
product. Changes should be small enough to review, linked to a public issue, and
green before they are committed and merged.

## Before starting

1. Find or create a GitHub Issue with testable acceptance criteria.
2. Record unresolved product or architecture choices instead of embedding them
   silently in implementation.
3. Branch from an up-to-date `main` using a descriptive prefix such as `feat/`,
   `fix/`, `docs/`, or `chore/`. Codex-created branches use `codex/`.
4. Keep credentials, local environment files, provider payloads, and user content
   out of the repository.

Machine-local tool installation is not tracked as a project issue. The repository
does track toolchain versions and the commands needed to reproduce checks.

## Commits

Every commit must:

- follow Conventional Commits, for example
  `fix(stream): stop fallback after visible output`;
- be signed with the contributor's configured SSH signing key using
  `git commit -S`;
- contain one focused, reviewable change;
- pass the checks relevant to the files it changes;
- avoid AI co-author or similar attribution trailers.

Do not commit generated files without also committing their source and proving
that regeneration is clean.

## Pull requests

All changes reach `main` through a pull request. A pull request must:

- link the issue it implements;
- explain the behavioral change and why it is needed;
- include commands and evidence used to verify it;
- describe observability, privacy, security, compatibility, and failure-mode
  effects, using `Not applicable` only with a reason;
- update `docs/TODO.md` and relevant contracts;
- call out migrations, rollbacks, generated code, and deferred follow-ups;
- resolve review conversations before merge.

Prefer a sequence of small green commits. Do not mix mechanical formatting,
unrelated refactors, and behavioral changes in one commit.

## Required checks

The stable CI job names become protected-branch checks after each job has run at
least once. The intended local equivalents are:

### Repository

```bash
git diff --check
```

### Backend

From `backend/` once the Go module exists:

```bash
gofmt -w .
go vet ./...
go test ./...
go test -race ./...
go build ./...
go tool govulncheck ./...
```

Code formatting should be run before review; CI verifies that it produces no
diff. Pinned tool dependencies are run with `go tool`, not unversioned global
installations.

### Frontend

From `frontend/` once the package exists:

```bash
pnpm install --frozen-lockfile
pnpm lint
pnpm typecheck
pnpm test
pnpm build
```

### Integration and resilience

Integration, race, load, replay, and fault-injection suites are added as their
fixtures land. Timing-sensitive tests must synchronize on observable events
instead of relying on arbitrary sleeps.

## Review guide

Reviewers should check that:

- the implementation matches a written contract or updates that contract;
- cancellation, deadlines, goroutine ownership, and cleanup are explicit;
- retries are bounded and cannot cross the client-visibility boundary;
- unsupported protocol semantics fail explicitly;
- metric labels remain bounded and secrets/content are redacted;
- tests cover the failure path, not only the successful path;
- dependency additions have a documented reason and narrower alternatives were
  considered.

## Dependency updates

Runtime and tool dependencies are pinned in project manifests and updated through
dedicated pull requests. Updates must pass the complete relevant test suite and
be reviewed for behavioral, license, vulnerability, and generated-code changes.
Major updates require an issue or ADR when they change an accepted contract.

## Branch protection

`main` is intended to require pull requests, signed commits, successful checks,
conversation resolution, and linear history. Force pushes, deletion, and
administrator bypass are disabled. Required approval is deferred while there is
no independent reviewer; it should be enabled as soon as a qualified collaborator
is available.
