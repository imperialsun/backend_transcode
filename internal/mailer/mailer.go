package mailer

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"log"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"time"

	"demeter-backend/internal/auth"
	"demeter-backend/internal/backenderrors"
	"demeter-backend/internal/observability"
)

var ErrUnavailable = errors.New("mailer unavailable")

const DocxContentType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

type Sender interface {
	Ready() error
	SendPasswordResetEmail(ctx context.Context, input PasswordResetEmail) error
	SendMeetingSummaryEmail(ctx context.Context, input MeetingSummaryEmail) error
	SendUserProvisioningEmail(ctx context.Context, input UserProvisioningEmail) error
}

type Config struct {
	Host      string
	Port      int
	Username  string
	Password  string
	FromEmail string
	FromName  string
}

type PasswordResetEmail struct {
	ToEmail     string
	ResetURL    string
	ExpiresAt   time.Time
	SessionType auth.SessionType
}

type MailAttachment struct {
	Filename    string
	ContentType string
	Data        []byte
}

type MeetingSummaryEmail struct {
	ToEmail     string
	Subject     string
	TextBody    string
	HTMLBody    string
	Attachments []MailAttachment
}

type UserProvisioningEmail struct {
	ToEmail           string
	Login             string
	TemporaryPassword string
}

type SMTPMailer struct {
	host      string
	port      int
	username  string
	password  string
	fromEmail string
	fromName  string
}

func NewSMTPMailer(cfg Config) *SMTPMailer {
	return &SMTPMailer{
		host:      strings.TrimSpace(cfg.Host),
		port:      cfg.Port,
		username:  strings.TrimSpace(cfg.Username),
		password:  cfg.Password,
		fromEmail: strings.TrimSpace(cfg.FromEmail),
		fromName:  strings.TrimSpace(cfg.FromName),
	}
}

func (m *SMTPMailer) Ready() error {
	if m == nil {
		return ErrUnavailable
	}
	if m.port <= 0 {
		return ErrUnavailable
	}
	if m.host == "" || m.fromEmail == "" {
		return ErrUnavailable
	}
	return nil
}

func (m *SMTPMailer) SendPasswordResetEmail(ctx context.Context, input PasswordResetEmail) error {
	if err := m.Ready(); err != nil {
		return err
	}

	logMailStep(ctx, "password_reset_request_received", map[string]any{
		"kind": "password_reset",
	})
	message, err := m.buildPasswordResetMessage(input)
	if err != nil {
		logMailStep(ctx, "password_reset_build_error", map[string]any{"error": err})
		return err
	}

	logMailStep(ctx, "password_reset_send_start", map[string]any{"message_bytes": len(message)})
	if err := m.sendMessage(ctx, input.ToEmail, message); err != nil {
		logMailStep(ctx, "password_reset_send_error", map[string]any{"error": err})
		return err
	}
	logMailStep(ctx, "password_reset_send_success", map[string]any{"message_bytes": len(message)})
	return nil
}

func (m *SMTPMailer) SendMeetingSummaryEmail(ctx context.Context, input MeetingSummaryEmail) error {
	if err := m.Ready(); err != nil {
		return err
	}

	logMailStep(ctx, "meeting_summary_request_received", map[string]any{
		"kind":             "meeting_summary",
		"attachment_count": len(input.Attachments),
	})
	message, err := m.buildMeetingSummaryMessage(input)
	if err != nil {
		logMailStep(ctx, "meeting_summary_build_error", map[string]any{"error": err})
		return err
	}

	logMailStep(ctx, "meeting_summary_send_start", map[string]any{
		"attachment_count": len(input.Attachments),
		"message_bytes":    len(message),
	})
	if err := m.sendMessage(ctx, input.ToEmail, message); err != nil {
		logMailStep(ctx, "meeting_summary_send_error", map[string]any{"error": err})
		return err
	}
	logMailStep(ctx, "meeting_summary_send_success", map[string]any{
		"attachment_count": len(input.Attachments),
		"message_bytes":    len(message),
	})
	return nil
}

func (m *SMTPMailer) SendUserProvisioningEmail(ctx context.Context, input UserProvisioningEmail) error {
	if err := m.Ready(); err != nil {
		return err
	}

	logMailStep(ctx, "provisioning_request_received", map[string]any{
		"kind": "user_provisioning",
	})
	message, err := m.buildUserProvisioningMessage(input)
	if err != nil {
		logMailStep(ctx, "provisioning_build_error", map[string]any{"error": err})
		return err
	}

	logMailStep(ctx, "provisioning_send_start", map[string]any{"message_bytes": len(message)})
	if err := m.sendMessage(ctx, input.ToEmail, message); err != nil {
		logMailStep(ctx, "provisioning_send_error", map[string]any{"error": err})
		return err
	}
	logMailStep(ctx, "provisioning_send_success", map[string]any{"message_bytes": len(message)})
	return nil
}

