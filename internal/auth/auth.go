package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/argon2"
)

const (
	AccessCookieName  = "tc_access"
	RefreshCookieName = "tc_refresh"
)

type Claims struct {
	UserID      string   `json:"uid"`
	OrgID       string   `json:"oid"`
	Email       string   `json:"email"`
	GlobalRoles []string `json:"global_roles"`
	OrgRoles    []string `json:"org_roles"`
	Permissions []string `json:"permissions"`
	jwt.RegisteredClaims
}

func HashPassword(password string) (string, error) {
	password = strings.TrimSpace(password)
	if len(password) < 8 {
		return "", errors.New("password must be at least 8 characters")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, 1, 64*1024, 4, 32)
	return fmt.Sprintf("argon2id$v=19$m=65536,t=1,p=4$%s$%s",
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

func VerifyPassword(encodedHash, password string) bool {
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 5 || parts[0] != "argon2id" {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	stored, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	candidate := argon2.IDKey([]byte(password), salt, 1, 64*1024, 4, uint32(len(stored)))
	return subtle.ConstantTimeCompare(stored, candidate) == 1
}

func NewAccessToken(secret string, ttl time.Duration, claims Claims) (string, time.Time, error) {
	now := time.Now().UTC()
	expiresAt := now.Add(ttl)
	claims.RegisteredClaims = jwt.RegisteredClaims{
		Subject:   claims.UserID,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(expiresAt),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, expiresAt, nil
}

func ParseAccessToken(secret, raw string) (*Claims, error) {
	claims := &Claims{}
	token, err := jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, errors.New("invalid access token")
	}
	return claims, nil
}

type RefreshTokenPayload struct {
	SessionID string
	RawToken  string
	Hash      string
	ExpiresAt time.Time
}

func NewRefreshToken(ttl time.Duration) (*RefreshTokenPayload, error) {
	sessionID := uuid.NewString()
	raw := make([]byte, 48)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	tokenPart := base64.RawStdEncoding.EncodeToString(raw)
	combined := sessionID + "." + tokenPart
	hash := sha256Hex(combined)
	return &RefreshTokenPayload{
		SessionID: sessionID,
		RawToken:  combined,
		Hash:      hash,
		ExpiresAt: time.Now().UTC().Add(ttl),
	}, nil
}

func ParseRefreshToken(raw string) (sessionID string, token string, err error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return "", "", errors.New("invalid refresh token format")
	}
	if parts[0] == "" || parts[1] == "" {
		return "", "", errors.New("invalid refresh token data")
	}
	return parts[0], parts[1], nil
}

func HashRefreshToken(raw string) string {
	return sha256Hex(raw)
}

func VerifyRefreshHash(hash, raw string) bool {
	candidate := sha256Hex(raw)
	return subtle.ConstantTimeCompare([]byte(hash), []byte(candidate)) == 1
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
