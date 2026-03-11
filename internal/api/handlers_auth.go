package api

import (
	"context"
	"strings"
	"sync"
	"time"

	"demeter-backend/internal/auth"
	"demeter-backend/internal/config"
	"demeter-backend/internal/rbac"
	"demeter-backend/internal/store"
	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
)

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (a *App) RegisterAuthRoutes(router fiber.Router) {
	router.Post("/login", newLoginRateLimiter(), a.login)
	router.Post("/refresh", a.refresh)
	router.Post("/logout", a.logout)
	router.Get("/me", a.AppAuthRequired(), a.me)
}

func (a *App) RegisterAdminAuthRoutes(router fiber.Router) {
	router.Post("/login", newLoginRateLimiter(), a.adminLogin)
	router.Post("/refresh", a.adminRefresh)
	router.Post("/logout", a.adminLogout)
	router.Get("/me", a.AdminAuthRequired(), a.adminMe)
}

func newLoginRateLimiter() fiber.Handler {
	type attemptWindow struct {
		StartedAt time.Time
		Count     int
	}

	var (
		mu      sync.Mutex
		windows = map[string]attemptWindow{}
		limit   = 10
		window  = time.Minute
	)

	return func(c *fiber.Ctx) error {
		ip := strings.TrimSpace(c.IP())
		if ip == "" {
			ip = "unknown"
		}

		now := time.Now().UTC()
		mu.Lock()
		entry := windows[ip]
		if entry.StartedAt.IsZero() || now.Sub(entry.StartedAt) > window {
			entry = attemptWindow{StartedAt: now, Count: 0}
		}
		entry.Count++
		windows[ip] = entry
		mu.Unlock()

		if entry.Count > limit {
			return c.Status(fiber.StatusTooManyRequests).JSON(ErrorResponse{Error: "too many login attempts"})
		}
		return c.Next()
	}
}

func (a *App) login(c *fiber.Ctx) error {
	return a.loginForSession(c, auth.SessionTypeApp)
}

func (a *App) adminLogin(c *fiber.Ctx) error {
	return a.loginForSession(c, auth.SessionTypeAdmin)
}

func (a *App) loginForSession(c *fiber.Ctx, sessionType auth.SessionType) error {
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
	payload, status, err := a.issueSession(ctx, user, sessionType)
	if err != nil {
		if status == fiber.StatusForbidden {
			return c.Status(status).JSON(ErrorResponse{Error: "admin scope required"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to issue session"})
	}
	setSessionCookies(c, payload, a.Config.CookieSecure)
	if sessionType == auth.SessionTypeAdmin {
		a.auditLogLogin(payload.Response)
	}
	return c.JSON(payload.Response)
}

func (a *App) refresh(c *fiber.Ctx) error {
	return a.refreshSession(c, auth.SessionTypeApp)
}

func (a *App) adminRefresh(c *fiber.Ctx) error {
	return a.refreshSession(c, auth.SessionTypeAdmin)
}

func (a *App) refreshSession(c *fiber.Ctx, sessionType auth.SessionType) error {
	raw := strings.TrimSpace(c.Cookies(sessionType.RefreshCookieName()))
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
	if session == nil || session.RevokedAt.Valid || session.ExpiresAt.Before(time.Now().UTC()) || session.SessionType != sessionType.String() {
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

	csrfToken := ""
	if sessionType == auth.SessionTypeAdmin {
		csrfToken, err = auth.NewCSRFToken()
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to refresh session"})
		}
	}
	response, err := a.buildAuthResponse(ctx, user, runtimeModeForSession(sessionType), csrfToken)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to refresh session"})
	}
	if sessionType == auth.SessionTypeAdmin && !canUseAdminPanel(response) {
		_ = a.Store.RevokeRefreshSession(ctx, session.ID)
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "admin scope required"})
	}

	accessToken, accessExp, err := auth.NewAccessToken(a.Config.JWTSecret, accessTTLForSession(a.Config, sessionType), auth.Claims{
		UserID:      user.ID,
		OrgID:       user.OrganizationID,
		Email:       user.Email,
		GlobalRoles: response.GlobalRoles,
		OrgRoles:    response.OrgRoles,
		Permissions: response.Permissions,
		CSRFToken:   csrfToken,
		RegisteredClaims: jwt.RegisteredClaims{
			Audience: jwt.ClaimStrings{sessionType.String()},
		},
	})
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to refresh access token"})
	}
	newRefresh, err := auth.NewRefreshToken(refreshTTLForSession(a.Config, sessionType))
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
		SessionType:        sessionType,
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
	return a.logoutSession(c, auth.SessionTypeApp)
}

