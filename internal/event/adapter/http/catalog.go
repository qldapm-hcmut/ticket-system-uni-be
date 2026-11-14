package http

import (
	"strconv"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/application"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/event/domain"
	"regexp"
)

func catalogFilter(c echo.Context) (application.CatalogFilter, error) {
	f := application.CatalogFilter{Filter: application.Filter{Page: 1, PageSize: 20, Search: c.QueryParam("q"), UnitID: c.QueryParam("unitId")}}
	for name, dst := range map[string]*int32{"page": &f.Page, "pageSize": &f.PageSize} {
		if raw := c.QueryParam(name); raw != "" {
			n, err := strconv.ParseInt(raw, 10, 32)
			if err != nil {
				return f, domain.ErrValidation
			}
			*dst = int32(n)
		}
	}
	if f.Page < 1 || f.PageSize < 1 || f.PageSize > 100 || (int64(f.Page)-1)*int64(f.PageSize) > 2147483647 || len(f.Search) > 200 {
		return f, domain.ErrValidation
	}
	if f.UnitID != "" {
		if !regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`).MatchString(f.UnitID) {
			return f, domain.ErrValidation
		}
	}
	for name, dst := range map[string]**time.Time{"from": &f.From, "to": &f.To} {
		if raw := c.QueryParam(name); raw != "" {
			t, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				return f, domain.ErrValidation
			}
			*dst = &t
		}
	}
	if f.From != nil && f.To != nil && f.From.After(*f.To) {
		return f, domain.ErrValidation
	}
	if raw := c.QueryParam("availableOnly"); raw != "" {
		if raw != "true" && raw != "false" {
			return f, domain.ErrValidation
		}
		f.AvailableOnly = raw == "true"
	}
	return f, nil
}

func (h *Handler) catalog(c echo.Context) error {
	f, err := catalogFilter(c)
	if err != nil {
		return failure(c, err)
	}
	rows, count, err := h.reader.Catalog(c.Request().Context(), actor(c), f)
	if err != nil {
		return failure(c, err)
	}
	return c.JSON(200, map[string]any{"data": rows, "meta": map[string]any{"page": f.Page, "pageSize": f.PageSize, "totalItems": count, "totalPages": (count + int64(f.PageSize) - 1) / int64(f.PageSize)}})
}
