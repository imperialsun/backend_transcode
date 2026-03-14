package api

import (
	"encoding/json"
	"testing"
	"time"

	"demeter-backend/internal/store"
)

func TestToSettingsEnvelope_NilRecord(t *testing.T) {
	env := toSettingsEnvelope(nil)
	if env.Version != 1 || env.SchemaVersion != 1 {
		t.Fatalf("expected default version values, got %+v", env)
	}
	if string(env.Settings) != "{}" {
		t.Fatalf("expected empty settings payload, got %q", env.Settings)
	}
}

func TestToSettingsEnvelope_SanitizesPayload(t *testing.T) {
	rec := &store.SettingsRecord{
		Version:       2,
		SchemaVersion: 3,
		UpdatedAt:     time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		Settings:      json.RawMessage(`{"foo": "bar"}`),
	}
	env := toSettingsEnvelope(rec)
	if env.Version != 2 || env.SchemaVersion != 3 {
		t.Fatalf("unexpected envelope meta: %+v", env)
	}
	if string(env.Settings) != `{"foo":"bar"}` {
		t.Fatalf("expected sanitized JSON payload, got %q", env.Settings)
	}
}

func TestToSettingsEnvelope_InvalidJSON(t *testing.T) {
	rec := &store.SettingsRecord{
		Version:       1,
		SchemaVersion: 1,
		UpdatedAt:     time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		Settings:      json.RawMessage(`{notjson}`),
	}
	env := toSettingsEnvelope(rec)
	if string(env.Settings) != "{}" {
		t.Fatalf("expected fallback to empty JSON, got %q", env.Settings)
	}
}
