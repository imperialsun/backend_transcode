package api

import (
	"context"
	"strings"
	"time"

	"demeter-backend/internal/auth"
	"demeter-backend/internal/store"
	"github.com/gofiber/fiber/v2"
)

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (a *App) RegisterAuthRoutes(router fiber.Router) {
	router.Post("/login", a.login)
	router.Post("/refresh", a.refresh)
	router.Post("/logout", a.logout)
	router.Get("/me", a.AuthRequired(), a.me)
}

func (a *App) login(c *fiber.Ctx) error {
	var req loginRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || strings.TrimSpace(req.Password) == "" {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "email and password are required"})
	}
	ctx := context.Background()
	user, err := a.Store.FindUserByEmail(ctx, email)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load user"})
	}
	if user == nil || !auth.VerifyPassword(user.PasswordHash, req.Password) {
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "invalid credentials"})
	}
	if user.Status != "active" {
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "user is inactive"})
	}
	payload, err := a.issueSession(ctx, user)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to issue session"})
	}
	setSessionCookies(c, payload, a.Config.CookieSecure)
	return c.JSON(payload.Response)
}

func (a *App) refresh(c *fiber.Ctx) error {
	raw := strings.TrimSpace(c.Cookies(auth.RefreshCookieName))
	if raw == "" {
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "missing refresh token"})
	}
	sessionID, _, err := auth.ParseRefreshToken(raw)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "invalid refresh token"})
	}
	ctx := context.Background()
	session, err := a.Store.GetRefreshSessionByID(ctx, sessionID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load refresh session"})
	}
	if session == nil || session.RevokedAt.Valid || session.ExpiresAt.Before(time.Now().UTC()) {
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "refresh token expired"})
	}
	if !auth.VerifyRefreshHash(session.TokenHash, raw) {
		_ = a.Store.RevokeRefreshSession(ctx, session.ID)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "invalid refresh token"})
	}
	user, err := a.Store.GetUserByID(ctx, session.UserID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load user"})
	}
	if user == nil || user.Status != "active" {
		_ = a.Store.RevokeRefreshSession(ctx, session.ID)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "user unavailable"})
	}
	response, err := a.buildAuthResponse(ctx, user)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to refresh session"})
	}
	accessToken, accessExp, err := auth.NewAccessToken(a.Config.JWTSecret, a.Config.AccessTTL, auth.Claims{
		UserID:      user.ID,
		OrgID:       user.OrganizationID,
		Email:       user.Email,
		GlobalRoles: response.GlobalRoles,
		OrgRoles:    response.OrgRoles,
		Permissions: response.Permissions,
	})
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to refresh access token"})
	}
	newRefresh, err := auth.NewRefreshToken(a.Config.RefreshTTL)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to refresh token"})
	}
	_, tokenPart, err := auth.ParseRefreshToken(newRefresh.RawToken)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to parse refresh token"})
	}
	rotatedRaw := session.ID + "." + tokenPart
	rotatedHash := auth.HashRefreshToken(rotatedRaw)
	if err := a.Store.RotateRefreshSession(ctx, session.ID, rotatedHash, newRefresh.ExpiresAt); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to rotate refresh session"})
	}
	setSessionCookies(c, &sessionPayload{
		AccessToken:        accessToken,
		AccessExpiresAt:    accessExp,
		RefreshCookieValue: rotatedRaw,
		RefreshHash:        rotatedHash,
		RefreshExpiresAt:   newRefresh.ExpiresAt,
		Response:           response,
	}, a.Config.CookieSecure)
	return c.JSON(response)
}

func (a *App) logout(c *fiber.Ctx) error {
	raw := strings.TrimSpace(c.Cookies(auth.RefreshCookieName))
	if raw != "" {
		sessionID, _, err := auth.ParseRefreshToken(raw)
		if err == nil {
			_ = a.Store.RevokeRefreshSession(context.Background(), sessionID)
		}
	}
	clearSessionCookies(c, a.Config.CookieSecure)
	return c.SendStatus(fiber.StatusNoContent)
}

func (a *App) me(c *fiber.Ctx) error {
	claims := MustClaims(c)
	if claims == nil {
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	ctx := context.Background()
	user, err := a.Store.GetUserByID(ctx, claims.UserID)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load user"})
	}
	if user == nil || user.Status != "active" {
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "user unavailable"})
	}
	response, err := a.buildAuthResponse(ctx, user)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to resolve user context"})
	}
	return c.JSON(response)
}

