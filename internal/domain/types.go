package domain

import "github.com/google/uuid"

// Tenant is one customer business, addressed publicly by its slug.
type Tenant struct {
	ID       uuid.UUID
	Slug     string
	Name     string
	Timezone string
}

// Role is a login's level of access inside its tenant.
type Role string

// The roles the users.role check constraint accepts.
const (
	RoleOwner Role = "owner"
	RoleStaff Role = "staff"
)

// Valid reports whether r is a role the database accepts.
func (r Role) Valid() bool {
	return r == RoleOwner || r == RoleStaff
}

// User is a login inside one tenant.
type User struct {
	ID           uuid.UUID
	TenantID     uuid.UUID
	Email        string
	PasswordHash string
	Role         Role
	StaffID      *uuid.UUID
}

// IsOwner reports whether the login may change the tenant's data.
func (u User) IsOwner() bool {
	return u.Role == RoleOwner
}

// Service is a bookable service offered by a tenant.
type Service struct {
	ID              uuid.UUID
	TenantID        uuid.UUID
	Name            string
	DurationMinutes int
	BufferMinutes   int
	PriceCents      int
	Active          bool
}

// Staff is a bookable staff member.
type Staff struct {
	ID       uuid.UUID
	TenantID uuid.UUID
	Name     string
	Email    string
	Active   bool
}

// Message is one outbound email.
type Message struct {
	To      string
	Subject string
	Body    string
}