func (a *App) adminLogout(c *fiber.Ctx) error {
	return a.logoutSession(c, auth.SessionTypeAdmin)
}

func (a *App) logoutSession(c *fiber.Ctx, sessionType auth.SessionType) error {
	raw := strings.TrimSpace(c.Cookies(sessionType.RefreshCookieName()))
	if raw != "" {
		sessionID, _, err := auth.ParseRefreshToken(raw)
		if err == nil {
			session, loadErr := a.Store.GetRefreshSessionByID(context.Background(), sessionID)
			if loadErr == nil && session != nil && session.SessionType == sessionType.String() {
				_ = a.Store.RevokeRefreshSession(context.Background(), sessionID)
			}
		}
	}
	clearSessionCookies(c, a.Config.CookieSecure, sessionType)
	return c.SendStatus(fiber.StatusNoContent)
}

func (a *App) me(c *fiber.Ctx) error {
	return a.meForSession(c, auth.SessionTypeApp)
}

func (a *App) adminMe(c *fiber.Ctx) error {
	return a.meForSession(c, auth.SessionTypeAdmin)
}

func (a *App) meForSession(c *fiber.Ctx, sessionType auth.SessionType) error {
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
	response, err := a.buildAuthResponse(ctx, user, runtimeModeForSession(sessionType), claims.CSRFToken)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to resolve user context"})
	}
	if sessionType == auth.SessionTypeAdmin && !canUseAdminPanel(response) {
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "admin scope required"})
	}
	return c.JSON(response)
}

type sessionPayload struct {
	SessionType        auth.SessionType
	AccessToken        string
	AccessExpiresAt    time.Time
	RefreshCookieValue string
	RefreshHash        string
	RefreshExpiresAt   time.Time
	Response           AuthResponse
}

func (a *App) issueSession(ctx context.Context, user *store.User, sessionType auth.SessionType) (*sessionPayload, int, error) {
	csrfToken := ""
	var err error
	if sessionType == auth.SessionTypeAdmin {
		csrfToken, err = auth.NewCSRFToken()
		if err != nil {
			return nil, fiber.StatusInternalServerError, err
		}
	}
	response, err := a.buildAuthResponse(ctx, user, runtimeModeForSession(sessionType), csrfToken)
	if err != nil {
		return nil, fiber.StatusInternalServerError, err
	}
	if sessionType == auth.SessionTypeAdmin && !canUseAdminPanel(response) {
		return nil, fiber.StatusForbidden, fiber.ErrForbidden
	}
	accessToken, accessExp, err := auth.NewAccessToken(a.Config.JWTSecret, accessTTLForSession(a.Config, sessionType), auth.Claims{
		UserID:      user.ID,
		OrgID:       user.OrganizationID,
		Email:       user.Email,
		GlobalRoles: response.GlobalRoles,
		OrgRoles:    response.OrgRoles,
		Permissions: response.Permissions,
		CSRFToken:   csrfToken,
		RegisteredClaims: jwt.RegisteredClaims{
			Audience: jwt.ClaimStrings{sessionType.String()},
		},
	})
	if err != nil {
		return nil, fiber.StatusInternalServerError, err
	}
	refresh, err := auth.NewRefreshToken(refreshTTLForSession(a.Config, sessionType))
	if err != nil {
		return nil, fiber.StatusInternalServerError, err
	}
	err = a.Store.SaveRefreshSession(ctx, store.RefreshSession{
		ID:             refresh.SessionID,
		UserID:         user.ID,
		OrganizationID: user.OrganizationID,
		SessionType:    sessionType.String(),
		TokenHash:      refresh.Hash,
		ExpiresAt:      refresh.ExpiresAt,
		CreatedAt:      time.Now().UTC(),
	})
	if err != nil {
		return nil, fiber.StatusInternalServerError, err
	}
	return &sessionPayload{
		SessionType:        sessionType,
		AccessToken:        accessToken,
		AccessExpiresAt:    accessExp,
		RefreshCookieValue: refresh.RawToken,
		RefreshHash:        refresh.Hash,
		RefreshExpiresAt:   refresh.ExpiresAt,
		Response:           response,
	}, fiber.StatusOK, nil
}

