package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"demeter-backend/internal/auth"
	"demeter-backend/internal/requestmeta"

	"github.com/gofiber/contrib/websocket"
	"github.com/gofiber/fiber/v2"
)

const adminDemeterQueueWebSocketRoute = "/api/v1/admin/providers/demeter-sante/queue/ws"

type demeterQueueWebSocketClientMessage struct {
	Type      string `json:"type"`
	CSRFToken string `json:"csrfToken,omitempty"`
	CommandID string `json:"commandId,omitempty"`
	Settings  *struct {
		Parallelism *int `json:"parallelism"`
	} `json:"settings,omitempty"`
}

type demeterQueueWebSocketServerMessage struct {
	Type      string                     `json:"type"`
	SentAt    string                     `json:"sentAt,omitempty"`
	Snapshot  *demeterAudioQueueSnapshot `json:"snapshot,omitempty"`
	CommandID string                     `json:"commandId,omitempty"`
	Code      string                     `json:"code,omitempty"`
	Message   string                     `json:"message,omitempty"`
}

// RegisterAdminWebSocketRoutes installs long-lived admin WebSocket endpoints.
func (a *App) RegisterAdminWebSocketRoutes(router fiber.Router) {
	group := a.adminWebSocketRouteGroup(router)
	a.registerAdminDemeterQueueWebSocketRoutes(group)
	a.registerAdminDemeterReportQueueWebSocketRoutes(group)
}

func (a *App) adminWebSocketRouteGroup(router fiber.Router) fiber.Router {
	return router.Group("/admin", a.AdminAuthRequired(), RequirePermissions("feature.admin"), RequireAdminScope())
}

func (a *App) registerAdminDemeterQueueWebSocketRoutes(group fiber.Router) {
	group.Get(
		"/providers/demeter-sante/queue/ws",
		RequireSuperAdminScope(),
		requireWebSocketUpgrade(),
		websocket.New(a.handleAdminDemeterQueueWebSocket, websocket.Config{
			Origins: a.Config.AdminCORSOrigins,
		}),
	)
}

func requireWebSocketUpgrade() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if websocket.IsWebSocketUpgrade(c) {
			return c.Next()
		}
		return fiber.ErrUpgradeRequired
	}
}

func (a *App) handleAdminDemeterQueueWebSocket(conn *websocket.Conn) {
	claims, _ := conn.Locals(claimsContextKey).(*auth.Claims)
	if claims == nil {
		_ = conn.WriteJSON(demeterQueueWebSocketServerMessage{Type: "auth_error", Code: "unauthorized"})
		_ = conn.Close()
		return
	}

	limit := parseDemeterQueueWebSocketLimit(conn.Query("limit"))
	if !a.authenticateDemeterQueueWebSocket(conn, claims) {
		_ = conn.Close()
		return
	}

	manager := a.EnsureDemeterQueueManager()
	if manager == nil {
		_ = conn.WriteJSON(demeterQueueWebSocketServerMessage{Type: "auth_error", Code: "queue_unavailable"})
		_ = conn.Close()
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	outbound := make(chan demeterQueueWebSocketServerMessage, 16)
	writerDone := make(chan struct{})
	go writeDemeterQueueWebSocketMessages(ctx, conn, outbound, writerDone, cancel)

	send := func(message demeterQueueWebSocketServerMessage) bool {
		select {
		case outbound <- message:
			return true
		case <-ctx.Done():
			return false
		}
	}

	if !send(demeterQueueWebSocketServerMessage{Type: "auth_ok"}) {
		return
	}
	if !a.sendDemeterQueueWebSocketSnapshot(ctx, manager, claims, limit, send) {
		return
	}

	changes, unsubscribe := manager.subscribeSnapshotChanges()
	defer unsubscribe()
	go a.streamDemeterQueueWebSocketSnapshots(ctx, manager, claims, limit, changes, send, cancel)

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			cancel()
			break
		}
		if !a.handleDemeterQueueWebSocketCommand(ctx, manager, claims, limit, raw, send) {
			cancel()
			break
		}
	}

	<-writerDone
}

func (a *App) authenticateDemeterQueueWebSocket(conn *websocket.Conn, claims *auth.Claims) bool {
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, raw, err := conn.ReadMessage()
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		_ = conn.WriteJSON(demeterQueueWebSocketServerMessage{Type: "auth_error", Code: "missing_auth"})
		return false
	}
	var message demeterQueueWebSocketClientMessage
	if err := json.Unmarshal(raw, &message); err != nil || strings.TrimSpace(message.Type) != "auth" {
		_ = conn.WriteJSON(demeterQueueWebSocketServerMessage{Type: "auth_error", Code: "missing_auth"})
		return false
	}
	if !validDemeterQueueWebSocketCSRF(claims, message.CSRFToken) {
		_ = conn.WriteJSON(demeterQueueWebSocketServerMessage{Type: "auth_error", Code: "invalid_csrf"})
		return false
	}
	if demeterQueueWebSocketClaimsExpired(claims) {
		_ = conn.WriteJSON(demeterQueueWebSocketServerMessage{Type: "auth_error", Code: "access_token_expired"})
		return false
	}
	return true
}

