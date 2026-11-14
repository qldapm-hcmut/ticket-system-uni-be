package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/auth/adapter/httpdto"
	"github.com/qldapm-hcmut/ticket-system-uni/internal/auth/application"
)

type Service interface {
	Login(context.Context, string) (application.AuthResult, error)
	Refresh(context.Context, string) (application.AuthResult, error)
	Logout(context.Context, string, string) error
}

type AccessVerifier interface{ VerifyAccessToken(string) (string, error) }

type Handler struct {
	service  Service
	tokens   AccessVerifier
	profiles application.ProfileReader
}

func New(service Service, tokens AccessVerifier, profiles application.ProfileReader) *Handler {
	return &Handler{service: service, tokens: tokens, profiles: profiles}
}

func (h *Handler) Register(e *echo.Echo) {
	g := e.Group("/api/v1")
	g.POST("/auth/google", h.login)
	g.POST("/auth/refresh", h.refresh)
	g.POST("/auth/logout", h.logout, h.Authenticate)
	g.GET("/me", h.me, h.Authenticate)
}

func fail(c echo.Context, status int, code, message string) error {
	return c.JSON(status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

// Missing users and identity conflicts are authentication failures, not 404/409.
// Infrastructure errors remain server errors and must not leak their details.
func respondError(c echo.Context, err error) error {
	if errors.Is(err, application.ErrUnauthenticated) || errors.Is(err, application.ErrNotFound) || errors.Is(err, application.ErrIdentityConflict) {
		return fail(c, 401, "UNAUTHENTICATED", "Authentication required.")
	}
	return fail(c, 500, "INTERNAL_ERROR", "Internal server error.")
}

func decode(c echo.Context, dst any) error {
	media, _, err := mime.ParseMediaType(c.Request().Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		return errors.New("expected JSON")
	}
	dec := json.NewDecoder(http.MaxBytesReader(c.Response(), c.Request().Body, 16*1024))
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return errors.New("expected one JSON object")
	}
	return nil
}

// Authenticate verifies the bearer token and loads the current active profile.
func (h *Handler) Authenticate(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		parts := strings.Fields(c.Request().Header.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || len(parts[1]) > 8192 {
			return respondError(c, application.ErrUnauthenticated)
		}
		id, err := h.tokens.VerifyAccessToken(parts[1])
		if err != nil {
			return respondError(c, application.ErrUnauthenticated)
		}
		profile, err := h.profiles.ReadProfile(c.Request().Context(), id)
		if err != nil {
			return respondError(c, err)
		}
		c.Set("auth.profile", profile)
		return next(c)
	}
}

func (h *Handler) login(c echo.Context) error {
	var body struct {
		IDToken string `json:"idToken"`
	}
	if decode(c, &body) != nil || strings.TrimSpace(body.IDToken) == "" {
		return fail(c, 400, "VALIDATION_ERROR", "idToken is required.")
	}
	result, err := h.service.Login(c.Request().Context(), body.IDToken)
	if err != nil {
		return respondError(c, err)
	}
	return h.respondWithTokensAndProfile(c, result)
}

func (h *Handler) refresh(c echo.Context) error {
	var body struct {
		RefreshToken string `json:"refreshToken"`
	}
	if decode(c, &body) != nil || strings.TrimSpace(body.RefreshToken) == "" {
		return fail(c, 400, "VALIDATION_ERROR", "refreshToken is required.")
	}
	result, err := h.service.Refresh(c.Request().Context(), body.RefreshToken)
	if err != nil {
		return respondError(c, err)
	}
	return h.respondWithTokensAndProfile(c, result)
}

// respondWithTokensAndProfile is called only after login/rotation has committed.
// A profile failure must not expose tokens or undo the committed session change.
// After a failed refresh response the old token is consumed: sign in again.
func (h *Handler) respondWithTokensAndProfile(c echo.Context, result application.AuthResult) error {
	profile, err := h.profiles.ReadProfile(c.Request().Context(), result.User.ID)
	if err != nil {
		return respondError(c, err)
	}
	data := map[string]any{
		"accessToken":  result.Tokens.AccessToken,
		"refreshToken": result.Tokens.RefreshToken,
		"expiresIn":    result.Tokens.ExpiresIn,
		"user":         httpdto.Profile(profile),
	}
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(200, map[string]any{"data": data})
}

func (h *Handler) logout(c echo.Context) error {
	var body struct {
		RefreshToken string `json:"refreshToken"`
	}
	if decode(c, &body) != nil || strings.TrimSpace(body.RefreshToken) == "" {
		return fail(c, 400, "VALIDATION_ERROR", "refreshToken is required.")
	}
	profile := c.Get("auth.profile").(application.Profile)
	if err := h.service.Logout(c.Request().Context(), profile.ID, body.RefreshToken); err != nil {
		return respondError(c, err)
	}
	return c.NoContent(204)
}

func (h *Handler) me(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.JSON(200, map[string]any{"data": httpdto.Profile(c.Get("auth.profile").(application.Profile))})
}
