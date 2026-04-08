package auth

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// GenerateTemporaryPassword returns a cryptographically random password that is
// safe to type and safe to embed in URLs or emails without extra escaping.
func GenerateTemporaryPassword(byteLen int) (string, error) {
	if byteLen < 16 {
		byteLen = 16
	}

	raw := make([]byte, byteLen)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate temporary password: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
