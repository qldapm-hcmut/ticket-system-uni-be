package http

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

func (h *Handler) deleteDraft(c echo.Context) error {
	if err := h.service.DeleteDraft(c.Request().Context(), actor(c), c.Param("id")); err != nil {
		return failure(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}
