# Roadmap

Exit gate for every milestone: `make check` green in CI, plus the listed test.

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
- M6 is the only open-ended milestone.
- Cut order if short on time: ICS export, then reschedule, then staff time off.
- Never cut M1 or M4.
- sqlc, goose and templ tool directives are added in M1, not M0.
