package api

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"demeter-backend/internal/auth"
	"demeter-backend/internal/requestmeta"

	"github.com/gofiber/contrib/websocket"
	"github.com/gofiber/fiber/v2"
)

const adminDemeterReportQueueWebSocketRoute = "/api/v1/admin/providers/demeter-sante/report-queue/ws"

type demeterReportQueueWebSocketClientMessage struct {
	Type      string `json:"type"`
	CSRFToken string `json:"csrfToken,omitempty"`
	CommandID string `json:"commandId,omitempty"`
	Settings  *struct {
		Parallelism    *int `json:"parallelism"`
		CRNParallelism *int `json:"crnParallelism"`
	} `json:"settings,omitempty"`
}

type demeterReportQueueWebSocketServerMessage struct {
	Type      string                      `json:"type"`
	SentAt    string                      `json:"sentAt,omitempty"`
	Snapshot  *demeterReportQueueSnapshot `json:"snapshot,omitempty"`
	CommandID string                      `json:"commandId,omitempty"`
	Code      string                      `json:"code,omitempty"`
	Message   string                      `json:"message,omitempty"`
}

func (a *App) registerAdminDemeterReportQueueWebSocketRoutes(group fiber.Router) {
	group.Get(
		"/providers/demeter-sante/report-queue/ws",
		RequireSuperAdminScope(),
		requireWebSocketUpgrade(),
		websocket.New(a.handleAdminDemeterReportQueueWebSocket, websocket.Config{
			Origins: a.Config.AdminCORSOrigins,
		}),
	)
}

func (a *App) handleAdminDemeterReportQueueWebSocket(conn *websocket.Conn) {
	claims, _ := conn.Locals(claimsContextKey).(*auth.Claims)
	if claims == nil {
		_ = conn.WriteJSON(demeterReportQueueWebSocketServerMessage{Type: "auth_error", Code: "unauthorized"})
		_ = conn.Close()
		return
	}

	limit := parseDemeterQueueWebSocketLimit(conn.Query("limit"))
	if !a.authenticateDemeterReportQueueWebSocket(conn, claims) {
		_ = conn.Close()
		return
	}

	manager := a.EnsureDemeterReportQueueManager()
	if manager == nil {
		_ = conn.WriteJSON(demeterReportQueueWebSocketServerMessage{Type: "auth_error", Code: "queue_unavailable"})
		_ = conn.Close()
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outbound := make(chan demeterReportQueueWebSocketServerMessage, 16)
	writerDone := make(chan struct{})
	go writeDemeterReportQueueWebSocketMessages(ctx, conn, outbound, writerDone, cancel)

	send := func(message demeterReportQueueWebSocketServerMessage) bool {
		select {
		case outbound <- message:
			return true
		case <-ctx.Done():
			return false
		}
	}

	if !send(demeterReportQueueWebSocketServerMessage{Type: "auth_ok"}) {
		return
	}
	if !a.sendDemeterReportQueueWebSocketSnapshot(ctx, manager, claims, limit, send) {
		return
	}

	changes, unsubscribe := manager.subscribeSnapshotChanges()
	defer unsubscribe()
	go a.streamDemeterReportQueueWebSocketSnapshots(ctx, manager, claims, limit, changes, send, cancel)

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			cancel()
			break
		}
		if !a.handleDemeterReportQueueWebSocketCommand(ctx, manager, claims, limit, raw, send) {
			cancel()
			break
		}
	}

	<-writerDone
}

func (a *App) authenticateDemeterReportQueueWebSocket(conn *websocket.Conn, claims *auth.Claims) bool {
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, raw, err := conn.ReadMessage()
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		_ = conn.WriteJSON(demeterReportQueueWebSocketServerMessage{Type: "auth_error", Code: "missing_auth"})
		return false
	}
	var message demeterReportQueueWebSocketClientMessage
	if err := json.Unmarshal(raw, &message); err != nil || strings.TrimSpace(message.Type) != "auth" {
		_ = conn.WriteJSON(demeterReportQueueWebSocketServerMessage{Type: "auth_error", Code: "missing_auth"})
		return false
	}
	if !validDemeterQueueWebSocketCSRF(claims, message.CSRFToken) {
		_ = conn.WriteJSON(demeterReportQueueWebSocketServerMessage{Type: "auth_error", Code: "invalid_csrf"})
		return false
	}
	if demeterQueueWebSocketClaimsExpired(claims) {
		_ = conn.WriteJSON(demeterReportQueueWebSocketServerMessage{Type: "auth_error", Code: "access_token_expired"})
		return false
	}
	return true
}

