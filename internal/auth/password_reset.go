package auth

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
	"time"
)

type PasswordResetTokenPayload struct {
	RawToken  string
	Hash      string
	ExpiresAt time.Time
}

func NewPasswordResetToken(ttl time.Duration) (*PasswordResetTokenPayload, error) {
	raw := make([]byte, 48)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	return &PasswordResetTokenPayload{
		RawToken:  token,
		Hash:      HashPasswordResetToken(token),
		ExpiresAt: time.Now().UTC().Add(ttl),
	}, nil
}

func HashPasswordResetToken(raw string) string {
	return sha256Hex(strings.TrimSpace(raw))
}
