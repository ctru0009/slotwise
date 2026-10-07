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

// ServiceStore is the persistence the services use case needs. The three write
// methods take the acting login as well as the tenant, because each one records
// an audit row in its own transaction and the actor is the only part of that
// row the store cannot read off the row it just wrote.
type ServiceStore interface {
	ListServices(ctx context.Context, tenantID uuid.UUID) ([]domain.Service, error)
	CreateService(ctx context.Context, tenantID, actor uuid.UUID, in ServiceInput) error
	UpdateService(ctx context.Context, tenantID, actor, id uuid.UUID, in ServiceInput) error
	SetServiceActive(ctx context.Context, tenantID, actor, id uuid.UUID, active bool) error
}

// StaffStore is the persistence the staff use case needs, with the same actor
// threading as ServiceStore and for the same reason.
type StaffStore interface {
	ListStaff(ctx context.Context, tenantID uuid.UUID) ([]domain.Staff, error)
	CreateStaff(ctx context.Context, tenantID, actor uuid.UUID, in StaffInput) error
	UpdateStaff(ctx context.Context, tenantID, actor, id uuid.UUID, in StaffInput) error
	SetStaffActive(ctx context.Context, tenantID, actor, id uuid.UUID, active bool) error
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

// BookingStore is the persistence the bookings use case needs.
type BookingStore interface {
	// BookingByIdempotencyKey returns the booking a key already created, or
	// domain.ErrNotFound. The lookup is tenant-scoped, so the same key in
	// another tenant is a different booking.
	BookingByIdempotencyKey(ctx context.Context, tenantID uuid.UUID, key string) (domain.Booking, error)
	// CreateBooking stores the booking, or returns the stored one when the key
	// already booked. An insert overlapping a confirmed booking reports
	// domain.ErrSlotTaken; a staff member who is unknown, invisible or
	// deactivated reports domain.ErrNotFound.
	CreateBooking(ctx context.Context, tenantID uuid.UUID, in BookingWrite) (domain.Booking, error)
	// BookingByID returns the booking with this id, or domain.ErrNotFound.
	BookingByID(ctx context.Context, tenantID, id uuid.UUID) (domain.Booking, error)
	// BookingMessage reads everything one confirmation or reminder email
	// renders: the booking, its tenant's slug, name and timezone, the service
	// name and the staff name. It is tenant-scoped, so another tenant's booking
	// reads as domain.ErrNotFound.
	BookingMessage(ctx context.Context, tenantID, bookingID uuid.UUID) (BookingMessage, error)
	// CancelBooking marks the booking cancelled. Cancelling a cancelled
	// booking succeeds; an unknown or invisible id reports domain.ErrNotFound.
	CancelBooking(ctx context.Context, tenantID, id uuid.UUID) error
	// ListBookingsInRange reads the bookings that occupy any part of
	// [from, to) with their service and staff names, ordered by start, in one
	// query per range; never nil.
	ListBookingsInRange(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]BookingInRange, error)
}

// AuditStore is the read side of the audit trail. There is no insert method
// here on purpose: a row is written by the store method whose change it
// records, inside that change's own transaction, so nothing can record a change
// that did not happen.
type AuditStore interface {
	// ListAuditLog returns the tenant's newest audit rows, at most limit of
	// them, never nil.
	ListAuditLog(ctx context.Context, tenantID uuid.UUID, limit int) ([]AuditEntry, error)
}

// JobStore is the queue as the worker drives it: claim one due job, then apply
// the transition its outcome asks for. The booking path enqueues inside its own
// transaction and never claims.
//
// Every transition is guarded by the claim that produced the job: the worker id
// and the attempt count together are a fencing token, so a stale claim whose
// lease expired and was reclaimed writes nothing. A false bool means the claim
// moved on, either because another worker reclaimed the job or because this
// worker's lease expired while it was running; the caller logs it and moves on
// rather than treating it as an error.
type JobStore interface {
	// ClaimJob leases the next due job to workerID until claimTime plus
	// leaseSeconds, and reports it with Attempts already incremented.
	// domain.ErrNotFound means nothing is due.
	ClaimJob(ctx context.Context, workerID string, claimTime time.Time, leaseSeconds int) (domain.Job, error)
	// CompleteJob marks the job done.
	CompleteJob(ctx context.Context, workerID string, job domain.Job) (bool, error)
	// RetryJob returns the job to the queue with runAt as its new due time and
	// reason as its last error.
	RetryJob(ctx context.Context, workerID string, job domain.Job, runAt time.Time, reason string) (bool, error)
	// DeadLetterJob marks the job dead and keeps reason as its last error. Dead
	// is terminal: nothing claims it again, and requeueing one is an operator
	// action.
	DeadLetterJob(ctx context.Context, workerID string, job domain.Job, reason string) (bool, error)
	// ReleaseJob returns an interrupted job to the queue with its run_at
	// unchanged and the claim's attempt refunded, which is what a graceful
	// shutdown does.
	ReleaseJob(ctx context.Context, workerID string, job domain.Job) (bool, error)
	// ListDeadJobs returns the tenant's dead-lettered jobs newest first, at
	// most limit of them; never nil.
	ListDeadJobs(ctx context.Context, tenantID uuid.UUID, limit int) ([]DeadJob, error)
}

// Sender delivers outbound messages. Development and this milestone wire the
// logging implementation; a real provider is a later one-file swap.
type Sender interface {
	Send(ctx context.Context, msg domain.Message) error
}
