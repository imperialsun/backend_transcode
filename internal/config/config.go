package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	AppEnv                 string
	Port                   string
	SQLitePath             string
	JWTSecret              string
	AccessTTL              time.Duration
	RefreshTTL             time.Duration
	AdminAccessTTL         time.Duration
	AdminRefreshTTL        time.Duration
	CookieSecure           bool
	AppCORSOrigins         []string
	AdminCORSOrigins       []string
	MistralAPIBaseURL      string
	MistralAPIKey          string
	BootstrapAdminEmail    string
	BootstrapAdminPassword string
	BootstrapOrgName       string
}

func Load() Config {
	appEnv := getEnv("APP_ENV", "development")
	legacyAppOrigins := getEnv("CORS_ORIGINS", "http://localhost:3000,http://localhost:5173")
	cfg := Config{
		AppEnv:                 appEnv,
		Port:                   getEnv("PORT", "8080"),
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
		BootstrapAdminEmail:    strings.TrimSpace(os.Getenv("BOOTSTRAP_ADMIN_EMAIL")),
		BootstrapAdminPassword: strings.TrimSpace(os.Getenv("BOOTSTRAP_ADMIN_PASSWORD")),
		BootstrapOrgName:       strings.TrimSpace(getEnv("BOOTSTRAP_ORG_NAME", "Default Organization")),
	}
	return cfg
}

func getEnv(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

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

func getEnvBool(key string, fallback bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if v == "" {
		return fallback
	}
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

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
