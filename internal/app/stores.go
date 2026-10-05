package app

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ctru0009/slotwise/internal/domain"
)

// TenantStore resolves a tenant by its public slug, before any session exists.
type TenantStore interface {
	// TenantBySlug returns the tenant with this slug, or domain.ErrNotFound.
	TenantBySlug(ctx context.Context, slug string) (domain.Tenant, error)
	// InsertTenant creates the tenant and reports whether it was inserted. The
	// tenant's own id is the id the caller generated, which is what lets the
	// store run the insert inside WithTenant(ctx, tenant.ID) and satisfy the
	// tenants policy (WITH CHECK (id = current_tenant())) without widening it.
	// An existing slug reports false and no error, so provisioning is
	// idempotent.
	InsertTenant(ctx context.Context, tenant domain.Tenant) (bool, error)
}

// UserStore reads and writes logins inside one tenant.
type UserStore interface {
	// UserByEmail looks a login up case-insensitively, or returns
	// domain.ErrNotFound.
	UserByEmail(ctx context.Context, tenantID uuid.UUID, email string) (domain.User, error)
	// UserByID re-reads a login, or returns domain.ErrNotFound.
	UserByID(ctx context.Context, tenantID, userID uuid.UUID) (domain.User, error)
	// InsertUser creates a login and reports whether it was inserted. An
	// existing login with the same email in the same tenant reports false and
	// no error, so provisioning stays idempotent.
	InsertUser(ctx context.Context, user domain.User) (bool, error)
}

// ResetTokenStore stores and spends single-use password reset tokens.
type ResetTokenStore interface {
	// IssueResetToken drops the user's open tokens and stores the new one.
	IssueResetToken(ctx context.Context, tenantID, userID uuid.UUID, tokenHash []byte, expiresAt time.Time) error
	// ConsumeResetToken marks the token used and replaces the user's password
	// hash in one transaction, so a token can only be spent once even under
	// concurrency. Unknown, used and expired tokens are all
	// domain.ErrResetTokenInvalid.
	ConsumeResetToken(ctx context.Context, tenantID uuid.UUID, tokenHash []byte, passwordHash string) (uuid.UUID, error)
}

// ServiceStore is the persistence the services use case needs.
type ServiceStore interface {
	ListServices(ctx context.Context, tenantID uuid.UUID) ([]domain.Service, error)
	CreateService(ctx context.Context, tenantID uuid.UUID, in ServiceInput) error
	UpdateService(ctx context.Context, tenantID, id uuid.UUID, in ServiceInput) error
	SetServiceActive(ctx context.Context, tenantID, id uuid.UUID, active bool) error
}

// StaffStore is the persistence the staff use case needs.
type StaffStore interface {
	ListStaff(ctx context.Context, tenantID uuid.UUID) ([]domain.Staff, error)
	CreateStaff(ctx context.Context, tenantID uuid.UUID, in StaffInput) error
	UpdateStaff(ctx context.Context, tenantID, id uuid.UUID, in StaffInput) error
	SetStaffActive(ctx context.Context, tenantID, id uuid.UUID, active bool) error
}

// Sender delivers outbound messages. Development wires the logging
// implementation; M5 adds the real one for confirmations and reminders.
type Sender interface {
	Send(ctx context.Context, msg domain.Message) error
}
