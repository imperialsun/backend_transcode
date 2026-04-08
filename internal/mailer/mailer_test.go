package mailer

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"strings"
	"testing"
	"time"

	"demeter-backend/internal/auth"
)

// These tests cover message rendering, multipart encoding, and readiness
// checks for the SMTP mailer.
func TestSMTPMailerReadyRejectsIncompleteConfig(t *testing.T) {
	mailer := NewSMTPMailer(Config{})
	if err := mailer.Ready(); err == nil {
		t.Fatal("expected Ready to reject incomplete SMTP config")
	}
}

// TestBuildPasswordResetMessageIncludesResetURL verifies the password-reset
// body contains the expected call-to-action links.
func TestBuildPasswordResetMessageIncludesResetURL(t *testing.T) {
	mailer := NewSMTPMailer(Config{
		Host:      "smtp.demeter.test",
		Port:      587,
		FromEmail: "noreply@demeter.test",
		FromName:  "Demeter",
	})

	message, err := mailer.buildPasswordResetMessage(PasswordResetEmail{
		ToEmail:        "user@example.com",
		ResetURL:       "https://app.demeter.test/reset-password?token=abc",
		ApplicationURL: "https://app.demeter.test/",
		ExpiresAt:      time.Date(2026, time.March, 13, 18, 0, 0, 0, time.UTC),
		SessionType:    auth.SessionTypeAdmin,
	})
	if err != nil {
		t.Fatalf("buildPasswordResetMessage returned error: %v", err)
	}
	raw := string(message)
	if !strings.Contains(raw, "https://app.demeter.test/reset-password?token=abc") {
		t.Fatalf("expected message to contain reset url, got %q", raw)
	}
	if !strings.Contains(raw, "https://app.demeter.test/") {
		t.Fatalf("expected message to contain application url, got %q", raw)
	}
	if !strings.Contains(raw, "Accéder à l'application") {
		t.Fatalf("expected application CTA in message, got %q", raw)
	}
	if !strings.Contains(raw, "administration Demeter Speech") {
		t.Fatalf("expected admin-specific content, got %q", raw)
	}
}

// TestBuildMeetingSummaryMessageIncludesAttachments verifies that meeting
// summaries render as multipart messages with attached DOCX files.
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

