# Continuous integration contract

CI check names are a public repository interface. Branch protection refers to
the job names below, so jobs must keep their names even when their implementation
changes. A job reports success with an explicit notice while its corresponding
scaffold does not exist; it becomes enforcing as soon as the activation path is
added.

| Required check | Activation path | Enforced commands |
| --- | --- | --- |
| `repository` | Always | whitespace, required files, YAML, generated files, and PR commit subjects |
| `backend` | `backend/go.mod` | `gofmt`, `go vet`, pinned `staticcheck`, unit tests, race tests, and build |
| `frontend` | `frontend/package.json` | frozen install, lint, typecheck, unit tests, and production build |
| `integration` | `tests/integration/run.sh` | PostgreSQL-backed tests using only the deterministic mock provider |
| `security` | Always; language scans activate with each manifest | repository policy, pinned `govulncheck`, and production dependency audit |
| `container` | Any tracked Dockerfile | build each deployment image without publishing it |

## Toolchain and harness requirements

The backend module declares its Go version in `backend/go.mod` and pins
`staticcheck` and `govulncheck` as Go tool dependencies. Workflows install Go
with `actions/setup-go`; they never assume Go or either analysis tool is present
on a contributor's machine.

The frontend pins Node.js in `frontend/.node-version` and pnpm through the
`packageManager` field in `frontend/package.json`. A committed lockfile is
mandatory.

`tests/integration/run.sh` is the single CI entrypoint for cross-service tests.
It receives `TEST_DATABASE_URL` for an ephemeral PostgreSQL service and
`TEST_PROVIDER_MODE=deterministic`. The runner must start or invoke the
controllable mock provider, must not call a real provider, and must synchronize
on observable readiness rather than sleeps.

When generated artifacts first appear, their generators must be reachable from
`scripts/generate.sh`. CI runs that entrypoint and fails if it changes the worktree.
Every tracked Dockerfile uses its containing directory as the default build
context; a deployment that needs a different context must update the container
check in the same pull request.

## Dependency policy

Runtime and tool dependencies are pinned in language manifests. GitHub Actions
updates are grouped weekly by Dependabot. Go and pnpm ecosystems are added to
`.github/dependabot.yml` in the same pull request that creates their manifest
directories, because Dependabot rejects update entries for missing directories.

Automated updates use dedicated pull requests and the same required checks as
human changes. Major versions require an issue or ADR when they alter an accepted
contract. Review includes behavior, license, vulnerability, transitive, lockfile,
and generated-file changes; an update is not merged solely because automation
opened it.