type sessionPayload struct {
	AccessToken        string
	AccessExpiresAt    time.Time
	RefreshCookieValue string
	RefreshHash        string
	RefreshExpiresAt   time.Time
	Response           AuthResponse
}

func (a *App) issueSession(ctx context.Context, user *store.User) (*sessionPayload, error) {
	response, err := a.buildAuthResponse(ctx, user)
	if err != nil {
		return nil, err
	}
	accessToken, accessExp, err := auth.NewAccessToken(a.Config.JWTSecret, a.Config.AccessTTL, auth.Claims{
		UserID:      user.ID,
		OrgID:       user.OrganizationID,
		Email:       user.Email,
		GlobalRoles: response.GlobalRoles,
		OrgRoles:    response.OrgRoles,
		Permissions: response.Permissions,
	})
	if err != nil {
		return nil, err
	}
	refresh, err := auth.NewRefreshToken(a.Config.RefreshTTL)
	if err != nil {
		return nil, err
	}
	err = a.Store.SaveRefreshSession(ctx, store.RefreshSession{
		ID:             refresh.SessionID,
		UserID:         user.ID,
		OrganizationID: user.OrganizationID,
		TokenHash:      refresh.Hash,
		ExpiresAt:      refresh.ExpiresAt,
		CreatedAt:      time.Now().UTC(),
	})
	if err != nil {
		return nil, err
	}
	return &sessionPayload{
		AccessToken:        accessToken,
		AccessExpiresAt:    accessExp,
		RefreshCookieValue: refresh.RawToken,
		RefreshHash:        refresh.Hash,
		RefreshExpiresAt:   refresh.ExpiresAt,
		Response:           response,
	}, nil
}

func (a *App) buildAuthResponse(ctx context.Context, user *store.User) (AuthResponse, error) {
	org, err := a.Store.GetOrganizationByID(ctx, user.OrganizationID)
	if err != nil {
		return AuthResponse{}, err
	}
	if org == nil {
		return AuthResponse{}, fiber.NewError(fiber.StatusBadRequest, "organization not found")
	}
	globalRoles, err := a.Store.GetGlobalRoleCodesByUser(ctx, user.ID)
	if err != nil {
		return AuthResponse{}, err
	}
	orgRoles, err := a.Store.GetOrganizationRoleCodesByUser(ctx, user.ID)
	if err != nil {
		return AuthResponse{}, err
	}
	permissions, err := a.Store.ResolveEffectivePermissions(ctx, user.ID)
	if err != nil {
		return AuthResponse{}, err
	}
	return AuthResponse{
		User: AuthUser{ID: user.ID, Email: user.Email, Status: user.Status},
		Organization: AuthOrg{
			ID: org.ID, Name: org.Name, Code: org.Code, Status: org.Status,
		},
		GlobalRoles: globalRoles,
		OrgRoles:    orgRoles,
		Permissions: permissions,
		RuntimeMode: "backend",
	}, nil
}

func setSessionCookies(c *fiber.Ctx, payload *sessionPayload, secure bool) {
	c.Cookie(&fiber.Cookie{
		Name:     auth.AccessCookieName,
		Value:    payload.AccessToken,
		Path:     "/",
		HTTPOnly: true,
		Secure:   secure,
		SameSite: "Lax",
		Expires:  payload.AccessExpiresAt,
	})
	c.Cookie(&fiber.Cookie{
		Name:     auth.RefreshCookieName,
		Value:    payload.RefreshCookieValue,
		Path:     "/",
		HTTPOnly: true,
		Secure:   secure,
		SameSite: "Lax",
		Expires:  payload.RefreshExpiresAt,
	})
}

func clearSessionCookies(c *fiber.Ctx, secure bool) {
	expired := time.Now().UTC().Add(-time.Hour)
	c.Cookie(&fiber.Cookie{
		Name:     auth.AccessCookieName,
		Value:    "",
		Path:     "/",
		HTTPOnly: true,
		Secure:   secure,
		SameSite: "Lax",
		Expires:  expired,
	})
	c.Cookie(&fiber.Cookie{
		Name:     auth.RefreshCookieName,
		Value:    "",
		Path:     "/",
		HTTPOnly: true,
		Secure:   secure,
		SameSite: "Lax",
		Expires:  expired,
	})
}
