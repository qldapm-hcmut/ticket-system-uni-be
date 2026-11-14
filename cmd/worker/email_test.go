package main

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/platform/config"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"
)

func TestDisabledEmailDoesNotAccessDatabase(t *testing.T) {
	t.Setenv("EMAIL_ENABLED", "false")
	// A nil pool proves disabled registration neither queries nor sends.
	app := fxtest.New(t, fx.Provide(func() *pgxpool.Pool { return nil }, config.LoadEmail), fx.Invoke(registerEmail))
	app.RequireStart()
	time.Sleep(10 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := app.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}
func TestEnabledInvalidEmailConfigRejectsWorkerStartup(t *testing.T) {
	t.Setenv("EMAIL_ENABLED", "true")
	t.Setenv("SMTP_HOST", "")
	app := fx.New(fx.Provide(func() *pgxpool.Pool { return nil }, config.LoadEmail), fx.Invoke(registerEmail), fx.NopLogger)
	if app.Err() == nil {
		t.Fatal("worker accepted enabled but invalid email configuration")
	}
}
