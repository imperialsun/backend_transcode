package config

import (
	"testing"
	"time"
)

func TestGetEnvHelpers(t *testing.T) {
	t.Setenv("TEST_ENV_STR", "  spaced ")
	if got := getEnv("TEST_ENV_STR", "fallback"); got != "spaced" {
		t.Fatalf("expected trimmed value, got %q", got)
	}

	if got := getEnv("MISSING_ENV", "fallback"); got != "fallback" {
		t.Fatalf("expected fallback value, got %q", got)
	}

	t.Setenv("TEST_ENV_INT", "42")
	if got := getEnvInt("TEST_ENV_INT", 1); got != 42 {
		t.Fatalf("expected 42, got %d", got)
	}
	// non-int should return fallback
	t.Setenv("TEST_ENV_INT", "notint")
	if got := getEnvInt("TEST_ENV_INT", 5); got != 5 {
		t.Fatalf("expected fallback 5, got %d", got)
	}

	t.Setenv("TEST_ENV_BOOL", "true")
	if got := getEnvBool("TEST_ENV_BOOL", false); !got {
		t.Fatalf("expected true, got false")
	}
	// ensure false values are respected
	t.Setenv("TEST_ENV_BOOL", "0")
	if got := getEnvBool("TEST_ENV_BOOL", true); got {
		t.Fatalf("expected false, got true")
	}
}

func TestSplitCSV(t *testing.T) {
	if got := splitCSV("a,b, c ,,"); len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("unexpected split result: %v", got)
	}
	if got := splitCSV("   "); len(got) != 1 || got[0] != "*" {
		t.Fatalf("expected default wildcard output, got %v", got)
	}
}

func TestLoadConfigUsesEnvVars(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv("PORT", "9999")
	t.Setenv("BODY_LIMIT_BYTES", "123")
	t.Setenv("ACCESS_TTL_MINUTES", "1")
	t.Setenv("REFRESH_TTL_HOURS", "1")

	cfg := Load()
	if cfg.AppEnv != "production" {
		t.Fatalf("expected AppEnv=production, got %q", cfg.AppEnv)
	}
	if cfg.Port != "9999" {
		t.Fatalf("expected Port=9999, got %q", cfg.Port)
	}
	if cfg.BodyLimitBytes != 123 {
		t.Fatalf("expected BodyLimitBytes=123, got %d", cfg.BodyLimitBytes)
	}
	if cfg.AccessTTL != time.Minute {
		t.Fatalf("expected AccessTTL=1m, got %s", cfg.AccessTTL)
	}
	if cfg.RefreshTTL != time.Hour {
		t.Fatalf("expected RefreshTTL=1h, got %s", cfg.RefreshTTL)
	}
}
