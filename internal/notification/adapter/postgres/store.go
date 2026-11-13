package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/db"
	a "github.com/qldapm-hcmut/ticket-system-uni/internal/notification/application"
	platform "github.com/qldapm-hcmut/ticket-system-uni/internal/platform/postgres"
)

type Store struct{ runner *platform.TransactionRunner }

func New(pool *pgxpool.Pool) *Store { return &Store{platform.NewTransactionRunner(pool)} }
func uuid(s string) (pgtype.UUID, error) {
	var id pgtype.UUID
	err := id.Scan(s)
	if err != nil || !id.Valid {
		return id, a.ErrValidation
	}
	return id, nil
}
func uuidString(id pgtype.UUID) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", id.Bytes[0:4], id.Bytes[4:6], id.Bytes[6:8], id.Bytes[8:10], id.Bytes[10:16])
}

func (s *Store) List(ctx context.Context, actor string, filter a.Filter) (out a.List, err error) {
	id, err := uuid(actor)
	if err != nil {
		return out, err
	}
	err = s.runner.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ, READ ONLY"); err != nil {
			return err
		}
		queries := db.New(tx)
		counts, err := queries.CountMyNotifications(ctx, db.CountMyNotificationsParams{UserID: id, UnreadOnly: filter.Unread})
		if err != nil {
			return err
		}
		rows, err := queries.ListMyNotifications(ctx, db.ListMyNotificationsParams{
			UserID:     id,
			UnreadOnly: filter.Unread,
			PageSize:   filter.PageSize,
			PageOffset: (filter.Page - 1) * filter.PageSize,
		})
		if err != nil {
			return err
		}
		out = a.List{
			Items:       make([]a.Notification, 0, len(rows)),
			TotalItems:  counts.TotalItems,
			UnreadCount: counts.UnreadCount,
		}
		for _, row := range rows {
			notification := a.Notification{
				ID:        uuidString(row.ID),
				Type:      string(row.Type),
				Title:     row.Title,
				Body:      row.Body,
				CreatedAt: row.CreatedAt.Time,
			}
			if row.EventID.Valid {
				eventID := uuidString(row.EventID)
				notification.EventID = &eventID
			}
			if row.ReadAt.Valid {
				readAt := row.ReadAt.Time
				notification.ReadAt = &readAt
			}
			out.Items = append(out.Items, notification)
		}
		return nil
	})
	if err != nil {
		return a.List{}, err
	}
	return out, nil
}

func (s *Store) MarkRead(ctx context.Context, actor string, options a.ReadOptions) (out a.ReadResult, err error) {
	id, err := uuid(actor)
	if err != nil {
		return out, err
	}
	ids := make([]pgtype.UUID, 0, len(options.IDs))
	for _, raw := range options.IDs {
		notificationID, err := uuid(raw)
		if err != nil {
			return out, err
		}
		ids = append(ids, notificationID)
	}
	err = s.runner.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		// Serialize read acknowledgements for the same user, including mark-all.
		if _, err := tx.Exec(ctx, "SELECT id FROM users WHERE id=$1 FOR UPDATE", id); err != nil {
			return err
		}
		queries := db.New(tx)
		updated, err := queries.MarkMyNotificationsRead(ctx, db.MarkMyNotificationsReadParams{
			UserID:  id,
			ReadAll: options.All,
			Ids:     ids,
		})
		if err != nil {
			return err
		}
		counts, err := queries.CountMyNotifications(ctx, db.CountMyNotificationsParams{UserID: id})
		if err != nil {
			return err
		}
		out = a.ReadResult{UpdatedCount: updated, UnreadCount: counts.UnreadCount}
		return nil
	})
	if err != nil {
		return a.ReadResult{}, err
	}
	return out, nil
}
