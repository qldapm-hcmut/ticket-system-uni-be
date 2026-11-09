package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	a "github.com/qldapm-hcmut/ticket-system-uni/internal/admin/application"
	auth "github.com/qldapm-hcmut/ticket-system-uni/internal/auth/application"
)

type Service interface {
	CreateUnit(context.Context, string, a.UnitCreate) (a.Unit, error)
	SetMembers(context.Context, string, string, []a.Member) ([]a.UnitMember, error)
	ListUsers(context.Context, string, a.Filter) (a.UserList, error)
	UpdateUser(context.Context, string, string, bool) (auth.Profile, error)
}
type Handler struct{ service Service }

func New(service Service) *Handler { return &Handler{service} }
func (h *Handler) Register(e *echo.Echo, authenticate echo.MiddlewareFunc) {
	g := e.Group("/api/v1/admin", authenticate, requireAdmin)
	g.POST("/units", h.createUnit)
	g.PUT("/units/:unitId/members", h.setMembers)
	g.GET("/users", h.listUsers)
	g.PATCH("/users/:userId", h.updateUser)
}
func requireAdmin(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		p, ok := c.Get("auth.profile").(auth.Profile)
		if !ok || !p.IsActive {
			return respondError(c, a.ErrUnauthenticated)
		}
		if !p.IsAdmin {
			return respondError(c, a.ErrForbidden)
		}
		c.Response().Header().Set("Cache-Control", "no-store")
		return next(c)
	}
}
func actor(c echo.Context) string { return c.Get("auth.profile").(auth.Profile).ID }
func respondError(c echo.Context, err error) error {
	status, code, message := 500, "INTERNAL_ERROR", "Internal server error."
	switch {
	case errors.Is(err, a.ErrValidation):
		status, code, message = 400, "VALIDATION_ERROR", "Invalid request."
	case errors.Is(err, a.ErrUnauthenticated):
		status, code, message = 401, "UNAUTHENTICATED", "Authentication required."
	case errors.Is(err, a.ErrForbidden):
		status, code, message = 403, "FORBIDDEN", "Administrator role required."
	case errors.Is(err, a.ErrNotFound):
		status, code, message = 404, "NOT_FOUND", "Resource not found."
	case errors.Is(err, a.ErrConflict):
		status, code, message = 409, "INVALID_STATE_TRANSITION", "Unit code already exists."
	}
	body := map[string]any{"code": code, "message": message}
	if status == 400 {
		body["details"] = map[string]any{"fields": []any{map[string]string{"field": "request", "message": "Check required fields, formats and allowed values."}}}
	}
	return c.JSON(status, map[string]any{"error": body})
}

// decodeObject enforces exact JSON keys and rejects duplicates; encoding/json's
// struct matching alone is case-insensitive even with DisallowUnknownFields.
func decodeObject(dec *json.Decoder, fields map[string]any) error {
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return a.ErrValidation
	}
	seen := make(map[string]bool, len(fields))
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return a.ErrValidation
		}
		key, ok := token.(string)
		dst, allowed := fields[key]
		if !ok || !allowed || seen[key] {
			return a.ErrValidation
		}
		seen[key] = true
		if err := dec.Decode(dst); err != nil {
			return a.ErrValidation
		}
	}
	if token, err = dec.Token(); err != nil || token != json.Delim('}') {
		return a.ErrValidation
	}
	return nil
}