func writeDemeterQueueWebSocketMessages(
	ctx context.Context,
	conn *websocket.Conn,
	outbound <-chan demeterQueueWebSocketServerMessage,
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

func (a *App) streamDemeterQueueWebSocketSnapshots(
	ctx context.Context,
	manager *DemeterAudioQueueManager,
	claims *auth.Claims,
	limit int,
	changes <-chan struct{},
	send func(demeterQueueWebSocketServerMessage) bool,
	cancel context.CancelFunc,
) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-changes:
			if !a.sendDemeterQueueWebSocketSnapshot(ctx, manager, claims, limit, send) {
				cancel()
				return
			}
		case <-ticker.C:
			if demeterQueueWebSocketClaimsExpired(claims) {
				send(demeterQueueWebSocketServerMessage{Type: "auth_error", Code: "access_token_expired"})
				cancel()
				return
			}
			if !a.sendDemeterQueueWebSocketSnapshot(ctx, manager, claims, limit, send) {
				cancel()
				return
			}
		}
	}
}

func (a *App) handleDemeterQueueWebSocketCommand(
	ctx context.Context,
	manager *DemeterAudioQueueManager,
	claims *auth.Claims,
	limit int,
	raw []byte,
	send func(demeterQueueWebSocketServerMessage) bool,
) bool {
	var message demeterQueueWebSocketClientMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		return send(demeterQueueWebSocketServerMessage{Type: "command_error", Code: "invalid_json", Message: "invalid websocket payload"})
	}
	if demeterQueueWebSocketClaimsExpired(claims) {
		send(demeterQueueWebSocketServerMessage{Type: "auth_error", Code: "access_token_expired"})
		return false
	}
	switch strings.TrimSpace(message.Type) {
	case "update_settings":
		return a.handleDemeterQueueWebSocketUpdateSettings(ctx, manager, claims, limit, message, send)
	default:
		return send(demeterQueueWebSocketServerMessage{
			Type:      "command_error",
			CommandID: strings.TrimSpace(message.CommandID),
			Code:      "unsupported_command",
			Message:   "unsupported websocket command",
		})
	}
}

func (a *App) handleDemeterQueueWebSocketUpdateSettings(
	ctx context.Context,
	manager *DemeterAudioQueueManager,
	claims *auth.Claims,
	limit int,
	message demeterQueueWebSocketClientMessage,
	send func(demeterQueueWebSocketServerMessage) bool,
) bool {
	commandID := strings.TrimSpace(message.CommandID)
	if message.Settings == nil || message.Settings.Parallelism == nil {
		return send(demeterQueueWebSocketServerMessage{
			Type:      "command_error",
			CommandID: commandID,
			Code:      "missing_parallelism",
			Message:   "parallelism is required",
		})
	}
	route := adminDemeterQueueWebSocketRoute
	commandCtx := requestmeta.WithActor(ctx, claims.UserID, claims.OrgID)
	if err := manager.Resize(commandCtx, route, *message.Settings.Parallelism); err != nil {
		return send(demeterQueueWebSocketServerMessage{
			Type:      "command_error",
			CommandID: commandID,
			Code:      "update_failed",
			Message:   "failed to update demeter queue settings",
		})
	}
	snapshot, err := manager.Snapshot(commandCtx, limit)
	if err != nil {
		return send(demeterQueueWebSocketServerMessage{
			Type:      "command_error",
			CommandID: commandID,
			Code:      "snapshot_failed",
			Message:   "failed to load demeter queue",
		})
	}
	return send(demeterQueueWebSocketServerMessage{
		Type:      "command_ok",
		CommandID: commandID,
		SentAt:    time.Now().UTC().Format(time.RFC3339),
		Snapshot:  &snapshot,
	})
}

func (a *App) sendDemeterQueueWebSocketSnapshot(
	ctx context.Context,
	manager *DemeterAudioQueueManager,
	claims *auth.Claims,
	limit int,
	send func(demeterQueueWebSocketServerMessage) bool,
) bool {
	if demeterQueueWebSocketClaimsExpired(claims) {
		send(demeterQueueWebSocketServerMessage{Type: "auth_error", Code: "access_token_expired"})
		return false
	}
	snapshot, err := manager.Snapshot(requestmeta.WithActor(ctx, claims.UserID, claims.OrgID), limit)
	if err != nil {
		return send(demeterQueueWebSocketServerMessage{
			Type:    "command_error",
			Code:    "snapshot_failed",
			Message: "failed to load demeter queue",
		})
	}
	return send(demeterQueueWebSocketServerMessage{
		Type:     "snapshot",
		SentAt:   time.Now().UTC().Format(time.RFC3339),
		Snapshot: &snapshot,
	})
}

func parseDemeterQueueWebSocketLimit(raw string) int {
	limit := 200
	if trimmed := strings.TrimSpace(raw); trimmed != "" {
		parsed, err := strconv.Atoi(trimmed)
		if err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > 500 {
		return 500
	}
	return limit
}

func validDemeterQueueWebSocketCSRF(claims *auth.Claims, candidate string) bool {
	if claims == nil || strings.TrimSpace(claims.CSRFToken) == "" {
		return false
	}
	expected := []byte(strings.TrimSpace(claims.CSRFToken))
	actual := []byte(strings.TrimSpace(candidate))
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func demeterQueueWebSocketClaimsExpired(claims *auth.Claims) bool {
	if claims == nil || claims.ExpiresAt == nil {
		return false
	}
	return !claims.ExpiresAt.After(time.Now().UTC())
}
