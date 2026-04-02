package mailer

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"demeter-backend/internal/auth"
	"demeter-backend/internal/observability"
)

func TestSMTPMailerSendPasswordResetEmail_Success(t *testing.T) {
	var logBuf bytes.Buffer
	previousWriter := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
	})

	server := startSMTPTestServer(t, false)

	m := NewSMTPMailer(Config{
		Host:      server.host,
		Port:      server.port,
		FromEmail: "noreply@demeter.test",
		FromName:  "Demeter",
	})

	ctx := observability.WithTraceID(context.Background(), "mailer-reset-trace")
	err := m.SendPasswordResetEmail(ctx, PasswordResetEmail{
		ToEmail:        "user@example.com",
		ResetURL:       "https://app.demeter.test/reset-password?token=abc",
		ApplicationURL: "https://app.demeter.test/",
		ExpiresAt:      time.Date(2026, time.March, 14, 20, 0, 0, 0, time.UTC),
		SessionType:    auth.SessionTypeApp,
	})
	if err != nil {
		t.Fatalf("SendPasswordResetEmail returned error: %v", err)
	}

	message := server.waitForMessage(t)
	if !strings.Contains(message, "Subject: Reinitialisation de votre mot de passe Demeter Speech") {
		t.Fatalf("expected subject in SMTP message, got %q", message)
	}
	if !strings.Contains(message, "https://app.demeter.test/reset-password?token=abc") {
		t.Fatalf("expected reset url in SMTP message, got %q", message)
	}
	if !strings.Contains(message, "https://app.demeter.test/") {
		t.Fatalf("expected application url in SMTP message, got %q", message)
	}
	if !strings.Contains(message, "Accéder à l'application") {
		t.Fatalf("expected application CTA in SMTP message, got %q", message)
	}
	if !strings.Contains(logBuf.String(), "trace_id=mailer-reset-trace") {
		t.Fatalf("expected trace id in mailer logs, got %q", logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "step=smtp_send_complete") {
		t.Fatalf("expected smtp completion log, got %q", logBuf.String())
	}
}

func TestSMTPMailerSendPasswordResetEmail_RCPTFailure(t *testing.T) {
	var logBuf bytes.Buffer
	previousWriter := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
	})

	server := startSMTPTestServer(t, true)

	m := NewSMTPMailer(Config{
		Host:      server.host,
		Port:      server.port,
		FromEmail: "noreply@demeter.test",
	})

	ctx := observability.WithTraceID(context.Background(), "mailer-reset-failure-trace")
	err := m.SendPasswordResetEmail(ctx, PasswordResetEmail{
		ToEmail:        "user@example.com",
		ResetURL:       "https://app.demeter.test/reset-password?token=abc",
		ApplicationURL: "https://app.demeter.test/",
		ExpiresAt:      time.Now().UTC().Add(time.Hour),
		SessionType:    auth.SessionTypeAdmin,
	})
	if err == nil {
		t.Fatal("expected SMTP RCPT failure")
	}
	if !strings.Contains(err.Error(), "550") {
		t.Fatalf("expected SMTP 550 error, got %v", err)
	}
	if !strings.Contains(logBuf.String(), "trace_id=mailer-reset-failure-trace") {
		t.Fatalf("expected trace id in mailer failure logs, got %q", logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "step=smtp_rcpt_to_error") {
		t.Fatalf("expected rcpt failure log, got %q", logBuf.String())
	}
}

func TestSMTPMailerSendMeetingSummaryEmail_Success(t *testing.T) {
	server := startSMTPTestServer(t, false)

	m := NewSMTPMailer(Config{
		Host:      server.host,
		Port:      server.port,
		FromEmail: "noreply@demeter.test",
		FromName:  "Demeter",
	})

	err := m.SendMeetingSummaryEmail(context.Background(), MeetingSummaryEmail{
		ToEmail:  "user@example.com",
		Subject:  "Compte rendu de reunion",
		TextBody: "Bonjour,\nVoici le compte rendu.",
		Attachments: []MailAttachment{
			{Filename: "transcription-brute.docx", ContentType: DocxContentType, Data: []byte("docx-raw")},
			{Filename: "rapport-cri.docx", ContentType: DocxContentType, Data: []byte("docx-cri")},
		},
	})
	if err != nil {
		t.Fatalf("SendMeetingSummaryEmail returned error: %v", err)
	}

	message := server.waitForMessage(t)
	if !strings.Contains(message, "Subject: Compte rendu de reunion") {
		t.Fatalf("expected subject in SMTP message, got %q", message)
	}
	if !strings.Contains(message, "transcription-brute.docx") || !strings.Contains(message, "rapport-cri.docx") {
		t.Fatalf("expected attachments in SMTP message, got %q", message)
	}
}

