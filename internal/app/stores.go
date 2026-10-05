package app

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/ctru0009/slotwise/internal/domain"
)

// TenantStore resolves a tenant by its public slug, before any session exists,
// and by id once a request is tenant-scoped.
type TenantStore interface {
	// TenantBySlug returns the tenant with this slug, or domain.ErrNotFound.
	TenantBySlug(ctx context.Context, slug string) (domain.Tenant, error)
	// TenantByID returns the tenant with this id, or domain.ErrNotFound. The
	// read is scoped to the id itself, so row level security only ever returns
	// the caller's own row.
	TenantByID(ctx context.Context, tenantID uuid.UUID) (domain.Tenant, error)
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

// AvailabilityStore is the persistence the availability use case needs.
type AvailabilityStore interface {
	// SlotSnapshot reads one search in a single tenant-scoped transaction: the
	// active service, the active staff, and per staff the weekly rules, time
	// off and confirmed busy intervals overlapping [from, to). Unknown or
	// inactive service and unknown staff report domain.ErrNotFound. Results are
	// grouped per active staff; never nil.
	SlotSnapshot(ctx context.Context, tenantID, serviceID uuid.UUID, from, to time.Time) (domain.SlotSnapshot, error)
	// ListWeeklyRules returns the staff member's whole week; never nil.
	ListWeeklyRules(ctx context.Context, tenantID, staffID uuid.UUID) ([]domain.WeeklyRule, error)
	// ReplaceWeeklyRules replaces the staff member's whole week in one
	// transaction. An invisible staff id reports domain.ErrNotFound.
	ReplaceWeeklyRules(ctx context.Context, tenantID, staffID uuid.UUID, rules []WeeklyRuleInput) error
	// ListTimeOff returns the staff member's absences; never nil.
	ListTimeOff(ctx context.Context, tenantID, staffID uuid.UUID) ([]domain.TimeOff, error)
	// InsertTimeOff stores one absence; an invisible staff id reports
	// domain.ErrNotFound.
	InsertTimeOff(ctx context.Context, tenantID, staffID uuid.UUID, from, to time.Time) error
	// DeleteTimeOff removes one absence; an unknown id reports
	// domain.ErrNotFound.
	DeleteTimeOff(ctx context.Context, tenantID, staffID, id uuid.UUID) error
}

// Sender delivers outbound messages. Development wires the logging
// implementation; M5 adds the real one for confirmations and reminders.
type Sender interface {
	Send(ctx context.Context, msg domain.Message) error
}
