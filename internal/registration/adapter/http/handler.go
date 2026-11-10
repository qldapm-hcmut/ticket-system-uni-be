package http

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	auth "github.com/qldapm-hcmut/ticket-system-uni/internal/auth/application"
	a "github.com/qldapm-hcmut/ticket-system-uni/internal/registration/application"
)

type Handler struct{ s *a.Service }

func New(s *a.Service) *Handler { return &Handler{s} }

func (h *Handler) Register(e *echo.Echo, authn echo.MiddlewareFunc) {
	g := e.Group("/api/v1", authn)
	g.POST("/events/:id/registrations", h.register)
	g.GET("/me/registrations", h.mine)
	g.POST("/registrations/:id/cancel", h.cancel)
}

func student(c echo.Context) (string, bool) {
	p := c.Get("auth.profile").(auth.Profile)
	return p.ID, p.StudentCode != ""
}

func mapErr(c echo.Context, err error) error {
	status, code := 500, "INTERNAL_ERROR"
	switch {
	case errors.Is(err, a.ErrValidation):
		status, code = 400, "VALIDATION_ERROR"
	case errors.Is(err, a.ErrState):
		status, code = 409, "INVALID_STATE_TRANSITION"
	case errors.Is(err, a.ErrCancelDeadline):
		status, code = 422, "CANCEL_DEADLINE_PASSED"
	case errors.Is(err, a.ErrForbidden):
		status, code = 403, "FORBIDDEN"
	case errors.Is(err, a.ErrNotFound):
		status, code = 404, "NOT_FOUND"
	case errors.Is(err, a.ErrAlreadyActive):
		status, code = 409, "ALREADY_REGISTERED"
	case errors.Is(err, a.ErrClosed):
		status, code = 422, "REGISTRATION_CLOSED"
	case errors.Is(err, a.ErrFull):
		status, code = 409, "EVENT_FULL"
	}
	return c.JSON(status, map[string]any{"error": map[string]string{"code": code, "message": http.StatusText(status)}})
}

func dto(r a.Registration) map[string]any {
	return map[string]any{"id": r.ID, "eventId": r.EventID, "status": r.Status, "waitlistPosition": nil, "checkinCode": func() any {
		if r.Status == "REGISTERED" {
			return r.CheckinCode
		}
		return nil
	}(), "createdAt": r.CreatedAt}
}

func (h *Handler) register(c echo.Context) error {
	id, ok := student(c)
	if !ok {
		return mapErr(c, a.ErrForbidden)
	}
	r, err := h.s.Register(c.Request().Context(), id, c.Param("id"))
	if err != nil {
		return mapErr(c, err)
	}
	return c.JSON(201, map[string]any{"data": dto(r)})
}

func (h *Handler) cancel(c echo.Context) error {
	// R2 has an optional JSON object; an absent body remains valid for self.
	rawBody, err := io.ReadAll(http.MaxBytesReader(c.Response(), c.Request().Body, 16384))
	if err != nil {
		return mapErr(c, a.ErrValidation)
	}
	var body map[string]json.RawMessage
	if len(rawBody) != 0 && (json.Unmarshal(rawBody, &body) != nil || body == nil) {
		return mapErr(c, a.ErrValidation)
	}
	reason := ""
	if raw, exists := body["reason"]; exists {
		if string(raw) == "null" || json.Unmarshal(raw, &reason) != nil {
			return mapErr(c, a.ErrValidation)
		}
	}
	id := c.Get("auth.profile").(auth.Profile).ID
	result, err := h.s.Cancel(c.Request().Context(), id, c.Param("id"), reason)
	if err != nil {
		return mapErr(c, err)
	}
	var promoted any
	if result.Promoted != nil {
		promoted = result.Promoted.ID
	}
	data := dto(result.Registration)
	data["cancelledAt"] = result.Registration.CancelledAt
	data["cancelledBy"] = result.Registration.CancelledBy
	return c.JSON(200, map[string]any{"data": data, "meta": map[string]any{"promotedRegistrationId": promoted}})
}

func (h *Handler) mine(c echo.Context) error {
	id, ok := student(c)
	if !ok {
		return mapErr(c, a.ErrForbidden)
	}
	page, size := int32(1), int32(20)
	if raw := c.QueryParam("page"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || v < 1 {
			return mapErr(c, a.ErrValidation)
		}
		page = int32(v)
	}
	if raw := c.QueryParam("pageSize"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || v < 1 || v > 100 {
			return mapErr(c, a.ErrValidation)
		}
		size = int32(v)
	}
	if int64(page-1)*int64(size) > 2147483647 {
		return mapErr(c, a.ErrValidation)
	}
	switch c.QueryParam("status") {
	case "", "REGISTERED", "WAITLISTED", "CANCELLED", "CANCELLED_BY_EVENT", "EXPIRED":
	default:
		return mapErr(c, a.ErrValidation)
	}
	rows, total, err := h.s.Mine(c.Request().Context(), id, c.QueryParam("status"), page, size)
	if err != nil {
		return mapErr(c, err)
	}
	data := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		data = append(data, dto(r))
	}
	return c.JSON(200, map[string]any{"data": data, "meta": map[string]any{"page": page, "pageSize": size, "totalItems": total, "totalPages": (total + int64(size) - 1) / int64(size)}})
}