func (m *SMTPMailer) sendMessage(ctx context.Context, toEmail string, message []byte) error {
	logMailStep(ctx, "smtp_dial_start", map[string]any{
		"message_bytes": len(message),
	})
	address := net.JoinHostPort(m.host, fmt.Sprintf("%d", m.port))
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		logMailStep(ctx, "smtp_dial_error", map[string]any{"error": err})
		return err
	}
	logMailStep(ctx, "smtp_client_start", map[string]any{})
	client, err := smtp.NewClient(conn, m.host)
	if err != nil {
		_ = conn.Close()
		logMailStep(ctx, "smtp_client_error", map[string]any{"error": err})
		return err
	}
	defer func() {
		_ = client.Quit()
		_ = client.Close()
	}()

	if ok, _ := client.Extension("STARTTLS"); ok {
		logMailStep(ctx, "smtp_starttls_start", map[string]any{})
		if err := client.StartTLS(&tls.Config{ServerName: m.host, MinVersion: tls.VersionTLS12}); err != nil {
			logMailStep(ctx, "smtp_starttls_error", map[string]any{"error": err})
			return err
		}
		logMailStep(ctx, "smtp_starttls_success", map[string]any{})
	}

	if m.username != "" {
		if ok, _ := client.Extension("AUTH"); ok {
			logMailStep(ctx, "smtp_auth_start", map[string]any{})
			authenticator := smtp.PlainAuth("", m.username, m.password, m.host)
			if err := client.Auth(authenticator); err != nil {
				logMailStep(ctx, "smtp_auth_error", map[string]any{"error": err})
				return err
			}
			logMailStep(ctx, "smtp_auth_success", map[string]any{})
		}
	}

	logMailStep(ctx, "smtp_mail_from_start", map[string]any{})
	if err := client.Mail(m.fromEmail); err != nil {
		logMailStep(ctx, "smtp_mail_from_error", map[string]any{"error": err})
		return err
	}
	logMailStep(ctx, "smtp_rcpt_to_start", map[string]any{})
	if err := client.Rcpt(strings.TrimSpace(toEmail)); err != nil {
		logMailStep(ctx, "smtp_rcpt_to_error", map[string]any{"error": err})
		return err
	}

	logMailStep(ctx, "smtp_data_start", map[string]any{})
	writer, err := client.Data()
	if err != nil {
		logMailStep(ctx, "smtp_data_error", map[string]any{"error": err})
		return err
	}
	logMailStep(ctx, "smtp_message_write_start", map[string]any{
		"message_bytes": len(message),
	})
	if _, err := writer.Write(message); err != nil {
		_ = writer.Close()
		logMailStep(ctx, "smtp_message_write_error", map[string]any{"error": err})
		return err
	}
	if err := writer.Close(); err != nil {
		logMailStep(ctx, "smtp_message_write_error", map[string]any{"error": err})
		return err
	}
	logMailStep(ctx, "smtp_send_complete", map[string]any{
		"message_bytes": len(message),
	})
	return nil
}

func (m *SMTPMailer) buildPasswordResetMessage(input PasswordResetEmail) ([]byte, error) {
	to := strings.TrimSpace(input.ToEmail)
	resetURL := strings.TrimSpace(input.ResetURL)
	if to == "" || resetURL == "" {
		return nil, ErrUnavailable
	}

	subject := "Reinitialisation de votre mot de passe Demeter Speech"
	if input.SessionType == auth.SessionTypeAdmin {
		subject = "Reinitialisation de votre mot de passe administration Demeter Speech"
	}
	subject = encodeHeaderWord(subject)

	textBody, htmlBody := buildPasswordResetBodies(input)
	boundary := "demeter-boundary-" + strings.ReplaceAll(fmt.Sprintf("%d", time.Now().UnixNano()), "-", "")
	from := formatAddress(m.fromName, m.fromEmail)

	var message bytes.Buffer
	message.WriteString("From: " + from + "\r\n")
	message.WriteString("To: " + to + "\r\n")
	message.WriteString("Subject: " + subject + "\r\n")
	message.WriteString("MIME-Version: 1.0\r\n")
	message.WriteString("Content-Type: multipart/alternative; boundary=" + boundary + "\r\n")
	message.WriteString("\r\n")
	message.WriteString("--" + boundary + "\r\n")
	message.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	message.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	message.WriteString(textBody)
	message.WriteString("\r\n--" + boundary + "\r\n")
	message.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	message.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	message.WriteString(htmlBody)
	message.WriteString("\r\n--" + boundary + "--\r\n")
	return message.Bytes(), nil
}

