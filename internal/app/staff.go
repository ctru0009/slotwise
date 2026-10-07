package app

import (
	"context"
	"fmt"
	"net/mail"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/ctru0009/slotwise/internal/domain"
)

// Staff implements the staff roster use cases.
type Staff struct {
	store StaffStore
}

// NewStaff returns a Staff backed by store.
func NewStaff(store StaffStore) *Staff {
	return &Staff{store: store}
}

// List returns the actor's tenant's staff. Both roles may read.
func (s *Staff) List(ctx context.Context, actor domain.User) ([]domain.Staff, error) {
	staff, err := s.store.ListStaff(ctx, actor.TenantID)
	if err != nil {
		return nil, fmt.Errorf("listing staff: %w", err)
	}
	return staff, nil
}

// Create adds a staff member to the actor's tenant. Only owners may write.
func (s *Staff) Create(ctx context.Context, actor domain.User, in StaffInput) error {
	if err := requireOwner(actor); err != nil {
		return err
	}
	if err := in.validate(); err != nil {
		return err
	}
	if err := s.store.CreateStaff(ctx, actor.TenantID, actor.ID, in); err != nil {
		return fmt.Errorf("creating staff: %w", err)
	}
	return nil
}

// Update replaces the staff member with id. Only owners may write; an id
// outside the actor's tenant stays domain.ErrNotFound.
func (s *Staff) Update(ctx context.Context, actor domain.User, id uuid.UUID, in StaffInput) error {
	if err := requireOwner(actor); err != nil {
		return err
	}
	if err := in.validate(); err != nil {
		return err
	}
	if err := s.store.UpdateStaff(ctx, actor.TenantID, actor.ID, id, in); err != nil {
		return fmt.Errorf("updating staff: %w", err)
	}
	return nil
}

// SetActive activates or deactivates the staff member with id. Only owners may
// write; deactivation keeps the member's booking history reachable.
func (s *Staff) SetActive(ctx context.Context, actor domain.User, id uuid.UUID, active bool) error {
	if err := requireOwner(actor); err != nil {
		return err
	}
	if err := s.store.SetStaffActive(ctx, actor.TenantID, actor.ID, id, active); err != nil {
		return fmt.Errorf("setting staff active: %w", err)
	}
	return nil
}

// validate normalises in in place and reports the first staff field that is
// out of bounds, so the store only ever sees a trimmed name and a lower-cased
// email.
func (in *StaffInput) validate() error {
	in.Name = strings.TrimSpace(in.Name)
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	switch {
	case in.Name == "":
		return domain.ValidationError{Field: "name", Message: "is required"}
	case utf8.RuneCountInString(in.Name) > maxNameRunes:
		return domain.ValidationError{Field: "name", Message: "must be at most 200 characters"}
	}
	if in.Email == "" {
		return domain.ValidationError{Field: "email", Message: "is required"}
	}
	if _, err := mail.ParseAddress(in.Email); err != nil {
		return domain.ValidationError{Field: "email", Message: "must be a valid email address"}
	}
	return nil
}
