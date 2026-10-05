package app

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/ctru0009/slotwise/internal/domain"
)

// fakeServiceStore is an in-memory ServiceStore that records what the use case
// asked it to do.
type fakeServiceStore struct {
	services []domain.Service
	listErr  error
	writeErr error

	tenantID uuid.UUID
	created  ServiceInput
	updated  ServiceInput
	activeID uuid.UUID
	active   bool
	writes   int
}

func (f *fakeServiceStore) ListServices(_ context.Context, tenantID uuid.UUID) ([]domain.Service, error) {
	f.tenantID = tenantID
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.services, nil
}

func (f *fakeServiceStore) CreateService(_ context.Context, tenantID uuid.UUID, in ServiceInput) error {
	f.tenantID = tenantID
	f.created = in
	f.writes++
	return f.writeErr
}

func (f *fakeServiceStore) UpdateService(_ context.Context, tenantID, id uuid.UUID, in ServiceInput) error {
	f.tenantID = tenantID
	f.updated = in
	f.activeID = id
	f.writes++
	return f.writeErr
}

func (f *fakeServiceStore) SetServiceActive(_ context.Context, tenantID, id uuid.UUID, active bool) error {
	f.tenantID = tenantID
	f.activeID = id
	f.active = active
	f.writes++
	return f.writeErr
}

// ownerActor and staffActor are the two logins the permission tests act as.
func ownerActor(tenantID uuid.UUID) domain.User {
	return domain.User{ID: uuid.New(), TenantID: tenantID, Email: "owner@example.com", Role: domain.RoleOwner}
}

func staffActor(tenantID uuid.UUID) domain.User {
	return domain.User{ID: uuid.New(), TenantID: tenantID, Email: "staff@example.com", Role: domain.RoleStaff}
}

func TestServicesListAllowsBothRoles(t *testing.T) {
	t.Parallel()
	tenantID := uuid.New()
	services := []domain.Service{{ID: uuid.New(), TenantID: tenantID, Name: "Cut", Active: true}}
	store := &fakeServiceStore{services: services}
	useCase := NewServices(store)

	for _, actor := range []domain.User{ownerActor(tenantID), staffActor(tenantID)} {
		got, err := useCase.List(t.Context(), actor)
		if err != nil {
			t.Fatalf("List as %s: %v", actor.Role, err)
		}
		if len(got) != 1 || got[0].ID != services[0].ID {
			t.Errorf("List as %s = %#v, want the tenant's services", actor.Role, got)
		}
		if store.tenantID != tenantID {
			t.Errorf("List scoped to %v, want %v", store.tenantID, tenantID)
		}
	}
}

func TestServicesWritesRequireOwner(t *testing.T) {
	t.Parallel()
	tenantID := uuid.New()
	tests := []struct {
		name string
		call func(ctx context.Context, useCase *Services, actor domain.User) error
	}{
		{name: "create", call: func(ctx context.Context, useCase *Services, actor domain.User) error {
			return useCase.Create(ctx, actor, validService())
		}},
		{name: "update", call: func(ctx context.Context, useCase *Services, actor domain.User) error {
			return useCase.Update(ctx, actor, uuid.New(), validService())
		}},
		{name: "set active", call: func(ctx context.Context, useCase *Services, actor domain.User) error {
			return useCase.SetActive(ctx, actor, uuid.New(), false)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := &fakeServiceStore{}
			err := tt.call(t.Context(), NewServices(store), staffActor(tenantID))
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("error = %v, want domain.ErrForbidden", err)
			}
			if store.writes != 0 {
				t.Errorf("the store was written %d times, want 0", store.writes)
			}
		})
	}
}

func TestServicesOwnerWriteNormalisesInput(t *testing.T) {
	t.Parallel()
	tenantID := uuid.New()
	store := &fakeServiceStore{}
	in := ServiceInput{Name: "  Cut and blow dry  ", DurationMinutes: 30, BufferMinutes: 10, PriceCents: 4_500}

	if err := NewServices(store).Create(t.Context(), ownerActor(tenantID), in); err != nil {
		t.Fatalf("Create: %v", err)
	}
	want := ServiceInput{Name: "Cut and blow dry", DurationMinutes: 30, BufferMinutes: 10, PriceCents: 4_500}
	if store.created != want {
		t.Errorf("stored input = %#v, want %#v", store.created, want)
	}
	if store.tenantID != tenantID {
		t.Errorf("stored tenant = %v, want %v", store.tenantID, tenantID)
	}
}

func TestServicesWriteValidatesBeforeStore(t *testing.T) {
	t.Parallel()
	store := &fakeServiceStore{}
	err := NewServices(store).Create(t.Context(), ownerActor(uuid.New()), ServiceInput{Name: "", DurationMinutes: 30})
	assertValidationError(t, err, "name")
	if store.writes != 0 {
		t.Errorf("the store was written %d times, want 0", store.writes)
	}
}

func TestServicesStoreNotFoundPropagates(t *testing.T) {
	t.Parallel()
	store := &fakeServiceStore{writeErr: domain.ErrNotFound}
	err := NewServices(store).Update(t.Context(), ownerActor(uuid.New()), uuid.New(), validService())
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want domain.ErrNotFound", err)
	}
}

func TestServicesSetActiveReachesStore(t *testing.T) {
	t.Parallel()
	tenantID := uuid.New()
	id := uuid.New()
	store := &fakeServiceStore{}
	if err := NewServices(store).SetActive(t.Context(), ownerActor(tenantID), id, false); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	if store.activeID != id || store.active || store.tenantID != tenantID {
		t.Errorf("SetServiceActive(%v, %v, %v), want (%v, %v, %v)",
			store.tenantID, store.activeID, store.active, tenantID, id, false)
	}
}
