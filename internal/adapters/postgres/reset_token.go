package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ctru0009/slotwise/internal/adapters/postgres/dbgen"
	"github.com/ctru0009/slotwise/internal/app"
	"github.com/ctru0009/slotwise/internal/domain"
)

// IssueResetToken drops the user's open reset tokens and stores the new hash in
// one transaction, so a user can never hold two usable links.
func (db *DB) IssueResetToken(ctx context.Context, tenantID, userID uuid.UUID, tokenHash []byte, expiresAt time.Time) error {
	return db.WithTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		q := dbgen.New(tx)
		if err := q.DeleteOpenResetTokens(ctx, userID); err != nil {
			return fmt.Errorf("dropping open reset tokens: %w", err)
		}
		if err := q.InsertResetToken(ctx, dbgen.InsertResetTokenParams{
			TokenHash: tokenHash,
			TenantID:  tenantID,
			UserID:    userID,
			ExpiresAt: expiresAt,
		}); err != nil {
			return fmt.Errorf("inserting reset token: %w", err)
		}
		return nil
	})
}

// ConsumeResetToken spends the token and replaces the user's password hash in
// one transaction, then drops the user's remaining open tokens. The UPDATE ...
// WHERE used_at IS NULL is what makes a link single-use under concurrency: two
// requests race for the same row and only the one that locks it first finds it,
// so the loser gets domain.ErrResetTokenInvalid instead of a second reset.
func (db *DB) ConsumeResetToken(ctx context.Context, tenantID uuid.UUID, tokenHash []byte, passwordHash string) (uuid.UUID, error) {
	var userID uuid.UUID
	err := db.WithTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		q := dbgen.New(tx)
		id, err := q.ConsumeResetToken(ctx, tokenHash)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("reset token: %w", domain.ErrResetTokenInvalid)
		}
		if err != nil {
			return fmt.Errorf("consuming reset token: %w", err)
		}
		affected, err := q.UpdateUserPassword(ctx, dbgen.UpdateUserPasswordParams{
			PasswordHash: passwordHash,
			ID:           id,
		})
		if err != nil {
			return fmt.Errorf("updating password: %w", err)
		}
		if affected == 0 {
			return fmt.Errorf("user %s: %w", id, domain.ErrNotFound)
		}
		if err := q.DeleteOpenResetTokens(ctx, id); err != nil {
			return fmt.Errorf("dropping remaining reset tokens: %w", err)
		}
		userID = id
		return nil
	})
	if err != nil {
		return uuid.Nil, err
	}
	return userID, nil
}

var _ app.ResetTokenStore = (*DB)(nil)
