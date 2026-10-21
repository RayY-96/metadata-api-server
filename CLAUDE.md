# CLAUDE.md

Instructions for AI coding agents working in this repository.

## Source of truth

[docs/DESIGN.md](docs/DESIGN.md) defines the scope, API contract, status codes,
storage configuration and decisions. Do not restate it here; follow it.

- Do not add features, endpoints, request or response fields, or status codes
  that are not in DESIGN.md. Its Non-goals are out of scope.
- If a detail is ambiguous, choose the simplest behavior consistent with
  DESIGN.md and record the assumption there. **Stop and ask** before any change
  to the public API, the data model, or the consistency guarantees.
- If an implementation needs to deviate from DESIGN.md, explain the conflict
  first. Then update DESIGN.md in the same commit as the code: edit the
  affected section, add or supersede an entry in the Decision Log (never delete
  old entries), and explain it in the commit body as `Design change (Dn): ... Why: ...`.

## Layout

```
cmd/server/          main: configuration, wiring, graceful shutdown
internal/api/        HTTP handlers, middleware, problem+json errors
internal/model/      resource types
internal/store/      SQLite access and domain errors
internal/validation/ field validation
e2e/                 end-to-end tests (build tag: e2e)
```

Keep the layout flat. Handlers deal with HTTP, validation with field rules,
the store with persistence and domain errors. No SQL in handlers.

## Go code

- Idiomatic, simple, explicit Go. Prefer removing complexity over adding abstractions.
- No interfaces, generic repositories, DI frameworks or extra layers unless a
  concrete need (usually a test) requires one.
- Handle every error; never discard one silently. Wrap with
  `fmt.Errorf("...: %w", err)` and compare with `errors.Is` / `errors.As`.
  Exception: the deferred `tx.Rollback()` after a successful `Commit` returns
  `sql.ErrTxDone` by design; ignoring it there is correct.
- No `panic` or `log.Fatal` for request-time failures.
- Typed request and response structs; no `map[string]any` for resources.
  Request structs contain only writable fields, so clients cannot set
  server-managed fields.
- No mutable package-level application state. Sentinel errors
  (`var ErrNotFound = errors.New(...)`) and constants are fine. Any shared
  mutable state must be justified and synchronized.
- Standard library first. Do not add a dependency without a clear need.

## HTTP

- Every error response uses the Problem Details format in DESIGN.md §4.3,
  including unmatched routes (404) and unsupported methods (405), which must
  not fall through to the standard library's plain-text defaults (DESIGN.md §4.1).
  Never expose SQL, stack traces or internal error messages.
- If a standard-library default conflicts with DESIGN.md, report the conflict
  instead of silently picking one side.
- Status codes, headers, error bodies and validation behave the same for both resources.
- Write the status and headers exactly once per response.
- POST, GET-by-ID and PUT responses carry an `ETag`. `If-Match` parsing follows DESIGN.md §4.4.
- Pagination limits and ordering are enforced in SQL.

## Database

- **Context everywhere.** Every store method that touches the database takes
  `ctx context.Context` first and uses `QueryContext` / `QueryRowContext` /
  `ExecContext` / `BeginTx`. Lifecycle methods such as `Close()` keep their
  conventional signatures. Do not turn cancellation errors into generic
  internal errors.
- **Transactions.** `defer tx.Rollback()` right after a successful `BeginTx`;
  check the error from `Commit`. Inside a transaction use only `tx.*`, never
  `db.*`: the pool has one connection, so that deadlocks.
- **Rows.** `defer rows.Close()` and check `rows.Err()` after iterating.
- **Optimistic concurrency.** The version check is part of the `UPDATE` /
  `DELETE` statement (`WHERE id = ? AND resource_version = ?`), never a read
  followed by an unconditional write. Check the execution error before
  `RowsAffected`. On 0 rows, distinguish 404 from 412 in the same transaction
  (DESIGN.md §6.2).
