package api

import (
	"context"
	"errors"
	"log"
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
	var req forgotPasswordRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "email is required"})
	}

	if err := a.requestPasswordResetByEmail(context.Background(), email, sessionType, ""); err != nil {
		log.Printf("[password-reset] request email failed session=%s email=%q err=%v", sessionType, email, err)
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func (a *App) resetPasswordForSession(c *fiber.Ctx, sessionType auth.SessionType) error {
	var req resetPasswordRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid payload"})
	}

	token := strings.TrimSpace(req.Token)
	if token == "" || strings.TrimSpace(req.Password) == "" {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "token and password are required"})
	}

	passwordHash, err := auth.HashPassword(req.Password)
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: err.Error()})
	}

	record, err := a.Store.ApplyPasswordReset(
		context.Background(),
		auth.HashPasswordResetToken(token),
		passwordHash,
		sessionType.String(),
	)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to reset password"})
	}
	if record == nil {
		return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "invalid or expired password reset token"})
	}

	return c.SendStatus(fiber.StatusNoContent)
}

func (a *App) requestPasswordResetByEmail(
	ctx context.Context,
	email string,
	sessionType auth.SessionType,
	requestedByUserID string,
) error {
	user, err := a.Store.FindUserByEmail(ctx, email)
	if err != nil {
		return err
	}
	if user == nil || user.Status != "active" {
		return nil
	}
	return a.sendPasswordResetForUser(ctx, user, sessionType, requestedByUserID)
}

func (a *App) sendPasswordResetForUser(
	ctx context.Context,
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
	if err := a.ensurePasswordResetAvailable(sessionType); err != nil {
		return err
	}

	if err := a.Store.RevokePasswordResetTokensByUser(ctx, user.ID, sessionType.String()); err != nil {
		return err
	}

	token, err := auth.NewPasswordResetToken(a.Config.PasswordResetTTL)
	if err != nil {
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
	if err := a.Store.SavePasswordResetToken(ctx, record); err != nil {
		return err
	}

	resetURL, err := a.passwordResetURL(sessionType, token.RawToken)
	if err != nil {
		_ = a.Store.RevokePasswordResetTokensByUser(ctx, user.ID, sessionType.String())
		return err
	}

	if err := a.Mailer.SendPasswordResetEmail(ctx, mailer.PasswordResetEmail{
		ToEmail:     user.Email,
		ResetURL:    resetURL,
		ExpiresAt:   token.ExpiresAt,
		SessionType: sessionType,
	}); err != nil {
		_ = a.Store.RevokePasswordResetTokensByUser(ctx, user.ID, sessionType.String())
		return err
	}
	return nil
}

func (a *App) ensurePasswordResetAvailable(sessionType auth.SessionType) error {
	if _, err := a.passwordResetBaseURL(sessionType); err != nil {
		return errPasswordResetUnavailable
	}
	if a.Mailer == nil {
		return errPasswordResetUnavailable
	}
	if err := a.Mailer.Ready(); err != nil {
		return errPasswordResetUnavailable
	}
	return nil
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
