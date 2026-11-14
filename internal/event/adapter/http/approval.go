package http

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/application"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/domain"
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
func (h *ApprovalHandler) eventResponse(c echo.Context, id string) error {
	out, err := h.reader.Detail(c.Request().Context(), actor(c), id)
	if err != nil {
		return failure(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": out})
}
func (h *ApprovalHandler) submit(c echo.Context) error {
	out, err := h.service.Submit(c.Request().Context(), actor(c), c.Param("id"), 0)
	if err != nil {
		return failure(c, err)
	}
	return h.eventResponse(c, out.ID)
}
func (h *ApprovalHandler) approve(c echo.Context) error {
	a := actor(c)
	if !a.IsAdmin {
		return failure(c, domain.ErrForbidden)
	}
	comment := ""
	// A missing body is allowed. If supplied, comment must be a string, not null.
	if c.Request().Body != nil {
		body := bufio.NewReader(c.Request().Body)
		if _, err := body.Peek(1); err != io.EOF {
			if err != nil {
				return failure(c, domain.ErrValidation)
			}
			c.Request().Body = struct {
				io.Reader
				io.Closer
			}{body, c.Request().Body}
			var fields map[string]json.RawMessage
			if err := decode(c, &fields); err != nil {
				return failure(c, err)
			}
			if fields == nil {
				return failure(c, domain.ErrValidation)
			}
			if raw, ok := fields["comment"]; ok {
				if string(raw) == "null" || json.Unmarshal(raw, &comment) != nil {
					return failure(c, domain.ErrValidation)
				}
			}
		}
	}
	out, err := h.service.Approve(c.Request().Context(), a, c.Param("id"), 0, comment)
	if err != nil {
		return failure(c, err)
	}
	return h.eventResponse(c, out.ID)
}
func (h *ApprovalHandler) reject(c echo.Context) error {
	a := actor(c)
	if !a.IsAdmin {
		return failure(c, domain.ErrForbidden)
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if err := decode(c, &body); err != nil {
		return failure(c, err)
	}
	out, err := h.service.Reject(c.Request().Context(), a, c.Param("id"), 0, body.Reason)
	if err != nil {
		return failure(c, err)
	}
	return h.eventResponse(c, out.ID)
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
	if page < 1 || size < 1 || size > 100 || (page-1)*size > 2147483647 {
		return failure(c, domain.ErrValidation)
	}
	rows, total, err := h.reader.Pending(c.Request().Context(), a, application.Filter{Page: int32(page), PageSize: int32(size)})
	if err != nil {
		return failure(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": rows, "meta": map[string]any{"page": page, "pageSize": size, "totalItems": total, "totalPages": (total + size - 1) / size}})
}
