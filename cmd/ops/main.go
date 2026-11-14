// Command ops performs verified internal account maintenance, never public API operations.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	opspg "github.com/qldapm-hcmut/ticket-system-uni/internal/ops/adapter/postgres"
	a "github.com/qldapm-hcmut/ticket-system-uni/internal/ops/application"
)

type opener func(context.Context) (a.Store, func(), error)

var usage = errors.New("usage: ops students import --file CSV --operator ID --ticket REF [--dry-run]; ops admin bootstrap --email EMAIL --full-name NAME --operator ID --ticket REF; ops admin grant|revoke --user-id UUID --operator ID --ticket REF")

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout, openDatabase); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func openDatabase(ctx context.Context) (a.Store, func(), error) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return nil, nil, errors.New("DATABASE_URL is required")
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, nil, err
	}
	if err = pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, nil, err
	}
	return opspg.New(pool), pool.Close, nil
}
func safeError(err error) error {
	var row *a.RowConflict
	if errors.As(err, &row) {
		return fmt.Errorf("row %d: account identity conflict; verify source data", row.Row)
	}
	switch {
	case errors.Is(err, a.ErrConflict):
		return errors.New("account identity conflict; verify source data or use explicit admin grant")
	case errors.Is(err, a.ErrNotFound):
		return a.ErrNotFound
	case errors.Is(err, a.ErrValidation):
		return a.ErrValidation
	case errors.Is(err, context.Canceled):
		return errors.New("operation cancelled; verify state before retrying")
	default:
		return errors.New("operation failed; commit outcome may be unknown, verify state before retrying")
	}
}
func run(ctx context.Context, args []string, out io.Writer, open opener) error {
	if len(args) < 2 {
		return usage
	}
	mode, action := args[0], args[1]
	flags := flag.NewFlagSet("ops", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var attr a.Attribution
	flags.StringVar(&attr.Operator, "operator", "", "verified internal operator ID")
	flags.StringVar(&attr.Ticket, "ticket", "", "verification ticket reference")
	var file string
	var dryRun bool
	var req a.AdminRequest
	switch {
	case mode == "students" && action == "import":
		flags.StringVar(&file, "file", "", "UTF-8 CSV roster")
		flags.BoolVar(&dryRun, "dry-run", false, "validate database writes and roll back")
	case mode == "admin" && action == "bootstrap":
		flags.StringVar(&req.Email, "email", "", "school email")
		flags.StringVar(&req.FullName, "full-name", "", "bootstrap full name")
	case mode == "admin" && (action == "grant" || action == "revoke"):
		flags.StringVar(&req.UserID, "user-id", "", "target user UUID")
	default:
		return usage
	}
	if err := flags.Parse(args[2:]); err != nil || flags.NArg() != 0 {
		return usage
	}
	var importReq a.ImportRequest
	if mode == "students" {
		if file == "" {
			return usage
		}
		input, err := os.Open(file)
		if err != nil {
			return errors.New("cannot read roster file")
		}
		rows, readErr := a.ReadStudents(input)
		closeErr := input.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return errors.New("cannot read roster file")
		}
		importReq = a.ImportRequest{Attribution: attr, Students: rows, DryRun: dryRun}
		if err := importReq.Validate(); err != nil {
			return err
		}
	} else {
		req.Action = action
		req.Attribution = attr
		if err := req.Validate(); err != nil {
			return err
		}
	}
	// Bound connections/locks and respond to operator cancellation. No dotenv reads.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	store, closeDB, err := open(ctx)
	if err != nil {
		return errors.New("cannot connect database; check DATABASE_URL and database availability")
	}
	defer closeDB()
	service := a.New(store)
	if mode == "students" {
		result, err := service.Import(ctx, importReq)
		if err != nil {
			return safeError(err)
		}
		status := "committed"
		if dryRun {
			status = "dry-run (rolled back)"
		}
		_, err = fmt.Fprintf(out, "%s: created=%d enriched=%d unchanged=%d\n", status, result.Created, result.Enriched, result.Unchanged)
		return err
	}
	result, err := service.Admin(ctx, req)
	if err != nil {
		return safeError(err)
	}
	_, err = fmt.Fprintf(out, "admin %s: changed=%t\n", action, result.Changed)
	return err
}
