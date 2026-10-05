# Slotwise

Multi-tenant booking app for small service businesses (barbers, physios, tutors).
Customers book from a public page. Owners and staff manage services, hours and bookings.

## Roles
- owner, staff: log in, manage the tenant
- customer: no account, books with name + email

## v1 scope
- Tenants with a slug; public page at /b/{slug}
- Services: name, duration, buffer, display price
- Staff: weekly availability rules, time off
- Slot search: service + date range returns bookable slots in the tenant's timezone
- Create, cancel, reschedule booking (idempotent)
- Email confirmation and 24h reminder via a Sender interface (log sender in dev)
- Owner dashboard: day/week calendar, booking list, audit log
- ICS download for customers

## Non-goals (v1)
Payments, calendar sync, SMS, multi-location, mobile app.

## Hard requirements
1. **No double booking.** Enforced by Postgres, not app code.
   - Exclusion constraint on (staff_id, tstzrange(starts_at, ends_at)) WHERE status='confirmed'
   - SQLSTATE 23P01 maps to domain ErrSlotTaken
   - Test: 100 concurrent requests for one slot, exactly 1 succeeds
2. **Tenant isolation via Row-Level Security.**
   - Every table has tenant_id; FORCE ROW LEVEL SECURITY
   - App connects as a non-owner role; each request tx runs
     set_config('app.tenant_id', $1, true)
   - Test: tenant A cannot read or write tenant B
3. **Time correctness.** timestamptz storage, slots computed in tenant IANA timezone,
   explicit DST-transition tests.
4. **Idempotent writes.** UNIQUE (tenant_id, idempotency_key); same key returns the same booking.
5. **Real auth.** scs sessions, argon2id, CSRF via http.CrossOriginProtection (Go 1.25+),
   single-use expiring reset tokens, login rate limiting.
6. **Reliable reminders.** Postgres job queue (FOR UPDATE SKIP LOCKED), retries with
   backoff, graceful shutdown. Test: kill mid-job, job is retried.

## Core schema (bookings)
```sql
CREATE EXTENSION IF NOT EXISTS btree_gist;
CREATE TABLE bookings (
  id              uuid PRIMARY KEY,
  tenant_id       uuid NOT NULL REFERENCES tenants(id),
  staff_id        uuid NOT NULL REFERENCES staff(id),
  service_id      uuid NOT NULL REFERENCES services(id),
  customer_name   text NOT NULL,
  customer_email  text NOT NULL,
  starts_at       timestamptz NOT NULL,
  ends_at         timestamptz NOT NULL,
  status          text NOT NULL CHECK (status IN ('confirmed','cancelled')),
  idempotency_key text NOT NULL,
  CHECK (ends_at > starts_at),
  UNIQUE (tenant_id, idempotency_key),
  EXCLUDE USING gist (staff_id WITH =, tstzrange(starts_at, ends_at) WITH &&)
    WHERE (status = 'confirmed')
);
```

## Stack
net/http (1.22+ router), Postgres + pgx, sqlc, goose, templ + HTMX, slog, scs,
testcontainers-go.

## Routes
```
GET  /b/{slug}
GET  /b/{slug}/slots?service=&from=&to=
POST /b/{slug}/bookings              Idempotency-Key header
POST /b/{slug}/bookings/{id}/cancel  signed link
GET  /app/...                        session-protected dashboard
GET  /healthz   /readyz
```

## Architecture
domain (pure) <- app (use cases, defines store interfaces) <- adapters (postgres, web, worker).
Dependencies point inward. time.Now only in internal/clock.
