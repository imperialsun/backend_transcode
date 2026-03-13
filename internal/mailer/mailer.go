package mailer

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"html"
	"net"
	"net/smtp"
	"strings"
	"time"

	"demeter-backend/internal/auth"
)

var ErrUnavailable = errors.New("password reset email unavailable")

type Sender interface {
	Ready() error
	SendPasswordResetEmail(ctx context.Context, input PasswordResetEmail) error
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
	if err := client.Rcpt(strings.TrimSpace(input.ToEmail)); err != nil {
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
