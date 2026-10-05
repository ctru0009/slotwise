-- name: TenantByID :one
SELECT id, slug, name, timezone
  FROM tenants
 WHERE id = @id;

-- name: ServiceForSlotSearch :one
SELECT id, tenant_id, name, duration_minutes, buffer_minutes, price_cents, active
  FROM services
 WHERE id = @id AND active;

-- name: ActiveStaffForSlotSearch :many
SELECT id, tenant_id, name, email, active
  FROM staff
 WHERE active
 ORDER BY name, id;

-- name: RulesForSlotSearch :many
SELECT staff_id, weekday, start_minute, end_minute
  FROM availability_rules
 ORDER BY staff_id, weekday, start_minute, end_minute;

-- Overlap is half-open, so a row that only touches the range edge does not count.
-- name: TimeOffForSlotSearch :many
SELECT id, staff_id, starts_at, ends_at
  FROM time_off
 WHERE starts_at < @range_end AND ends_at > @range_start
 ORDER BY staff_id, starts_at, id;

-- name: BusyForSlotSearch :many
SELECT staff_id, starts_at, ends_at
  FROM bookings
 WHERE status = 'confirmed'
   AND starts_at < @range_end
   AND ends_at > @range_start
 ORDER BY staff_id, starts_at, id;

-- name: ListWeeklyRulesForStaff :many
SELECT weekday, start_minute, end_minute
  FROM availability_rules
 WHERE staff_id = @staff_id
 ORDER BY weekday, start_minute, end_minute;

-- name: DeleteWeeklyRulesForStaff :exec
DELETE FROM availability_rules
 WHERE staff_id = @staff_id;

-- name: InsertWeeklyRule :exec
INSERT INTO availability_rules (tenant_id, staff_id, weekday, start_minute, end_minute)
VALUES (@tenant_id, @staff_id, @weekday, @start_minute, @end_minute);

-- name: StaffVisible :one
SELECT 1 AS visible FROM staff WHERE id = @id;

-- name: LockStaff :one
SELECT 1 AS visible FROM staff WHERE id = @id FOR UPDATE;

-- name: ListTimeOffForStaff :many
SELECT id, staff_id, starts_at, ends_at
  FROM time_off
 WHERE staff_id = @staff_id
 ORDER BY starts_at, id;

-- name: InsertTimeOff :one
INSERT INTO time_off (tenant_id, staff_id, starts_at, ends_at)
VALUES (@tenant_id, @staff_id, @starts_at, @ends_at)
RETURNING id;

-- name: DeleteTimeOffForStaff :execrows
DELETE FROM time_off
 WHERE staff_id = @staff_id AND id = @id;
