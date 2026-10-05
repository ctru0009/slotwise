package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ctru0009/slotwise/internal/adapters/postgres/dbgen"
	"github.com/ctru0009/slotwise/internal/app"
	"github.com/ctru0009/slotwise/internal/domain"
)

var _ app.TenantStore = (*DB)(nil)

// TenantBySlug resolves a slug to its tenant on the shared pool rather than
// through WithTenant: no tenant exists on the connection yet, so there is no
// transaction to scope. The read goes through the SECURITY DEFINER function
// 0003 defines, which is allowed to see the tenants table before a tenant is
// set and returns only the public id, name and timezone. The slug is the key
// the caller just supplied, so filling it in here gives callers a complete
// tenant without widening what the function exposes.
func (db *DB) TenantBySlug(ctx context.Context, slug string) (domain.Tenant, error) {
	tenant := domain.Tenant{Slug: slug}
	err := db.pool.QueryRow(ctx,
		"SELECT id, name, timezone FROM public.tenant_by_slug($1)", slug).
		Scan(&tenant.ID, &tenant.Name, &tenant.Timezone)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Tenant{}, fmt.Errorf("tenant %q: %w", slug, domain.ErrNotFound)
	}
	if err != nil {
		return domain.Tenant{}, fmt.Errorf("resolving tenant %q: %w", slug, err)
	}
	return tenant, nil
}

// TenantByID returns the tenant with this id. The read runs inside WithTenant,
// so row level security only ever returns the caller's own row and another
// tenant's id is indistinguishable from a missing one.
func (db *DB) TenantByID(ctx context.Context, tenantID uuid.UUID) (domain.Tenant, error) {
	var tenant domain.Tenant
	err := db.WithTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		row, err := dbgen.New(tx).TenantByID(ctx, tenantID)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("tenant %s: %w", tenantID, domain.ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("reading tenant by id: %w", err)
		}
		tenant = domain.Tenant{ID: row.ID, Slug: row.Slug, Name: row.Name, Timezone: row.Timezone}
		return nil
	})
	if err != nil {
		return domain.Tenant{}, err
	}
	return tenant, nil
}

// InsertTenant creates tenant on the caller's behalf and reports whether the
// row was inserted. The tenants policy requires id = current_tenant(), so the
// insert runs inside WithTenant keyed on the id the caller generated; ON
// CONFLICT (slug) DO NOTHING makes provisioning idempotent, and an existing
// slug is reported as false, nil rather than as an error.
func (db *DB) InsertTenant(ctx context.Context, tenant domain.Tenant) (bool, error) {
	inserted := false
	err := db.WithTenant(ctx, tenant.ID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := dbgen.New(tx).InsertTenant(ctx, dbgen.InsertTenantParams{
			ID:       tenant.ID,
			Slug:     tenant.Slug,
			Name:     tenant.Name,
			Timezone: tenant.Timezone,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inserting tenant: %w", err)
		}
		inserted = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return inserted, nil
}