// TestBuildMeetingSummaryMessageEncodesAccentedHeaders verifies that accented
// display names and subjects are MIME-encoded correctly.
func TestBuildMeetingSummaryMessageEncodesAccentedHeaders(t *testing.T) {
	mailer := NewSMTPMailer(Config{
		Host:      "smtp.demeter.test",
		Port:      587,
		FromEmail: "noreply@demeter.test",
		FromName:  "Équipe Démo",
	})

	message, err := mailer.buildMeetingSummaryMessage(MeetingSummaryEmail{
		ToEmail:  "user@example.com",
		Subject:  "Compte rendu de réunion - Revue qualité",
		TextBody: "Bonjour,\nVoici le compte rendu.",
		Attachments: []MailAttachment{
			{Filename: "rapport-équipe.docx", ContentType: DocxContentType, Data: []byte("docx-1")},
		},
	})
	if err != nil {
		t.Fatalf("buildMeetingSummaryMessage returned error: %v", err)
	}

	msg, err := mail.ReadMessage(bytes.NewReader(message))
	if err != nil {
		t.Fatalf("failed to parse built message: %v", err)
	}

	rawSubject := msg.Header.Get("Subject")
	if !strings.Contains(rawSubject, "=?utf-8?") {
		t.Fatalf("expected encoded subject header, got %q", rawSubject)
	}
	rawFrom := msg.Header.Get("From")
	if !strings.Contains(rawFrom, "=?utf-8?") {
		t.Fatalf("expected encoded from header, got %q", rawFrom)
	}

	decoder := mime.WordDecoder{}
	subject, err := decoder.DecodeHeader(rawSubject)
	if err != nil {
		t.Fatalf("failed to decode subject: %v", err)
	}
	if subject != "Compte rendu de réunion - Revue qualité" {
		t.Fatalf("expected decoded subject with accents, got %q", subject)
	}

	from, err := decoder.DecodeHeader(rawFrom)
	if err != nil {
		t.Fatalf("failed to decode from header: %v", err)
	}
	if from != "Équipe Démo <noreply@demeter.test>" {
		t.Fatalf("expected decoded from header with accents, got %q", from)
	}

	mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("failed to parse top-level content type: %v", err)
	}
	if mediaType != "multipart/mixed" {
		t.Fatalf("expected multipart/mixed top-level content type, got %q", mediaType)
	}
	boundary := params["boundary"]
	if boundary == "" {
		t.Fatal("expected multipart boundary")
	}

	reader := multipart.NewReader(msg.Body, boundary)
	textPart, err := reader.NextPart()
	if err != nil {
		t.Fatalf("failed to read text part: %v", err)
	}
	if got := textPart.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Fatalf("expected first part to be text/plain, got %q", got)
	}

	attachmentPart, err := reader.NextPart()
	if err != nil {
		t.Fatalf("failed to read attachment part: %v", err)
	}
	if _, err := reader.NextPart(); err != io.EOF {
		t.Fatalf("expected only one attachment part, got err=%v", err)
	}

	attachmentType, attachmentParams, err := mime.ParseMediaType(attachmentPart.Header.Get("Content-Type"))
	if err != nil {
		t.Fatalf("failed to parse attachment content type: %v", err)
	}
	if attachmentType != DocxContentType {
		t.Fatalf("expected docx attachment type, got %q", attachmentType)
	}
	if got := attachmentParams["name"]; got != "rapport-équipe.docx" {
		t.Fatalf("expected decoded attachment name, got %q", got)
	}

	dispositionType, dispositionParams, err := mime.ParseMediaType(attachmentPart.Header.Get("Content-Disposition"))
	if err != nil {
		t.Fatalf("failed to parse attachment content disposition: %v", err)
	}
	if dispositionType != "attachment" {
		t.Fatalf("expected attachment disposition, got %q", dispositionType)
	}
	if got := dispositionParams["filename"]; got != "rapport-équipe.docx" {
		t.Fatalf("expected decoded attachment filename, got %q", got)
	}
}

// TestBuildUserProvisioningMessageIncludesLoginAndPassword verifies the onboarding
// email contains the generated credentials.
func TestBuildUserProvisioningMessageIncludesLoginAndPassword(t *testing.T) {
	mailer := NewSMTPMailer(Config{
		Host:      "smtp.demeter.test",
		Port:      587,
		FromEmail: "noreply@demeter.test",
		FromName:  "Demeter",
	})

	message, err := mailer.buildUserProvisioningMessage(UserProvisioningEmail{
		ToEmail:           "user@example.com",
		Login:             "user@example.com",
		TemporaryPassword: "TmpPass-123456",
		ApplicationURL:    "https://app.demeter.test/",
	})
	if err != nil {
		t.Fatalf("buildUserProvisioningMessage returned error: %v", err)
	}
	raw := string(message)
	if !strings.Contains(raw, "user@example.com") {
		t.Fatalf("expected login in message, got %q", raw)
	}
	if !strings.Contains(raw, "TmpPass-123456") {
		t.Fatalf("expected temporary password in message, got %q", raw)
	}
	if !strings.Contains(raw, "https://app.demeter.test/") {
		t.Fatalf("expected application url in message, got %q", raw)
	}
	if !strings.Contains(raw, "Accéder à l'application") {
		t.Fatalf("expected application CTA in message, got %q", raw)
	}
	if !strings.Contains(raw, "Vos identifiants Demeter Speech") {
		t.Fatalf("expected provisioning subject, got %q", raw)
	}
}

// TestSendMeetingSummaryEmailRequiresReadyMailer verifies that the runtime
// readiness check blocks sends when SMTP is not configured.
func TestSendMeetingSummaryEmailRequiresReadyMailer(t *testing.T) {
	mailer := NewSMTPMailer(Config{})
	if err := mailer.SendMeetingSummaryEmail(context.Background(), MeetingSummaryEmail{ToEmail: "user@example.com"}); err == nil {
		t.Fatal("expected SendMeetingSummaryEmail to fail when SMTP config is incomplete")
	}
}
