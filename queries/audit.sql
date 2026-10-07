-- name: InsertAuditLog :exec
INSERT INTO audit_log (tenant_id, actor_user_id, action, booking_id, service_id, staff_id)
VALUES (@tenant_id, @actor_user_id, @action, @booking_id, @service_id, @staff_id);

-- The dashboard's audit list: one row per entry with the labels the page shows,
-- so no entry costs a second query. Every join is tenant-composite, and row
-- level security scopes the whole statement to the caller's tenant.
-- name: ListAuditLog :many
SELECT a.id, a.action, a.created_at, u.email AS actor_email,
       b.customer_name AS booking_customer, b.starts_at AS booking_starts_at,
       s.name AS service_name, st.name AS staff_name
  FROM audit_log a
  LEFT JOIN users u ON u.tenant_id = a.tenant_id AND u.id = a.actor_user_id
  LEFT JOIN bookings b ON b.tenant_id = a.tenant_id AND b.id = a.booking_id
  LEFT JOIN services s ON s.tenant_id = a.tenant_id AND s.id = a.service_id
  LEFT JOIN staff st ON st.tenant_id = a.tenant_id AND st.id = a.staff_id
 ORDER BY a.created_at DESC, a.id DESC
 LIMIT @row_limit;
