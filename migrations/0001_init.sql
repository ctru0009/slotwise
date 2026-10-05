-- +goose Up
CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE TABLE tenants (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug       text NOT NULL UNIQUE,
    name       text NOT NULL,
    timezone   text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE services (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    name             text NOT NULL,
    duration_minutes integer NOT NULL CHECK (duration_minutes > 0),
    buffer_minutes   integer NOT NULL DEFAULT 0 CHECK (buffer_minutes >= 0),
    price_cents      integer NOT NULL DEFAULT 0 CHECK (price_cents >= 0),
    created_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX services_tenant_idx ON services (tenant_id);

CREATE TABLE staff (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    name       text NOT NULL,
    email      text NOT NULL,
    active     boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX staff_tenant_idx ON staff (tenant_id);

CREATE TABLE bookings (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       uuid NOT NULL REFERENCES tenants (id) ON DELETE CASCADE,
    staff_id        uuid NOT NULL REFERENCES staff (id),
    service_id      uuid NOT NULL REFERENCES services (id),
    customer_name   text NOT NULL,
    customer_email  text NOT NULL,
    starts_at       timestamptz NOT NULL,
    ends_at         timestamptz NOT NULL,
    status          text NOT NULL CHECK (status IN ('confirmed', 'cancelled')),
    idempotency_key text NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    CHECK (ends_at > starts_at),
    UNIQUE (tenant_id, idempotency_key),
    EXCLUDE USING gist (staff_id WITH =, tstzrange(starts_at, ends_at) WITH &&)
        WHERE (status = 'confirmed')
);
CREATE INDEX bookings_tenant_starts_at_idx ON bookings (tenant_id, starts_at);

-- Tenant for the current transaction. The app sets app.tenant_id per request
-- with set_config(..., true); an unset or empty value matches no rows, so a
-- missing tenant fails closed.
-- +goose StatementBegin
CREATE FUNCTION current_tenant() RETURNS uuid
    LANGUAGE sql STABLE PARALLEL SAFE
AS $$ SELECT nullif(current_setting('app.tenant_id', true), '')::uuid $$;
-- +goose StatementEnd

ALTER TABLE tenants ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenants FORCE ROW LEVEL SECURITY;
CREATE POLICY tenants_isolation ON tenants
    USING (id = current_tenant())
    WITH CHECK (id = current_tenant());

ALTER TABLE services ENABLE ROW LEVEL SECURITY;
ALTER TABLE services FORCE ROW LEVEL SECURITY;
CREATE POLICY services_isolation ON services
    USING (tenant_id = current_tenant())
    WITH CHECK (tenant_id = current_tenant());

ALTER TABLE staff ENABLE ROW LEVEL SECURITY;
ALTER TABLE staff FORCE ROW LEVEL SECURITY;
CREATE POLICY staff_isolation ON staff
    USING (tenant_id = current_tenant())
    WITH CHECK (tenant_id = current_tenant());

ALTER TABLE bookings ENABLE ROW LEVEL SECURITY;
ALTER TABLE bookings FORCE ROW LEVEL SECURITY;
CREATE POLICY bookings_isolation ON bookings
    USING (tenant_id = current_tenant())
    WITH CHECK (tenant_id = current_tenant());

-- The application connects as a non-owner role; grants are applied when that
-- role exists (it is provisioned per environment, not by a migration).
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'slotwise_app') THEN
        GRANT USAGE ON SCHEMA public TO slotwise_app;
        GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO slotwise_app;
        ALTER DEFAULT PRIVILEGES IN SCHEMA public
            GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO slotwise_app;
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS bookings;
DROP TABLE IF EXISTS staff;
DROP TABLE IF EXISTS services;
DROP TABLE IF EXISTS tenants;
DROP FUNCTION IF EXISTS current_tenant();
