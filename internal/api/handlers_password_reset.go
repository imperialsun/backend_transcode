package api

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"database/sql"
	"demeter-backend/internal/auth"
	"demeter-backend/internal/mailer"
	"demeter-backend/internal/store"
	"github.com/gofiber/fiber/v2"
)

type forgotPasswordRequest struct {
	Email string `json:"email"`
}

type resetPasswordRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

var errPasswordResetUnavailable = errors.New("password reset unavailable")

func (a *App) forgotPasswordForSession(c *fiber.Ctx, sessionType auth.SessionType) error {
	route := requestRoutePath(c)
	logAPIStep(c, "password-reset", route, "request_received", sessionType.String(), map[string]any{"session_type": sessionType.String()})

	var req forgotPasswordRequest
	if err := c.BodyParser(&req); err != nil {
		logAPIStep(c, "password-reset", route, "request_parse_error", sessionType.String(), map[string]any{"error": err})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" {
		logAPIStep(c, "password-reset", route, "request_validation_error", sessionType.String(), map[string]any{"email_present": false})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "email is required"})
	}

	logAPIStep(c, "password-reset", route, "request_lookup_start", sessionType.String(), map[string]any{"session_type": sessionType.String()})
	if err := a.requestPasswordResetByEmail(requestContext(c), route, email, sessionType, ""); err != nil {
		logAPIStep(c, "password-reset", route, "request_email_error", sessionType.String(), map[string]any{"error": err})
	} else {
		logAPIStep(c, "password-reset", route, "request_email_accepted", sessionType.String(), nil)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func (a *App) resetPasswordForSession(c *fiber.Ctx, sessionType auth.SessionType) error {
	route := requestRoutePath(c)
	logAPIStep(c, "password-reset", route, "request_received", sessionType.String(), map[string]any{"session_type": sessionType.String()})

	var req resetPasswordRequest
	if err := c.BodyParser(&req); err != nil {
		logAPIStep(c, "password-reset", route, "request_parse_error", sessionType.String(), map[string]any{"error": err})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}

	token := strings.TrimSpace(req.Token)
	if token == "" || strings.TrimSpace(req.Password) == "" {
		logAPIStep(c, "password-reset", route, "request_validation_error", sessionType.String(), map[string]any{
			"token_present":    token != "",
			"password_present": strings.TrimSpace(req.Password) != "",
		})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "token and password are required"})
	}

	logAPIStep(c, "password-reset", route, "password_hash_start", sessionType.String(), nil)
	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		logAPIStep(c, "password-reset", route, "password_hash_error", sessionType.String(), map[string]any{"error": err})
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: err.Error()})
	}

	logAPIStep(c, "password-reset", route, "reset_apply_start", sessionType.String(), nil)
	record, err := a.Store.ApplyPasswordReset(
		requestContext(c),
		auth.HashPasswordResetToken(token),
		passwordHash,
		sessionType.String(),
	)
	if err != nil {
		logAPIStep(c, "password-reset", route, "reset_apply_error", sessionType.String(), map[string]any{"error": err})
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to reset password"})
	}
	if record == nil {
		logAPIStep(c, "password-reset", route, "reset_token_invalid", sessionType.String(), nil)
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid or expired password reset token"})
	}

	logAPIStep(c, "password-reset", route, "response_ready", sessionType.String(), nil)
	return c.SendStatus(fiber.StatusNoContent)
}

func (a *App) requestPasswordResetByEmail(
	ctx context.Context,
	route string,
	email string,
	sessionType auth.SessionType,
	requestedByUserID string,
) error {
	logContextStep(ctx, "password-reset", route, "lookup_start", sessionType.String(), map[string]any{"session_type": sessionType.String()})
	user, err := a.Store.FindUserByEmail(ctx, email)
	if err != nil {
		logContextStep(ctx, "password-reset", route, "lookup_error", sessionType.String(), map[string]any{"error": err})
		return err
	}
	if user == nil || user.Status != "active" {
		logContextStep(ctx, "password-reset", route, "lookup_skipped", sessionType.String(), nil)
		return nil
	}
	return a.sendPasswordResetForUser(ctx, route, user, sessionType, requestedByUserID)
}

