# Roadmap

Exit gate for every milestone: `make check` green in CI, plus the listed test.

Progress: M0–M6 done. M1 shipped without sqlc, which landed in M2 with the first
store methods. M6 was the only open-ended milestone; M7 (ship) is next.

| # | Milestone | Exit criteria | Size |
|---|---|---|---|
| M0 | Scaffold + guardrails | Healthz/readyz endpoints, all guardrail files, CI required on main | 1 evening |
| M1 | Schema + tenancy | Migrations, sqlc, RLS, cross-tenant isolation test | 1 weekend |
| M2 | Auth + CRUD | Login, CSRF, reset flow, services/staff CRUD | 1 weekend |
| M3 | Slot engine (pure domain) | Availability, time off, buffers; table, fuzz and DST tests | 1 weekend |
| M4 | Booking + concurrency | Exclusion constraint, idempotency, 100-goroutine race test, exactly 1 winner | 1 weekend |
| M5 | Jobs + reminders | SKIP LOCKED queue, retries, kill-mid-job test, graceful shutdown | 1 weekend |
| M6 | UI | Public page + dashboard (templ/HTMX), ICS export | 1-2 weekends |
| M7 | Ship | Dockerfile, live URL, structured logs, backups, README with diagram + load-test numbers | 1 weekend |

## Notes
- M3 and M4 carry the real risk (time zones, concurrency), so the tests are the deliverable.
- M5: the confirmation and reminder jobs are inserted inside the booking's transaction, so
  a booking committed through this write path always has its job (rows written before 0006
  are out of scope and get no backfill); never move the enqueue out of that transaction.
  Scheduling fields (`run_at`, `locked_until`) are written from the injected clock and
  compared to a claim-time parameter, never to the database's `now()`, which stays the
  source for audit columns only. The claim is `job_claim`, a `SECURITY DEFINER` function —
  the one cross-tenant path the app role has, bounded to the queue columns; direct reads
  of `jobs` stay tenant-scoped. A lease lasts two minutes, `attempts` counts claims,
  failures back off 30s → 30m doubling, and a job dead-letters after eight attempts; a
  graceful shutdown releases in-flight work and refunds its attempt. A booking made inside
  the 24h window gets no reminder job, only the confirmation, because both messages carry
  the same cancel link. Delivery is at-least-once. The worker is `cmd/worker`, a separate
  process; M7's image runs both binaries.
- M4 must store `bookings.ends_at` as the occupied end with the service buffer included
  (`starts_at + duration + buffer`). The exclusion constraint then enforces the buffer, and
  the M3 slot engine treats stored busy intervals as blocked spans without padding them.
  A booking writer that stores the appointment end instead would let the engine offer a
  start inside the previous appointment's buffer, and the constraint alone would not catch
  it.
- M6: the web layer is templ, in internal/adapters/web/views, with htmx 2.0.4 vendored in
  internal/adapters/web/static and served from a fixed name allowlist. `make generate` runs templ
  and then gofumpt, because templ's generated files are not gofumpt-clean and CI diffs the worktree
  after `make check`. Public pages show a staff member's display name and never an address.
- M6 audit: the append-only trail is `audit_log` (0007). Append-only is a privilege, not a
  convention: the migration envelope grants every table UPDATE and DELETE, and this one gives them
  back, so the app role holds SELECT and INSERT only. A row is written by the store method whose
  change it records, inside that change's own transaction, so the trail cannot name a change that
  did not happen. It records the actor (NULL for the public booking flow, which has no login) and
  exactly one subject through composite keys, and no before/after snapshot: it answers who changed
  what, when, not which field moved.
- M6 calendar: one tenant-scoped range query per window with the service and staff names joined in,
  weeks running Monday to Monday in the tenant's timezone through `domain.DayStart`, so a window
  holding a DST transition is 167 or 169 hours and still reads as seven local days. The dashboard
  mints no cancel link and offers no owner-side cancel: the only cancel credential stays the signed
  link in the customer's email, and a page that minted tokens would put a working one in the
  owner's HTML and history.
- M6 ICS: hand-written RFC 5545, no new dependency. Instants travel as UTC `Z` with no VTIMEZONE,
  because a hand-written timezone table rots when a government changes its DST rules; the span is
  the occupied `starts_at`..`ends_at` the database stores, so the customer's calendar blocks the
  same interval the staff member's does; the `UID` is the booking id at the public host, so a second
  download is the same event; the file carries no cancel link, because a bearer token must not be
  copied into a calendar store the user syncs elsewhere. A cancelled booking's download is 410.
- M6 cut, still open: reschedule and the availability/time-off UI. The slot engine's weekly rules
  and time off have no HTTP surface, so a tenant's opening hours are still set with SQL. This is
  the roadmap's own cut order (ICS, then reschedule, then staff time off) with ICS kept.
- M6 is the only open-ended milestone.
- Cut order if short on time: ICS export, then reschedule, then staff time off.
- Never cut M1 or M4.
- Tool directives: goose in M1; templ in M6; sqlc with the first store methods (M2).
- M2/M6: `GET /b/{slug}` needs a slug to tenant-id lookup, which the `tenants` policy
  deliberately does not allow (with no tenant set it matches no rows). Add a narrowly scoped
  `SECURITY DEFINER` resolver with a pinned `search_path` that returns only id/name/timezone
  — do not widen the policy and do not connect with `BYPASSRLS`, both of which would silently
  remove the isolation that M1 exists to establish.
