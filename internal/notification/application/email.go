package application

import (
	"context"
	"errors"
)

var (
	ErrEmailOutboxRead  = errors.New("email outbox read failed")
	ErrEmailOutboxWrite = errors.New("email outbox update failed")
)

// EmailDelivery contains private message data; never log it.
type EmailDelivery struct{ ID, Recipient, Title, Body string }
type EmailOutbox interface {
	PendingEmails(context.Context, int32) ([]EmailDelivery, error)
	RecordEmailResult(context.Context, string, bool) error
}
type EmailSender interface {
	Send(context.Context, EmailDelivery) error
}
type EmailService struct {
	outbox EmailOutbox
	sender EmailSender
}

func NewEmailService(outbox EmailOutbox, sender EmailSender) *EmailService {
	return &EmailService{outbox: outbox, sender: sender}
}

// DeliverEmails is called by one active consumer. Each row is attempted once
// per run, with no database transaction held during external delivery. SMTP
// acceptance followed by a failed database update can cause duplicate delivery.
func (s *EmailService) DeliverEmails(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	rows, err := s.outbox.PendingEmails(ctx, 100)
	if err != nil {
		return 0, ErrEmailOutboxRead
	}
	var processed int64
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return processed, err
		}
		sendErr := s.sender.Send(ctx, row)
		// Shutdown must not consume the remaining retry budget.
		if err := ctx.Err(); err != nil {
			return processed, err
		}
		if err := s.outbox.RecordEmailResult(ctx, row.ID, sendErr == nil); err != nil {
			return processed, ErrEmailOutboxWrite
		}
		processed++
	}
	return processed, nil
}