func TestSMTPMailerSendUserProvisioningEmail_Success(t *testing.T) {
	server := startSMTPTestServer(t, false)

	m := NewSMTPMailer(Config{
		Host:      server.host,
		Port:      server.port,
		FromEmail: "noreply@demeter.test",
		FromName:  "Demeter",
	})

	err := m.SendUserProvisioningEmail(context.Background(), UserProvisioningEmail{
		ToEmail:           "user@example.com",
		Login:             "user@example.com",
		TemporaryPassword: "TmpPass-123456",
		ApplicationURL:    "https://app.demeter.test/",
	})
	if err != nil {
		t.Fatalf("SendUserProvisioningEmail returned error: %v", err)
	}

	message := server.waitForMessage(t)
	if !strings.Contains(message, "Subject: Vos identifiants Demeter Speech") {
		t.Fatalf("expected provisioning subject in SMTP message, got %q", message)
	}
	if !strings.Contains(message, "TmpPass-123456") {
		t.Fatalf("expected temporary password in SMTP message, got %q", message)
	}
	if !strings.Contains(message, "https://app.demeter.test/") {
		t.Fatalf("expected application url in SMTP message, got %q", message)
	}
	if !strings.Contains(message, "Accéder à l'application") {
		t.Fatalf("expected application CTA in SMTP message, got %q", message)
	}
}

type smtpTestServer struct {
	host       string
	port       int
	rejectRcpt bool
	messageCh  chan string
	listener   net.Listener
	closeOnce  sync.Once
}

func startSMTPTestServer(t *testing.T, rejectRcpt bool) *smtpTestServer {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start smtp test server: %v", err)
	}
	host, portRaw, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("failed to parse smtp listener address: %v", err)
	}
	var port int
	if _, err := fmt.Sscanf(portRaw, "%d", &port); err != nil {
		t.Fatalf("failed to parse smtp listener port: %v", err)
	}

	server := &smtpTestServer{
		host:       host,
		port:       port,
		rejectRcpt: rejectRcpt,
		messageCh:  make(chan string, 1),
		listener:   listener,
	}
	t.Cleanup(server.close)

	go server.serve(t)
	return server
}

func (s *smtpTestServer) serve(t *testing.T) {
	t.Helper()

	conn, err := s.listener.Accept()
	if err != nil {
		return
	}
	defer closeSMTPConn(t, conn)

	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	writeLine := func(line string) {
		_, _ = writer.WriteString(line + "\r\n")
		_ = writer.Flush()
	}

	writeLine("220 smtp.demeter.test ESMTP ready")
	var dataLines []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")

		switch {
		case strings.HasPrefix(line, "EHLO ") || strings.HasPrefix(line, "HELO "):
			writeLine("250 smtp.demeter.test")
		case strings.HasPrefix(line, "MAIL FROM:"):
			writeLine("250 OK")
		case strings.HasPrefix(line, "RCPT TO:"):
			if s.rejectRcpt {
				writeLine("550 mailbox unavailable")
				return
			}
			writeLine("250 OK")
		case line == "DATA":
			writeLine("354 End data with <CR><LF>.<CR><LF>")
			for {
				dataLine, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				dataLine = strings.TrimRight(dataLine, "\r\n")
				if dataLine == "." {
					break
				}
				dataLines = append(dataLines, dataLine)
			}
			s.messageCh <- strings.Join(dataLines, "\n")
			writeLine("250 queued")
		case line == "QUIT":
			writeLine("221 bye")
			return
		default:
			writeLine("250 OK")
		}
	}
}

func closeSMTPConn(t *testing.T, conn net.Conn) {
	t.Helper()
	if conn == nil {
		return
	}
	if err := conn.Close(); err != nil {
		t.Errorf("close smtp connection: %v", err)
	}
}

func (s *smtpTestServer) waitForMessage(t *testing.T) string {
	t.Helper()
	select {
	case message := <-s.messageCh:
		return message
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for smtp message")
		return ""
	}
}

func (s *smtpTestServer) close() {
	s.closeOnce.Do(func() {
		_ = s.listener.Close()
	})
}
