package api

import (
	"strings"

	"demeter-backend/internal/auth"
	"demeter-backend/internal/rbac"
	"github.com/gofiber/fiber/v2"
)

const claimsContextKey = "claims"

func (a *App) AuthRequired() fiber.Handler {
	return func(c *fiber.Ctx) error {
		raw := readAccessToken(c)
		if raw == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "missing access token"})
		}
		claims, err := auth.ParseAccessToken(a.Config.JWTSecret, raw)
		if err != nil {
			return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "invalid access token"})
		}
		c.Locals(claimsContextKey, claims)
		return c.Next()
	}
}

func RequirePermissions(codes ...string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		claims, ok := c.Locals(claimsContextKey).(*auth.Claims)
		if !ok || claims == nil {
			return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
		}
		for _, code := range codes {
			if !rbac.HasPermission(claims.Permissions, code) {
				return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden"})
			}
		}
		return c.Next()
	}
}

func RequireAnyPermission(codes ...string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		claims, ok := c.Locals(claimsContextKey).(*auth.Claims)
		if !ok || claims == nil {
			return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
		}
		if !rbac.HasAnyPermission(claims.Permissions, codes...) {
			return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden"})
		}
		return c.Next()
	}
}

func RequireAdminScope() fiber.Handler {
	return func(c *fiber.Ctx) error {
		claims := MustClaims(c)
		if claims == nil {
			return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "unauthorized"})
		}
		if rbac.HasRole(claims.GlobalRoles, "super_admin") || rbac.HasRole(claims.OrgRoles, "org_admin") {
			return c.Next()
		}
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "admin scope required"})
	}
}

func MustClaims(c *fiber.Ctx) *auth.Claims {
	claims, _ := c.Locals(claimsContextKey).(*auth.Claims)
	return claims
}

func readAccessToken(c *fiber.Ctx) string {
	cookie := strings.TrimSpace(c.Cookies(auth.AccessCookieName))
	if cookie != "" {
		return cookie
	}
	authHeader := strings.TrimSpace(c.Get(fiber.HeaderAuthorization))
	if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		return strings.TrimSpace(authHeader[7:])
	}
	return ""
}
