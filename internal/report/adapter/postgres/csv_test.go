package postgres

import (
	"bytes"
	"encoding/csv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/db"
)

func TestSafeCell(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"", ""}, {"Nguyễn An", "Nguyễn An"}, {"normal,quoted", "normal,quoted"},
		{"=1+1", "'=1+1"}, {" +cmd", "' +cmd"}, {"-1", "'-1"}, {"@SUM(A1)", "'@SUM(A1)"},
		{"\tplain", "'\tplain"}, {"\rplain", "'\rplain"}, {"\nplain", "'\nplain"}, {" \t=1", "' \t=1"},
		{" \t\r\n@SUM(A1)", "' \t\r\n@SUM(A1)"}, {"   ", "   "},
		{"\t", "'\t"}, {" \tplain", " \tplain"}, {" \rplain", " \rplain"}, {" \nplain", " \nplain"},
		{"'already protected", "'already protected"}, {"＝1+1", "＝1+1"},
	} {
		if got := safeCell(tc.input); got != tc.want {
			t.Errorf("safeCell(%q)=%q want %q", tc.input, got, tc.want)
		}
	}
}
func TestRenderCSV(t *testing.T) {
	at := time.Date(2026, 10, 24, 10, 30, 0, 0, time.UTC)
	data, err := renderCSV([]db.ListReportRegistrationsRow{
		{StudentCode: pgtype.Text{String: "00123", Valid: true}, FullName: "Nguyễn, \"An\"", Email: "student@example.invalid", Status: db.RegistrationStatusREGISTERED, CheckedInAt: pgtype.Timestamptz{Time: at, Valid: true}, CheckinMethod: db.NullCheckinMethod{CheckinMethod: db.CheckinMethodQR, Valid: true}},
		{FullName: "=1+1", Status: db.RegistrationStatusWAITLISTED},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) || !bytes.Contains(data, []byte("\r\n")) {
		t.Fatal("missing BOM/CRLF")
	}
	rows, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(data), "\xef\xbb\xbf"))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || len(rows[0]) != 7 {
		t.Fatalf("rows: %v", rows)
	}
	if rows[1][1] != "00123" || rows[1][2] != "Nguyễn, \"An\"" || rows[1][5] != at.Format(time.RFC3339) || rows[1][6] != "QR" {
		t.Fatal(rows[1])
	}
	if rows[2][2] != "'=1+1" || rows[2][1] != "" || rows[2][5] != "" || rows[2][6] != "" {
		t.Fatal(rows[2])
	}
	empty, err := renderCSV(nil)
	if err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(bytes.NewReader(empty[3:])).ReadAll()
	if err != nil || len(records) != 1 {
		t.Fatalf("empty export: %v %v", records, err)
	}
}
