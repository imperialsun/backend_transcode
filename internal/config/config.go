package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	// AppEnv selects the runtime profile and controls a few safe defaults.
	AppEnv string
	// Port is the HTTP listen port used by the Fiber server.
	Port string
	// BodyLimitBytes caps request bodies before they reach handler logic.
	BodyLimitBytes int
	// SQLitePath points to the single backend database file.
	SQLitePath string
	// JWTSecret signs access tokens.
	JWTSecret string
	// AccessTTL and refresh TTLs define the lifetime of app sessions.
	AccessTTL  time.Duration
	RefreshTTL time.Duration
	// AdminAccessTTL and AdminRefreshTTL are intentionally shorter and separate
	// from app sessions because the admin surface carries higher privilege.
	AdminAccessTTL  time.Duration
	AdminRefreshTTL time.Duration
	// CookieSecure toggles the Secure flag on auth cookies.
	CookieSecure bool
	// AppCORSOrigins and AdminCORSOrigins gate browser access for the two UIs.
	AppCORSOrigins   []string
	AdminCORSOrigins []string
	// Mistral settings configure the upstream provider relay.
	MistralAPIBaseURL     string
	MistralAPIKey         string
	MistralRequestTimeout time.Duration
	MistralAudioTimeout   time.Duration
	// SMTP settings configure password-reset, meeting, and provisioning emails.
	SMTPHost      string
	SMTPPort      int
	SMTPUsername  string
	SMTPPassword  string
	SMTPFromEmail string
	SMTPFromName  string
	// Public URLs are embedded in transactional emails and reset links.
	AppPublicURL   string
	AdminPublicURL string
	// PasswordResetTTL controls how long reset tokens remain valid.
	PasswordResetTTL time.Duration
	// Bootstrap values seed the first admin organization when the database is
	// empty.
	BootstrapAdminEmail    string
	BootstrapAdminPassword string
	BootstrapOrgName       string
}

// Load reads environment variables once at startup and fills in a fully
// usable configuration with conservative defaults.
func Load() Config {
	appEnv := getEnv("APP_ENV", "development")
	legacyAppOrigins := getEnv("CORS_ORIGINS", "http://localhost:3000,http://localhost:4173")
	cfg := Config{
		AppEnv:                 appEnv,
		Port:                   getEnv("PORT", "8080"),
		BodyLimitBytes:         getEnvInt("BODY_LIMIT_BYTES", 500*1024*1024),
		SQLitePath:             getEnv("SQLITE_PATH", "./backend.sqlite"),
		JWTSecret:              getEnv("JWT_SECRET", "dev-insecure-jwt-secret-change-me"),
		AccessTTL:              time.Duration(getEnvInt("ACCESS_TTL_MINUTES", 15)) * time.Minute,
		RefreshTTL:             time.Duration(getEnvInt("REFRESH_TTL_HOURS", 24*30)) * time.Hour,
		AdminAccessTTL:         time.Duration(getEnvInt("ADMIN_ACCESS_TTL_MINUTES", 10)) * time.Minute,
		AdminRefreshTTL:        time.Duration(getEnvInt("ADMIN_REFRESH_TTL_HOURS", 12)) * time.Hour,
		CookieSecure:           getEnvBool("COOKIE_SECURE", appEnv == "production"),
		AppCORSOrigins:         splitCSV(getEnv("APP_CORS_ORIGINS", legacyAppOrigins)),
		AdminCORSOrigins:       splitCSV(getEnv("ADMIN_CORS_ORIGINS", "http://localhost:4173")),
		MistralAPIBaseURL:      strings.TrimRight(getEnv("MISTRAL_API_BASE_URL", "https://api.mistral.ai"), "/"),
		MistralAPIKey:          strings.TrimSpace(os.Getenv("MISTRAL_API_KEY")),
		MistralRequestTimeout:  time.Duration(getEnvInt("MISTRAL_REQUEST_TIMEOUT_SECONDS", 480)) * time.Second,
		MistralAudioTimeout:    time.Duration(getEnvInt("MISTRAL_AUDIO_TRANSCRIPTION_TIMEOUT_SECONDS", 1200)) * time.Second,
		SMTPHost:               strings.TrimSpace(os.Getenv("SMTP_HOST")),
		SMTPPort:               getEnvInt("SMTP_PORT", 587),
		SMTPUsername:           strings.TrimSpace(os.Getenv("SMTP_USERNAME")),
		SMTPPassword:           strings.TrimSpace(os.Getenv("SMTP_PASSWORD")),
		SMTPFromEmail:          strings.TrimSpace(os.Getenv("SMTP_FROM_EMAIL")),
		SMTPFromName:           strings.TrimSpace(os.Getenv("SMTP_FROM_NAME")),
		AppPublicURL:           strings.TrimSpace(os.Getenv("APP_PUBLIC_URL")),
		AdminPublicURL:         strings.TrimSpace(os.Getenv("ADMIN_PUBLIC_URL")),
		PasswordResetTTL:       time.Duration(getEnvInt("PASSWORD_RESET_TTL_MINUTES", 60)) * time.Minute,
		BootstrapAdminEmail:    strings.TrimSpace(os.Getenv("BOOTSTRAP_ADMIN_EMAIL")),
		BootstrapAdminPassword: strings.TrimSpace(os.Getenv("BOOTSTRAP_ADMIN_PASSWORD")),
		BootstrapOrgName:       strings.TrimSpace(getEnv("BOOTSTRAP_ORG_NAME", "Default Organization")),
	}
	return cfg
}

// getEnv returns the trimmed environment value when present, otherwise the
// supplied fallback.
func getEnv(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

// getEnvInt parses an integer environment variable and falls back on invalid
// or missing input.
func getEnvInt(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return parsed
}

// getEnvBool accepts the common truthy spellings used in shell environments.
func getEnvBool(key string, fallback bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if v == "" {
		return fallback
	}
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

// splitCSV normalizes a comma-separated list of origins or returns a wildcard
// fallback when nothing usable is configured.
func splitCSV(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if t == "" {
			continue
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		return []string{"*"}
	}
	return out
}
