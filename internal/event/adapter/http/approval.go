package http

import (
	"github.com/labstack/echo/v4"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/application"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/domain"
	"net/http"
	"strconv"
)

type ApprovalHandler struct {
	service *application.ApprovalService
	reader  application.Reader
}

func NewApprovalHandler(s *application.ApprovalService, r application.Reader) *ApprovalHandler {
	return &ApprovalHandler{s, r}
}
func (h *ApprovalHandler) Register(e *echo.Echo, auth echo.MiddlewareFunc) {
	g := e.Group("/api/v1", auth)
	g.POST("/events/:id/submit", h.submit)
	g.GET("/approvals/pending", h.pending)
	g.POST("/events/:id/approve", h.approve)
	g.POST("/events/:id/reject", h.reject)
}
func (h *ApprovalHandler) submit(c echo.Context) error {
	out, err := h.service.Submit(c.Request().Context(), actor(c), c.Param("id"), 0)
	if err != nil {
		return failure(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": out})
}
func (h *ApprovalHandler) approve(c echo.Context) error {
	out, err := h.service.Approve(c.Request().Context(), actor(c), c.Param("id"), 0)
	if err != nil {
		return failure(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": out})
}
func (h *ApprovalHandler) reject(c echo.Context) error {
	var body struct {
		Reason string `json:"reason"`
	}
	if err := decode(c, &body); err != nil {
		return failure(c, err)
	}
	out, err := h.service.Reject(c.Request().Context(), actor(c), c.Param("id"), 0, body.Reason)
	if err != nil {
		return failure(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": out})
}
func (h *ApprovalHandler) pending(c echo.Context) error {
	a := actor(c)
	if !a.IsAdmin {
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
	if page < 1 || size < 1 || size > 100 {
		return failure(c, domain.ErrValidation)
	}
	rows, total, err := h.reader.Managed(c.Request().Context(), a, application.Filter{Status: "PENDING_APPROVAL", Page: int32(page), PageSize: int32(size)})
	if err != nil {
		return failure(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": rows, "meta": map[string]any{"page": page, "pageSize": size, "totalItems": total, "totalPages": (total + size - 1) / size}})
}
