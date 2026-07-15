package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// These tests cover the core token, password, and reset-token helpers.
func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("StrongPass123!")
	if err != nil {
		t.Fatalf("HashPassword returned error: %v", err)
	}

	if !VerifyPassword(hash, "StrongPass123!") {
		t.Fatal("VerifyPassword should accept the original password")
	}
	if VerifyPassword(hash, "wrong-password") {
		t.Fatal("VerifyPassword should reject an invalid password")
	}
}

func TestNewAndParseAccessToken(t *testing.T) {
	secret := "unit-test-secret"
	token, _, err := NewAccessToken(secret, 5*time.Minute, Claims{
		UserID:      "user-1",
		OrgID:       "org-1",
		Email:       "user@example.com",
		GlobalRoles: []string{"user"},
		OrgRoles:    []string{"org_member"},
		Permissions: []string{"feature.settings"},
	})
	if err != nil {
		t.Fatalf("NewAccessToken returned error: %v", err)
	}

	claims, err := ParseAccessToken(secret, token)
	if err != nil {
		t.Fatalf("ParseAccessToken returned error: %v", err)
	}
	if claims.UserID != "user-1" || claims.OrgID != "org-1" {
		t.Fatalf("unexpected claims payload: %+v", claims)
	}
}

func TestParseAccessTokenRejectsAlternateHMACAlgorithms(t *testing.T) {
	secret := "unit-test-secret"
	token := jwt.NewWithClaims(jwt.SigningMethodHS384, Claims{
		UserID: "user-1",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().UTC().Add(5 * time.Minute)),
		},
	})
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("failed to sign alternate token: %v", err)
	}

	if _, err := ParseAccessToken(secret, signed); err == nil {
		t.Fatal("expected non-HS256 access token to be rejected")
	}
}

func TestParseRefreshToken(t *testing.T) {
	sessionID, token, err := ParseRefreshToken("session.token")
	if err != nil {
		t.Fatalf("ParseRefreshToken returned error: %v", err)
	}
	if sessionID != "session" || token != "token" {
		t.Fatalf("unexpected parsed refresh token: sessionID=%q token=%q", sessionID, token)
	}

	if _, _, err := ParseRefreshToken("invalid"); err == nil {
		t.Fatal("expected ParseRefreshToken to fail on invalid format")
	}
}

func TestNewPasswordResetToken(t *testing.T) {
	token, err := NewPasswordResetToken(30 * time.Minute)
	if err != nil {
		t.Fatalf("NewPasswordResetToken returned error: %v", err)
	}
	if token.RawToken == "" {
		t.Fatal("expected raw token to be populated")
	}
	if token.Hash != HashPasswordResetToken(token.RawToken) {
		t.Fatal("expected token hash to match raw token")
	}
	if !token.ExpiresAt.After(time.Now().UTC()) {
		t.Fatal("expected password reset token to expire in the future")
	}
}