func writeDemeterReportQueueWebSocketMessages(
	ctx context.Context,
	conn *websocket.Conn,
	outbound <-chan demeterReportQueueWebSocketServerMessage,
	writerDone chan<- struct{},
	cancel context.CancelFunc,
) {
	defer close(writerDone)
	for {
		select {
		case <-ctx.Done():
			return
		case message := <-outbound:
			if err := conn.WriteJSON(message); err != nil {
				cancel()
				return
			}
		}
	}
}

func (a *App) streamDemeterReportQueueWebSocketSnapshots(
	ctx context.Context,
	manager *DemeterReportQueueManager,
	claims *auth.Claims,
	limit int,
	changes <-chan struct{},
	send func(demeterReportQueueWebSocketServerMessage) bool,
	cancel context.CancelFunc,
) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-changes:
			if !a.sendDemeterReportQueueWebSocketSnapshot(ctx, manager, claims, limit, send) {
				cancel()
				return
			}
		case <-ticker.C:
			if demeterQueueWebSocketClaimsExpired(claims) {
				send(demeterReportQueueWebSocketServerMessage{Type: "auth_error", Code: "access_token_expired"})
				cancel()
				return
			}
			if !a.sendDemeterReportQueueWebSocketSnapshot(ctx, manager, claims, limit, send) {
				cancel()
				return
			}
		}
	}
}

func (a *App) handleDemeterReportQueueWebSocketCommand(
	ctx context.Context,
	manager *DemeterReportQueueManager,
	claims *auth.Claims,
	limit int,
	raw []byte,
	send func(demeterReportQueueWebSocketServerMessage) bool,
) bool {
	var message demeterReportQueueWebSocketClientMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		return send(demeterReportQueueWebSocketServerMessage{Type: "command_error", Code: "invalid_json", Message: "invalid websocket payload"})
	}
	if demeterQueueWebSocketClaimsExpired(claims) {
		send(demeterReportQueueWebSocketServerMessage{Type: "auth_error", Code: "access_token_expired"})
		return false
	}
	switch strings.TrimSpace(message.Type) {
	case "update_settings":
		commandID := strings.TrimSpace(message.CommandID)
		if message.Settings == nil || message.Settings.Parallelism == nil {
			return send(demeterReportQueueWebSocketServerMessage{Type: "command_error", CommandID: commandID, Code: "missing_parallelism", Message: "parallelism is required"})
		}
		commandCtx := requestmeta.WithActor(ctx, claims.UserID, claims.OrgID)
		crnParallelism := demeterReportQueueDefaultCRNParallelism
		if settings, err := manager.app.Store.GetDemeterReportQueueSettings(commandCtx); err == nil && settings != nil {
			crnParallelism = settings.CRNParallelism
		}
		if message.Settings.CRNParallelism != nil {
			crnParallelism = *message.Settings.CRNParallelism
		}
		if err := manager.Resize(commandCtx, *message.Settings.Parallelism, crnParallelism); err != nil {
			return send(demeterReportQueueWebSocketServerMessage{Type: "command_error", CommandID: commandID, Code: "update_failed", Message: "failed to update demeter report queue settings"})
		}
		snapshot, err := manager.Snapshot(commandCtx, limit)
		if err != nil {
			return send(demeterReportQueueWebSocketServerMessage{Type: "command_error", CommandID: commandID, Code: "snapshot_failed", Message: "failed to load demeter report queue"})
		}
		return send(demeterReportQueueWebSocketServerMessage{Type: "command_ok", CommandID: commandID, SentAt: time.Now().UTC().Format(time.RFC3339), Snapshot: &snapshot})
	default:
		return send(demeterReportQueueWebSocketServerMessage{Type: "command_error", CommandID: strings.TrimSpace(message.CommandID), Code: "unsupported_command", Message: "unsupported websocket command"})
	}
}

func (a *App) sendDemeterReportQueueWebSocketSnapshot(
	ctx context.Context,
	manager *DemeterReportQueueManager,
	claims *auth.Claims,
	limit int,
	send func(demeterReportQueueWebSocketServerMessage) bool,
) bool {
	if demeterQueueWebSocketClaimsExpired(claims) {
		send(demeterReportQueueWebSocketServerMessage{Type: "auth_error", Code: "access_token_expired"})
		return false
	}
	snapshot, err := manager.Snapshot(requestmeta.WithActor(ctx, claims.UserID, claims.OrgID), limit)
	if err != nil {
		return send(demeterReportQueueWebSocketServerMessage{Type: "command_error", Code: "snapshot_failed", Message: "failed to load demeter report queue"})
	}
	return send(demeterReportQueueWebSocketServerMessage{Type: "snapshot", SentAt: time.Now().UTC().Format(time.RFC3339), Snapshot: &snapshot})
}
