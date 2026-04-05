package api

import (
	"context"
	"strings"
	"sync"
	"time"

	"demeter-backend/internal/auth"
	"demeter-backend/internal/backenderrors"
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

type changePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	Password        string `json:"password"`
}

func (a *App) RegisterAuthRoutes(router fiber.Router) {
	a.RegisterAuthCoreRoutes(router)
	a.RegisterAuthForgotPasswordRoutes(router)
}

func (a *App) RegisterAdminAuthRoutes(router fiber.Router) {
	a.RegisterAdminAuthCoreRoutes(router)
	a.RegisterAdminAuthForgotPasswordRoutes(router)
}

func (a *App) RegisterAuthCoreRoutes(router fiber.Router) {
	router.Post("/login", newLoginRateLimiter(), a.login)
	router.Post("/reset-password", newPasswordResetApplyRateLimiter(), a.resetPassword)
	router.Put("/me/password", a.AppAuthRequired(), a.changePassword)
	router.Post("/refresh", a.refresh)
	router.Post("/logout", a.logout)
	router.Get("/me", a.AppAuthRequired(), a.me)
}

func (a *App) RegisterAuthForgotPasswordRoutes(router fiber.Router) {
	router.Post("/forgot-password", newPasswordResetRequestRateLimiter(), a.forgotPassword)
}

func (a *App) RegisterAdminAuthCoreRoutes(router fiber.Router) {
	router.Post("/login", newLoginRateLimiter(), a.adminLogin)
	router.Post("/reset-password", newPasswordResetApplyRateLimiter(), a.adminResetPassword)
	router.Post("/refresh", a.adminRefresh)
	router.Post("/logout", a.adminLogout)
	router.Get("/me", a.AdminAuthRequired(), a.adminMe)
}

func (a *App) RegisterAdminAuthForgotPasswordRoutes(router fiber.Router) {
	router.Post("/forgot-password", newPasswordResetRequestRateLimiter(), a.adminForgotPassword)
}

func newLoginRateLimiter() fiber.Handler {
	return newIPRateLimiter(10, time.Minute, "too many login attempts")
}

func newPasswordResetRequestRateLimiter() fiber.Handler {
	return newIPRateLimiter(5, time.Minute, "too many password reset requests")
}

func newPasswordResetApplyRateLimiter() fiber.Handler {
	return newIPRateLimiter(10, time.Minute, "too many password reset attempts")
}

func newIPRateLimiter(limit int, window time.Duration, message string) fiber.Handler {
	type attemptWindow struct {
		StartedAt time.Time
		Count     int
	}

	var (
		mu      sync.Mutex
		windows = map[string]attemptWindow{}
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
			return c.Status(fiber.StatusTooManyRequests).JSON(ErrorResponse{Error: message})
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

func (a *App) forgotPassword(c *fiber.Ctx) error {
	return a.forgotPasswordForSession(c, auth.SessionTypeApp)
}

func (a *App) adminForgotPassword(c *fiber.Ctx) error {
	return a.forgotPasswordForSession(c, auth.SessionTypeAdmin)
}

func (a *App) resetPassword(c *fiber.Ctx) error {
	return a.resetPasswordForSession(c, auth.SessionTypeApp)
}

func (a *App) adminResetPassword(c *fiber.Ctx) error {
	return a.resetPasswordForSession(c, auth.SessionTypeAdmin)
}

func (a *App) changePassword(c *fiber.Ctx) error {
	route := requestRoutePath(c)
	logAPIStep(c, "auth", route, "request_received", "change_password", nil)

	claims := MustClaims(c)
	if claims == nil {
		logAPIStep(c, "auth", route, "request_unauthorized", "change_password", nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}

	var req changePasswordRequest
	if err := c.BodyParser(&req); err != nil {
		logAPIStep(c, "auth", route, "request_parse_error", "change_password", map[string]any{"error": err})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}

	currentPassword := strings.TrimSpace(req.CurrentPassword)
	newPassword := strings.TrimSpace(req.Password)
	if currentPassword == "" || newPassword == "" {
		logAPIStep(c, "auth", route, "request_validation_error", "change_password", map[string]any{
			"current_password_present": currentPassword != "",
			"new_password_present":     newPassword != "",
		})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "current password and new password are required"})
	}

	ctx := requestContext(c)
	logAPIStep(c, "auth", route, "user_lookup_start", "change_password", nil)
	user, err := a.Store.GetUserByID(ctx, claims.UserID)
	if err != nil {
		logAPIStep(c, "auth", route, "user_lookup_error", "change_password", map[string]any{"error": err})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load user"})
	}
	if user == nil {
		logAPIStep(c, "auth", route, "user_lookup_missing", "change_password", nil)
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load user"})
	}
	if !auth.VerifyPassword(user.PasswordHash, req.CurrentPassword) {
		logAPIStep(c, "auth", route, "current_password_rejected", "change_password", nil)
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid current password"})
	}

	logAPIStep(c, "auth", route, "password_hash_start", "change_password", nil)
	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		logAPIStep(c, "auth", route, "password_hash_error", "change_password", map[string]any{"error": err})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: err.Error()})
	}

	logAPIStep(c, "auth", route, "password_update_start", "change_password", nil)
	if err := a.Store.ChangeUserPassword(ctx, user.ID, passwordHash); err != nil {
		logAPIStep(c, "auth", route, "password_update_error", "change_password", map[string]any{"error": err})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to change password"})
	}
	logAPIStep(c, "auth", route, "response_ready", "change_password", nil)
	return c.SendStatus(fiber.StatusNoContent)
}

