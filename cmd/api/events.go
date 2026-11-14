package main

import (
	"github.com/labstack/echo/v4"
	eventhttp "github.com/qldapm-hcmut/ticket-system-uni/internal/event/adapter/http"
	eventpg "github.com/qldapm-hcmut/ticket-system-uni/internal/event/adapter/postgres"
	eventapp "github.com/qldapm-hcmut/ticket-system-uni/internal/event/application"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/shared/kernel"
)

func registerEventHandlers(e *echo.Echo, store *eventpg.Store, reader eventapp.Reader, authenticate echo.MiddlewareFunc) {
	eventhttp.New(eventapp.New(store, kernel.SystemClock{}), reader).Register(e, authenticate)
	eventhttp.NewManagementHandler(eventapp.NewManagementService(store)).Register(e, authenticate)
	eventhttp.NewApprovalHandler(eventapp.NewApprovalService(store, kernel.SystemClock{}), reader).Register(e, authenticate)
}