- **Constraints.** `UNIQUE` and `FOREIGN KEY` are the final authority. Store
  methods translate SQLite errors into domain errors per operation, using typed
  error codes, never message strings (DESIGN.md §4.6).
- Parameterized SQL only.

## Tests

- New behavior ships with its tests in the same commit.
- Cover success, validation failures, missing resources, conflicts and boundaries.
  Test observable behavior, not implementation details. Prefer table-driven tests.
- Store tests run against real SQLite files in `t.TempDir()`. No mocked store
  as a substitute for real constraints and conditional SQL.
- No dependence on test order, existing data, sleeps or timing. E2E tests
  create their own resources with random emails.
- Concurrency tests assert the response codes **and** the final persisted state.
- E2E tests (`e2e/`, build tag `e2e`) go through the running server over HTTP,
  never call handlers directly.
- **Never weaken, skip or delete a failing test to get green.** Fix the code.
  If the test itself is wrong, say so and explain the fix.

## Repository hygiene

This repository is public.

- Never commit secrets, tokens, database files, local configuration, or code or
  documents from any employer.
- Never log request bodies or email addresses.
- Do not describe the API as safe for public deployment: it has no authentication.
- Do not create files outside the agreed scope (speculative examples, benchmarks, extra docs).
- Do not claim a feature (limit, timeout, guarantee) unless it is enforced in
  code and covered by a test.

## Workflow

Work one milestone (one commit) at a time. There are two gates:

- **Local gate** (steps 1–7): decides whether the change may be committed.
- **Remote gate** (step 8): decides whether the pushed commit is verified in CI.

1. **Before:** read the relevant DESIGN.md sections and the existing code; name
   the design decisions involved and the main correctness risks.
2. **Implement** the milestone and its tests. Never build ahead and split the
   finished result into commits afterwards.
3. **Check** (all must pass):
   ```
   gofmt -l .        # must print nothing
   go vet ./...
   go build ./...
   go test ./...
   ```
4. **Review the diff** for correctness, scope creep, unintended changes,
   security issues and unnecessary complexity.
5. **Independent review gate.** Run the `code-reviewer` subagent
   (`.claude/agents/code-reviewer.md`) on the uncommitted change, stating the
   milestone and its acceptance criteria. A `BLOCK`
   verdict must be resolved (fixed, or shown to be a false positive with
   evidence) and the reviewer re-run before asking for approval.
6. **Report** using the format below, including the reviewer's verdict and how
   each finding was handled. Do not hide failures.
7. **Commit only after the human explicitly approves.** Conventional Commits
   (`feat:`, `fix:`, `test:`, `docs:`, `ci:`, `chore:`), one coherent working
   change per commit.
8. **Push and verify CI** (once the repository has CI). Find the run for the
   pushed commit (`gh run list --commit <sha>`) and confirm its conclusion and
   job results. A green run for an earlier commit proves nothing about this
   one. If CI fails, fix it through the local gate again and commit the fix as
   a new commit; never rewrite the pushed history.

### Milestone report

1. Files changed and the responsibility of each.
2. Key implementation decisions, with the DESIGN.md sections they follow.
3. Go concepts introduced in this milestone, explained briefly (the human
   reviewer is new to Go).
4. Checks and tests run, with their actual results.
5. Reviewer gate verdict, and for each finding: fixed, or why it was rejected.
6. Deviations from DESIGN.md, limitations, and open risks.
7. Proposed commit message.

Do not amend, squash, reset, force-push or otherwise rewrite commits unless
explicitly asked.

## Definition of done

A milestone is done when it matches DESIGN.md, passes all checks, covers its
error and boundary cases, has a reviewed diff, has updated docs if observable
behavior or a decision changed, and is recorded as a commit.

The project is done when every assignment deliverable is met: a successful
GitHub Actions E2E run, reported unit-test coverage, and a README that links to
that run. **CI counts as passing only after verifying the actual remote run**,
never based on local tests or the presence of a workflow file.
