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

type SessionType string

const (
	SessionTypeApp   SessionType = "app"
	SessionTypeAdmin SessionType = "admin"
)

const (
	AppAccessCookieName    = "tc_app_access"
	AppRefreshCookieName   = "tc_app_refresh"
	AppAccessCookiePath    = "/api/v1"
	AppRefreshCookiePath   = "/api/v1/auth"
	AdminAccessCookieName  = "tc_admin_access"
	AdminRefreshCookieName = "tc_admin_refresh"
	AdminAccessCookiePath  = "/api/v1/admin"
	AdminRefreshCookiePath = "/api/v1/admin/auth"
	AdminCSRFHeaderName    = "X-Admin-CSRF"
)

type Claims struct {
	UserID      string   `json:"uid"`
	OrgID       string   `json:"oid"`
	Email       string   `json:"email"`
	GlobalRoles []string `json:"global_roles"`
	OrgRoles    []string `json:"org_roles"`
	Permissions []string `json:"permissions"`
	CSRFToken   string   `json:"csrf,omitempty"`
	jwt.RegisteredClaims
}

func (s SessionType) String() string {
	return string(s)
}

func (s SessionType) AccessCookieName() string {
	if s == SessionTypeAdmin {
		return AdminAccessCookieName
	}
	return AppAccessCookieName
}

func (s SessionType) RefreshCookieName() string {
	if s == SessionTypeAdmin {
		return AdminRefreshCookieName
	}
	return AppRefreshCookieName
}

func (s SessionType) AccessCookiePath() string {
	if s == SessionTypeAdmin {
		return AdminAccessCookiePath
	}
	return AppAccessCookiePath
}

func (s SessionType) RefreshCookiePath() string {
	if s == SessionTypeAdmin {
		return AdminRefreshCookiePath
	}
	return AppRefreshCookiePath
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
	audience := claims.RegisteredClaims.Audience
	if len(audience) == 0 {
		audience = jwt.ClaimStrings{string(SessionTypeApp)}
	}
	claims.RegisteredClaims = jwt.RegisteredClaims{
		Subject:   claims.UserID,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(expiresAt),
		Audience:  audience,
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

func HasAudience(claims *Claims, sessionType SessionType) bool {
	if claims == nil {
		return false
	}
	target := sessionType.String()
	for _, audience := range claims.RegisteredClaims.Audience {
		if audience == target {
			return true
		}
	}
	return false
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

func NewCSRFToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(raw), nil
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