func (a *App) loginForSession(c *fiber.Ctx, sessionType auth.SessionType) error {
	route := requestRoutePath(c)
	logAPIStep(c, "auth", route, "request_received", sessionType.String(), map[string]any{"session_type": sessionType.String()})

	var req loginRequest
	if err := c.BodyParser(&req); err != nil {
		logAPIStep(c, "auth", route, "request_parse_error", sessionType.String(), map[string]any{"error": err})
		backenderrors.RecordLog(requestContext(c), "auth", route, "login_request_failed", sessionType.String(), map[string]any{
			"status_code":  fiber.StatusBadRequest,
			"error":        err,
			"session_type": sessionType.String(),
		})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || strings.TrimSpace(req.Password) == "" {
		logAPIStep(c, "auth", route, "request_validation_error", sessionType.String(), map[string]any{
			"email_present":    email != "",
			"password_present": strings.TrimSpace(req.Password) != "",
		})
		backenderrors.RecordLog(requestContext(c), "auth", route, "login_request_failed", sessionType.String(), map[string]any{
			"status_code":      fiber.StatusBadRequest,
			"error":            "email and password are required",
			"email_present":    email != "",
			"password_present": strings.TrimSpace(req.Password) != "",
			"session_type":     sessionType.String(),
		})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "email and password are required"})
	}
	ctx := requestContext(c)
	logAPIStep(c, "auth", route, "user_lookup_start", sessionType.String(), map[string]any{"session_type": sessionType.String()})
	user, err := a.Store.FindUserByEmail(ctx, email)
	if err != nil {
		logAPIStep(c, "auth", route, "user_lookup_error", sessionType.String(), map[string]any{"error": err})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load user"})
	}
	if user == nil || !auth.VerifyPassword(user.PasswordHash, req.Password) {
		logAPIStep(c, "auth", route, "credentials_rejected", sessionType.String(), nil)
		backenderrors.RecordLog(requestContext(c), "auth", route, "login_failed", sessionType.String(), map[string]any{
			"status_code":  fiber.StatusUnauthorized,
			"error":        "invalid credentials",
			"email":        email,
			"session_type": sessionType.String(),
		})
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "invalid credentials"})
	}
	if user.Status != "active" {
		logAPIStep(c, "auth", route, "user_inactive", sessionType.String(), nil)
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "user is inactive"})
	}
	logAPIStep(c, "auth", route, "session_issue_start", sessionType.String(), map[string]any{"session_type": sessionType.String()})
	payload, status, err := a.issueSession(ctx, user, sessionType)
	if err != nil {
		logAPIStep(c, "auth", route, "session_issue_error", sessionType.String(), map[string]any{
			"status": status,
			"error":  err,
		})
		if status == fiber.StatusForbidden {
			return c.Status(status).JSON(ErrorResponse{Error: "admin scope required"})
		}
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to issue session"})
	}
	setSessionCookies(c, payload, a.Config.CookieSecure)
	if sessionType == auth.SessionTypeAdmin {
		a.auditLogLogin(ctx, payload.Response)
	}
	logAPIStep(c, "auth", route, "response_ready", sessionType.String(), map[string]any{"session_type": sessionType.String()})
	return c.JSON(payload.Response)
}

