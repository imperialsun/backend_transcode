package auth

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
	"time"
)

// PasswordResetTokenPayload stores the raw token to send to the user and the
// hashed token used for persistence.
type PasswordResetTokenPayload struct {
	RawToken  string
	Hash      string
	ExpiresAt time.Time
}

// NewPasswordResetToken generates a one-time token with an expiration time and
// stores only the hashed value for later verification.
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

// HashPasswordResetToken normalizes and hashes the token exactly as the store
// layer expects it.
func HashPasswordResetToken(raw string) string {
	return sha256Hex(strings.TrimSpace(raw))
}
