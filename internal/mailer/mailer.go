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
	"net"
	"net/smtp"
	"strings"
	"time"

	"demeter-backend/internal/auth"
)

var ErrUnavailable = errors.New("mailer unavailable")

const DocxContentType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

type Sender interface {
	Ready() error
	SendPasswordResetEmail(ctx context.Context, input PasswordResetEmail) error
	SendMeetingSummaryEmail(ctx context.Context, input MeetingSummaryEmail) error
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

	message, err := m.buildPasswordResetMessage(input)
	if err != nil {
		return err
	}

	return m.sendMessage(ctx, input.ToEmail, message)
}

func (m *SMTPMailer) SendMeetingSummaryEmail(ctx context.Context, input MeetingSummaryEmail) error {
	if err := m.Ready(); err != nil {
		return err
	}

	message, err := m.buildMeetingSummaryMessage(input)
	if err != nil {
		return err
	}

	return m.sendMessage(ctx, input.ToEmail, message)
}

func (m *SMTPMailer) sendMessage(ctx context.Context, toEmail string, message []byte) error {
	address := net.JoinHostPort(m.host, fmt.Sprintf("%d", m.port))
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	client, err := smtp.NewClient(conn, m.host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer func() {
		_ = client.Quit()
		_ = client.Close()
	}()

	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: m.host, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	}

	if m.username != "" {
		if ok, _ := client.Extension("AUTH"); ok {
			authenticator := smtp.PlainAuth("", m.username, m.password, m.host)
			if err := client.Auth(authenticator); err != nil {
				return err
			}
		}
	}

	if err := client.Mail(m.fromEmail); err != nil {
		return err
	}
	if err := client.Rcpt(strings.TrimSpace(toEmail)); err != nil {
		return err
	}

	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := writer.Write(message); err != nil {
		_ = writer.Close()
		return err
	}
	return writer.Close()
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
		message.WriteString("--" + mixedBoundary + "\r\n")
		message.WriteString("Content-Type: " + contentType + `; name="` + filename + `"` + "\r\n")
		message.WriteString("Content-Disposition: attachment; filename=\"" + filename + "\"\r\n")
		message.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
		message.WriteString(wrapBase64(base64.StdEncoding.EncodeToString(attachment.Data)))
		message.WriteString("\r\n")
	}

	message.WriteString("--" + mixedBoundary + "--\r\n")
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

func formatAddress(name, address string) string {
	cleanAddress := strings.TrimSpace(address)
	cleanName := strings.TrimSpace(name)
	if cleanName == "" {
		return cleanAddress
	}
	return fmt.Sprintf("\"%s\" <%s>", strings.ReplaceAll(cleanName, "\"", ""), cleanAddress)
}

func newBoundary(prefix string) string {
	var raw [12]byte
	_, _ = rand.Read(raw[:])
	return fmt.Sprintf("%s-%x", prefix, raw)
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
