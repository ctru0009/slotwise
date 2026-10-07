// Package views renders the web pages with templ. A handler passes plain
// values in and renders the component it gets back, so a page holds no routing,
// no session and no database access: it is a function from data to HTML.
package views

import (
	"time"

	"github.com/google/uuid"

	"github.com/ctru0009/slotwise/internal/app"
	"github.com/ctru0009/slotwise/internal/domain"
)

// StartPage is the tenant slug form served at GET /login.
type StartPage struct {
	Slug  string
	Error string
}

// LoginPage is the sign-in form for one tenant.
type LoginPage struct {
	Tenant domain.Tenant
	Notice string
	Error  string
}

// ForgotPage is the password reset request form for one tenant.
type ForgotPage struct {
	Tenant domain.Tenant
	Sent   bool
}

// ResetPage is the new-password form reached from a reset link.
type ResetPage struct {
	Tenant domain.Tenant
	Token  string
	Error  string
}

// LandingPage is the public page for one business: its name and the services
// a customer can book, with the local date the slot search starts from.
type LandingPage struct {
	Tenant   domain.Tenant
	Services []domain.Service
	Today    domain.LocalDate
}

// Slot is one bookable start with the key the form that books it submits. The
// key is minted per rendered form, so a resubmitted form replays one booking
// instead of creating another.
type Slot struct {
	StaffID        uuid.UUID
	StaffName      string
	StartsAt       time.Time
	IdempotencyKey string
}

// SlotsPage is the public list of bookable starts for one service over a local
// date range, with the tenant's clock the times are rendered on and the range
// the search form resubmits.
type SlotsPage struct {
	Tenant    domain.Tenant
	Location  *time.Location
	ServiceID uuid.UUID
	From      string
	To        string
	Slots     []Slot
}

// BookingPage is one booking: its details on the tenant's clock, the form that
// cancels it with the token from the signed link, and the token that downloads
// its calendar file.
type BookingPage struct {
	Tenant        domain.Tenant
	Location      *time.Location
	Booking       domain.Booking
	Token         string
	CalendarToken string
}

// DashboardPage is the owner's page: the window the calendar shows, the
// sections that hang off it, and the forms that change the catalogue. A write
// either redirects here or re-renders with an Error, so there is no notice to
// show.
type DashboardPage struct {
	Overview app.Overview
	User     domain.User
	Error    string
}

// ErrorPage is an HTTP status and a human-readable message.
type ErrorPage struct {
	Status  int
	Title   string
	Message string
}
