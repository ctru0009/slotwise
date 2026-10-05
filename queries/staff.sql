-- name: ListStaff :many
SELECT id, tenant_id, name, email, active
  FROM staff
 ORDER BY name, id;

-- name: InsertStaff :one
INSERT INTO staff (tenant_id, name, email)
VALUES (@tenant_id, @name, @email)
RETURNING id;

-- name: UpdateStaff :execrows
UPDATE staff SET name = @name, email = @email WHERE id = @id;

-- name: SetStaffActive :execrows
UPDATE staff SET active = @active WHERE id = @id;
