package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/db"
	a "github.com/qldapm-hcmut/ticket-system-uni/internal/notification/application"
)

// EmailOutbox uses separate short queries, never a transaction across SMTP.
type EmailOutbox struct{ queries *db.Queries }

func NewEmailOutbox(pool *pgxpool.Pool) *EmailOutbox { return &EmailOutbox{queries: db.New(pool)} }
func (s *EmailOutbox) PendingEmails(ctx context.Context, limit int32) ([]a.EmailDelivery, error) {
	rows, err := s.queries.ListPendingEmails(ctx, limit)
	if err != nil {
		return nil, err
	}
	deliveries := make([]a.EmailDelivery, 0, len(rows))
	for _, row := range rows {
		deliveries = append(deliveries, a.EmailDelivery{ID: row.ID.String(), Recipient: row.Recipient, Title: row.Title, Body: row.Body})
	}
	return deliveries, nil
}
func (s *EmailOutbox) RecordEmailResult(ctx context.Context, id string, sent bool) error {
	notificationID, err := uuid(id)
	if err != nil {
		return err
	}
	changed, err := s.queries.RecordEmailDelivery(ctx, db.RecordEmailDeliveryParams{ID: notificationID, Sent: sent})
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("email outbox outcome was not recorded")
	}
	return nil
}
