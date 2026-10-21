# Design: Metadata API Server

- **Status:** Living document. Changes are made in the same commit as the code
  that motivates them, and recorded in the [Decision Log](#9-decision-log).
- **Last updated:** 2026-10-20

---

## 1. Goals and Non-goals

The assignment is intentionally open-ended. This project optimizes for a small
system that is **correct, bounded, and verifiable**, rather than for feature count.

### Goals

1. **Correctness first.** Data relationships are enforced by the database,
   concurrent writes never silently overwrite each other, and every error has
   a well-defined status code and body.
2. **Bounded per-request resource usage.** Input size limits, pagination caps and
   request deadlines bound what a single request can make the server read,
   buffer, return, or wait for. Aggregate load (many concurrent requests) is
   not bounded by this server; that risk is documented in [§10](#10-limitations-and-future-work).
3. **Verifiable.** Every behavior described in this document is covered by a
   unit test or an end-to-end test that runs in CI against the real server.

### Non-goals

| Not built | Why |
|---|---|
| Authentication / authorization | A half-finished auth layer is worse than none. The server is assumed to run behind a trusted gateway. See [§10](#10-limitations-and-future-work). |
| Rate limiting | The bounded-resource goal is met by body limits, pagination caps and deadlines. A per-process limiter does not protect a multi-instance deployment and adds test flakiness. |
| Idempotency keys for POST | Discussed as future work. Retried POSTs can create duplicates; for Maintainers the email uniqueness constraint already turns a duplicate into a 409. |
| YAML, labels, selectors | Not needed to demonstrate the goals above. |
| Multiple instances / HA | SQLite is a single-node store by design. See [§5.4](#54-scalability). |

---

## 2. Assumptions

The requirements leave these points open. Each one is a choice, stated so the
reviewer can disagree with it explicitly.

| # | Question | Assumption |
|---|---|---|
| A1 | How do Applications and Maintainers relate? | **One-to-many.** "Maintainers are the creator of the application": each Application has exactly one creator Maintainer; a Maintainer can create many Applications. |
| A2 | Can an Application's creator change? | **No.** `maintainerId` is set on create and is immutable. Changing the creator would rewrite history. |
| A3 | What happens when a Maintainer who still owns Applications is deleted? | **Rejected with 409.** No silent cascade; the client must delete the Maintainer's Applications first. Creator reassignment is not supported (A2). |
| A4 | Is Maintainer email unique? | **Yes**, case-insensitively. Email is the natural identity of a person. |
| A5 | How is email case handled? | The whole address is trimmed and lower-cased before storage and comparison. This is an **application-level assumption**: RFC 5321 allows the local part to be case-sensitive, but treating it as case-insensitive matches the behavior users expect. |
| A6 | Is `Application.Version` validated as SemVer? | **No.** It is free-form text (1–64 chars). Projects use SemVer, CalVer and others; rejecting valid real-world versions is worse than accepting unusual ones. |
| A7 | Is `License` validated against SPDX? | **No.** Free-form text, length-limited. |
| A8 | Which fields are required? | Maintainer: `name`, `email`. Application: `title`, `version`, `maintainerId`. All others are optional. |
| A9 | What does PUT mean? | **Full replacement of the writable fields.** An omitted optional field is cleared. Server-managed fields cannot be set by the client. |

> **`Version` vs `resourceVersion`.** `version` is the application's software
> version (e.g. `1.4.2`), owned by the client. `resourceVersion` is the
> server-managed revision of the *metadata record*, used for concurrency
> control. They are unrelated.

---

## 3. Data Model

### 3.1 Resources

Server-managed fields are read-only for clients.

**Maintainer**

| Field | Type | Rules |
|---|---|---|
| `id` | string (UUID) | Server-generated |
| `name` | string | Required, 1–100 chars after trimming |
| `email` | string | Required, ≤ 254 chars, a bare address (`a@b.com`, not `Name <a@b.com>`), normalized per A5, unique |
| `resourceVersion` | integer | Server-managed; starts at 1, +1 on every update |
| `createdAt`, `updatedAt` | string (RFC 3339, UTC) | Server-managed |

**Application**

| Field | Type | Rules |
|---|---|---|
| `id` | string (UUID) | Server-generated |
| `maintainerId` | string | Required on create, must reference an existing Maintainer, immutable |
| `title` | string | Required, 1–200 chars |
| `version` | string | Required, 1–64 chars |
| `company` | string | Optional, ≤ 200 chars |
| `website` | string | Optional, absolute `http`/`https` URL with a host, ≤ 2048 chars |
| `source` | string | Optional, same rules as `website` |
| `license` | string | Optional, ≤ 64 chars |
| `description` | string | Optional, ≤ 4096 chars |
| `resourceVersion`, `createdAt`, `updatedAt` | | As for Maintainer |

**Validation rules** (details the tables leave open):

- Every string field is trimmed of surrounding Unicode whitespace before it
  is validated and stored, so a whitespace-only required field is missing.
- Lengths count Unicode code points (Go's `utf8.RuneCountInString`), not
  bytes and not user-perceived characters: `é` written as `e` plus a
  combining accent counts as 2.
- An email address is checked syntactically (RFC 5322 via Go's `net/mail`):
  the input must parse to exactly itself, so display names, angle brackets,
  comments and quoted local parts are rejected. The domain is not looked up,
  so `a@localhost` is accepted.
- `maintainerId` is required on create and only checked for presence;
  whether it refers to an existing Maintainer is decided by the foreign key
  (§4.6). On update an empty or whitespace-only `maintainerId` counts as
  omitted; a given one must equal the stored value after trimming, which is
  checked with the constraint checks (§4.2 step 6). An update never writes
  `maintainer_id`, so omitting it cannot clear or change the stored value.
- Every invalid field is reported, in the order of the tables above.

### 3.2 Schema

```sql
CREATE TABLE maintainers (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    email            TEXT NOT NULL UNIQUE,      -- stored normalized (A5)
    resource_version INTEGER NOT NULL,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);

CREATE TABLE applications (
    id               TEXT PRIMARY KEY,
    maintainer_id    TEXT NOT NULL REFERENCES maintainers(id) ON DELETE RESTRICT,
    title            TEXT NOT NULL,
    version          TEXT NOT NULL,
    company          TEXT NOT NULL DEFAULT '',
    website          TEXT NOT NULL DEFAULT '',
    source           TEXT NOT NULL DEFAULT '',
    license          TEXT NOT NULL DEFAULT '',
    description      TEXT NOT NULL DEFAULT '',
    resource_version INTEGER NOT NULL,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL
);

-- Serves both FK checks on maintainer delete and the ?maintainerId= filter.
CREATE INDEX idx_applications_maintainer ON applications(maintainer_id);
```

The schema is created at startup (`CREATE TABLE IF NOT EXISTS`); there are no
migrations yet. Timestamps are stored as fixed-width UTC text with microsecond
precision (`2026-10-20T10:00:00.000000Z`), so ordering by the column is
chronological, and a created resource is returned with exactly the stored
values.

Validation happens in the application layer for good error messages. The
`UNIQUE` and `FOREIGN KEY` constraints remain the final authority, so a race
between two requests can never produce invalid data (see [§4.6](#46-mapping-database-errors)).

---

## 4. API Contract

### 4.1 Endpoints

All resource endpoints are under `/api/v1`. Bodies are JSON
(`Content-Type: application/json`).

| Method | Path | Success | Notes |
|---|---|---|---|
| `POST` | `/api/v1/maintainers` | `201` | `Location` and `ETag` headers |
| `GET` | `/api/v1/maintainers` | `200` | Paginated |
| `GET` | `/api/v1/maintainers/{id}` | `200` | `ETag` header |
| `PUT` | `/api/v1/maintainers/{id}` | `200` | Requires `If-Match`; returns new `ETag` |
| `DELETE` | `/api/v1/maintainers/{id}` | `204` | Requires `If-Match`; `409` if it owns Applications |
| `POST` | `/api/v1/applications` | `201` | `Location` and `ETag` headers |
| `GET` | `/api/v1/applications` | `200` | Paginated; optional `?maintainerId=` filter |
| `GET` | `/api/v1/applications/{id}` | `200` | `ETag` header |
| `PUT` | `/api/v1/applications/{id}` | `200` | Requires `If-Match`; `maintainerId` must be omitted or unchanged |
| `DELETE` | `/api/v1/applications/{id}` | `204` | Requires `If-Match` |
| `GET` | `/livez` | `200` | Liveness: the process is running and serving HTTP. No dependency checks |
| `GET` | `/readyz` | `200` | Readiness: `503` if the database is unreachable or the server is shutting down |

**Liveness vs readiness.** The probes follow the Kubernetes convention
(`/livez`, `/readyz`). A database outage makes the server *not ready*:
traffic should be routed elsewhere. It does not make it *not alive*: restarting
the process would not fix the database. Keeping the database check out of
`/livez` prevents a dependency outage from turning into a restart loop.
A successful probe returns `200` with the plain-text body `ok`. A failed
readiness check is an error response like any
other: Problem Details with code `not_ready`.

**Unmatched routes and methods.** Every error, including routing errors, uses
the Problem Details format (§4.3). An unknown path returns `404`. A known path
with an unsupported method returns `405` with an `Allow` header listing the
supported methods. The standard library's plain-text defaults are never
returned. A path that is not in clean form (e.g. `/a/../livez`) is redirected
with `307` to its clean form, as `net/http` does; that is not an error. The
request target `*` matches nothing and returns `404`, except `OPTIONS *`,
which `net/http` answers itself with an empty `200` before any handler runs
(not an error response, so no plain-text body is sent).

Request bodies contain **only writable fields**. Unknown fields, including
server-managed ones such as `id` or `resourceVersion`, are rejected with `400`
so that client mistakes surface immediately instead of being silently ignored.

**Request body rules.** Implemented by one decoding function that every POST
and PUT endpoint uses; the endpoints are added in later milestones.

- `Content-Type` must be `application/json`. Parameters such as
  `charset=utf-8` are ignored; the body is always parsed as UTF-8. A missing,
  malformed or different media type (including `application/problem+json`
  and `+json` suffixes) is `415`.
- The body is buffered up to 64 KiB (65536 bytes) before it is parsed, so any
  larger body is `413`, however malformed it is.
- An empty body, malformed JSON, a value of the wrong type, a non-object
  value, an unknown field, or anything but whitespace after the JSON value is
  `400`.
- A body that cannot be read completely (the connection broke, or the read
  deadline fired while the client was still sending) is `400`. The response
  is written even though the request context is cancelled (D9).
- Standard `encoding/json` behavior is kept, as an assumption: field names
  match case-insensitively (`{"NAME": ...}` sets `name`), for a duplicate key
  the last value wins, invalid UTF-8 is replaced with U+FFFD, and a `null`
  body decodes to empty fields, which are left to field validation.

### 4.2 Status codes

| Code | When |
|---|---|
| `200` | Successful GET / PUT |
| `201` | Resource created |
| `204` | Resource deleted |
| `400` | Malformed JSON, unknown field, trailing data, bad query parameter, malformed `If-Match` |
| `404` | Resource does not exist, or unknown path |
| `405` | Known path, unsupported method (`Allow` header lists supported methods) |
| `409` | Duplicate email; deleting a Maintainer that still owns Applications |
| `412` | `If-Match` does not match the current `resourceVersion` |
| `413` | Request body larger than 64 KiB |
| `415` | `Content-Type` is not `application/json` |
| `422` | Well-formed request that fails field validation, references a missing Maintainer, or tries to change `maintainerId` |
| `428` | `If-Match` missing on PUT or DELETE |
| `500` | Unexpected internal error (details are logged, never returned) |
| `503` | Request deadline exceeded, database temporarily busy, or `/readyz` not ready (shutting down or database unreachable) |

**400 vs 422.** `400` means the request could not be understood (syntax).
`422` means it was understood but is not acceptable (semantics).

**Evaluation order** is deterministic, so the same bad request always produces
the same error:

1. `415` / `413` / `400` — content type, size, syntax, query parameters
2. `428` / `400` — presence and format of `If-Match`
3. `422` — field validation (stateless)
4. `404` — resource lookup
5. `412` — version check
6. `409` / `422` — constraint checks (duplicate email, missing or in-use Maintainer, changed `maintainerId`)

### 4.3 Error format

Errors use [RFC 9457 Problem Details](https://www.rfc-editor.org/rfc/rfc9457)
with `Content-Type: application/problem+json`, plus a stable machine-readable
`code` and optional per-field errors:

```json
{
  "type": "about:blank",
  "title": "Unprocessable Content",
  "status": 422,
  "code": "validation_failed",
  "detail": "request body failed validation",
  "errors": [
    { "field": "email", "message": "must be a valid email address" }
  ]
}
```

`code` values are stable; clients may switch on them. `detail` is for humans
and may change.

| `code` | Status | Meaning |
|---|---|---|
| `invalid_body` | `400` | Body is empty, malformed, not an object, has a wrong-typed value, an unknown field or trailing data |
| `not_found` | `404` | No route matches the path (resource lookups add their own rows) |
| `method_not_allowed` | `405` | The path exists but not for this method; see `Allow` |
| `body_too_large` | `413` | Body exceeds 64 KiB |
| `unsupported_media_type` | `415` | `Content-Type` is not `application/json` |
| `internal_error` | `500` | Unexpected server error; details are only logged |
| `not_ready` | `503` | `/readyz`: shutting down or database unreachable |

Codes for the resource endpoints (validation, version checks, constraints) are
added to this table together with the endpoints.

### 4.4 Optimistic concurrency headers

| Operation | Behavior |
|---|---|
| `POST` | Response carries `ETag: "1"` so the client can update immediately |
| `GET` (single) | Response carries the current `ETag` |
| `PUT` | `If-Match` required; success returns the new `ETag` |
| `DELETE` | `If-Match` required |
| `GET` (list) | No `ETag`; each item contains its `resourceVersion` |

`ETag` is the `resourceVersion` as a strong entity tag, e.g. `"3"`.
`If-Match` must contain **exactly one strong entity tag holding a positive
integer**. Weak tags (`W/"3"`), `*`, and lists of tags are rejected with `400`.
This keeps the parsing rules small and unambiguous; the trade-off is that
generic HTTP clients relying on `If-Match: *` are not supported.

### 4.5 Pagination

- Query parameters: `limit` (default 20, maximum 100) and `offset` (default 0).
  Out-of-range or non-integer values return `400`; they are not silently clamped.
- Results are ordered by `(created_at, id)` so pages are stable.
- The server fetches `limit + 1` rows to compute `hasMore` without a `COUNT(*)`.

```json
{ "items": [ ... ], "limit": 20, "offset": 0, "hasMore": true }
```

### 4.6 Mapping database errors

Constraint violations are translated into domain errors **inside each store
method**, because the same SQLite error code means different things depending
on the operation:

| Operation | SQLite error | Domain error | HTTP |
|---|---|---|---|
| Create / update Maintainer | `SQLITE_CONSTRAINT_UNIQUE` on email | `ErrEmailTaken` | `409` |
| Create Application | `SQLITE_CONSTRAINT_FOREIGNKEY` | `ErrMaintainerNotFound` | `422` |
| Delete Maintainer | `SQLITE_CONSTRAINT_FOREIGNKEY` | `ErrMaintainerInUse` | `409` |
| Any | `SQLITE_BUSY`, `context.DeadlineExceeded` | `ErrUnavailable` | `503` |
| Any | `context.Canceled` (client disconnected or half-closed the connection) | returned as-is | an error response is still written, since the client may still be reading (D9); its status is defined with the resource endpoints. Logged as a client cancellation, not as a server error |
| Any | anything else | wrapped internal error | `500` |

Errors are classified by typed error code (`sqlite.Error.Code()`), never by
matching message strings. Internal details are logged and not returned to the client.

---

## 5. Storage

### 5.1 Choice

**SQLite** via `modernc.org/sqlite`, a pure-Go driver, so building requires no
C toolchain on any platform.

### 5.2 Configuration

| Setting | Value | Reason |
|---|---|---|
| `journal_mode` | `WAL` | Readers do not block the writer; crash-safe commits |
| `synchronous` | `FULL` | The WAL is fsynced on every commit (see §5.3) |
| `foreign_keys` | `ON` | Enforces A1–A3 in the database |
| `busy_timeout` | 3000 ms | Waits for a file lock instead of failing immediately; kept below the 5 s request deadline (§7) so lock contention surfaces as `SQLITE_BUSY` → `503` rather than racing the deadline |
| Max open connections | 1 | See D2 |
| Transaction mode | `_txlock=immediate` | Write transactions take the write lock at `BEGIN`, so a write cannot fail halfway with `SQLITE_BUSY_SNAPSHOT` |
| Database path | `DB_PATH` environment variable, default `metadata.db` in the working directory | Tests and CI use temporary files |

Pragmas are set through the DSN (`_pragma=...`), so they apply to every
connection the driver opens, not just the first one.

**Where waiting happens.** With one connection, concurrent requests inside this
process queue for the connection in `database/sql`'s pool, and that wait
honors the request context. `busy_timeout` only matters when *another process*
holds a lock on the same file (for example, a backup tool or a second server
instance started by mistake). That wait does not end early when the request
is cancelled or its deadline passes: the driver returns after `busy_timeout`,
and the error is then classified by the context (§4.6). This is why
`busy_timeout` stays below the request deadline.

### 5.3 Guarantees

- **Consistency.** Every write runs in a transaction; SQLite transactions are
  serializable. `UNIQUE` and `FOREIGN KEY` constraints hold even if
  application-level checks race.
- **Durability.** With `WAL` + `synchronous=FULL`, a committed transaction
  survives a process crash, an OS crash, or power loss, **provided the
  underlying storage honors fsync**. Data lives in a single file on local disk;
  there is no replication, so losing the disk loses the data.
- **Isolation of the conditional write.** The version check and the update are a
  single SQL statement (see [§6](#6-concurrency-control-highlight)), so no other
  write can interleave between "check" and "write".

### 5.4 Scalability

This design targets a **single instance**. Writes are serialized, which is
ample for a metadata service (writes are rare compared to reads) but does not
scale horizontally.

The growth path, in order:

1. **More read throughput:** a separate read-only connection pool. WAL allows
   concurrent readers alongside one writer.
2. **Multiple instances:** move to PostgreSQL. The concurrency mechanism carries
   over unchanged: `UPDATE ... WHERE id = $1 AND resource_version = $2` is
   portable SQL.
3. **Large collections:** replace offset pagination with keyset (cursor)
   pagination, similar to Kubernetes `continue` tokens, because `OFFSET` gets
   slower and can skip or repeat rows under concurrent inserts.

---

## 6. Concurrency Control (Highlight)

### 6.1 Problem: the lost update

Two clients read Application `X` at `resourceVersion` 3. Both edit it and send
PUT. Without concurrency control, the second write silently discards the
first; neither client is told.

### 6.2 Mechanism

Every resource carries a `resourceVersion`. Clients must send the version they
read in `If-Match`. The check and the write happen **atomically in one
statement**, inside one transaction:

```sql
BEGIN;
UPDATE applications
   SET title = ?, ..., resource_version = resource_version + 1, updated_at = ?
 WHERE id = ? AND resource_version = ?;
-- if 1 row affected: COMMIT -> 200 with the new ETag
-- if 0 rows affected, it could mean "missing" or "stale", so ask:
SELECT EXISTS (SELECT 1 FROM applications WHERE id = ?);
-- false -> 404 Not Found
-- true  -> 412 Precondition Failed
COMMIT;
```

`DELETE` follows the same pattern. Key points:

- There is **no read-then-write window**: a check followed by a separate,
  unconditional write would allow two writers to pass the check at once.
- The `EXISTS` query uses the **same transaction**, so the answer refers to the
  same database state as the failed update.
- A database error (constraint, busy, I/O) is handled as an error. It is never
  confused with "0 rows affected".

### 6.3 Why `If-Match` is required (D5)

Kubernetes treats `resourceVersion` as optional on update: without it the write is
unconditional. This server **requires** it (`428` if missing) for PUT and DELETE.
Every write is therefore safe by default, and no client can opt out by accident.
The cost is that clients must read before writing. For a metadata service,
where writes are rare and correctness matters, this trade-off is acceptable.

### 6.4 How it is verified

- **Unit tests:** stale version → 412, missing resource → 404, missing header → 428,
  malformed header → 400.
- **Concurrency test** (unit and E2E): N requests carrying the same version are
  sent concurrently. Assertions:
  1. exactly one returns `200`, the other N−1 return `412`;
  2. the final `resourceVersion` increased by exactly one;
  3. the stored data equals the body of the single successful request;
  4. a further write with the old version still returns `412`.
- CI runs unit tests with `-race`, and runs the E2E suite against a server
  binary **built with `-race`**, so data races inside the server process are
  detected too.

**Caveat.** With a single database connection, database access is serialized
within the process. The test proves the conditional update is correct for this
deployment. It does not, by itself, prove behavior with multiple connections or
multiple processes; the statement-level design is what carries over to those.

---

## 7. Resource Protection and Reliability

| Mechanism | Setting | Protects against |
|---|---|---|
| Request body limit | 64 KiB → `413` | Memory exhaustion from large bodies |
| Field length limits | §3.1 | Oversized records accepted within the body limit |
| Pagination cap | `limit` ≤ 100 | Unbounded list responses; queries use SQL `LIMIT`, not in-memory slicing |
| Strict JSON decoding | Unknown fields and trailing data → `400` | Silent client mistakes |
| Server timeouts | ReadHeader 5 s, Read 10 s, Write 15 s, Idle 60 s | Slow clients holding connections (e.g. Slowloris) |
| Request deadline | 5 s per request, propagated via `context.Context` → `503` | Requests running unboundedly |
| Graceful shutdown | On SIGINT/SIGTERM: `/readyz` returns `503`, stop accepting, drain in-flight requests (up to 10 s), then close the database | Dropped requests and an unclean database close during deploys |

The listen address comes from the `ADDR` environment variable. It defaults to
`127.0.0.1:8080`: the server has no authentication, so it listens only on
loopback unless explicitly told otherwise (e.g. `ADDR=:8080` inside a container,
where the published port decides who can reach it).

**Shutdown order.** On the first SIGINT/SIGTERM, readiness is switched to `503`,
then the listener is closed and in-flight requests are drained for up to 10 s.
The database is closed only after that. If the drain finishes in time, no
in-flight request loses its database.

- There is no delay between switching readiness and closing the listener, so a
  load balancer polling `/readyz` may not see the `503` before connections are
  refused. A pre-stop delay (e.g. a Kubernetes `preStop` hook) covers that.
- If the drain does not finish in time, the remaining connections are closed,
  the database is closed, and the server exits with status 1. Closing the
  database makes new database calls fail, but a handler that already holds the
  connection keeps running until the process exits. It may still commit in
  that time, even though its client already saw the connection close; a
  transaction that has not committed when the process exits is never applied,
  because SQLite ignores uncommitted WAL content when the database is next
  opened.
- If the server stops accepting connections on its own (a listener failure),
  the open connections are closed before the database, and the server exits
  with status 1.

The request deadline works only because every storage call takes a
`context.Context` and uses the `*Context` database methods. A deadline set by
middleware is useless if the database call ignores it. `http.TimeoutHandler` is
not used: it cannot stop running handler code, and its timeout response would
bypass the Problem Details format.

Cancellation is **cooperative**: the deadline stops work at the next point that
checks the context (waiting for the connection, executing a statement). It
does not forcibly interrupt arbitrary code. The context is the total time
budget for the request; no lower layer may extend it.

---

## 8. Testing Strategy

| Layer | Scope | Runs |
|---|---|---|
| Unit — validation | Field rules, boundary lengths, email and URL edge cases (table-driven) | `go test ./...` |
| Unit — store | CRUD against a temporary SQLite file, constraints, version checks, error mapping, **persistence across close/reopen** | `go test ./...` |
| Unit — HTTP | Handlers via `httptest`: every status code in §4.2, headers, error bodies | `go test ./...` |
| E2E | Real HTTP against the compiled server: CRUD for both resources, relationships, error contract, ETag lifecycle, concurrency | `go test -tags=e2e ./e2e/...` |

E2E tests are excluded from `go test ./...` by a build tag, so the default
command needs no running server. They read the server address from `BASE_URL`.

**Test isolation.** Each E2E run uses a fresh temporary database. Each test
creates its own data with random emails and does not depend on pre-existing
data or on test order.

**CI pipeline** (GitHub Actions):

1. `gofmt` check, `go vet`, build
2. Unit tests with `-race` and a coverage report in the job summary
3. Build the server with `-race`, start it on a temporary database, and poll
   `/readyz` until ready
4. Run the E2E suite
5. Send SIGTERM and verify the server exits cleanly; upload the server log on failure

---

## 9. Decision Log

Superseded decisions are kept and marked, not deleted.

### D1. Go with the standard library HTTP server

- **Status:** Accepted (2026-10-20)
- **Alternatives:** Go with Gin or Echo
- **Why:** Go matches the Kubernetes ecosystem the service would live in. Since
  Go 1.22, `net/http`'s `ServeMux` supports method-based routing and path
  parameters (`GET /api/v1/maintainers/{id}`), which covers everything this
  API needs without a framework dependency.
- **Trade-off:** Middleware and request decoding are written by hand (a small
  amount of code).

### D2. SQLite with a single connection

- **Status:** Accepted (2026-10-20)
- **Alternatives:** in-memory map (with or without snapshotting), BoltDB, PostgreSQL
- **Why:** ACID transactions, `UNIQUE` and `FOREIGN KEY` constraints, and real
  durability, with zero operational setup locally and in CI. An in-memory store
  would require reimplementing constraints and persistence by hand. PostgreSQL
  would add a service to run in every environment without benefiting a
  single-instance deployment.
  A single connection serializes database access within the process, which
  simplifies transaction behavior and avoids lock contention between
  connections, at the cost of parallelism.
- **Trade-off:** No write parallelism; single instance only.
- **Revisit when:** more than one instance is needed (→ PostgreSQL, §5.4), or
  read latency suffers under load (→ separate read pool).

### D3. One-to-many relationship, creator is immutable

- **Status:** Accepted (2026-10-20)
- **Alternatives:** many-to-many (Application has a list of maintainers);
  Applications nested under Maintainers (`/maintainers/{id}/applications`)
- **Why:** The requirement describes Maintainers as *the creator* of an
  Application, which is a single owner. Many-to-many would add a join table, and
  rules about list mutation and orphaning, for a need the requirement does not
  state. Keeping both as top-level resources keeps URLs stable and lets
  Applications be listed across Maintainers.
- **Trade-off:** Co-maintainers cannot be expressed.
- **Revisit when:** shared ownership is required; then add a separate
  `maintainers` list and keep `creatorId` immutable.

### D4. Optimistic concurrency instead of last-write-wins

- **Status:** Accepted (2026-10-20)
- **Alternatives:** last-write-wins; pessimistic locking (lock or lease resources)
- **Why:** Last-write-wins silently loses data. Pessimistic locks do not fit a
  stateless HTTP API (lock lifetimes, abandoned locks). Optimistic concurrency
  costs nothing when there are no conflicts and reports conflicts explicitly.
  It also follows the Kubernetes `resourceVersion` model.
- **Trade-off:** Clients must handle `412` by re-reading and retrying.

### D5. `If-Match` is required on PUT and DELETE

- **Status:** Accepted (2026-10-20)
- **Alternatives:** optional `If-Match` (unconditional write when absent, as in Kubernetes)
- **Why:** Safe by default; see §6.3.
- **Trade-off:** Every write needs a prior read; `curl` examples are slightly longer.

### D6. Bounded queries with offset pagination

- **Status:** Accepted (2026-10-20)
- **Alternatives:** unbounded lists; cursor (keyset) pagination
- **Why:** Unbounded lists let one request consume memory proportional to the
  table size. Offset pagination is the simplest bounded option, and is adequate
  at this scale.
- **Trade-off:** Rows can be skipped or repeated if data changes between page
  requests; deep offsets are slower.
- **Revisit when:** collections grow large or clients need consistent
  iteration (→ keyset pagination, §5.4).

### D7. Modular monolith

- **Status:** Accepted (2026-10-20)
- **Alternatives:** separate services per resource
- **Why:** The two resources share a transactional boundary (the foreign key).
  Splitting them would turn a database constraint into a distributed
  consistency problem. Packages (`api`, `store`, `validation`) give the
  separation without the network.
- **Trade-off:** Resources cannot be deployed or scaled independently, which is not needed here.

### D8. Classify constraint errors per operation

- **Status:** Accepted (2026-10-20)
- **Alternatives:** map SQLite error codes to HTTP status globally; rely on
  pre-checks only
- **Why:** The same error code means different things: a foreign key violation
  is a `422` when creating an Application and a `409` when deleting a
  Maintainer. Pre-checks alone are racy; the constraint is the final authority.
  See §4.6.
- **Trade-off:** Each store method owns its error mapping (a little repetition).

### D9. Always write a response, even when the request context is cancelled

- **Status:** Accepted (2026-10-20). Supersedes the "none: the client is gone"
  rule for `context.Canceled` in the first version of §4.6.
- **Alternatives:** write nothing when the request context is cancelled; a
  new `408 Request Timeout` for incomplete bodies
- **Why:** A cancelled request context does not mean the client is gone.
  `net/http` also cancels it when the read deadline fires or the client
  half-closes the connection, and the client can still read the response. A
  handler that writes nothing then sends `net/http`'s implicit `200` with an
  empty body: a failed request looks successful, and `/readyz` looks ready.
  This was found in review and is reproduced by a test that sends part of a
  declared body and then either waits for the read deadline or half-closes
  the connection. An incomplete body is the
  client's fault, so the existing `400 invalid_body` is used instead of a new
  status code.
- **Trade-off:** A response is sometimes written to a client that is gone;
  this costs nothing.

---

## 10. Limitations and Future Work

| Area | Current state | Next step |
|---|---|---|
| Authentication | None; assumes a trusted network | Bearer token authentication, then per-Maintainer ownership checks on writes |
| POST retries | A retried create can duplicate an Application | `Idempotency-Key` header with stored responses |
| Rate limiting | None | Token bucket per client at the gateway, or shared (e.g. Redis-backed) if done in-process across instances |
| Scale-out | Single instance | PostgreSQL, keyset pagination (§5.4) |
| Overload | Per-request limits only; the number of concurrent connections and requests is not capped, so a flood of requests can still exhaust memory or file descriptors | Cap in-flight requests (load shedding with `503`), and rate limit at the gateway |
| Observability | Logs only | Metrics (request rate, latency, `412` rate as a contention signal) |