func (a *App) refresh(c *fiber.Ctx) error {
	return a.refreshSession(c, auth.SessionTypeApp)
}

func (a *App) adminRefresh(c *fiber.Ctx) error {
	return a.refreshSession(c, auth.SessionTypeAdmin)
}

func (a *App) refreshSession(c *fiber.Ctx, sessionType auth.SessionType) error {
	route := requestRoutePath(c)
	logAPIStep(c, "auth", route, "request_received", sessionType.String(), map[string]any{"session_type": sessionType.String()})

	raw := strings.TrimSpace(c.Cookies(sessionType.RefreshCookieName()))
	if raw == "" {
		logAPIStep(c, "auth", route, "refresh_token_missing", sessionType.String(), nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "missing refresh token"})
	}
	logAPIStep(c, "auth", route, "refresh_token_parse_start", sessionType.String(), nil)
	sessionID, _, err := auth.ParseRefreshToken(raw)
	if err != nil {
		logAPIStep(c, "auth", route, "refresh_token_parse_error", sessionType.String(), map[string]any{"error": err})
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "invalid refresh token"})
	}
	ctx := requestContext(c)
	logAPIStep(c, "auth", route, "refresh_session_load_start", sessionType.String(), nil)
	session, err := a.Store.GetRefreshSessionByID(ctx, sessionID)
	if err != nil {
		logAPIStep(c, "auth", route, "refresh_session_load_error", sessionType.String(), map[string]any{"error": err})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load refresh session"})
	}
	if session == nil || session.RevokedAt.Valid || session.ExpiresAt.Before(time.Now().UTC()) || session.SessionType != sessionType.String() {
		logAPIStep(c, "auth", route, "refresh_session_invalid", sessionType.String(), map[string]any{
			"revoked": session != nil && session.RevokedAt.Valid,
		})
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "refresh token expired"})
	}
	if !auth.VerifyRefreshHash(session.TokenHash, raw) {
		_ = a.Store.RevokeRefreshSession(ctx, session.ID)
		logAPIStep(c, "auth", route, "refresh_token_rejected", sessionType.String(), nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "invalid refresh token"})
	}
	logAPIStep(c, "auth", route, "user_lookup_start", sessionType.String(), nil)
	user, err := a.Store.GetUserByID(ctx, session.UserID)
	if err != nil {
		logAPIStep(c, "auth", route, "user_lookup_error", sessionType.String(), map[string]any{"error": err})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load user"})
	}
	if user == nil || user.Status != "active" {
		_ = a.Store.RevokeRefreshSession(ctx, session.ID)
		logAPIStep(c, "auth", route, "user_unavailable", sessionType.String(), nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "user unavailable"})
	}

	csrfToken := ""
	if sessionType == auth.SessionTypeAdmin {
		logAPIStep(c, "auth", route, "csrf_token_start", sessionType.String(), nil)
		csrfToken, err = auth.NewCSRFToken()
		if err != nil {
			logAPIStep(c, "auth", route, "csrf_token_error", sessionType.String(), map[string]any{"error": err})
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to refresh session"})
		}
	}
	logAPIStep(c, "auth", route, "response_build_start", sessionType.String(), nil)
	response, err := a.buildAuthResponse(ctx, user, runtimeModeForSession(sessionType), csrfToken)
	if err != nil {
		logAPIStep(c, "auth", route, "response_build_error", sessionType.String(), map[string]any{"error": err})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to refresh session"})
	}
	if sessionType == auth.SessionTypeAdmin && !canUseAdminPanel(response) {
		_ = a.Store.RevokeRefreshSession(ctx, session.ID)
		logAPIStep(c, "auth", route, "admin_scope_required", sessionType.String(), nil)
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "admin scope required"})
	}

	logAPIStep(c, "auth", route, "access_token_issue_start", sessionType.String(), nil)
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
		logAPIStep(c, "auth", route, "access_token_issue_error", sessionType.String(), map[string]any{"error": err})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to refresh access token"})
	}
	logAPIStep(c, "auth", route, "refresh_token_issue_start", sessionType.String(), nil)
	newRefresh, err := auth.NewRefreshToken(refreshTTLForSession(a.Config, sessionType))
	if err != nil {
		logAPIStep(c, "auth", route, "refresh_token_issue_error", sessionType.String(), map[string]any{"error": err})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to refresh token"})
	}
	_, tokenPart, err := auth.ParseRefreshToken(newRefresh.RawToken)
	if err != nil {
		logAPIStep(c, "auth", route, "refresh_token_parse_error", sessionType.String(), map[string]any{"error": err})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to parse refresh token"})
	}
	rotatedRaw := session.ID + "." + tokenPart
	rotatedHash := auth.HashRefreshToken(rotatedRaw)
	logAPIStep(c, "auth", route, "refresh_session_rotate_start", sessionType.String(), nil)
	if err := a.Store.RotateRefreshSession(ctx, session.ID, rotatedHash, newRefresh.ExpiresAt); err != nil {
		logAPIStep(c, "auth", route, "refresh_session_rotate_error", sessionType.String(), map[string]any{"error": err})
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
	logAPIStep(c, "auth", route, "response_ready", sessionType.String(), map[string]any{"session_type": sessionType.String()})
	return c.JSON(response)
}

