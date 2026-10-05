-- name: UserByEmail :one
SELECT id, tenant_id, email, password_hash, role
  FROM users
 WHERE lower(email) = lower(@email);

-- name: UserByID :one
SELECT id, tenant_id, email, password_hash, role
  FROM users
 WHERE id = @id;

-- name: UpdateUserPassword :execrows
UPDATE users SET password_hash = @password_hash WHERE id = @id;

-- name: InsertUser :one
INSERT INTO users (id, tenant_id, email, password_hash, role)
VALUES (@id, @tenant_id, @email, @password_hash, @role)
ON CONFLICT DO NOTHING
RETURNING id;
