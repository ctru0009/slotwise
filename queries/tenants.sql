-- name: InsertTenant :one
INSERT INTO tenants (id, slug, name, timezone)
VALUES (@id, @slug, @name, @timezone)
ON CONFLICT (slug) DO NOTHING
RETURNING id;
