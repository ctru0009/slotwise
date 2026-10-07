package domain

import (
	"github.com/google/uuid"
)

// JobKind is the work one queue row asks for. The values are the strings the
// jobs.kind check constraint accepts.
type JobKind string

// The kinds v1 queues.
const (
	JobBookingConfirmation JobKind = "booking_confirmation"
	JobBookingReminder     JobKind = "booking_reminder"
)

// Valid reports whether k is a kind the database accepts.
func (k JobKind) Valid() bool {
	return k == JobBookingConfirmation || k == JobBookingReminder
}

// Job is one claimed queue row: the unit of work the worker hands to its
// handler. Attempts counts the claims this row has had, including the claim
// that produced this value, so a handler sees Attempts 1 on its first run.
type Job struct {
	ID        uuid.UUID
	TenantID  uuid.UUID
	BookingID uuid.UUID
	Kind      JobKind
	Attempts  int
}
