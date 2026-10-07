package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ctru0009/slotwise/internal/clock"
	"github.com/ctru0009/slotwise/internal/domain"
)

// Landing is what the public page of one business shows: the business itself
// and the services it currently offers, with the local date a slot search
// starts from.
type Landing struct {
	Tenant   domain.Tenant
	Services []domain.Service
	Today    domain.LocalDate
}

// Public implements the reads a visitor makes before any session exists. The
// tenant comes from the URL, so nothing here takes an actor.
type Public struct {
	tenants  TenantStore
	services ServiceStore
	clock    clock.Clock
}

// NewPublic returns a Public resolving tenants by slug through tenants,
// reading the catalogue through services and taking the current instant from
// clk.
func NewPublic(tenants TenantStore, services ServiceStore, clk clock.Clock) *Public {
	return &Public{tenants: tenants, services: services, clock: clk}
}

// Landing returns the business a slug names and the services it offers today.
// Only active services are listed: deactivation is how an owner withdraws one,
// and a withdrawn service must not be published, bookable or priced anywhere a
// visitor can see. An unknown slug reports domain.ErrTenantNotFound.
func (p *Public) Landing(ctx context.Context, slug string) (Landing, error) {
	tenant, err := p.tenants.TenantBySlug(ctx, slug)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		// The store's miss is the public route's 404: no business is served at
		// this slug.
		return Landing{}, domain.ErrTenantNotFound
	case err != nil:
		return Landing{}, fmt.Errorf("resolving tenant %q: %w", slug, err)
	}
	offered, err := p.services.ListServices(ctx, tenant.ID)
	if err != nil {
		return Landing{}, fmt.Errorf("listing services: %w", err)
	}
	loc, err := time.LoadLocation(tenant.Timezone)
	if err != nil {
		return Landing{}, fmt.Errorf("loading tenant timezone %q: %w", tenant.Timezone, err)
	}
	active := make([]domain.Service, 0, len(offered))
	for _, service := range offered {
		if service.Active {
			active = append(active, service)
		}
	}
	return Landing{
		Tenant:   tenant,
		Services: active,
		Today:    localDateOf(p.clock.Now(), loc),
	}, nil
}
