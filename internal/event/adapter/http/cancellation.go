package http

import (
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/domain"
)

func (h *Handler) cancel(c echo.Context) error {
	var body struct {
		Reason string `json:"reason"`
	}
	if err := decode(c, &body); err != nil {
		return failure(c, err)
	}
	var version int32
	if raw, ok := c.Request().Header["If-Match"]; ok {
		if len(raw) != 1 {
			return failure(c, domain.ErrValidation)
		}
		n, err := strconv.ParseInt(raw[0], 10, 32)
		if err != nil || n < 1 {
			return failure(c, domain.ErrValidation)
		}
		version = int32(n)
	}
	result, err := h.service.Cancel(c.Request().Context(), actor(c), c.Param("id"), version, body.Reason)
	if err != nil {
		return failure(c, err)
	}
	// Read inside the handler only after the transaction has committed.
	detail, err := h.reader.Detail(c.Request().Context(), actor(c), result.Event.ID)
	if err != nil {
		return failure(c, err)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": detail, "meta": map[string]any{"affectedCount": result.AffectedCount}})
}
