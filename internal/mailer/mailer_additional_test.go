package mailer

import (
	"testing"
	"time"

	"demeter-backend/internal/auth"
)

func TestBuildPasswordResetMessage_RequiresData(t *testing.T) {
	m := NewSMTPMailer(Config{Host: "smtp.test", Port: 587, FromEmail: "noreply@test"})
	_, err := m.buildPasswordResetMessage(PasswordResetEmail{ToEmail: "", ResetURL: ""})
	if err == nil {
		t.Fatal("expected error when missing required fields")
	}

	_, err = m.buildPasswordResetMessage(PasswordResetEmail{ToEmail: "user@test", ResetURL: ""})
	if err == nil {
		t.Fatal("expected error when reset url is missing")
	}

	_, err = m.buildPasswordResetMessage(PasswordResetEmail{
		ToEmail:        "user@test",
		ResetURL:       "https://app.test/reset-password?token=abc",
		ApplicationURL: "",
	})
	if err == nil {
		t.Fatal("expected error when application url is missing")
	}
}

func TestBuildUserProvisioningMessage_RequiresApplicationURL(t *testing.T) {
	m := NewSMTPMailer(Config{Host: "smtp.test", Port: 587, FromEmail: "noreply@test"})
	_, err := m.buildUserProvisioningMessage(UserProvisioningEmail{
		ToEmail:           "user@test",
		Login:             "user@test",
		TemporaryPassword: "TmpPass-123456",
	})
	if err == nil {
		t.Fatal("expected error when application url is missing")
	}
}

func TestFormatAddress(t *testing.T) {
	if got := formatAddress("", "a@b.com"); got != "a@b.com" {
		t.Fatalf("expected simple address, got %q", got)
	}
	if got := formatAddress("Name", "a@b.com"); got != "\"Name\" <a@b.com>" {
		t.Fatalf("unexpected formatted address: %q", got)
	}
}

func TestSMTPMailerReady_RequiresConfig(t *testing.T) {
	m := NewSMTPMailer(Config{})
	if err := m.Ready(); err == nil {
		t.Fatal("expected Ready to fail on empty config")
	}
	m = NewSMTPMailer(Config{Host: "smtp.test", Port: 587, FromEmail: "noreply@test"})
	if err := m.Ready(); err != nil {
		t.Fatalf("expected Ready to succeed, got %v", err)
	}
	// Missing port should fail
	m = NewSMTPMailer(Config{Host: "smtp.test", Port: 0, FromEmail: "noreply@test"})
	if err := m.Ready(); err == nil {
		t.Fatal("expected Ready to fail when port is 0")
	}
	// Missing host should fail
	m = NewSMTPMailer(Config{Host: "", Port: 587, FromEmail: "noreply@test"})
	if err := m.Ready(); err == nil {
		t.Fatal("expected Ready to fail when host is empty")
	}
	// Missing from should fail
	m = NewSMTPMailer(Config{Host: "smtp.test", Port: 587, FromEmail: ""})
	if err := m.Ready(); err == nil {
		t.Fatal("expected Ready to fail when from email is empty")
	}

	// ensure tokens are stable for a moment
	_, _ = auth.NewPasswordResetToken(30 * time.Minute)
}
