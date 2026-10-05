//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/ctru0009/slotwise/internal/domain"
)

// TestConcurrentBookingsForOneSlot fires attempts at the same staff member and
// window; the exclusion constraint, not the application, must pick one winner.
func TestConcurrentBookingsForOneSlot(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	appDSN, ownerDSN := startPostgres(t)
	owner := newOwnerPool(t, ownerDSN)
	tenant := seed(t, owner, "race")

	db := newAppDB(t, appDSN)

	const attempts = 100

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		wins     int
		taken    int
		failures []error
	)

	for i := range attempts {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			err := db.WithTenant(ctx, tenant.tenant, func(ctx context.Context, tx pgx.Tx) error {
				_, execErr := tx.Exec(ctx, `
					INSERT INTO bookings (tenant_id, staff_id, service_id, customer_name, customer_email,
					                      starts_at, ends_at, status, idempotency_key)
					VALUES ($1::uuid, $2::uuid, $3::uuid, 'Racer', 'racer@example.com',
					        '2026-11-02T09:00:00Z', '2026-11-02T09:30:00Z', 'confirmed', $4)`,
					tenant.tenant.String(), tenant.staff.String(), tenant.service.String(),
					fmt.Sprintf("race-%d", n))
				return execErr
			})

			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, domain.ErrSlotTaken):
				taken++
			default:
				failures = append(failures, err)
			}
		}(i)
	}
	wg.Wait()

	if wins != 1 {
		t.Errorf("%d attempts booked the slot, want exactly 1", wins)
	}
	if taken != attempts-1 {
		t.Errorf("%d attempts returned ErrSlotTaken, want %d", taken, attempts-1)
	}
	if len(failures) > 0 {
		t.Errorf("unexpected failures (%d), first: %v", len(failures), failures[0])
	}
}
