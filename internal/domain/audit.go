package domain

import "slices"

// AuditAction is what one audit row records: the change that happened, not the
// route that asked for it. The values are the strings the audit_log.action
// check constraint accepts, and TestAuditVocabularyMatchesTheDatabase fails if
// the two lists ever drift apart.
type AuditAction string

// The changes v1 audits. The booking actions have no signed-in actor, because
// the public booking flow is the only writer of a booking; the catalogue
// actions are always taken by a login.
const (
	AuditBookingCreated   AuditAction = "booking.created"
	AuditBookingCancelled AuditAction = "booking.cancelled"

	AuditServiceCreated     AuditAction = "service.created"
	AuditServiceUpdated     AuditAction = "service.updated"
	AuditServiceActivated   AuditAction = "service.activated"
	AuditServiceDeactivated AuditAction = "service.deactivated"

	AuditStaffCreated     AuditAction = "staff.created"
	AuditStaffUpdated     AuditAction = "staff.updated"
	AuditStaffActivated   AuditAction = "staff.activated"
	AuditStaffDeactivated AuditAction = "staff.deactivated"
)

// AuditActions lists every action the database accepts, in the order the check
// constraint names them.
var AuditActions = []AuditAction{
	AuditBookingCreated,
	AuditBookingCancelled,
	AuditServiceCreated,
	AuditServiceUpdated,
	AuditServiceActivated,
	AuditServiceDeactivated,
	AuditStaffCreated,
	AuditStaffUpdated,
	AuditStaffActivated,
	AuditStaffDeactivated,
}

// Valid reports whether a is an action the database accepts.
func (a AuditAction) Valid() bool {
	return slices.Contains(AuditActions, a)
}