func (a *App) sendPasswordResetForUser(
	ctx context.Context,
	route string,
	user *store.User,
	sessionType auth.SessionType,
	requestedByUserID string,
) error {
	if user == nil || strings.TrimSpace(user.ID) == "" {
		return nil
	}
	if user.Status != "active" {
		return fiber.NewError(fiber.StatusBadRequest, "user is inactive")
	}
	applicationURL, err := a.ensurePasswordResetAvailable(sessionType)
	if err != nil {
		logContextStep(ctx, "password-reset", route, "mailer_unavailable", sessionType.String(), map[string]any{"error": err})
		return err
	}

	logContextStep(ctx, "password-reset", route, "token_revoke_start", sessionType.String(), nil)
	if err := a.Store.RevokePasswordResetTokensByUser(ctx, user.ID, sessionType.String()); err != nil {
		logContextStep(ctx, "password-reset", route, "token_revoke_error", sessionType.String(), map[string]any{"error": err})
		return err
	}

	logContextStep(ctx, "password-reset", route, "token_issue_start", sessionType.String(), nil)
	token, err := auth.NewPasswordResetToken(a.Config.PasswordResetTTL)
	if err != nil {
		logContextStep(ctx, "password-reset", route, "token_issue_error", sessionType.String(), map[string]any{"error": err})
		return err
	}
	record := store.PasswordResetToken{
		UserID:      user.ID,
		SessionType: sessionType.String(),
		TokenHash:   token.Hash,
		ExpiresAt:   token.ExpiresAt,
		CreatedAt:   time.Now().UTC(),
	}
	if strings.TrimSpace(requestedByUserID) != "" {
		record.RequestedByUserID = sql.NullString{String: strings.TrimSpace(requestedByUserID), Valid: true}
	}
	logContextStep(ctx, "password-reset", route, "token_save_start", sessionType.String(), nil)
	if err := a.Store.SavePasswordResetToken(ctx, record); err != nil {
		logContextStep(ctx, "password-reset", route, "token_save_error", sessionType.String(), map[string]any{"error": err})
		return err
	}

	logContextStep(ctx, "password-reset", route, "reset_url_build_start", sessionType.String(), nil)
	resetURL, err := a.passwordResetURL(sessionType, token.RawToken)
	if err != nil {
		_ = a.Store.RevokePasswordResetTokensByUser(ctx, user.ID, sessionType.String())
		logContextStep(ctx, "password-reset", route, "reset_url_build_error", sessionType.String(), map[string]any{"error": err})
		return err
	}

	logContextStep(ctx, "password-reset", route, "mailer_send_start", sessionType.String(), nil)
	if err := a.Mailer.SendPasswordResetEmail(ctx, mailer.PasswordResetEmail{
		ToEmail:        user.Email,
		ResetURL:       resetURL,
		ApplicationURL: applicationURL,
		ExpiresAt:      token.ExpiresAt,
		SessionType:    sessionType,
	}); err != nil {
		_ = a.Store.RevokePasswordResetTokensByUser(ctx, user.ID, sessionType.String())
		logContextStep(ctx, "password-reset", route, "mailer_send_error", sessionType.String(), map[string]any{"error": err})
		return err
	}
	logContextStep(ctx, "password-reset", route, "mailer_send_success", sessionType.String(), nil)
	return nil
}

func (a *App) ensurePasswordResetAvailable(sessionType auth.SessionType) (string, error) {
	applicationURL, err := a.applicationPublicURL()
	if err != nil {
		return "", errPasswordResetUnavailable
	}
	if _, err := a.passwordResetBaseURL(sessionType); err != nil {
		return "", errPasswordResetUnavailable
	}
	if a.Mailer == nil {
		return "", errPasswordResetUnavailable
	}
	if err := a.Mailer.Ready(); err != nil {
		return "", errPasswordResetUnavailable
	}
	return applicationURL, nil
}

func (a *App) applicationPublicURL() (string, error) {
	baseURL := strings.TrimSpace(a.Config.AppPublicURL)
	if baseURL == "" {
		return "", errPasswordResetUnavailable
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errPasswordResetUnavailable
	}
	path := strings.TrimSpace(parsed.Path)
	if path != "" && path != "/" {
		return "", errPasswordResetUnavailable
	}
	if strings.TrimSpace(parsed.RawQuery) != "" || strings.TrimSpace(parsed.Fragment) != "" {
		return "", errPasswordResetUnavailable
	}
	return strings.TrimRight(baseURL, "/") + "/", nil
}

func (a *App) passwordResetURL(sessionType auth.SessionType, token string) (string, error) {
	baseURL, err := a.passwordResetBaseURL(sessionType)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(baseURL, "/") + "/reset-password?token=" + url.QueryEscape(strings.TrimSpace(token)), nil
}

func (a *App) passwordResetBaseURL(sessionType auth.SessionType) (string, error) {
	baseURL := strings.TrimSpace(a.Config.AppPublicURL)
	if sessionType == auth.SessionTypeAdmin {
		baseURL = strings.TrimSpace(a.Config.AdminPublicURL)
	}
	if baseURL == "" {
		return "", errPasswordResetUnavailable
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errPasswordResetUnavailable
	}
	return strings.TrimRight(baseURL, "/"), nil
}
