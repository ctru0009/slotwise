-- name: InsertBooking :one
INSERT INTO bookings (tenant_id, staff_id, service_id, customer_name, customer_email,
                      starts_at, ends_at, status, idempotency_key)
VALUES (@tenant_id, @staff_id, @service_id, @customer_name, @customer_email,
        @starts_at, @ends_at, 'confirmed', @idempotency_key)
ON CONFLICT (tenant_id, idempotency_key) DO NOTHING
RETURNING id, tenant_id, staff_id, service_id, customer_name, customer_email,
          starts_at, ends_at, status, idempotency_key, created_at;

-- name: BookingByIdempotencyKey :one
SELECT id, tenant_id, staff_id, service_id, customer_name, customer_email,
       starts_at, ends_at, status, idempotency_key, created_at
  FROM bookings
 WHERE tenant_id = @tenant_id AND idempotency_key = @idempotency_key;

-- name: BookingByID :one
SELECT id, tenant_id, staff_id, service_id, customer_name, customer_email,
       starts_at, ends_at, status, idempotency_key, created_at
  FROM bookings
 WHERE id = @id;

-- name: BookingMessage :one
SELECT b.id, b.tenant_id, b.staff_id, b.service_id, b.customer_name, b.customer_email,
       b.starts_at, b.ends_at, b.status, b.created_at,
       t.slug, t.name AS tenant_name, t.timezone,
       s.name AS service_name, st.name AS staff_name
  FROM bookings b
  JOIN tenants t ON t.id = b.tenant_id
  JOIN services s ON s.tenant_id = b.tenant_id AND s.id = b.service_id
  JOIN staff st ON st.tenant_id = b.tenant_id AND st.id = b.staff_id
 WHERE b.id = @id;

-- Cancelling is status-blind, so a repeat cancel still reports one row.
-- name: CancelBooking :execrows
UPDATE bookings SET status = 'cancelled' WHERE id = @id;

-- The write path locks the staff row before inserting, so concurrent bookings
-- for one staff member queue instead of deadlocking on the exclusion
-- constraint's index. A deactivated staff member is not lockable: a booking
-- would otherwise be stored for somebody who no longer takes appointments.
-- name: LockActiveStaff :one
SELECT 1 AS visible FROM staff WHERE id = @id AND active FOR UPDATE;

-- The calendar and the booking list are one query per rendered range: the
-- interval test is half-open and uses the stored occupied end, so a booking
-- that starts before the range and still occupies it is included. Both names
-- come from the join, so a fifty-booking week costs one round trip, not fifty.
-- name: ListBookingsInRange :many
SELECT b.id, b.staff_id, b.service_id, b.customer_name, b.starts_at, b.ends_at, b.status,
       s.name AS service_name, st.name AS staff_name
  FROM bookings b
  JOIN services s ON s.tenant_id = b.tenant_id AND s.id = b.service_id
  JOIN staff st ON st.tenant_id = b.tenant_id AND st.id = b.staff_id
 WHERE b.starts_at < @range_end AND b.ends_at > @range_start
 ORDER BY b.starts_at, b.id;
