# Rules
- Work is done only when `make check` passes. Fix, don't suppress.
- Never edit .golangci.yml, lefthook.yml, .github/, internal/arch/. Ask instead.
- No //nolint without a specific linter and a reason.
- Never modify an existing migration. Add a new one.
- Never hand-edit generated code (sqlc, templ). Edit queries/ or .templ, run `make generate`.
- Dependencies point inward: domain <- app <- adapters. domain imports stdlib only.
- Wrap errors: fmt.Errorf("doing X: %w", err). No panics outside main.
- ctx first param. No context.Background() or time.Now() outside main/clock. Inject both.
- Every query runs inside the tenant-scoped transaction helper. No raw pool access in handlers.
  Booking writes take the staff row lock (SELECT ... FOR UPDATE) before inserting, so contention
  queues instead of deadlocking; use WithTenantRetry for other transient aborts.
- Referential integrity bypasses row level security, so tenant-owned tables reference their
  parents with composite keys: bookings (tenant_id, staff_id) -> staff (tenant_id, id). A
  policy alone does not stop one tenant pointing at another tenant's rows.
- No float64 for money. Store timestamptz. Compute in tenant timezone.
- No new dependencies without asking. Prefer stdlib.
- Every behavior change gets a test that fails without the change.
  Concurrency and tenancy changes need an integration test.
- Small diffs, one concern each. Don't refactor unrelated code.

# Final review
Before declaring work done, spawn a separate agent with fresh context to review the change
adversarially. Give it the diff and the requirement — not your reasoning — and ask it to
falsify the work: edge cases (empty, boundary, duplicate, concurrent), ordering and retries,
swallowed errors, tenancy and permission boundaries, and tests that assert the
implementation instead of the requirement. Fix or answer every finding; your own "looks
good" is not a review.

# Layout
cmd/web wiring | domain pure logic | app use cases | adapters/{postgres,web,worker} I/O

# Commands
make check | make check-all (every failure in one pass) | make test | make test-int | make generate
Run make check (or make lint) yourself: the git hooks skip when nothing is staged,
so a quiet hook is not evidence.
