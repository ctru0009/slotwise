package app

import (
	"regexp"
	"time"

	"github.com/google/uuid"
)

// slugPattern is what a public tenant slug may look like: lower-case letters,
// digits and inner hyphens, 1 to 63 characters.
var slugPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// reservedSlugs are the second path segments the router uses for its own
// routes. A tenant cannot be named after one: the literal /app/services and
// /app/staff routes would shadow that tenant's sign-in, forgot and reset pages.
var reservedSlugs = map[string]bool{"services": true, "staff": true}

// ValidSlug reports whether slug can address a tenant publicly. The web layer
// uses it to decide whether a submitted slug is worth a lookup.
func ValidSlug(slug string) bool {
	return slugPattern.MatchString(slug) && !reservedSlugs[slug]
}

// ServiceInput is a service create or update request. Validation lives in
// ServiceInput.validate, so no handler can write an unchecked row.
type ServiceInput struct {
	Name            string
	DurationMinutes int
	BufferMinutes   int
	PriceCents      int
}

// StaffInput is a staff create or update request.
type StaffInput struct {
	Name  string
	Email string
}

// BookingInput is a public booking request. Validation lives in
// BookingInput.validate, so no handler can store an unchecked booking.
type BookingInput struct {
	ServiceID      uuid.UUID
	StaffID        uuid.UUID
	StartsAt       time.Time
	CustomerName   string
	CustomerEmail  string
	IdempotencyKey string
}

// BootstrapInput is the environment-driven provisioning of one tenant and its
// first owner login.
type BootstrapInput struct {
	Slug          string
	Name          string
	Timezone      string
	OwnerEmail    string
	OwnerPassword string
}
