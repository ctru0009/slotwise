-- name: ListServices :many
SELECT id, tenant_id, name, duration_minutes, buffer_minutes, price_cents, active
  FROM services
 ORDER BY name, id;

-- name: InsertService :one
INSERT INTO services (tenant_id, name, duration_minutes, buffer_minutes, price_cents)
VALUES (@tenant_id, @name, @duration_minutes, @buffer_minutes, @price_cents)
RETURNING id;

-- name: UpdateService :execrows
UPDATE services
   SET name = @name,
       duration_minutes = @duration_minutes,
       buffer_minutes = @buffer_minutes,
       price_cents = @price_cents
 WHERE id = @id;

-- name: SetServiceActive :execrows
UPDATE services SET active = @active WHERE id = @id;