func (m *SMTPMailer) buildMeetingSummaryMessage(input MeetingSummaryEmail) ([]byte, error) {
	to := strings.TrimSpace(input.ToEmail)
	if to == "" {
		return nil, ErrUnavailable
	}

	subject := sanitizeHeaderValue(strings.TrimSpace(input.Subject))
	if subject == "" {
		subject = "Compte rendu de reunion Demeter Speech"
	}
	subject = encodeHeaderWord(subject)
	textBody := strings.TrimSpace(input.TextBody)
	htmlBody := strings.TrimSpace(input.HTMLBody)
	if textBody == "" && htmlBody == "" {
		return nil, ErrUnavailable
	}

	mixedBoundary := newBoundary("demeter-meeting")
	alternativeBoundary := newBoundary("demeter-alt")
	from := formatAddress(m.fromName, m.fromEmail)

	var message bytes.Buffer
	message.WriteString("From: " + from + "\r\n")
	message.WriteString("To: " + to + "\r\n")
	message.WriteString("Subject: " + subject + "\r\n")
	message.WriteString("MIME-Version: 1.0\r\n")
	message.WriteString("Content-Type: multipart/mixed; boundary=" + mixedBoundary + "\r\n")
	message.WriteString("\r\n")

	message.WriteString("--" + mixedBoundary + "\r\n")
	if htmlBody != "" {
		message.WriteString("Content-Type: multipart/alternative; boundary=" + alternativeBoundary + "\r\n\r\n")
		message.WriteString("--" + alternativeBoundary + "\r\n")
		message.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
		message.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
		message.WriteString(textBody)
		message.WriteString("\r\n--" + alternativeBoundary + "\r\n")
		message.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
		message.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
		message.WriteString(htmlBody)
		message.WriteString("\r\n--" + alternativeBoundary + "--\r\n")
	} else {
		message.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
		message.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
		message.WriteString(textBody)
		message.WriteString("\r\n")
	}

	for _, attachment := range input.Attachments {
		filename := sanitizeHeaderValue(strings.TrimSpace(attachment.Filename))
		if filename == "" || len(attachment.Data) == 0 {
			continue
		}
		contentType := strings.TrimSpace(attachment.ContentType)
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		contentTypeHeader := formatMediaTypeHeader(contentType, map[string]string{"name": filename})
		dispositionHeader := formatMediaTypeHeader("attachment", map[string]string{"filename": filename})
		message.WriteString("--" + mixedBoundary + "\r\n")
		message.WriteString("Content-Type: " + contentTypeHeader + "\r\n")
		message.WriteString("Content-Disposition: " + dispositionHeader + "\r\n")
		message.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
		message.WriteString(wrapBase64(base64.StdEncoding.EncodeToString(attachment.Data)))
		message.WriteString("\r\n")
	}

	message.WriteString("--" + mixedBoundary + "--\r\n")
	return message.Bytes(), nil
}

func (m *SMTPMailer) buildUserProvisioningMessage(input UserProvisioningEmail) ([]byte, error) {
	to := strings.TrimSpace(input.ToEmail)
	login := strings.TrimSpace(input.Login)
	temporaryPassword := strings.TrimSpace(input.TemporaryPassword)
	if to == "" || login == "" || temporaryPassword == "" {
		return nil, ErrUnavailable
	}

	subject := "Vos identifiants Demeter Speech"
	subject = encodeHeaderWord(subject)
	from := formatAddress(m.fromName, m.fromEmail)
	textBody, htmlBody := buildUserProvisioningBodies(input)
	boundary := "demeter-provisioning-" + strings.ReplaceAll(fmt.Sprintf("%d", time.Now().UnixNano()), "-", "")

	var message bytes.Buffer
	message.WriteString("From: " + from + "\r\n")
	message.WriteString("To: " + to + "\r\n")
	message.WriteString("Subject: " + subject + "\r\n")
	message.WriteString("MIME-Version: 1.0\r\n")
	message.WriteString("Content-Type: multipart/alternative; boundary=" + boundary + "\r\n")
	message.WriteString("\r\n")
	message.WriteString("--" + boundary + "\r\n")
	message.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	message.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	message.WriteString(textBody)
	message.WriteString("\r\n--" + boundary + "\r\n")
	message.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	message.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	message.WriteString(htmlBody)
	message.WriteString("\r\n--" + boundary + "--\r\n")
	return message.Bytes(), nil
}

