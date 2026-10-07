package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ctru0009/slotwise/internal/adapters/postgres/dbgen"
	"github.com/ctru0009/slotwise/internal/app"
	"github.com/ctru0009/slotwise/internal/domain"
)

// auditSubject is the row an audit entry is about. Exactly one field is set,
// which is what the table's num_nonnulls check enforces.
type auditSubject struct {
	booking uuid.UUID
	service uuid.UUID
	staff   uuid.UUID
}

// recordAudit writes one audit row in the caller's transaction. The method
// whose change it records calls it after that change affected a row and before
// the transaction commits, so the change and its audit row are one atomic fact:
// neither can land without the other.
func recordAudit(ctx context.Context, q *dbgen.Queries, tenantID, actor uuid.UUID, action domain.AuditAction, subject auditSubject) error {
	err := q.InsertAuditLog(ctx, dbgen.InsertAuditLogParams{
		TenantID:    tenantID,
		ActorUserID: nullableUUID(actor),
		Action:      string(action),
		BookingID:   nullableUUID(subject.booking),
		ServiceID:   nullableUUID(subject.service),
		StaffID:     nullableUUID(subject.staff),
	})
	if err != nil {
		return fmt.Errorf("recording audit entry %s: %w", action, err)
	}
	return nil
}

// nullableUUID maps the zero uuid to SQL NULL. The public booking flow has no
// login to name, and an audit row that names one subject leaves the other two
// columns empty.
func nullableUUID(id uuid.UUID) pgtype.UUID {
	if id == uuid.Nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: id, Valid: true}
}

// ListAuditLog returns the tenant's newest audit rows with the labels the
// dashboard shows. The result is never nil.
func (db *DB) ListAuditLog(ctx context.Context, tenantID uuid.UUID, limit int) ([]app.AuditEntry, error) {
	entries := []app.AuditEntry{}
	err := db.WithTenant(ctx, tenantID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := dbgen.New(tx).ListAuditLog(ctx, narrowInt32(limit))
		if err != nil {
			return fmt.Errorf("listing audit log: %w", err)
		}
		for _, row := range rows {
			entries = append(entries, toAuditEntry(row))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// toAuditEntry maps one joined row: a NULL actor reads as an empty email.
func toAuditEntry(row dbgen.ListAuditLogRow) app.AuditEntry {
	entry := app.AuditEntry{
		ID:              row.ID,
		Action:          domain.AuditAction(row.Action),
		CreatedAt:       row.CreatedAt,
		BookingCustomer: row.BookingCustomer.String,
		ServiceName:     row.ServiceName.String,
		StaffName:       row.StaffName.String,
	}
	if row.ActorEmail.Valid {
		entry.ActorEmail = row.ActorEmail.String
	}
	if row.BookingStartsAt.Valid {
		starts := row.BookingStartsAt.Time
		entry.BookingStarts = &starts
	}
	return entry
}

var _ app.AuditStore = (*DB)(nil)
