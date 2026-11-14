package application

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type emailStoreFake struct {
	rows               []EmailDelivery
	results            []emailResult
	listErr, recordErr error
	limit              int32
}
type emailResult struct {
	id   string
	sent bool
}

func (s *emailStoreFake) PendingEmails(_ context.Context, limit int32) ([]EmailDelivery, error) {
	s.limit = limit
	return s.rows, s.listErr
}
func (s *emailStoreFake) RecordEmailResult(_ context.Context, id string, sent bool) error {
	if s.recordErr != nil {
		return s.recordErr
	}
	s.results = append(s.results, emailResult{id, sent})
	return nil
}

type emailSenderFunc func(context.Context, EmailDelivery) error

func (f emailSenderFunc) Send(ctx context.Context, d EmailDelivery) error { return f(ctx, d) }

func TestEmailDeliveryPersistsEachOutcomeAndContinuesAfterSendFailure(t *testing.T) {
	store := &emailStoreFake{rows: []EmailDelivery{{ID: "one", Recipient: "one@example.test", Title: "First", Body: "First body"}, {ID: "two", Recipient: "two@example.test", Title: "Second", Body: "Second body"}}}
	var sent []EmailDelivery
	service := NewEmailService(store, emailSenderFunc(func(_ context.Context, d EmailDelivery) error {
		sent = append(sent, d)
		if d.ID == "one" {
			return errors.New("recipient one@example.test rejected")
		}
		return nil
	}))
	n, err := service.DeliverEmails(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("processed=%d err=%v", n, err)
	}
	if store.limit != 100 || !reflect.DeepEqual(sent, store.rows) {
		t.Fatal("pending messages were not delivered intact in a bounded batch")
	}
	if !reflect.DeepEqual(store.results, []emailResult{{"one", false}, {"two", true}}) {
		t.Fatalf("outcomes=%v", store.results)
	}
}
func TestEmailDeliveryStopsOnPersistenceFailureWithoutDisclosingDetails(t *testing.T) {
	store := &emailStoreFake{rows: []EmailDelivery{{ID: "one"}, {ID: "two"}}, recordErr: errors.New("private database information")}
	calls := 0
	n, err := NewEmailService(store, emailSenderFunc(func(context.Context, EmailDelivery) error { calls++; return nil })).DeliverEmails(context.Background())
	if n != 0 || !errors.Is(err, ErrEmailOutboxWrite) || calls != 1 {
		t.Fatalf("processed=%d calls=%d err=%v", n, calls, err)
	}
	if err.Error() != "email outbox update failed" {
		t.Fatal("outbox error leaked details")
	}
}
func TestEmailDeliveryReadFailureDoesNotSend(t *testing.T) {
	store := &emailStoreFake{listErr: errors.New("private database information")}
	_, err := NewEmailService(store, emailSenderFunc(func(context.Context, EmailDelivery) error { t.Fatal("sent after read failure"); return nil })).DeliverEmails(context.Background())
	if !errors.Is(err, ErrEmailOutboxRead) {
		t.Fatalf("err=%v", err)
	}
}
func TestEmailDeliveryCancellationDoesNotConsumeRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &emailStoreFake{rows: []EmailDelivery{{ID: "one"}, {ID: "two"}}}
	calls := 0
	n, err := NewEmailService(store, emailSenderFunc(func(context.Context, EmailDelivery) error { calls++; cancel(); return ctx.Err() })).DeliverEmails(ctx)
	if n != 0 || !errors.Is(err, context.Canceled) || calls != 1 || len(store.results) != 0 {
		t.Fatalf("processed=%d calls=%d outcomes=%v err=%v", n, calls, store.results, err)
	}
}
func TestEmailDeliveryCanceledBeforeRunDoesNotSend(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := &emailStoreFake{rows: []EmailDelivery{{ID: "one"}}}
	_, err := NewEmailService(store, emailSenderFunc(func(context.Context, EmailDelivery) error { t.Fatal("sent after cancellation"); return nil })).DeliverEmails(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}
