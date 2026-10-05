-- +goose Up

CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    email         text NOT NULL,
    password_hash text NOT NULL,
    role          text NOT NULL CHECK (role IN ('owner', 'staff')),
    staff_id      uuid,
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, id),
    -- Composite key for the same reason as 0002: referential checks bypass row
    -- level security, so tenant_id travels with every reference.
    FOREIGN KEY (tenant_id, staff_id) REFERENCES staff (tenant_id, id)
);
CREATE UNIQUE INDEX users_tenant_email_key ON users (tenant_id, lower(email));

-- Login is tenant-scoped, so the unique index above is per tenant and the same
-- email can be an owner of two different tenants.

CREATE TABLE password_reset_tokens (
    token_hash bytea PRIMARY KEY,
    tenant_id  uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    user_id    uuid NOT NULL,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (tenant_id, user_id) REFERENCES users (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX password_reset_tokens_user_idx ON password_reset_tokens (tenant_id, user_id);

-- Only the SHA-256 of the emailed token is stored, so a database leak does not
-- hand out working reset links.

-- Sessions for the scs store. The table is deliberately not reachable by the
-- application role: rows are read and written through the SECURITY DEFINER
-- functions below, which hash the token the same way the app does.
CREATE TABLE sessions (
    token_hash bytea PRIMARY KEY,
    data       bytea NOT NULL,
    expiry     timestamptz NOT NULL
);
CREATE INDEX sessions_expiry_idx ON sessions (expiry);

ALTER TABLE users ENABLE ROW LEVEL SECURITY;
ALTER TABLE users FORCE ROW LEVEL SECURITY;
CREATE POLICY users_isolation ON users
    USING (tenant_id = current_tenant())
    WITH CHECK (tenant_id = current_tenant());

ALTER TABLE password_reset_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE password_reset_tokens FORCE ROW LEVEL SECURITY;
CREATE POLICY password_reset_tokens_isolation ON password_reset_tokens
    USING (tenant_id = current_tenant())
    WITH CHECK (tenant_id = current_tenant());

ALTER TABLE sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE sessions FORCE ROW LEVEL SECURITY;
-- No policy: the table is pre-tenant machinery. 0001's ALTER DEFAULT PRIVILEGES
-- hands every later table in public to slotwise_app, so revoke it explicitly,
-- the same treatment goose_db_version got in 0002.
REVOKE ALL ON TABLE sessions FROM slotwise_app;

-- The resolver functions below run as their owner. FORCE ROW LEVEL SECURITY
-- applies to the table owner too, so a migration role that is neither superuser
-- nor BYPASSRLS would see zero rows and silently break login instead of failing
-- here.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles
                    WHERE rolname = current_user AND (rolsuper OR rolbypassrls)) THEN
        RAISE EXCEPTION 'migrations must run as a role that bypasses row level security: the SECURITY DEFINER functions below cannot read tenants or sessions otherwise';
    END IF;
END
$$;
-- +goose StatementEnd

-- Resolves a public slug to its tenant before any session exists. It returns
-- only the three public columns and is the single sanctioned pre-tenant read,
-- as ROADMAP's note prescribes.
-- +goose StatementBegin
CREATE FUNCTION public.tenant_by_slug(slug text) RETURNS TABLE(id uuid, name text, timezone text)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = ''
AS $$ SELECT t.id, t.name, t.timezone FROM public.tenants t WHERE t.slug = $1 $$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.tenant_by_slug(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.tenant_by_slug(text) TO slotwise_app;

-- +goose StatementBegin
CREATE FUNCTION public.session_find(token text) RETURNS TABLE(data bytea, expiry timestamptz)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path = ''
AS $$
    SELECT s.data, s.expiry
      FROM public.sessions s
     WHERE s.token_hash = pg_catalog.sha256(pg_catalog.convert_to($1, 'UTF8'))
       AND s.expiry > pg_catalog.now()
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.session_find(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.session_find(text) TO slotwise_app;

-- +goose StatementBegin
CREATE FUNCTION public.session_commit(token text, data bytea, expiry timestamptz) RETURNS void
    LANGUAGE sql SECURITY DEFINER
    SET search_path = ''
AS $$
    INSERT INTO public.sessions (token_hash, data, expiry)
    VALUES (pg_catalog.sha256(pg_catalog.convert_to($1, 'UTF8')), $2, $3)
    ON CONFLICT (token_hash) DO UPDATE SET data = EXCLUDED.data, expiry = EXCLUDED.expiry
$$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.session_commit(text, bytea, timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.session_commit(text, bytea, timestamptz) TO slotwise_app;

-- +goose StatementBegin
CREATE FUNCTION public.session_delete(token text) RETURNS void
    LANGUAGE sql SECURITY DEFINER
    SET search_path = ''
AS $$ DELETE FROM public.sessions WHERE token_hash = pg_catalog.sha256(pg_catalog.convert_to($1, 'UTF8')) $$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.session_delete(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.session_delete(text) TO slotwise_app;

-- +goose StatementBegin
CREATE FUNCTION public.session_purge() RETURNS void
    LANGUAGE sql SECURITY DEFINER
    SET search_path = ''
AS $$ DELETE FROM public.sessions WHERE expiry < pg_catalog.now() $$;
-- +goose StatementEnd
REVOKE EXECUTE ON FUNCTION public.session_purge() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.session_purge() TO slotwise_app;

-- +goose Down
DROP FUNCTION IF EXISTS public.session_purge();
DROP FUNCTION IF EXISTS public.session_delete(text);
DROP FUNCTION IF EXISTS public.session_commit(text, bytea, timestamptz);
DROP FUNCTION IF EXISTS public.session_find(text);
DROP FUNCTION IF EXISTS public.tenant_by_slug(text);
DROP TABLE IF EXISTS sessions;
DROP TABLE IF EXISTS password_reset_tokens;
DROP TABLE IF EXISTS users;
