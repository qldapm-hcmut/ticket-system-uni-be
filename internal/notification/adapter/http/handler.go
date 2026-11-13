package http

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	auth "github.com/qldapm-hcmut/ticket-system-uni/internal/auth/application"
	a "github.com/qldapm-hcmut/ticket-system-uni/internal/notification/application"
)

type Handler struct{ service *a.Service }

func New(s *a.Service) *Handler { return &Handler{s} }
func (h *Handler) Register(e *echo.Echo, authenticate echo.MiddlewareFunc) {
	g := e.Group("/api/v1/me/notifications", authenticate)
	g.GET("", h.list)
	g.POST("/read", h.read)
}
func failure(c echo.Context, err error) error {
	status, code := 500, "INTERNAL_ERROR"
	if errors.Is(err, a.ErrValidation) {
		status, code = 400, "VALIDATION_ERROR"
	}
	return c.JSON(status, map[string]any{"error": map[string]string{"code": code, "message": http.StatusText(status)}})
}
func (h *Handler) list(c echo.Context) error {
	filter := a.Filter{Page: 1, PageSize: 20}
	params := c.QueryParams()
	for name, destination := range map[string]*int32{"page": &filter.Page, "pageSize": &filter.PageSize} {
		if raw, ok := params[name]; ok {
			if len(raw) != 1 {
				return failure(c, a.ErrValidation)
			}
			n, err := strconv.ParseInt(raw[0], 10, 32)
			if err != nil {
				return failure(c, a.ErrValidation)
			}
			*destination = int32(n)
		}
	}
	if raw, ok := params["unread"]; ok {
		if len(raw) != 1 || raw[0] != "true" && raw[0] != "false" {
			return failure(c, a.ErrValidation)
		}
		filter.Unread = raw[0] == "true"
	}
	result, err := h.service.List(c.Request().Context(), c.Get("auth.profile").(auth.Profile).ID, filter)
	if err != nil {
		return failure(c, err)
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	totalPages := (result.TotalItems + int64(filter.PageSize) - 1) / int64(filter.PageSize)
	return c.JSON(200, map[string]any{
		"data": result.Items,
		"meta": map[string]any{
			"page":        filter.Page,
			"pageSize":    filter.PageSize,
			"totalItems":  result.TotalItems,
			"totalPages":  totalPages,
			"unreadCount": result.UnreadCount,
		},
	})
}
func (h *Handler) read(c echo.Context) error {
	kind, _, err := mime.ParseMediaType(c.Request().Header.Get("Content-Type"))
	if err != nil || kind != "application/json" {
		return failure(c, a.ErrValidation)
	}
	var body struct {
		IDs *[]string `json:"ids"`
		All *bool     `json:"all"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(c.Response(), c.Request().Body, 16384))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return failure(c, a.ErrValidation)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return failure(c, a.ErrValidation)
	}
	hasIDs, hasAll := body.IDs != nil, body.All != nil
	// Exactly one option must be present; an explicit all=false is not valid.
	if hasIDs == hasAll {
		return failure(c, a.ErrValidation)
	}
	if hasAll && !*body.All {
		return failure(c, a.ErrValidation)
	}
	options := a.ReadOptions{All: hasAll}
	if body.IDs != nil {
		options.IDs = *body.IDs
	}
	result, err := h.service.MarkRead(c.Request().Context(), c.Get("auth.profile").(auth.Profile).ID, options)
	if err != nil {
		return failure(c, err)
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(200, map[string]any{"data": result})
}