func buildPasswordResetBodies(input PasswordResetEmail) (string, string) {
	expiresAt := input.ExpiresAt.UTC().Format("2006-01-02 15:04 UTC")
	space := "application"
	if input.SessionType == auth.SessionTypeAdmin {
		space = "administration"
	}

	textBody := strings.Join([]string{
		"Bonjour,",
		"",
		"Une demande de reinitialisation de mot de passe a ete recue pour votre " + space + " Demeter Speech.",
		"",
		"Utilisez ce lien pour choisir un nouveau mot de passe:",
		input.ResetURL,
		"",
		"Ce lien expire le " + expiresAt + ".",
		"",
		"Si vous n etes pas a l origine de cette demande, ignorez simplement cet email.",
	}, "\n")

	htmlBody := strings.Join([]string{
		"<html><body style=\"font-family:Arial,sans-serif;color:#1f2937;line-height:1.5\">",
		"<p>Bonjour,</p>",
		"<p>Une demande de reinitialisation de mot de passe a ete recue pour votre " + html.EscapeString(space) + " Demeter Speech.</p>",
		"<p><a href=\"" + html.EscapeString(input.ResetURL) + "\">Choisir un nouveau mot de passe</a></p>",
		"<p>Ce lien expire le " + html.EscapeString(expiresAt) + ".</p>",
		"<p>Si vous n etes pas a l origine de cette demande, ignorez simplement cet email.</p>",
		"</body></html>",
	}, "")
	return textBody, htmlBody
}

func buildUserProvisioningBodies(input UserProvisioningEmail) (string, string) {
	login := strings.TrimSpace(input.Login)
	temporaryPassword := strings.TrimSpace(input.TemporaryPassword)
	textBody := strings.Join([]string{
		"Bonjour,",
		"",
		"Votre compte Demeter Speech vient d etre cree.",
		"",
		"Identifiant: " + login,
		"Mot de passe temporaire: " + temporaryPassword,
		"",
		"Connectez-vous puis changez ce mot de passe des que possible.",
	}, "\n")

	htmlBody := strings.Join([]string{
		"<html><body style=\"font-family:Arial,sans-serif;color:#1f2937;line-height:1.5\">",
		"<p>Bonjour,</p>",
		"<p>Votre compte Demeter Speech vient d etre cree.</p>",
		"<p><strong>Identifiant :</strong> " + html.EscapeString(login) + "<br>",
		"<strong>Mot de passe temporaire :</strong> " + html.EscapeString(temporaryPassword) + "</p>",
		"<p>Connectez-vous puis changez ce mot de passe des que possible.</p>",
		"</body></html>",
	}, "")
	return textBody, htmlBody
}

func formatAddress(name, address string) string {
	cleanAddress := strings.TrimSpace(address)
	cleanName := strings.TrimSpace(name)
	if cleanName == "" {
		return cleanAddress
	}
	encodedName := encodeHeaderWord(cleanName)
	if encodedName != cleanName {
		return fmt.Sprintf("%s <%s>", encodedName, cleanAddress)
	}
	return fmt.Sprintf("\"%s\" <%s>", strings.ReplaceAll(cleanName, "\"", ""), cleanAddress)
}

func newBoundary(prefix string) string {
	var raw [12]byte
	_, _ = rand.Read(raw[:])
	return fmt.Sprintf("%s-%x", prefix, raw)
}

func encodeHeaderWord(value string) string {
	value = sanitizeHeaderValue(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	return mime.QEncoding.Encode("utf-8", value)
}

func formatMediaTypeHeader(mediaType string, params map[string]string) string {
	cleanMediaType := strings.TrimSpace(mediaType)
	if cleanMediaType == "" {
		cleanMediaType = "application/octet-stream"
	}
	if formatted := mime.FormatMediaType(cleanMediaType, params); formatted != "" {
		return formatted
	}
	if len(params) > 0 {
		if formatted := mime.FormatMediaType("application/octet-stream", params); formatted != "" {
			return formatted
		}
	}
	return cleanMediaType
}

func sanitizeHeaderValue(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\"", "")
	return value
}

func wrapBase64(encoded string) string {
	const width = 76
	if len(encoded) <= width {
		return encoded
	}
	var builder strings.Builder
	for start := 0; start < len(encoded); start += width {
		end := start + width
		if end > len(encoded) {
			end = len(encoded)
		}
		builder.WriteString(encoded[start:end])
		builder.WriteString("\r\n")
	}
	return builder.String()
}

func logMailStep(ctx context.Context, step string, fields map[string]any) {
	log.Print(observability.FormatStepLine("mailer", "smtp", step, observability.TraceIDFromContext(ctx), observability.DefaultTraceID, observability.DefaultTraceID, "", fields))
	backenderrors.RecordLog(ctx, "mailer", "smtp", step, "", fields)
}
