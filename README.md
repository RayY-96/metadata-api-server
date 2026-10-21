# metadata-api-server

[![CI](https://github.com/RayY-96/metadata-api-server/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/RayY-96/metadata-api-server/actions/workflows/ci.yml)

A RESTful metadata API for **Applications** and the **Maintainers** who create
them, written in Go (standard library HTTP server) with SQLite storage.

- **Highlight:** [optimistic concurrency](#highlight-optimistic-concurrency):
  concurrent writers can never silently overwrite each other.
- **E2E pipeline:** a [successful run](https://github.com/RayY-96/metadata-api-server/actions/runs/39645553718)
  builds the server with the race detector, starts it, and runs the end-to-end
  suite against the real API ([all runs](https://github.com/RayY-96/metadata-api-server/actions)).
- **Design document:** [docs/DESIGN.md](docs/DESIGN.md) has the full API
  contract, the assumptions and every decision with its reasons.

> The server has **no authentication**. It assumes a trusted network and is not
> safe to expose publicly as is (see [Limitations](#limitations)).

## Quick start

Requires [Go 1.27 or later](https://go.dev/dl/). No C toolchain is needed (the SQLite driver is pure
Go); only the race detector (`-race`) needs cgo.

```bash
git clone https://github.com/RayY-96/metadata-api-server.git
cd metadata-api-server

go test ./...          # unit tests (no server needed)
go run ./cmd/server    # serves on 127.0.0.1:8080, data in ./metadata.db
```

Build a binary instead with `go build -o server ./cmd/server` (`-o server.exe`
on Windows). Stop the server
with Ctrl+C (or SIGTERM); it shuts down gracefully.

| Variable | Default | Meaning |
|---|---|---|
| `ADDR` | `127.0.0.1:8080` | Listen address. Loopback by default, because there is no authentication. Use `:8080` inside a container; the published port then decides who can reach it |
| `DB_PATH` | `metadata.db` | SQLite file, created if missing |

**End-to-end tests** run against a running server (build tag `e2e`):

```bash
# terminal 1
go run ./cmd/server
# terminal 2
BASE_URL=http://127.0.0.1:8080 go test -tags=e2e -count=1 ./e2e
```

In PowerShell, set the variable with `$env:BASE_URL="http://127.0.0.1:8080"`
before running `go test -tags=e2e -count=1 ./e2e`. The suite creates its own
data with random emails, so it does not depend on what is already stored, but
it does not delete what it creates. Use a separate test database, for example
`DB_PATH=e2e.db go run ./cmd/server` in terminal 1 (CI uses a fresh one).

## Try it

With the server running, in bash or zsh (in PowerShell, use Git Bash, or
`curl.exe` with adjusted quoting):

```bash
B=http://127.0.0.1:8080/api/v1

# Create a Maintainer: 201, ETag "1"
curl -i -X POST $B/maintainers -H 'Content-Type: application/json' \
  -d '{"name":"Ada Lovelace","email":"Ada@Example.com"}'
```

```http
HTTP/1.1 201 Created
Content-Type: application/json
Etag: "1"
Location: /api/v1/maintainers/7cad2678-7ed6-424f-9e3b-e47e25ead60d

{"id":"7cad2678-7ed6-424f-9e3b-e47e25ead60d","name":"Ada Lovelace","email":"ada@example.com",
 "resourceVersion":1,"createdAt":"2026-10-21T09:26:03.229987Z","updatedAt":"2026-10-21T09:26:03.229987Z"}
```

```bash
ID="<paste-the-id-from-your-POST-response>"

# Create an Application owned by that Maintainer
curl -i -X POST $B/applications -H 'Content-Type: application/json' \
  -d "{\"maintainerId\":\"$ID\",\"title\":\"Valet\",\"version\":\"1.4.2\",\"license\":\"MIT\"}"

# Update with the current version: 200, ETag "2"
curl -i -X PUT $B/maintainers/$ID -H 'If-Match: "1"' -H 'Content-Type: application/json' \
  -d '{"name":"Ada King","email":"ada@example.com"}'

# Update again with the old version: 412, the newer change is not overwritten
curl -i -X PUT $B/maintainers/$ID -H 'If-Match: "1"' -H 'Content-Type: application/json' \
  -d '{"name":"Someone else","email":"ada@example.com"}'
```

```json
{"type":"about:blank","title":"Precondition Failed","status":412,
 "code":"version_mismatch","detail":"If-Match does not match the current resource version"}
```

## API reference

All resource endpoints are under `/api/v1` and use JSON. The complete contract,
including the field rules and the evaluation order of errors, is in
DESIGN.md [§3 Data Model](docs/DESIGN.md#3-data-model) and
[§4 API Contract](docs/DESIGN.md#4-api-contract).

| Method | Path | Success | Notes |
|---|---|---|---|
| `POST` | `/maintainers` | `201` | `Location` and `ETag` headers |
| `GET` | `/maintainers` | `200` | Paginated: `?limit=` (1–100, default 20), `?offset=` |
| `GET` | `/maintainers/{id}` | `200` | `ETag` header |
| `PUT` | `/maintainers/{id}` | `200` | Requires `If-Match`; returns the new `ETag` |
| `DELETE` | `/maintainers/{id}` | `204` | Requires `If-Match`; `409` while it owns Applications |
| `POST` | `/applications` | `201` | `Location` and `ETag` headers; `maintainerId` must refer to an existing Maintainer |
| `GET` | `/applications` | `200` | Paginated; optional `?maintainerId=` filter |
| `GET` | `/applications/{id}` | `200` | `ETag` header |
| `PUT` | `/applications/{id}` | `200` | Requires `If-Match`; `maintainerId` may be omitted, never changed |
| `DELETE` | `/applications/{id}` | `204` | Requires `If-Match` |
| `GET` | `/livez` | `200` | Liveness: the process serves HTTP; no dependency checks |
| `GET` | `/readyz` | `200` | Readiness: `503` while shutting down or if the database does not answer |

**Resources.** A Maintainer has `name` and `email` (unique, compared
case-insensitively). An Application has `maintainerId`, `title`, `version`
(required), and `company`, `website`, `source`, `license`, `description`
(optional; `website` and `source` must be absolute http(s) URLs). The server
manages `id` (UUID), `resourceVersion`, `createdAt` and `updatedAt`; a request
that tries to set them is rejected. PUT replaces all writable fields, except
that an Application's `maintainerId` never changes.

**Lists** return `{"items":[...],"limit":20,"offset":0,"hasMore":false}`,
ordered by creation time, then id.

**Errors** use [RFC 9457 Problem Details](https://www.rfc-editor.org/rfc/rfc9457)
(`application/problem+json`) with a stable `code`, also for unknown routes and
unsupported methods. Validation errors list every invalid field:

```json
{"type":"about:blank","title":"Unprocessable Entity","status":422,"code":"validation_failed",
 "detail":"request body failed validation",
 "errors":[{"field":"name","message":"is required"},
           {"field":"email","message":"must be a bare email address such as name@example.com"}]}
```

| Status | `code` | When |
|---|---|---|
| `400` | `invalid_body`, `invalid_query`, `invalid_if_match` | Malformed JSON, unknown field, trailing data; bad `limit`/`offset`, empty or repeated `maintainerId`; `If-Match` that is not a single strong ETag such as `"3"` |
| `404` | `not_found` | Resource or route does not exist |
| `405` | `method_not_allowed` | Known path, other method; `Allow` header lists the supported ones |
| `409` | `email_taken`, `maintainer_in_use` | Duplicate email; deleting a Maintainer that still owns Applications |
| `412` | `version_mismatch` | `If-Match` is not the current version |
| `413` | `body_too_large` | Body over 64 KiB |
| `415` | `unsupported_media_type` | `Content-Type` is not `application/json` |
| `422` | `validation_failed`, `maintainer_not_found`, `maintainer_changed` | Invalid fields; unknown Maintainer on create; changed `maintainerId` on update |
| `428` | `if_match_required` | `If-Match` missing on PUT or DELETE |
| `500` | `internal_error` | Unexpected error (details are only logged) |
| `503` | `unavailable`, `not_ready` | Database busy, request deadline exceeded, request canceled; readiness |

## Highlight: optimistic concurrency

**The problem.** Two clients read an Application at version 3, both edit it,
both send PUT. Without concurrency control the second write silently discards
the first, and neither client is told (the *lost update*).

**The mechanism.** Every resource has a `resourceVersion`, exposed as a strong
`ETag`. PUT and DELETE must send it in `If-Match`; a missing header is `428`,
so no client can skip the check by accident. The check and the write are a
single SQL statement, so nothing can happen between them:

```sql
UPDATE maintainers
   SET name = ?, email = ?, resource_version = resource_version + 1, updated_at = ?
 WHERE id = ? AND resource_version = ?
RETURNING ...
```

If no row matches, the same transaction asks whether the row exists, so the
answer refers to the same state: missing is `404`, stale is `412`. A check
followed by a separate write would let two writers pass the check at once.

**The proof.** An end-to-end test races 20 clients with the same `If-Match`
against the running server, in 5 rounds per resource. Every round must give
exactly one `200` and nineteen `412`s, raise the version by exactly one, and
store exactly the winner's body; the old version stays rejected afterwards.
Two more tests race updates against deletes (`TestConcurrentUpdateAndDelete`)
and deletes against deletes (`TestConcurrentDeletes`). CI runs these against a server
built with `-race`. Replacing the conditional `UPDATE` with an unconditional
one makes the test fail at once (two writers both get `200`).

**The honest caveat.** The server uses one database connection, so database
work is serialized and even a check-then-write inside one transaction would
be safe here. The design that carries over to several connections or to
PostgreSQL is the version check *inside* the statement
([DESIGN.md §6](docs/DESIGN.md#6-concurrency-control-highlight)).

## Production features

What is in place. Authentication, rate limiting and the other gaps are listed
under [Limitations](#limitations).

| Feature | What and why |
|---|---|
| Optimistic concurrency | See above. `If-Match` is required, so no client can skip the version check |
| Referential integrity | Foreign key from Application to Maintainer. Creating an Application needs an existing Maintainer (`422`); a Maintainer who owns Applications cannot be deleted (`409`). The database is the final authority, so races cannot create orphans |
| Input validation | Field rules with lengths, bare email addresses (normalized to lower case), http(s) URLs; every invalid field is reported, so a client fixes all of them in one round trip and bad data never reaches storage |
| Strict request decoding | Unknown fields, trailing data and wrong content types are rejected instead of silently ignored, so a typo in a field name is an error, not lost data |
| Per-request bounds | 64 KiB body limit, field length limits, page size capped at 100 and applied in SQL, so one request cannot make the server read, store or return unbounded data |
| Timeouts | Per-request 5 s deadline passed to every database call (`503` when used up). Server timeouts against slow clients: read header 5 s, read 10 s, write 15 s, idle 60 s |
| Graceful shutdown | On SIGINT/SIGTERM: readiness turns `503`, the listener closes, in-flight requests drain for up to 10 s, then the database closes |
| Health probes | `/livez` and `/readyz`, with the Kubernetes meaning: a database outage makes the server not ready, not dead |
| Durable storage | SQLite in WAL mode with `synchronous=FULL`, so a committed write is fsynced before the response. Tests check that these settings are active and that data survives a reopen; crash survival itself depends on the disk honoring fsync and is not tested |
| Consistent errors | Problem Details with stable codes everywhere, so clients branch on `code`, not on message text; internal errors are logged, never returned |

## Storage

SQLite (via the pure-Go `modernc.org/sqlite`), one file on local disk.

- **Consistency:** every write is a transaction; transactions are serializable;
  `UNIQUE` and `FOREIGN KEY` constraints hold even when requests race.
- **Durability:** with WAL and `synchronous=FULL` a committed transaction
  survives a process or OS crash, provided the disk honors fsync. There is no
  replication, so losing the disk loses the data.
- **Scaling:** a single instance with serialized writes, which suits a metadata
  service where reads dominate. Next steps: a separate read pool (WAL allows
  concurrent readers), then PostgreSQL for several instances (the conditional
  `UPDATE` is portable SQL), then keyset pagination for large lists
  ([DESIGN.md §5](docs/DESIGN.md#5-storage)).

## Key design decisions

Each decision is in the [Decision Log](docs/DESIGN.md#9-decision-log) with
alternatives and trade-offs.

- **Go standard library** (`net/http` routing since Go 1.22), no framework (D1).
- **SQLite with a single connection:** real constraints and durability with no
  setup; simple transaction behavior (D2).
- **One-to-many, immutable creator:** "Maintainers are the creator of the
  application" reads as one owner; deleting an owner with Applications is
  refused, not cascaded (D3, assumptions A1–A3).
- **Optimistic concurrency, `If-Match` required** (D4, D5).
- **Bounded offset pagination** (D6). **Modular monolith** (D7).
- **Constraint errors classified per operation:** the same foreign key error is
  a `422` on create and a `409` on delete (D8).
- **Always write a response,** even when the request context is cancelled; an
  empty response would be an implicit `200` (D9, found in review).
- **No `ON DELETE RESTRICT`:** SQLite reports it with a trigger error code; the
  default action blocks deletes the same way and reports the foreign key code
  (D10, found by a test).

The ambiguous parts of the assignment are answered explicitly in
[DESIGN.md §2 Assumptions](docs/DESIGN.md#2-assumptions) (for example: email is
unique and case-insensitive, `version` is free-form, PUT is a full replacement).

## Testing and CI

| Layer | What | Command |
|---|---|---|
| Unit | Validation rules, store against real SQLite files (constraints, version checks, error mapping, persistence across reopen, concurrency), HTTP handlers through `httptest` | `go test ./...` |
| E2E | 10 tests over real HTTP against the running server: CRUD of both resources, ETag flow, relationship rules, filter and pagination, error contract, concurrency | `BASE_URL=http://127.0.0.1:8080 go test -tags=e2e -count=1 ./e2e` |

GitHub Actions ([workflow](.github/workflows/ci.yml)) runs two jobs on every
push to `main` and on every pull request:

1. **Unit:** `gofmt`, `go mod tidy -diff`, `go vet`, build, `go test -race`
   with a coverage report in the job summary.
2. **E2E:** `go vet -tags=e2e`, build the server with `-race`, start it on a
   fresh database, wait for `/readyz`, run the suite, check that every test
   ran and passed, send SIGTERM and require a clean exit with no data race.

**Coverage** (unit tests, per package, from CI): `internal/validation` 100%,
`internal/api` 97.9%, `internal/store` 87.5%, `cmd/server` 75.0%; 92.0% of
statements overall. Each package counts only its own statements.

## Limitations

| Area | Current state |
|---|---|
| Authentication | None; assumes a trusted network. Next: bearer tokens, then per-Maintainer ownership checks |
| POST retries | A retried create can duplicate an Application. Next: an `Idempotency-Key` header |
| Rate limiting, load shedding | Per-request limits only; a flood of requests is not capped. Next: cap in-flight requests (`503`), rate limit at the gateway |
| Scale-out | Single instance (see Storage). Next: PostgreSQL |
| Pagination | Offset based: rows can shift between pages under concurrent writes. Next: keyset pagination |
| Schema changes | Tables are created at startup; there are no migrations. Next: versioned migrations |
| Observability | Logs only. Next: metrics (request rate, latency, `412` rate as a contention signal) |

A `503` does not prove that a write had no effect (the success response may
have been lost on the way), so clients should read before retrying.

## Project structure

```
cmd/server/          main: configuration, wiring, graceful shutdown
internal/api/        HTTP handlers, middleware, Problem Details errors
internal/model/      resource types
internal/store/      SQLite access and domain errors
internal/validation/ field validation
e2e/                 end-to-end tests (build tag e2e)
docs/DESIGN.md       design document and decision log
```

Suggested reading order: `docs/DESIGN.md` §6, then
`internal/store/maintainers.go` (`UpdateMaintainer`), `internal/api/maintainers.go`,
and `e2e/e2e_test.go` (`TestConcurrentUpdatesHaveOneWinner`).

## Cost

About 6 hours in total (local time): from 21:20 on 2026-10-20 to about
02:30 the next morning for the design document, the implementation, the
tests and CI (16 commits), and about 45 minutes from 10:00 that morning to
finish this README and verify the final CI run.

## Anything else

- **Process.** I deliberately chose a contract-first approach. I defined the
  data model, API contract, error semantics, and concurrency guarantees
  upfront, then implemented storage, HTTP handlers, end-to-end tests, and CI
  in focused, reviewed milestones.

  This helped maintain consistency across layers, gave each milestone clear
  acceptance criteria and tests, and produced a Git history that documents
  the implementation process. When review or testing revealed a design issue,
  I updated the design document alongside the implementation (e.g., D9 and
  D10).

  The main trade-off was **time to the first working end-to-end feature**: the
  first successful E2E CI run came with the 14th commit. If early delivery
  were the primary constraint, I would instead prioritize a minimal vertical
  slice (one resource working end to end) before expanding functionality and
  hardening the system.

  Both approaches are valid; the choice depends on the delivery constraints
  and the cost of rework.
- **AI usage.** I built this with an AI coding assistant (Claude Code). The
  assistant wrote most of the code and tests to the design document; I
  decided the open questions it raised (for example the `503` for cancelled
  requests and D10) and approved every commit. Every milestone also went
  through separate AI review agents, and their findings were checked against
  the code before they were accepted. Several real bugs were found that way:
  an empty `200` for cancelled requests (D9), a slow upload reported as a
  database outage, and a CI check that could have passed with skipped tests.
- **Why Go.** I chose Go deliberately to learn the stack that a Kubernetes-
  based platform team works in; the probes, `resourceVersion` and
  `If-Match` semantics follow Kubernetes conventions.