func decode(c echo.Context, fields map[string]any) error {
	media, _, err := mime.ParseMediaType(c.Request().Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return a.ErrValidation
	}
	dec := json.NewDecoder(http.MaxBytesReader(c.Response(), c.Request().Body, 1024*1024))
	if err = decodeObject(dec, fields); err != nil {
		return a.ErrValidation
	}
	if err = dec.Decode(&struct{}{}); err != io.EOF {
		return a.ErrValidation
	}
	return nil
}
func (h *Handler) createUnit(c echo.Context) error {
	var body struct {
		Code string `json:"code"`
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if decode(c, map[string]any{"code": &body.Code, "name": &body.Name, "type": &body.Type}) != nil {
		return respondError(c, a.ErrValidation)
	}
	out, err := h.service.CreateUnit(c.Request().Context(), actor(c), a.UnitCreate{Code: body.Code, Name: body.Name, Type: body.Type})
	if err != nil {
		return respondError(c, err)
	}
	return c.JSON(201, map[string]any{"data": map[string]any{"id": out.ID, "code": out.Code, "name": out.Name, "type": out.Type, "isActive": out.IsActive}})
}
func (h *Handler) setMembers(c echo.Context) error {
	var members []json.RawMessage
	if decode(c, map[string]any{"members": &members}) != nil || members == nil {
		return respondError(c, a.ErrValidation)
	}
	in := make([]a.Member, 0, len(members))
	for _, raw := range members {
		var m a.Member
		if decodeObject(json.NewDecoder(bytes.NewReader(raw)), map[string]any{"userId": &m.UserID, "memberRole": &m.MemberRole}) != nil {
			return respondError(c, a.ErrValidation)
		}
		in = append(in, m)
	}
	out, err := h.service.SetMembers(c.Request().Context(), actor(c), c.Param("unitId"), in)
	if err != nil {
		return respondError(c, err)
	}
	data := make([]any, 0, len(out))
	for _, m := range out {
		data = append(data, map[string]any{"user": map[string]string{"id": m.UserID, "fullName": m.FullName}, "memberRole": m.MemberRole})
	}
	return c.JSON(200, map[string]any{"data": data})
}
func page(c echo.Context, key string, def int64) (int64, error) {
	values, ok := c.QueryParams()[key]
	if !ok {
		return def, nil
	}
	if len(values) != 1 {
		return 0, a.ErrValidation
	}
	n, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil {
		return 0, a.ErrValidation
	}
	return n, nil
}
func (h *Handler) listUsers(c echo.Context) error {
	role := c.QueryParam("role")
	if values, present := c.QueryParams()["role"]; present && (len(values) != 1 || (role != "STUDENT" && role != "ORGANIZER" && role != "ADMIN")) {
		return respondError(c, a.ErrValidation)
	}
	p, err := page(c, "page", 1)
	if err != nil {
		return respondError(c, err)
	}
	size, err := page(c, "pageSize", 20)
	if err != nil {
		return respondError(c, err)
	}
	out, err := h.service.ListUsers(c.Request().Context(), actor(c), a.Filter{Q: c.QueryParam("q"), Role: role, Page: p, PageSize: size})
	if err != nil {
		return respondError(c, err)
	}
	data := make([]any, 0, len(out.Items))
	for _, u := range out.Items {
		data = append(data, userDTO(u))
	}
	pages := out.TotalItems / size
	if out.TotalItems%size != 0 {
		pages++
	}
	return c.JSON(200, map[string]any{"data": data, "meta": map[string]int64{"page": p, "pageSize": size, "totalItems": out.TotalItems, "totalPages": pages}})
}
func (h *Handler) updateUser(c echo.Context) error {
	var body struct {
		IsActive *bool `json:"isActive"`
	}
	if decode(c, map[string]any{"isActive": &body.IsActive}) != nil || body.IsActive == nil {
		return respondError(c, a.ErrValidation)
	}
	out, err := h.service.UpdateUser(c.Request().Context(), actor(c), c.Param("userId"), *body.IsActive)
	if err != nil {
		return respondError(c, err)
	}
	return c.JSON(200, map[string]any{"data": userDTO(out)})
}
func userDTO(p auth.Profile) map[string]any {
	roles := []string{}
	var student any
	if p.StudentCode != "" {
		student = p.StudentCode
		roles = append(roles, "STUDENT")
	}
	if len(p.Memberships) > 0 {
		roles = append(roles, "ORGANIZER")
	}
	if p.IsAdmin {
		roles = append(roles, "ADMIN")
	}
	units := []any{}
	for _, m := range p.Memberships {
		units = append(units, map[string]any{"unit": map[string]string{"id": m.UnitID, "code": m.Code, "name": m.Name}, "memberRole": m.MemberRole})
	}
	return map[string]any{"id": p.ID, "email": p.Email, "fullName": p.FullName, "studentCode": student, "isAdmin": p.IsAdmin, "isActive": p.IsActive, "roles": roles, "units": units}
}
