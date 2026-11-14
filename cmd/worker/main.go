// Command worker declares the background worker dependency graph.
package main

import (
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	eventpg "github.com/qldapm-hcmut/ticket-system-uni/internal/event/adapter/postgres"

	"github.com/qldapm-hcmut/ticket-system-uni/internal/platform/config"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/platform/postgres"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/platform/worker"
	"go.uber.org/fx"
)

func main() {
	fx.New(
		fx.Provide(config.Load, config.LoadEmail, postgres.NewPool,
			func(pool *pgxpool.Pool) worker.Jobs { return eventpg.New(pool) },
		),
		fx.Invoke(worker.RegisterLifecycle, registerEmail),
		fx.StartTimeout(15*time.Second),
		fx.StopTimeout(10*time.Second),
	).Run()
}
