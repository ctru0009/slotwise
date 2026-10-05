package app

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/ctru0009/slotwise/internal/domain"
)

// fakeStaffStore is an in-memory StaffStore that records what the use case
// asked it to do.
type fakeStaffStore struct {
	staff    []domain.Staff
	listErr  error
	writeErr error

	tenantID uuid.UUID
	created  StaffInput
	updated  StaffInput
	activeID uuid.UUID
	active   bool
	writes   int
}

func (f *fakeStaffStore) ListStaff(_ context.Context, tenantID uuid.UUID) ([]domain.Staff, error) {
	f.tenantID = tenantID
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.staff, nil
}

func (f *fakeStaffStore) CreateStaff(_ context.Context, tenantID uuid.UUID, in StaffInput) error {
	f.tenantID = tenantID
	f.created = in
	f.writes++
	return f.writeErr
}

func (f *fakeStaffStore) UpdateStaff(_ context.Context, tenantID, id uuid.UUID, in StaffInput) error {
	f.tenantID = tenantID
	f.updated = in
	f.activeID = id
	f.writes++
	return f.writeErr
}

func (f *fakeStaffStore) SetStaffActive(_ context.Context, tenantID, id uuid.UUID, active bool) error {
	f.tenantID = tenantID
	f.activeID = id
	f.active = active
	f.writes++
	return f.writeErr
}

func TestStaffListAllowsBothRoles(t *testing.T) {
	t.Parallel()
	tenantID := uuid.New()
	staff := []domain.Staff{{ID: uuid.New(), TenantID: tenantID, Name: "Ana", Active: true}}
	store := &fakeStaffStore{staff: staff}
	useCase := NewStaff(store)

	for _, actor := range []domain.User{ownerActor(tenantID), staffActor(tenantID)} {
		got, err := useCase.List(t.Context(), actor)
		if err != nil {
			t.Fatalf("List as %s: %v", actor.Role, err)
		}
		if len(got) != 1 || got[0].ID != staff[0].ID {
			t.Errorf("List as %s = %#v, want the tenant's staff", actor.Role, got)
		}
		if store.tenantID != tenantID {
			t.Errorf("List scoped to %v, want %v", store.tenantID, tenantID)
		}
	}
}

func TestStaffWritesRequireOwner(t *testing.T) {
	t.Parallel()
	tenantID := uuid.New()
	tests := []struct {
		name string
		call func(ctx context.Context, useCase *Staff, actor domain.User) error
	}{
		{name: "create", call: func(ctx context.Context, useCase *Staff, actor domain.User) error {
			return useCase.Create(ctx, actor, validStaff())
		}},
		{name: "update", call: func(ctx context.Context, useCase *Staff, actor domain.User) error {
			return useCase.Update(ctx, actor, uuid.New(), validStaff())
		}},
		{name: "set active", call: func(ctx context.Context, useCase *Staff, actor domain.User) error {
			return useCase.SetActive(ctx, actor, uuid.New(), false)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := &fakeStaffStore{}
			err := tt.call(t.Context(), NewStaff(store), staffActor(tenantID))
			if !errors.Is(err, domain.ErrForbidden) {
				t.Fatalf("error = %v, want domain.ErrForbidden", err)
			}
			if store.writes != 0 {
				t.Errorf("the store was written %d times, want 0", store.writes)
			}
		})
	}
}

func TestStaffOwnerWriteNormalisesInput(t *testing.T) {
	t.Parallel()
	tenantID := uuid.New()
	store := &fakeStaffStore{}
	in := StaffInput{Name: "  Ana  ", Email: "  Ana@Example.COM  "}

	if err := NewStaff(store).Create(t.Context(), ownerActor(tenantID), in); err != nil {
		t.Fatalf("Create: %v", err)
	}
	want := StaffInput{Name: "Ana", Email: "ana@example.com"}
	if store.created != want {
		t.Errorf("stored input = %#v, want %#v", store.created, want)
	}
	if store.tenantID != tenantID {
		t.Errorf("stored tenant = %v, want %v", store.tenantID, tenantID)
	}
}

func TestStaffWriteValidatesBeforeStore(t *testing.T) {
	t.Parallel()
	store := &fakeStaffStore{}
	err := NewStaff(store).Create(t.Context(), ownerActor(uuid.New()), StaffInput{Name: "Ana", Email: "not-an-email"})
	assertValidationError(t, err, "email")
	if store.writes != 0 {
		t.Errorf("the store was written %d times, want 0", store.writes)
	}
}

func TestStaffStoreNotFoundPropagates(t *testing.T) {
	t.Parallel()
	store := &fakeStaffStore{writeErr: domain.ErrNotFound}
	err := NewStaff(store).Update(t.Context(), ownerActor(uuid.New()), uuid.New(), validStaff())
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("error = %v, want domain.ErrNotFound", err)
	}
}

func TestStaffSetActiveReachesStore(t *testing.T) {
	t.Parallel()
	tenantID := uuid.New()
	id := uuid.New()
	store := &fakeStaffStore{}
	if err := NewStaff(store).SetActive(t.Context(), ownerActor(tenantID), id, false); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	if store.activeID != id || store.active || store.tenantID != tenantID {
		t.Errorf("SetStaffActive(%v, %v, %v), want (%v, %v, %v)",
			store.tenantID, store.activeID, store.active, tenantID, id, false)
	}
}
