# Roadmap

Exit gate for every milestone: `make check` green in CI, plus the listed test.

Progress: M0–M5 done. M1 shipped without sqlc, which landed in M2 with the first
store methods. M6 (UI) next.

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
- M6 is the only open-ended milestone.
- Cut order if short on time: ICS export, then reschedule, then staff time off.
- Never cut M1 or M4.
- Tool directives: goose in M1; templ in M6; sqlc with the first store methods (M2).
- M2/M6: `GET /b/{slug}` needs a slug to tenant-id lookup, which the `tenants` policy
  deliberately does not allow (with no tenant set it matches no rows). Add a narrowly scoped
  `SECURITY DEFINER` resolver with a pinned `search_path` that returns only id/name/timezone
  — do not widen the policy and do not connect with `BYPASSRLS`, both of which would silently
  remove the isolation that M1 exists to establish.
