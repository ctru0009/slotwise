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

-- Cancelling is status-blind, so a repeat cancel still reports one row.
-- name: CancelBooking :execrows
UPDATE bookings SET status = 'cancelled' WHERE id = @id;

-- The write path locks the staff row before inserting, so concurrent bookings
-- for one staff member queue instead of deadlocking on the exclusion
-- constraint's index. A deactivated staff member is not lockable: a booking
-- would otherwise be stored for somebody who no longer takes appointments.
-- name: LockActiveStaff :one
SELECT 1 AS visible FROM staff WHERE id = @id AND active FOR UPDATE;
