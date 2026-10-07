package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ctru0009/slotwise/internal/clock"
	"github.com/ctru0009/slotwise/internal/domain"
)

// landingSlug is the public address the landing tests resolve.
const landingSlug = "acme"

// fakeLandingTenantStore is the in-memory TenantStore the landing tests read
// through. An unknown slug reports domain.ErrNotFound, exactly as the postgres
// store does, so translating the miss into domain.ErrTenantNotFound is
// Landing's job rather than the fake's.
type fakeLandingTenantStore struct {
	bySlug    map[string]domain.Tenant
	lookupErr error
}

func (f *fakeLandingTenantStore) TenantBySlug(_ context.Context, slug string) (domain.Tenant, error) {
	if f.lookupErr != nil {
		return domain.Tenant{}, f.lookupErr
	}
	tenant, found := f.bySlug[slug]
	if !found {
		return domain.Tenant{}, domain.ErrNotFound
	}
	return tenant, nil
}

func (f *fakeLandingTenantStore) TenantByID(_ context.Context, tenantID uuid.UUID) (domain.Tenant, error) {
	for _, tenant := range f.bySlug {
		if tenant.ID == tenantID {
			return tenant, nil
		}
	}
	return domain.Tenant{}, domain.ErrNotFound
}

func (f *fakeLandingTenantStore) InsertTenant(_ context.Context, tenant domain.Tenant) (bool, error) {
	if f.bySlug == nil {
		f.bySlug = make(map[string]domain.Tenant)
	}
	if _, exists := f.bySlug[tenant.Slug]; exists {
		return false, nil
	}
	f.bySlug[tenant.Slug] = tenant
	return true, nil
}

// fakeLandingServiceStore is the in-memory ServiceStore the landing tests read
// through. Landing never writes, so the three write methods exist only to
// satisfy the interface.
type fakeLandingServiceStore struct {
	services []domain.Service
	tenantID uuid.UUID
	listErr  error
}

func (f *fakeLandingServiceStore) ListServices(_ context.Context, tenantID uuid.UUID) ([]domain.Service, error) {
	f.tenantID = tenantID
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.services, nil
}

func (f *fakeLandingServiceStore) CreateService(context.Context, uuid.UUID, uuid.UUID, ServiceInput) error {
	return nil
}

func (f *fakeLandingServiceStore) UpdateService(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, ServiceInput) error {
	return nil
}

func (f *fakeLandingServiceStore) SetServiceActive(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, bool) error {
	return nil
}

// landingFixture is one tenant wired to Public through the fake stores, with a
// clock the test drives.
type landingFixture struct {
	public   *Public
	tenants  *fakeLandingTenantStore
	services *fakeLandingServiceStore
	tenant   domain.Tenant
}

func newLandingFixture(t *testing.T, timezone string, now time.Time) landingFixture {
	t.Helper()
	tenant := domain.Tenant{ID: uuid.New(), Slug: landingSlug, Name: "Acme", Timezone: timezone}
	tenants := &fakeLandingTenantStore{bySlug: map[string]domain.Tenant{landingSlug: tenant}}
	services := &fakeLandingServiceStore{}
	return landingFixture{
		public:   NewPublic(tenants, services, clock.NewFake(now)),
		tenants:  tenants,
		services: services,
		tenant:   tenant,
	}
}

// TestLandingListsOnlyActiveServices pins the withdrawal rule: deactivating a
// service takes it off the public page, so a visitor cannot see, book or be
// quoted a service the owner has withdrawn.
func TestLandingListsOnlyActiveServices(t *testing.T) {
	t.Parallel()
	f := newLandingFixture(t, "Europe/Berlin", time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC))
	active := domain.Service{
		ID: uuid.New(), TenantID: f.tenant.ID, Name: "Cut",
		DurationMinutes: 30, PriceCents: 3_500, Active: true,
	}
	inactive := domain.Service{
		ID: uuid.New(), TenantID: f.tenant.ID, Name: "Withdrawn",
		DurationMinutes: 20, PriceCents: 1_000, Active: false,
	}
	f.services.services = []domain.Service{active, inactive}

	got, err := f.public.Landing(t.Context(), landingSlug)
	if err != nil {
		t.Fatalf("Landing: %v", err)
	}
	if len(got.Services) != 1 || got.Services[0].ID != active.ID {
		t.Errorf("Landing services = %#v, want only the active %q", got.Services, active.Name)
	}
	if got.Tenant.ID != f.tenant.ID {
		t.Errorf("Landing tenant = %v, want %v", got.Tenant.ID, f.tenant.ID)
	}
	if f.services.tenantID != f.tenant.ID {
		t.Errorf("services read for tenant %v, want %v", f.services.tenantID, f.tenant.ID)
	}
}

