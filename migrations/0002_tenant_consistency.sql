-- +goose Up

-- Referential integrity checks run with the table owner's privileges and ignore
-- row level security, so a single-column foreign key lets one tenant point a
-- booking at another tenant's staff or service: the row-level policy only checks
-- bookings.tenant_id, and the exclusion constraint is keyed on staff_id alone.
-- Making tenant_id part of the reference closes both the squatting hole and the
-- availability oracle it creates.
ALTER TABLE staff ADD CONSTRAINT staff_tenant_id_id_key UNIQUE (tenant_id, id);
ALTER TABLE services ADD CONSTRAINT services_tenant_id_id_key UNIQUE (tenant_id, id);

ALTER TABLE bookings DROP CONSTRAINT bookings_staff_id_fkey;
ALTER TABLE bookings DROP CONSTRAINT bookings_service_id_fkey;
ALTER TABLE bookings
    ADD CONSTRAINT bookings_tenant_staff_fkey
        FOREIGN KEY (tenant_id, staff_id) REFERENCES staff (tenant_id, id),
    ADD CONSTRAINT bookings_tenant_service_fkey
        FOREIGN KEY (tenant_id, service_id) REFERENCES services (tenant_id, id);

-- A SQL function body is parsed under the caller's search_path, and this one is
-- called from row level security policies, so pin it and qualify the builtins.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION current_tenant() RETURNS uuid
    LANGUAGE sql STABLE PARALLEL SAFE
    SET search_path = ''
AS $$ SELECT nullif(pg_catalog.current_setting('app.tenant_id', true), '')::pg_catalog.uuid $$;
-- +goose StatementEnd

-- 0001 granted privileges only if the application role already existed, so a
-- deployment that created it later ended up with no privileges and failed at
-- request time with "permission denied" long after a green migration. Assert the
-- precondition and re-apply the grants here instead. This runs before the revoke
-- below, which would otherwise be undone by the blanket grant.
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'slotwise_app') THEN
        RAISE EXCEPTION 'role slotwise_app must exist before migrating: create it first, or the application will have no privileges';
    END IF;

    GRANT USAGE ON SCHEMA public TO slotwise_app;
    GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO slotwise_app;
END
$$;
-- +goose StatementEnd

-- goose's bookkeeping table is migration infrastructure, not application data;
-- the envelope of grants above reaches it because goose creates it before the
-- migrations run.
-- +goose StatementBegin
DO $$
BEGIN
    IF to_regclass('public.goose_db_version') IS NOT NULL
       AND EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'slotwise_app') THEN
        REVOKE ALL ON TABLE goose_db_version FROM slotwise_app;
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF to_regclass('public.goose_db_version') IS NOT NULL
       AND EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'slotwise_app') THEN
        GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE goose_db_version TO slotwise_app;
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION current_tenant() RETURNS uuid
    LANGUAGE sql STABLE PARALLEL SAFE
AS $$ SELECT nullif(current_setting('app.tenant_id', true), '')::uuid $$;
-- +goose StatementEnd

ALTER TABLE bookings DROP CONSTRAINT bookings_tenant_staff_fkey;
ALTER TABLE bookings DROP CONSTRAINT bookings_tenant_service_fkey;
ALTER TABLE bookings
    ADD CONSTRAINT bookings_staff_id_fkey FOREIGN KEY (staff_id) REFERENCES staff (id),
    ADD CONSTRAINT bookings_service_id_fkey FOREIGN KEY (service_id) REFERENCES services (id);

ALTER TABLE services DROP CONSTRAINT services_tenant_id_id_key;
ALTER TABLE staff DROP CONSTRAINT staff_tenant_id_id_key;
