package views

import (
	"time"

	"github.com/ctru0009/slotwise/internal/app"
	"github.com/ctru0009/slotwise/internal/domain"
)

// modeLabel names a calendar window for the page.
func modeLabel(mode app.CalendarMode) string {
	if mode == app.CalendarDay {
		return "Day"
	}
	return "Week"
}

// activeLabel is how a row's active flag reads.
func activeLabel(active bool) string {
	if active {
		return "Active"
	}
	return "Inactive"
}

// bookingClass marks a cancelled appointment so the grid reads at a glance.
func bookingClass(status domain.BookingStatus) string {
	if status == domain.BookingCancelled {
		return "cancelled"
	}
	return ""
}

// actorLabel names who made a change. An entry with no login was made from the
// public booking flow, where there is no account to name.
func actorLabel(entry app.AuditEntry) string {
	if entry.ActorEmail == "" {
		return "customer"
	}
	return entry.ActorEmail
}

// auditSubject names the row an entry is about. Which subject the store filled
// in follows from the action. The switch lists every action and has no default,
// so the exhaustive linter stops a new action from rendering a blank cell.
func auditSubject(entry app.AuditEntry, loc *time.Location) string {
	switch entry.Action {
	case domain.AuditBookingCreated, domain.AuditBookingCancelled:
		if entry.BookingStarts == nil {
			return entry.BookingCustomer
		}
		return entry.BookingCustomer + " on " + localDay(*entry.BookingStarts, loc)
	case domain.AuditServiceCreated, domain.AuditServiceUpdated,
		domain.AuditServiceActivated, domain.AuditServiceDeactivated:
		return entry.ServiceName
	case domain.AuditStaffCreated, domain.AuditStaffUpdated,
		domain.AuditStaffActivated, domain.AuditStaffDeactivated:
		return entry.StaffName
	}
	return ""
}