// TestLandingReportsUnknownSlug pins the sentinel the public landing route
// matches on to render a 404. The store reports domain.ErrNotFound, as the
// postgres one does, so Landing is what has to turn that miss into
// domain.ErrTenantNotFound.
func TestLandingReportsUnknownSlug(t *testing.T) {
	t.Parallel()
	f := newLandingFixture(t, "Europe/Berlin", time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC))

	if _, err := f.public.Landing(t.Context(), "missing"); !errors.Is(err, domain.ErrTenantNotFound) {
		t.Fatalf("Landing(unknown slug) error = %v, want domain.ErrTenantNotFound", err)
	}
}

// TestLandingRelaysOtherTenantLookupFailures is the other half of the miss
// rule: a lookup that fails for any reason other than a missing row is an
// outage, not a 404, so it must not be reported as an unknown tenant.
func TestLandingRelaysOtherTenantLookupFailures(t *testing.T) {
	t.Parallel()
	f := newLandingFixture(t, "Europe/Berlin", time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC))
	f.tenants.lookupErr = errors.New("connection reset")

	_, err := f.public.Landing(t.Context(), landingSlug)
	if err == nil {
		t.Fatal("Landing with a failing tenant store returned no error")
	}
	if errors.Is(err, domain.ErrTenantNotFound) {
		t.Errorf("Landing error = %v, want the lookup failure, not domain.ErrTenantNotFound", err)
	}
	if !strings.Contains(err.Error(), "connection reset") {
		t.Errorf("error %v does not wrap the store's failure", err)
	}
}

// TestLandingTodayIsTenantLocal pins Today to the injected clock read in the
// tenant's timezone. Every case crosses local midnight, so the tenant-local
// date differs from the UTC one and a UTC read fails.
//
// The milestone brief named 2026-03-29T22:30Z as the CET case, but Berlin had
// already sprung forward that morning, so that instant reads as 2026-03-30
// 00:30 CEST. The CET case is one day earlier; 2026-03-29T22:30Z is kept as a
// CEST case, where it also crosses midnight.
func TestLandingTodayIsTenantLocal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		now  time.Time
		want domain.LocalDate
		utc  domain.LocalDate
	}{
		{
			name: "CET after local midnight, before the spring change",
			now:  time.Date(2026, time.March, 28, 23, 30, 0, 0, time.UTC),
			want: domain.LocalDate{Year: 2026, Month: time.March, Day: 29},
			utc:  domain.LocalDate{Year: 2026, Month: time.March, Day: 28},
		},
		{
			name: "CEST after local midnight, after the spring change",
			now:  time.Date(2026, time.March, 29, 22, 30, 0, 0, time.UTC),
			want: domain.LocalDate{Year: 2026, Month: time.March, Day: 30},
			utc:  domain.LocalDate{Year: 2026, Month: time.March, Day: 29},
		},
		{
			name: "CEST an hour past local midnight",
			now:  time.Date(2026, time.March, 29, 23, 30, 0, 0, time.UTC),
			want: domain.LocalDate{Year: 2026, Month: time.March, Day: 30},
			utc:  domain.LocalDate{Year: 2026, Month: time.March, Day: 29},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.want == tt.utc {
				t.Fatalf("case %s does not cross a date boundary between UTC and Berlin", tt.name)
			}
			f := newLandingFixture(t, "Europe/Berlin", tt.now)

			got, err := f.public.Landing(t.Context(), landingSlug)
			if err != nil {
				t.Fatalf("Landing: %v", err)
			}
			if !got.Today.Equal(tt.want) {
				t.Errorf("Landing Today = %s, want the tenant-local %s, not the UTC %s",
					got.Today, tt.want, tt.utc)
			}
		})
	}
}

// TestLandingRejectsUnloadableTimezone pins that a tenant whose timezone does
// not load is an error. Falling back to UTC would silently publish the wrong
// day, which is worse than failing the page.
func TestLandingRejectsUnloadableTimezone(t *testing.T) {
	t.Parallel()
	f := newLandingFixture(t, "Not/AZone", time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC))
	f.services.services = []domain.Service{{
		ID: uuid.New(), TenantID: f.tenant.ID, Name: "Cut", Active: true,
	}}

	got, err := f.public.Landing(t.Context(), landingSlug)
	if err == nil {
		t.Fatalf("Landing with an unloadable timezone returned %#v and no error, want an error", got)
	}
	if !strings.Contains(err.Error(), "Not/AZone") {
		t.Errorf("error %v does not name the unloadable timezone", err)
	}
}
