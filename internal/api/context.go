package api

import (
	"encoding/json"
	"strings"
	"time"

	"demeter-backend/internal/config"
	"demeter-backend/internal/mailer"
	"demeter-backend/internal/mistral"
	"demeter-backend/internal/store"
)

// App bundles the dependencies shared by every API handler.
type App struct {
	Config        config.Config
	Store         *store.Store
	MistralClient *mistral.Client
	Mailer        mailer.Sender
	DemeterQueue  *DemeterAudioQueueManager
}

// ErrorResponse is the common JSON envelope returned for user-facing failures.
type ErrorResponse struct {
	Error         string `json:"error"`
	Code          string `json:"code,omitempty"`
	TraceID       string `json:"traceId,omitempty"`
	Path          string `json:"path,omitempty"`
	FileName      string `json:"fileName,omitempty"`
	FileSizeBytes *int64 `json:"fileSizeBytes,omitempty"`
	MimeType      string `json:"mimeType,omitempty"`
}

// AuthResponse is the standard login and session refresh response.
type AuthResponse struct {
	User         AuthUser `json:"user"`
	Organization AuthOrg  `json:"organization"`
	GlobalRoles  []string `json:"globalRoles"`
	OrgRoles     []string `json:"orgRoles"`
	Permissions  []string `json:"permissions"`
	CsrfToken    string   `json:"csrfToken,omitempty"`
	RuntimeMode  string   `json:"runtimeMode"`
}

// AuthUser is the user portion of the auth response.
type AuthUser struct {
	ID     string `json:"id"`
	Email  string `json:"email"`
	Status string `json:"status"`
}

// AuthOrg is the organization portion of the auth response.
type AuthOrg struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Code   string `json:"code"`
	Status string `json:"status"`
}

// SettingsEnvelope wraps the settings document with versioning metadata.
type SettingsEnvelope struct {
	Version       int             `json:"version"`
	SchemaVersion int             `json:"schemaVersion"`
	UpdatedAt     string          `json:"updatedAt"`
	Settings      json.RawMessage `json:"settings"`
}

// toSettingsEnvelope converts a persisted settings record into the response
// shape expected by the frontends.
func toSettingsEnvelope(rec *store.SettingsRecord) SettingsEnvelope {
	if rec == nil {
		return SettingsEnvelope{
			Version:       1,
			SchemaVersion: 1,
			UpdatedAt:     time.Now().UTC().Format(time.RFC3339),
			Settings:      json.RawMessage(`{}`),
		}
	}
	payload := rec.Settings
	if len(strings.TrimSpace(string(payload))) == 0 {
		payload = json.RawMessage(`{}`)
	}
	sanitizedPayload, err := sanitizeSettingsPayload(payload)
	if err == nil {
		payload = sanitizedPayload
	} else {
		payload = json.RawMessage(`{}`)
	}
	return SettingsEnvelope{
		Version:       rec.Version,
		SchemaVersion: rec.SchemaVersion,
		UpdatedAt:     rec.UpdatedAt.UTC().Format(time.RFC3339),
		Settings:      payload,
	}
}