func (a *App) logout(c *fiber.Ctx) error {
	return a.logoutSession(c, auth.SessionTypeApp)
}

func (a *App) adminLogout(c *fiber.Ctx) error {
	return a.logoutSession(c, auth.SessionTypeAdmin)
}

func (a *App) logoutSession(c *fiber.Ctx, sessionType auth.SessionType) error {
	route := requestRoutePath(c)
	logAPIStep(c, "auth", route, "request_received", sessionType.String(), map[string]any{"session_type": sessionType.String()})

	raw := strings.TrimSpace(c.Cookies(sessionType.RefreshCookieName()))
	if raw != "" {
		logAPIStep(c, "auth", route, "refresh_token_parse_start", sessionType.String(), nil)
		sessionID, _, err := auth.ParseRefreshToken(raw)
		if err == nil {
			ctx := requestContext(c)
			logAPIStep(c, "auth", route, "refresh_session_load_start", sessionType.String(), nil)
			session, loadErr := a.Store.GetRefreshSessionByID(ctx, sessionID)
			if loadErr == nil && session != nil && session.SessionType == sessionType.String() {
				_ = a.Store.RevokeRefreshSession(ctx, sessionID)
				logAPIStep(c, "auth", route, "refresh_session_revoked", sessionType.String(), nil)
			}
		}
	}
	clearSessionCookies(c, a.Config.CookieSecure, sessionType)
	logAPIStep(c, "auth", route, "response_ready", sessionType.String(), nil)
	return c.SendStatus(fiber.StatusNoContent)
}

func (a *App) me(c *fiber.Ctx) error {
	return a.meForSession(c, auth.SessionTypeApp)
}

func (a *App) adminMe(c *fiber.Ctx) error {
	return a.meForSession(c, auth.SessionTypeAdmin)
}

func (a *App) meForSession(c *fiber.Ctx, sessionType auth.SessionType) error {
	route := requestRoutePath(c)
	logAPIStep(c, "auth", route, "request_received", sessionType.String(), map[string]any{"session_type": sessionType.String()})

	claims := MustClaims(c)
	if claims == nil {
		logAPIStep(c, "auth", route, "request_unauthorized", sessionType.String(), nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
	}
	ctx := requestContext(c)
	logAPIStep(c, "auth", route, "user_lookup_start", sessionType.String(), nil)
	user, err := a.Store.GetUserByID(ctx, claims.UserID)
	if err != nil {
		logAPIStep(c, "auth", route, "user_lookup_error", sessionType.String(), map[string]any{"error": err})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to load user"})
	}
	if user == nil || user.Status != "active" {
		logAPIStep(c, "auth", route, "user_unavailable", sessionType.String(), nil)
		return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "user unavailable"})
	}
	logAPIStep(c, "auth", route, "response_build_start", sessionType.String(), nil)
	response, err := a.buildAuthResponse(ctx, user, runtimeModeForSession(sessionType), claims.CSRFToken)
	if err != nil {
		logAPIStep(c, "auth", route, "response_build_error", sessionType.String(), map[string]any{"error": err})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to resolve user context"})
	}
	if sessionType == auth.SessionTypeAdmin && !canUseAdminPanel(response) {
		logAPIStep(c, "auth", route, "admin_scope_required", sessionType.String(), nil)
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "admin scope required"})
	}
	logAPIStep(c, "auth", route, "response_ready", sessionType.String(), nil)
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

func (a *App) auditLogLogin(ctx context.Context, response AuthResponse) {
	_ = a.Store.InsertAuditLog(ctx, store.AuditLogInput{
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
