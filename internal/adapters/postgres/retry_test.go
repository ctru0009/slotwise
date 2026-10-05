//go:build integration

package postgres_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ctru0009/slotwise/internal/testsupport/pgtest"
)

// TestWithTenantRetrySurvivesDeadlock forces a lock cycle between two
// transactions and requires both to end up committed. Without the retry the
// victim surfaces SQLSTATE 40P01 to the caller even though the work was fine to
// replay.
func TestWithTenantRetrySurvivesDeadlock(t *testing.T) {
	t.Parallel()
	appDSN, ownerDSN := pgtest.Start(t)
	owner := pgtest.Owner(t, ownerDSN)
	tenant := pgtest.Seed(t, owner, "retry")
	second := pgtest.SeedStaff(t, owner, tenant, "Retry second", "retry-second@example.com")
	db := pgtest.AppDB(t, appDSN)

	rows := [2]uuid.UUID{tenant.Staff, second}

	var wg sync.WaitGroup
	failures := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			// Each side takes its rows in the opposite order, so one of them is
			// the deadlock victim.
			first, last := rows[slot], rows[1-slot]
			failures[slot] = db.WithTenantRetry(t.Context(), tenant.Tenant, func(ctx context.Context, tx pgx.Tx) error {
				if _, err := tx.Exec(ctx, "SELECT 1 FROM staff WHERE id = $1::uuid FOR UPDATE", first.String()); err != nil {
					return err
				}
				time.Sleep(50 * time.Millisecond)
				_, err := tx.Exec(ctx, "SELECT 1 FROM staff WHERE id = $1::uuid FOR UPDATE", last.String())
				return err
			})
		}(i)
	}
	wg.Wait()

	for slot, err := range failures {
		if err != nil {
			t.Errorf("transaction %d failed after retries: %v", slot, err)
		}
	}
}
