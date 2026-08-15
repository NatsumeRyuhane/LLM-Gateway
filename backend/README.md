# Backend

The backend is one Go 1.26.6 module containing the gateway modular monolith and
the standalone mock-provider process. The scaffold uses only the Go standard
library at runtime.

## Commands

Run all backend checks from this directory:

```bash
test -z "$(find . -type f -name '*.go' -print0 | xargs -0 gofmt -l)"
go vet ./...
go tool staticcheck ./...
go test ./...
go test -race ./...
go build ./...
go tool govulncheck ./...
```

Start either process locally:

```bash
go run ./cmd/gateway
go run ./cmd/mock-provider
```

The gateway listens on `127.0.0.1:8080` by default. The mock provider listens
on `127.0.0.1:8081`. Both expose:

- `GET /livez`, which succeeds while the process can serve HTTP; and
- `GET /readyz`, which succeeds only after the listener is owned and before
  shutdown begins.

The mock provider currently contains only the process lifecycle and health
surface. Deterministic response profiles, fault injection, and any test-only
control surface remain scoped to issue #7.

## Configuration

Each process accepts the same suffixes under a distinct environment prefix:
`GATEWAY` or `MOCK_PROVIDER`.

| Variable suffix | Gateway default | Mock-provider default |
| --- | --- | --- |
| `_HTTP_ADDR` | `127.0.0.1:8080` | `127.0.0.1:8081` |
| `_HTTP_READ_HEADER_TIMEOUT` | `5s` | `5s` |
| `_HTTP_READ_TIMEOUT` | `30s` | `30s` |
| `_HTTP_WRITE_TIMEOUT` | `30s` | `30s` |
| `_HTTP_IDLE_TIMEOUT` | `60s` | `60s` |
| `_HTTP_SHUTDOWN_TIMEOUT` | `10s` | `10s` |

For example, `GATEWAY_HTTP_ADDR=127.0.0.1:9090` changes the gateway listener.
Configuration is validated before the process binds a socket. Errors identify
the rejected variable without repeating its value.

## Package boundaries

- `cmd/gateway` and `cmd/mock-provider` are process entry points.
- `internal/app` owns construction, the bounded HTTP lifecycle, and the
  authenticated single-route data-plane handler. It composes authentication
  with the public codec so handlers receive typed requests without raw identity
  transport, then injects one application-authorized route and performs exactly
  one buffered or streaming provider attempt with gateway-generated request,
  decision, response, and attempt identifiers.
- `internal/config` owns configuration loading and validation.
- `internal/health` owns process and route-health state.
- `internal/protocol` owns provider-neutral Chat Completions requests, responses,
  capabilities, failures, bounded JSON Schema validation, and stream lifecycle
  enforcement.
- `internal/openai` owns the strict public v0 HTTP/JSON/SSE codec, deterministic
  conformance goldens, and safe public error translation. It depends only on the
  canonical protocol package among internal domains.
- `internal/auth` owns strict application-credential parsing, keyed verification,
  typed principals, exact data-plane scopes, and application-bound request
  attribution. The concrete security contract is in
  [`docs/authentication.md`](../docs/authentication.md).
- `internal/provider` owns the consumer-facing adapter contract and immutable
  validated route inputs. `internal/provider/openai` translates one
  OpenAI-compatible upstream Chat Completions route, validates buffered and
  incremental SSE success paths, creates fresh allowlisted outbound requests,
  places route-owned credentials, and closes all upstream response bodies.
- `internal/telemetry` defines bounded metadata-only request, decision, attempt,
  latency, usage, output-acceptance, downstream-commit, and terminal evidence
  records. Exporters remain deferred.
- `routing`, `accounting`, `storage`, and `controlapi` reserve the remaining
  accepted domain boundaries. General route selection, retry, and fallback are
  deliberately absent from the single-route slice.

Unit tests stay beside their packages. Root `tests/` remains reserved for
cross-service, end-to-end, load, replay, and fault assets.

## Dependencies

There are no runtime dependencies. `go.mod` pins two build-time tools required
by the repository CI contract:

- `honnef.co/go/tools/cmd/staticcheck` for static analysis beyond `go vet`; and
- `golang.org/x/vuln/cmd/govulncheck` for reachable Go vulnerability analysis.

Their transitive modules are tool-only and do not enter either production
binary unless imported by runtime code.
