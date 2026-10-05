package app

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/ctru0009/slotwise/internal/domain"
)

// maxNameRunes is the longest service or staff name a form may submit.
const maxNameRunes = 200

// Services implements the service catalogue use cases.
type Services struct {
	store ServiceStore
}

// NewServices returns a Services backed by store.
func NewServices(store ServiceStore) *Services {
	return &Services{store: store}
}

// List returns the actor's tenant's services. Both roles may read.
func (s *Services) List(ctx context.Context, actor domain.User) ([]domain.Service, error) {
	services, err := s.store.ListServices(ctx, actor.TenantID)
	if err != nil {
		return nil, fmt.Errorf("listing services: %w", err)
	}
	return services, nil
}

// Create adds a service to the actor's tenant. Only owners may write.
func (s *Services) Create(ctx context.Context, actor domain.User, in ServiceInput) error {
	if err := requireOwner(actor); err != nil {
		return err
	}
	if err := in.validate(); err != nil {
		return err
	}
	if err := s.store.CreateService(ctx, actor.TenantID, in); err != nil {
		return fmt.Errorf("creating service: %w", err)
	}
	return nil
}

// Update replaces the service with id. Only owners may write; an id outside
// the actor's tenant stays domain.ErrNotFound.
func (s *Services) Update(ctx context.Context, actor domain.User, id uuid.UUID, in ServiceInput) error {
	if err := requireOwner(actor); err != nil {
		return err
	}
	if err := in.validate(); err != nil {
		return err
	}
	if err := s.store.UpdateService(ctx, actor.TenantID, id, in); err != nil {
		return fmt.Errorf("updating service: %w", err)
	}
	return nil
}

// SetActive activates or deactivates the service with id. Only owners may
// write; deactivation replaces deletion because bookings reference the row.
func (s *Services) SetActive(ctx context.Context, actor domain.User, id uuid.UUID, active bool) error {
	if err := requireOwner(actor); err != nil {
		return err
	}
	if err := s.store.SetServiceActive(ctx, actor.TenantID, id, active); err != nil {
		return fmt.Errorf("setting service active: %w", err)
	}
	return nil
}

// requireOwner is the write rule both use cases share: owners change tenant
// data, staff logins only read it.
func requireOwner(actor domain.User) error {
	if !actor.IsOwner() {
		return domain.ErrForbidden
	}
	return nil
}

// validate normalises in in place and reports the first service field that is
// out of bounds, so the store only ever sees trimmed values.
func (in *ServiceInput) validate() error {
	in.Name = strings.TrimSpace(in.Name)
	switch {
	case in.Name == "":
		return domain.ValidationError{Field: "name", Message: "is required"}
	case utf8.RuneCountInString(in.Name) > maxNameRunes:
		return domain.ValidationError{Field: "name", Message: "must be at most 200 characters"}
	}
	if in.DurationMinutes < 1 || in.DurationMinutes > 1440 {
		return domain.ValidationError{Field: "duration_minutes", Message: "must be between 1 and 1440"}
	}
	if in.BufferMinutes < 0 || in.BufferMinutes > 240 {
		return domain.ValidationError{Field: "buffer_minutes", Message: "must be between 0 and 240"}
	}
	if in.PriceCents < 0 || in.PriceCents > 10_000_000 {
		return domain.ValidationError{Field: "price_cents", Message: "must be between 0 and 10000000"}
	}
	return nil
}