func (a *App) buildAuthResponse(ctx context.Context, user *store.User, runtimeMode string, csrfToken string) (AuthResponse, error) {
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
		CsrfToken:   csrfToken,
		RuntimeMode: runtimeMode,
	}, nil
}

func setSessionCookies(c *fiber.Ctx, payload *sessionPayload, secure bool) {
	c.Cookie(&fiber.Cookie{
		Name:     payload.SessionType.AccessCookieName(),
		Value:    payload.AccessToken,
		Path:     payload.SessionType.AccessCookiePath(),
		HTTPOnly: true,
		Secure:   secure,
		SameSite: "Strict",
		Expires:  payload.AccessExpiresAt,
	})
	c.Cookie(&fiber.Cookie{
		Name:     payload.SessionType.RefreshCookieName(),
		Value:    payload.RefreshCookieValue,
		Path:     payload.SessionType.RefreshCookiePath(),
		HTTPOnly: true,
		Secure:   secure,
		SameSite: "Strict",
		Expires:  payload.RefreshExpiresAt,
	})
}

func clearSessionCookies(c *fiber.Ctx, secure bool, sessionType auth.SessionType) {
	expired := time.Now().UTC().Add(-time.Hour)
	c.Cookie(&fiber.Cookie{
		Name:     sessionType.AccessCookieName(),
		Value:    "",
		Path:     sessionType.AccessCookiePath(),
		HTTPOnly: true,
		Secure:   secure,
		SameSite: "Strict",
		Expires:  expired,
	})
	c.Cookie(&fiber.Cookie{
		Name:     sessionType.RefreshCookieName(),
		Value:    "",
		Path:     sessionType.RefreshCookiePath(),
		HTTPOnly: true,
		Secure:   secure,
		SameSite: "Strict",
		Expires:  expired,
	})
}

func accessTTLForSession(cfg config.Config, sessionType auth.SessionType) time.Duration {
	if sessionType == auth.SessionTypeAdmin {
		return cfg.AdminAccessTTL
	}
	return cfg.AccessTTL
}

func refreshTTLForSession(cfg config.Config, sessionType auth.SessionType) time.Duration {
	if sessionType == auth.SessionTypeAdmin {
		return cfg.AdminRefreshTTL
	}
	return cfg.RefreshTTL
}

func runtimeModeForSession(sessionType auth.SessionType) string {
	if sessionType == auth.SessionTypeAdmin {
		return "admin"
	}
	return "backend"
}

func canUseAdminPanel(response AuthResponse) bool {
	if !rbac.HasPermission(response.Permissions, "feature.admin") {
		return false
	}
	return rbac.HasRole(response.GlobalRoles, "super_admin") || rbac.HasRole(response.OrgRoles, "org_admin")
}

func (a *App) auditLogLogin(response AuthResponse) {
	_ = a.Store.InsertAuditLog(context.Background(), store.AuditLogInput{
		ActorUserID:    response.User.ID,
		OrganizationID: response.Organization.ID,
		Action:         "admin.login",
		TargetType:     "user",
		TargetID:       response.User.ID,
		Payload: map[string]any{
			"runtimeMode": response.RuntimeMode,
		},
	})
}
