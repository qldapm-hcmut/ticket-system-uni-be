package main

import (
	"context"
	"net/http"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	adminhttp "github.com/qldapm-hcmut/ticket-system-uni/internal/admin/adapter/http"
	adminpg "github.com/qldapm-hcmut/ticket-system-uni/internal/admin/adapter/postgres"
	adminapp "github.com/qldapm-hcmut/ticket-system-uni/internal/admin/application"
	googleauth "github.com/qldapm-hcmut/ticket-system-uni/internal/auth/adapter/google"
	authhttp "github.com/qldapm-hcmut/ticket-system-uni/internal/auth/adapter/http"
	authpg "github.com/qldapm-hcmut/ticket-system-uni/internal/auth/adapter/postgres"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/auth/adapter/token"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/auth/application"
	eventpg "github.com/qldapm-hcmut/ticket-system-uni/internal/event/adapter/postgres"
	notifhttp "github.com/qldapm-hcmut/ticket-system-uni/internal/notification/adapter/http"
	notifpg "github.com/qldapm-hcmut/ticket-system-uni/internal/notification/adapter/postgres"
	notifapp "github.com/qldapm-hcmut/ticket-system-uni/internal/notification/application"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/platform/config"
	reghttp "github.com/qldapm-hcmut/ticket-system-uni/internal/registration/adapter/http"
	regpg "github.com/qldapm-hcmut/ticket-system-uni/internal/registration/adapter/postgres"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/registration/adapter/qrtoken"
	regapp "github.com/qldapm-hcmut/ticket-system-uni/internal/registration/application"
	reporthttp "github.com/qldapm-hcmut/ticket-system-uni/internal/report/adapter/http"
	reportpg "github.com/qldapm-hcmut/ticket-system-uni/internal/report/adapter/postgres"
	reportapp "github.com/qldapm-hcmut/ticket-system-uni/internal/report/application"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/shared/kernel"
	"go.uber.org/fx"
)

// Auth setup runs before the HTTP lifecycle hook starts accepting requests.
func registerAuth(lc fx.Lifecycle, e *echo.Echo, pool *pgxpool.Pool) error {
	clientID, err := config.LoadGoogleClientID()
	if err != nil {
		return err
	}
	cfg, err := config.LoadAuthToken()
	if err != nil {
		return err
	}
	tokens, err := token.New(cfg.JWTSecret, kernel.SystemClock{})
	if err != nil {
		return err
	}
	qrSecret, err := config.LoadQRSecret()
	if err != nil {
		return err
	}
	qr, err := qrtoken.New(qrSecret)
	if err != nil {
		return err
	}
	lifetime, cancel := context.WithCancel(context.Background())
	lc.Append(fx.Hook{OnStart: func(ctx context.Context) error {
		// Bound discovery/key fetches with an HTTP timeout while keeping the
		// remote JWKS context alive until shutdown.
		clientContext := oidc.ClientContext(lifetime, &http.Client{Timeout: 10 * time.Second})
		verifier, err := googleauth.New(clientContext, clientID)
		if err != nil {
			cancel()
			return err
		}
		service := application.NewService(authpg.NewUnitOfWork(pool), verifier, tokens, kernel.SystemClock{})
		authHandler := authhttp.New(service, tokens, authpg.NewProfileReader(pool))
		authHandler.Register(e)
		adminhttp.New(adminapp.New(adminpg.New(pool))).Register(e, authHandler.Authenticate)
		notifhttp.New(notifapp.New(notifpg.New(pool))).Register(e, authHandler.Authenticate)
		eventStore := eventpg.New(pool)
		eventReader := eventpg.NewReader(pool, kernel.SystemClock{})
		registerEventHandlers(e, eventStore, eventReader, authHandler.Authenticate)
		reporthttp.New(reportapp.New(reportpg.New(pool, kernel.SystemClock{}))).Register(e, authHandler.Authenticate)
		registrationStore := regpg.New(pool)
		reghttp.New(regapp.New(registrationStore)).Register(e, authHandler.Authenticate)
		reghttp.NewDetail(regapp.NewDetailService(registrationStore, qr)).Register(e, authHandler.Authenticate)
		reghttp.NewCheckin(regapp.NewCheckinService(registrationStore, qr)).Register(e, authHandler.Authenticate)
		return nil
	}, OnStop: func(context.Context) error { cancel(); return nil }})
	return nil
}
