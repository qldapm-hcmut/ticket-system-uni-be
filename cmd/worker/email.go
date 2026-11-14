package main

import (
	"github.com/jackc/pgx/v5/pgxpool"
	notificationpg "github.com/qldapm-hcmut/ticket-system-uni/internal/notification/adapter/postgres"
	notificationsmtp "github.com/qldapm-hcmut/ticket-system-uni/internal/notification/adapter/smtp"
	notification "github.com/qldapm-hcmut/ticket-system-uni/internal/notification/application"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/platform/config"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/platform/worker"
	"go.uber.org/fx"
)

func registerEmail(lc fx.Lifecycle, c config.Email, pool *pgxpool.Pool) {
	if !c.Enabled {
		return
	}
	service := notification.NewEmailService(notificationpg.NewEmailOutbox(pool), notificationsmtp.New(c))
	worker.RegisterEmailLifecycle(lc, service)
}
