package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
	auth "github.com/qldapm-hcmut/ticket-system-uni/internal/auth/application"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/application"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/domain"
)

type Handler struct {
	service *application.Service
	reader  application.Reader
}

func New(s *application.Service, r application.Reader) *Handler { return &Handler{s, r} }
func (h *Handler) Register(e *echo.Echo, authenticate echo.MiddlewareFunc) {
	g := e.Group("/api/v1", authenticate)
	g.GET("/units", h.units)
	g.GET("/events", h.catalog)
	g.POST("/events", h.create)
	g.POST("/events/:id/cancel", h.cancel)
	g.PATCH("/events/:id", h.patch)
	g.DELETE("/events/:id", h.deleteDraft)
	g.GET("/events/:id", h.detail)
	g.GET("/manage/events", h.managed)
}
func actor(c echo.Context) application.Actor {
	p := c.Get("auth.profile").(auth.Profile)
	return application.Actor{ID: p.ID, IsAdmin: p.IsAdmin, IsOrganizer: len(p.Memberships) > 0}
}
func failure(c echo.Context, err error) error {
	status, code := 500, "INTERNAL_ERROR"
	switch {
	case errors.Is(err, domain.ErrValidation):
		status, code = 400, "VALIDATION_ERROR"
	case errors.Is(err, domain.ErrNotFound):
		status, code = 404, "NOT_FOUND"
	case errors.Is(err, domain.ErrForbidden):
		status, code = 403, "FORBIDDEN"
	case errors.Is(err, domain.ErrCapacity):
		status, code = 409, "CAPACITY_BELOW_REGISTERED"
	case errors.Is(err, domain.ErrVersion):
		status, code = 409, "VERSION_CONFLICT"
	case errors.Is(err, domain.ErrState):
		status, code = 409, "INVALID_STATE_TRANSITION"
	}
	return c.JSON(status, map[string]any{"error": map[string]string{"code": code, "message": http.StatusText(status)}})
}
func decode(c echo.Context, dst any) error {
	kind, _, err := mime.ParseMediaType(c.Request().Header.Get("Content-Type"))
	if err != nil || kind != "application/json" {
		return domain.ErrValidation
	}
	dec := json.NewDecoder(http.MaxBytesReader(c.Response(), c.Request().Body, 1024*1024))
	if err := dec.Decode(dst); err != nil {
		return domain.ErrValidation
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return domain.ErrValidation
	}
	return nil
}
func (h *Handler) units(c echo.Context) error {
	kind := c.QueryParam("type")
	if kind != "" && kind != "FACULTY" && kind != "CLUB" && kind != "DEPARTMENT" {
		return failure(c, domain.ErrValidation)
	}
	rows, err := h.reader.Units(c.Request().Context(), kind)
	if err != nil {
		return failure(c, err)
	}
	return c.JSON(200, map[string]any{"data": rows})
}
func (h *Handler) create(c echo.Context) error {
	var body struct {
		UnitID string `json:"unitId"`
		domain.Fields
	}
	if err := decode(c, &body); err != nil {
		return failure(c, err)
	}
	e, err := h.service.Create(c.Request().Context(), actor(c), body.UnitID, body.Fields)
	if err != nil {
		return failure(c, err)
	}
	result, err := h.reader.Detail(c.Request().Context(), e.ID)
	if err != nil {
		return failure(c, err)
	}
	return c.JSON(201, map[string]any{"data": result})
}
func (h *Handler) detail(c echo.Context) error {
	a := actor(c)
	result, err := h.reader.Detail(c.Request().Context(), c.Param("id"))
	if err != nil {
		return failure(c, err)
	}
	if fmt.Sprint(result["status"]) == "PUBLISHED" {
		return c.JSON(200, map[string]any{"data": result})
	}
	if !a.IsAdmin && !a.IsOrganizer {
		return failure(c, domain.ErrNotFound)
	}
	_, err = h.service.Get(c.Request().Context(), a, c.Param("id"))
	if err != nil {
		return failure(c, err)
	}
	result, err = h.reader.Detail(c.Request().Context(), c.Param("id"))
	if err != nil {
		return failure(c, err)
	}
	return c.JSON(200, map[string]any{"data": result})
}
func (h *Handler) patch(c echo.Context) error {
	rawVersion := c.Request().Header.Get("If-Match")
	if rawVersion == "" {
		return c.JSON(http.StatusPreconditionRequired, map[string]any{"error": map[string]string{"code": "PRECONDITION_REQUIRED", "message": "If-Match header is required"}})
	}
	version, err := strconv.ParseInt(rawVersion, 10, 32)
	if err != nil || version < 1 {
		return failure(c, domain.ErrValidation)
	}
	var fields map[string]json.RawMessage
	if err := decode(c, &fields); err != nil || len(fields) == 0 {
		return failure(c, domain.ErrValidation)
	}
	allowed := map[string]bool{"title": true, "description": true, "location": true, "startAt": true, "endAt": true, "registrationOpensAt": true, "registrationClosesAt": true, "capacity": true, "waitlistLimit": true}
	for key, value := range fields {
		if !allowed[key] || (string(value) == "null" && key != "description" && key != "waitlistLimit") {
			return failure(c, domain.ErrValidation)
		}
	}
	updated, err := h.service.Patch(c.Request().Context(), actor(c), c.Param("id"), int32(version), func(current domain.Fields) (domain.Fields, error) {
		raw, _ := json.Marshal(current)
		var merged map[string]json.RawMessage
		_ = json.Unmarshal(raw, &merged)
		for k, v := range fields {
			merged[k] = v
		}
		raw, _ = json.Marshal(merged)
		if err := json.Unmarshal(raw, &current); err != nil {
			return current, domain.ErrValidation
		}
		return current, nil
	})
	if err != nil {
		return failure(c, err)
	}
	result, err := h.reader.Detail(c.Request().Context(), c.Param("id"))
	if err != nil {
		return failure(c, err)
	}
	return c.JSON(200, map[string]any{"data": result, "meta": map[string]any{"significantChange": updated.SignificantChange, "notifiedCount": updated.NotifiedCount, "promotedCount": updated.PromotedCount}})
}
func (h *Handler) managed(c echo.Context) error {
	a := actor(c)
	if !a.IsAdmin && !a.IsOrganizer {
		return failure(c, domain.ErrForbidden)
	}
	page, size := int64(1), int64(20)
	var err error
	if raw := c.QueryParam("page"); raw != "" {
		page, err = strconv.ParseInt(raw, 10, 32)
		if err != nil {
			return failure(c, domain.ErrValidation)
		}
	}
	if raw := c.QueryParam("pageSize"); raw != "" {
		size, err = strconv.ParseInt(raw, 10, 32)
		if err != nil {
			return failure(c, domain.ErrValidation)
		}
	}
	if page < 1 || size < 1 || size > 100 || (page-1)*size > 2147483647 {
		return failure(c, domain.ErrValidation)
	}
	search := c.QueryParam("q")
	if len(search) > 200 {
		return failure(c, domain.ErrValidation)
	}
	status := c.QueryParam("status")
	valid := map[string]bool{"DRAFT": true, "REJECTED": true, "PENDING_APPROVAL": true, "PUBLISHED": true, "CANCELLED": true, "COMPLETED": true}
	if status != "" {
		for _, s := range strings.Split(status, ",") {
			if !valid[s] {
				return failure(c, domain.ErrValidation)
			}
		}
	}
	rows, count, err := h.reader.Managed(c.Request().Context(), a, application.Filter{UnitID: c.QueryParam("unitId"), Status: status, Search: search, Page: int32(page), PageSize: int32(size)})
	if err != nil {
		return failure(c, err)
	}
	return c.JSON(200, map[string]any{"data": rows, "meta": map[string]any{"page": page, "pageSize": size, "totalItems": count, "totalPages": (count + size - 1) / size}})
}
