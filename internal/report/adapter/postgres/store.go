package postgres

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/db"
	platform "github.com/qldapm-hcmut/ticket-system-uni/internal/platform/postgres"
	a "github.com/qldapm-hcmut/ticket-system-uni/internal/report/application"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/shared/kernel"
)

type Store struct {
	runner *platform.TransactionRunner
	clock  kernel.Clock
}

func New(pool *pgxpool.Pool, clock kernel.Clock) *Store {
	return &Store{platform.NewTransactionRunner(pool), clock}
}

// Each report uses one snapshot, including authorization. Export bytes are not
// released to the caller until the audit transaction commits successfully.
func (s *Store) snapshot(ctx context.Context, actor, event string, run func(*db.Queries, db.GetReportEventRow, pgtype.UUID) error) error {
	var actorID, eventID pgtype.UUID
	if actorID.Scan(actor) != nil || eventID.Scan(event) != nil || !actorID.Valid || !eventID.Valid {
		return a.ErrNotFound
	}
	return s.runner.Do(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET TRANSACTION ISOLATION LEVEL REPEATABLE READ"); err != nil {
			return err
		}
		queries := db.New(tx)
		reportEvent, err := queries.GetReportEvent(ctx, db.GetReportEventParams{ActorID: actorID, EventID: eventID})
		if errors.Is(err, pgx.ErrNoRows) {
			return a.ErrNotFound
		}
		if err != nil {
			return err
		}
		return run(queries, reportEvent, actorID)
	})
}

func (s *Store) Summary(ctx context.Context, actor, event string) (out a.Summary, err error) {
	err = s.snapshot(ctx, actor, event, func(queries *db.Queries, reportEvent db.GetReportEventRow, _ pgtype.UUID) error {
		counts, err := queries.GetReportSummary(ctx, reportEvent.ID)
		if err != nil {
			return err
		}
		now := s.clock.Now()
		if !now.Before(reportEvent.StartAt.Time) {
			counts.Waitlisted = 0
		}
		out = a.Summary{
			Capacity:   reportEvent.Capacity,
			Registered: counts.Registered,
			Waitlisted: counts.Waitlisted,
			Cancelled:  counts.Cancelled,
			Promoted:   counts.Promoted,
			CheckedIn:  counts.CheckedIn,
			ByMethod: map[string]int64{
				"QR":     counts.Qr,
				"CODE":   counts.Code,
				"MANUAL": counts.Manual,
			},
			GeneratedAt: now,
		}
		if counts.Registered > 0 {
			out.AttendanceRate = float64(counts.CheckedIn) / float64(counts.Registered)
		}
		return nil
	})
	return
}

// Prevent spreadsheet formula execution even for values prefixed by whitespace.
func safeCell(value string) string {
	if strings.HasPrefix(value, "\t") || strings.HasPrefix(value, "\r") || strings.HasPrefix(value, "\n") {
		return "'" + value
	}
	trimmed := strings.TrimLeft(value, " \t\r\n")
	if len(trimmed) > 0 && strings.ContainsRune("=+-@", rune(trimmed[0])) {
		return "'" + value
	}
	return value
}

func renderCSV(rows []db.ListReportRegistrationsRow) ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteString("\xef\xbb\xbf")
	writer := csv.NewWriter(&buffer)
	writer.UseCRLF = true
	if err := writer.Write([]string{"STT", "MSSV", "Họ tên", "Email", "Trạng thái đăng ký", "Thời gian check-in", "Phương thức"}); err != nil {
		return nil, err
	}
	for i, row := range rows {
		checkedInAt, method := "", ""
		if row.CheckedInAt.Valid {
			checkedInAt = row.CheckedInAt.Time.Format(time.RFC3339)
		}
		if row.CheckinMethod.Valid {
			method = string(row.CheckinMethod.CheckinMethod)
		}
		record := []string{
			strconv.Itoa(i + 1),
			row.StudentCode.String,
			row.FullName,
			row.Email,
			string(row.Status),
			checkedInAt,
			method,
		}
		for j := range record {
			record[j] = safeCell(record[j])
		}
		if err := writer.Write(record); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	return buffer.Bytes(), writer.Error()
}

func (s *Store) Export(ctx context.Context, actor, event, kind string) (out []byte, err error) {
	err = s.snapshot(ctx, actor, event, func(queries *db.Queries, reportEvent db.GetReportEventRow, actorID pgtype.UUID) error {
		rows, err := queries.ListReportRegistrations(ctx, db.ListReportRegistrationsParams{
			EventID:        reportEvent.ID,
			AttendanceOnly: kind == "attendance",
		})
		if err != nil {
			return err
		}
		out, err = renderCSV(rows)
		if err != nil {
			return err
		}
		metadata, err := json.Marshal(map[string]any{"kind": kind, "format": "csv", "rowCount": len(rows)})
		if err != nil {
			return err
		}
		return queries.AuditReportExport(ctx, db.AuditReportExportParams{
			ActorID:  actorID,
			EventID:  reportEvent.ID,
			Metadata: metadata,
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
