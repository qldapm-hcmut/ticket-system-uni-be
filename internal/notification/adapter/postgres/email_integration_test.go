//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	a "github.com/qldapm-hcmut/ticket-system-uni/internal/notification/application"
)

type integrationEmailSender func(context.Context, a.EmailDelivery) error

func (f integrationEmailSender) Send(ctx context.Context, d a.EmailDelivery) error { return f(ctx, d) }

func TestEmailOutboxRetriesAndPersistsAcrossServiceRestart(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	var user string
	if err := pool.QueryRow(ctx, `INSERT INTO users(email,full_name) VALUES ('student@hcmut.edu.vn','Test') RETURNING id::text`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	insert := func(title string) string {
		t.Helper()
		var id string
		if err := pool.QueryRow(ctx, `INSERT INTO notifications(user_id,type,title,body) VALUES ($1,'REGISTERED',$2,'Saved body') RETURNING id::text`, user, title).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	failedID := insert("Always fails")
	recoveredID := insert("Recovers")
	sentID := insert("Succeeds")
	calls := map[string]int{}
	sender := integrationEmailSender(func(ctx context.Context, d a.EmailDelivery) error {
		calls[d.ID]++
		// A real update on the same row completes while SMTP is in progress:
		// no transaction/row lock may span the external send.
		queryCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		if _, err := pool.Exec(queryCtx, `UPDATE notifications SET read_at=COALESCE(read_at,now()) WHERE id=$1`, d.ID); err != nil {
			t.Fatalf("SMTP held database lock: %v", err)
		}
		if d.Recipient != "student@hcmut.edu.vn" || d.Body != "Saved body" {
			t.Fatal("message data not preserved")
		}
		if d.ID == failedID || (d.ID == recoveredID && calls[d.ID] == 1) {
			return errors.New("private SMTP response student@hcmut.edu.vn")
		}
		return nil
	})
	for run := 1; run <= 6; run++ {
		// Reconstructing adapters and service must retain attempt state in PostgreSQL.
		service := a.NewEmailService(NewEmailOutbox(pool), sender)
		if _, err := service.DeliverEmails(ctx); err != nil {
			t.Fatal(err)
		}
		var status string
		var attempts int
		var lastError *string
		if err := pool.QueryRow(ctx, `SELECT email_status::text,email_attempts,last_error FROM notifications WHERE id=$1`, failedID).Scan(&status, &attempts, &lastError); err != nil {
			t.Fatal(err)
		}
		wantStatus := "PENDING"
		wantAttempts := run
		if run >= 5 {
			wantStatus = "FAILED"
			wantAttempts = 5
		}
		if status != wantStatus || attempts != wantAttempts || lastError == nil || *lastError != "Email delivery failed" {
			t.Fatalf("run=%d status=%s attempts=%d error=%v", run, status, attempts, lastError)
		}
	}
	if calls[failedID] != 5 || calls[recoveredID] != 2 || calls[sentID] != 1 {
		t.Fatalf("send counts=%v", calls)
	}
	for id, wantAttempts := range map[string]int{recoveredID: 1, sentID: 0} {
		var status string
		var attempts int
		var lastError *string
		if err := pool.QueryRow(ctx, `SELECT email_status::text,email_attempts,last_error FROM notifications WHERE id=$1`, id).Scan(&status, &attempts, &lastError); err != nil {
			t.Fatal(err)
		}
		if status != "SENT" || attempts != wantAttempts || lastError != nil {
			t.Fatalf("status=%s attempts=%d lastError=%v", status, attempts, lastError)
		}
	}
	if err := NewEmailOutbox(pool).RecordEmailResult(ctx, sentID, false); err == nil {
		t.Fatal("stale outcome silently accepted")
	}
}
func TestEmailPendingOrderAndBatch(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	var user string
	if err := pool.QueryRow(ctx, `INSERT INTO users(email,full_name) VALUES ('order@hcmut.edu.vn','Test') RETURNING id::text`).Scan(&user); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"00000000-0000-0000-0000-000000000002", "00000000-0000-0000-0000-000000000001"} {
		if _, err := pool.Exec(ctx, `INSERT INTO notifications(id,user_id,type,title,body,created_at) VALUES ($1,$2,'WAITLISTED','Title','Body','2026-01-01T00:00:00Z')`, id, user); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := NewEmailOutbox(pool).PendingEmails(ctx, 1)
	if err != nil || len(rows) != 1 || rows[0].ID != "00000000-0000-0000-0000-000000000001" {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
}
