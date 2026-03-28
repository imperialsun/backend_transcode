package auth

import (
	"strings"
	"testing"
	"time"
)

func TestSessionTypeMethods(t *testing.T) {
	if SessionTypeApp.String() != "app" {
		t.Fatalf("expected String() to return app, got %q", SessionTypeApp.String())
	}
	if SessionTypeAdmin.AccessCookieName() != AdminAccessCookieName {
		t.Fatalf("expected admin access cookie name, got %q", SessionTypeAdmin.AccessCookieName())
	}
	if SessionTypeApp.AccessCookieName() != AppAccessCookieName {
		t.Fatalf("expected app access cookie name, got %q", SessionTypeApp.AccessCookieName())
	}
	if SessionTypeAdmin.RefreshCookieName() != AdminRefreshCookieName {
		t.Fatalf("expected admin refresh cookie name, got %q", SessionTypeAdmin.RefreshCookieName())
	}
	if SessionTypeApp.RefreshCookieName() != AppRefreshCookieName {
		t.Fatalf("expected app refresh cookie name, got %q", SessionTypeApp.RefreshCookieName())
	}
	if SessionTypeAdmin.AccessCookiePath() != AdminAccessCookiePath {
		t.Fatalf("expected admin access cookie path, got %q", SessionTypeAdmin.AccessCookiePath())
	}
	if SessionTypeApp.AccessCookiePath() != AppAccessCookiePath {
		t.Fatalf("expected app access cookie path, got %q", SessionTypeApp.AccessCookiePath())
	}
	if SessionTypeAdmin.RefreshCookiePath() != AdminRefreshCookiePath {
		t.Fatalf("expected admin refresh cookie path, got %q", SessionTypeAdmin.RefreshCookiePath())
	}
	if SessionTypeApp.RefreshCookiePath() != AppRefreshCookiePath {
		t.Fatalf("expected app refresh cookie path, got %q", SessionTypeApp.RefreshCookiePath())
	}
}

func TestHashPasswordAndVerifyPassword(t *testing.T) {
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("expected error for short password")
	}

	hash, err := HashPassword("LongEnough123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !VerifyPassword(hash, "LongEnough123") {
		t.Fatal("expected password verify to succeed")
	}
	if VerifyPassword(hash, "wrong") {
		t.Fatal("expected wrong password to fail")
	}
	// corrupted format
	if VerifyPassword("notavalidhash", "pass") {
		t.Fatal("expected invalid hash format to fail")
	}
}

func TestAccessTokenAudienceAndParsing(t *testing.T) {
	secret := "test-secret"
	claims := Claims{UserID: "u1", OrgID: "o1", Email: "e@x.com"}
	token, _, err := NewAccessToken(secret, time.Minute, claims)
	if err != nil {
		t.Fatalf("NewAccessToken error: %v", err)
	}
	parsed, err := ParseAccessToken(secret, token)
	if err != nil {
		t.Fatalf("ParseAccessToken error: %v", err)
	}
	if parsed.UserID != "u1" || parsed.OrgID != "o1" {
		t.Fatalf("unexpected parsed claims: %+v", parsed)
	}
	if !HasAudience(parsed, SessionTypeApp) {
		t.Fatal("expected default audience to include app")
	}
	if HasAudience(parsed, SessionTypeAdmin) {
		t.Fatal("expected admin not to be present in default audience")
	}

	// token with wrong secret should fail
	if _, err := ParseAccessToken("wrong-secret", token); err == nil {
		t.Fatal("expected parse to fail with wrong secret")
	}
}

func TestNewRefreshTokenAndHashVerification(t *testing.T) {
	tok, err := NewRefreshToken(time.Hour)
	if err != nil {
		t.Fatalf("NewRefreshToken error: %v", err)
	}
	if tok.SessionID == "" || tok.RawToken == "" || tok.Hash == "" {
		t.Fatalf("expected refresh token payload to be populated: %+v", tok)
	}
	if !VerifyRefreshHash(tok.Hash, tok.RawToken) {
		t.Fatal("expected refresh hash to verify")
	}
	if VerifyRefreshHash(tok.Hash, tok.RawToken+"x") {
		t.Fatal("expected modified token to fail verification")
	}
	if HashRefreshToken(tok.RawToken) != tok.Hash {
		t.Fatalf("expected HashRefreshToken to match stored hash, got %q want %q", HashRefreshToken(tok.RawToken), tok.Hash)
	}
}

func TestParseRefreshToken_Errors(t *testing.T) {
	if _, _, err := ParseRefreshToken("invalid"); err == nil {
		t.Fatal("expected error for invalid refresh token format")
	}
	if _, _, err := ParseRefreshToken("."); err == nil {
		t.Fatal("expected error for empty refresh token fields")
	}
}

func TestNewCSRFToken(t *testing.T) {
	tok, err := NewCSRFToken()
	if err != nil {
		t.Fatalf("NewCSRFToken error: %v", err)
	}
	if strings.TrimSpace(tok) == "" {
		t.Fatal("expected non-empty csrf token")
	}
}

func TestGenerateTemporaryPassword(t *testing.T) {
	password, err := GenerateTemporaryPassword(24)
	if err != nil {
		t.Fatalf("GenerateTemporaryPassword error: %v", err)
	}
	if strings.TrimSpace(password) == "" {
		t.Fatal("expected a non-empty temporary password")
	}
	if len(password) < 20 {
		t.Fatalf("expected a reasonably long password, got %q", password)
	}
}
