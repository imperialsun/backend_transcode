package api

import (
	"log"
	"strings"
	"sync"

	"demeter-backend/internal/auth"
	"demeter-backend/internal/observability"

	"github.com/gofiber/fiber/v2"
)

// inFlightRequestGate manages concurrent access to track active in-flight requests.
// It uses a mutex to ensure thread-safe operations on the map of active requests.
// The active map stores request identifiers as keys, with empty structs as values
// to efficiently track which requests are currently being processed.
type inFlightRequestGate struct {
	mu     sync.Mutex
	active map[string]struct{}
}

// TryAcquire attempts to acquire a gate slot for the given key.
// It returns true if the slot was successfully acquired, false if the key
// is already in use or the gate is at capacity.
// This is typically used to prevent concurrent processing of the same resource.
func (g *inFlightRequestGate) TryAcquire(key string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return true
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if g.active == nil {
		g.active = make(map[string]struct{})
	}
	if _, exists := g.active[key]; exists {
		return false
	}
	g.active[key] = struct{}{}
	return true
}

func (g *inFlightRequestGate) Release(key string) {
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if g.active == nil {
		return
	}
	delete(g.active, key)
}

func (a *App) finalizeMeetingGate() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if a == nil {
			return c.Next()
		}

		traceID := requestTraceID(c)
		raw := readAccessToken(c, auth.SessionTypeApp)
		if raw == "" {
			return c.Next()
		}

		tokenClaims, err := auth.ParseAccessToken(a.Config.JWTSecret, raw)
		if err != nil || tokenClaims == nil || !auth.HasAudience(tokenClaims, auth.SessionTypeApp) {
			return c.Next()
		}

		userID := strings.TrimSpace(tokenClaims.UserID)
		orgID := strings.TrimSpace(tokenClaims.OrgID)
		key := meetingFinalizeLockKey(userID, orgID)
		if key == "" {
			return c.Next()
		}

		if !a.FinalizeMeetingGate.TryAcquire(key) {
			log.Print(observability.FormatStepLine("meetings", meetingFinalizeRoute, "finalize_locked", traceID, userID, orgID, "", map[string]any{
				"reason": "in_flight",
			}))
			return c.Status(fiber.StatusConflict).JSON(ErrorResponse{Error: "meeting finalization already in progress"})
		}

		defer a.FinalizeMeetingGate.Release(key)
		return c.Next()
	}
}

func meetingFinalizeLockKey(userID, orgID string) string {
	userID = strings.TrimSpace(userID)
	orgID = strings.TrimSpace(orgID)
	if userID == "" || orgID == "" {
		return ""
	}
	return strings.Join([]string{orgID, userID, "finalize_meeting"}, ":")
}
