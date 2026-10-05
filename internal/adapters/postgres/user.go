package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ctru0009/slotwise/internal/adapters/postgres/dbgen"
	"github.com/ctru0009/slotwise/internal/app"
	"github.com/ctru0009/slotwise/internal/domain"
)

var _ app.UserStore = (*DB)(nil)

// UserByEmail returns the tenant's login for email, matched case-insensitively.
func (db *DB) UserByEmail(ctx context.Context, tenantID uuid.UUID, email string) (domain.User, error) {
	var user domain.User
	err := db.WithTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		row, err := dbgen.New(tx).UserByEmail(ctx, email)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("user %q: %w", email, domain.ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("reading user by email: %w", err)
		}
		user = userFromRow(row.ID, row.TenantID, row.Email, row.PasswordHash, row.Role)
		return nil
	})
	if err != nil {
		return domain.User{}, err
	}
	return user, nil
}

// UserByID returns the tenant's login with this id.
func (db *DB) UserByID(ctx context.Context, tenantID, userID uuid.UUID) (domain.User, error) {
	var user domain.User
	err := db.WithTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		row, err := dbgen.New(tx).UserByID(ctx, userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("user %s: %w", userID, domain.ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("reading user by id: %w", err)
		}
		user = userFromRow(row.ID, row.TenantID, row.Email, row.PasswordHash, row.Role)
		return nil
	})
	if err != nil {
		return domain.User{}, err
	}
	return user, nil
}

// InsertUser creates a login inside user's tenant and reports whether it was
// inserted. A login with the same email already in that tenant is reported as
// false, nil so provisioning can be replayed; the insert never sees another
// tenant's users because the work runs in user.TenantID's transaction.
func (db *DB) InsertUser(ctx context.Context, user domain.User) (bool, error) {
	inserted := false
	err := db.WithTenant(ctx, user.TenantID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := dbgen.New(tx).InsertUser(ctx, dbgen.InsertUserParams{
			ID:           user.ID,
			TenantID:     user.TenantID,
			Email:        user.Email,
			PasswordHash: user.PasswordHash,
			Role:         string(user.Role),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inserting user: %w", err)
		}
		inserted = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return inserted, nil
}

// userFromRow copies the columns every user query selects. StaffID is left nil:
// staff logins do not exist yet, so no query returns it.
func userFromRow(id, tenantID uuid.UUID, email, passwordHash, role string) domain.User {
	return domain.User{
		ID:           id,
		TenantID:     tenantID,
		Email:        email,
		PasswordHash: passwordHash,
		Role:         domain.Role(role),
	}
}
