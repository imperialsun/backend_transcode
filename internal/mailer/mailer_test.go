package mailer

import (
	"strings"
	"testing"
	"time"

	"demeter-backend/internal/auth"
)

func TestSMTPMailerReadyRejectsIncompleteConfig(t *testing.T) {
	mailer := NewSMTPMailer(Config{})
	if err := mailer.Ready(); err == nil {
		t.Fatal("expected Ready to reject incomplete SMTP config")
	}
}

func TestBuildPasswordResetMessageIncludesResetURL(t *testing.T) {
	mailer := NewSMTPMailer(Config{
		Host:      "smtp.demeter.test",
		Port:      587,
		FromEmail: "noreply@demeter.test",
		FromName:  "Demeter",
	})

	message, err := mailer.buildPasswordResetMessage(PasswordResetEmail{
		ToEmail:     "user@example.com",
		ResetURL:    "https://app.demeter.test/reset-password?token=abc",
		ExpiresAt:   time.Date(2026, time.March, 13, 18, 0, 0, 0, time.UTC),
		SessionType: auth.SessionTypeAdmin,
	})
	if err != nil {
		t.Fatalf("buildPasswordResetMessage returned error: %v", err)
	}
	raw := string(message)
	if !strings.Contains(raw, "https://app.demeter.test/reset-password?token=abc") {
		t.Fatalf("expected message to contain reset url, got %q", raw)
	}
	if !strings.Contains(raw, "administration Demeter Speech") {
		t.Fatalf("expected admin-specific content, got %q", raw)
	}
}
