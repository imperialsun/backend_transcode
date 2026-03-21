package mailer

import (
	"context"
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

func TestBuildMeetingSummaryMessageIncludesAttachments(t *testing.T) {
	mailer := NewSMTPMailer(Config{
		Host:      "smtp.demeter.test",
		Port:      587,
		FromEmail: "noreply@demeter.test",
		FromName:  "Demeter",
	})

	message, err := mailer.buildMeetingSummaryMessage(MeetingSummaryEmail{
		ToEmail:  "user@example.com",
		Subject:  "Compte rendu de reunion",
		TextBody: "Bonjour,\nLa reunion est terminee.",
		HTMLBody: "<p>Bonjour,</p><p>La reunion est terminee.</p>",
		Attachments: []MailAttachment{
			{Filename: "transcription.docx", ContentType: DocxContentType, Data: []byte("docx-1")},
			{Filename: "rapport-cri.docx", ContentType: DocxContentType, Data: []byte("docx-2")},
		},
	})
	if err != nil {
		t.Fatalf("buildMeetingSummaryMessage returned error: %v", err)
	}
	raw := string(message)
	if !strings.Contains(raw, "transcription.docx") || !strings.Contains(raw, "rapport-cri.docx") {
		t.Fatalf("expected attachments in message, got %q", raw)
	}
	if !strings.Contains(raw, "multipart/mixed") || !strings.Contains(raw, "multipart/alternative") {
		t.Fatalf("expected nested multipart body, got %q", raw)
	}
}

func TestSendMeetingSummaryEmailRequiresReadyMailer(t *testing.T) {
	mailer := NewSMTPMailer(Config{})
	if err := mailer.SendMeetingSummaryEmail(context.Background(), MeetingSummaryEmail{ToEmail: "user@example.com"}); err == nil {
		t.Fatal("expected SendMeetingSummaryEmail to fail when SMTP config is incomplete")
	}
}
