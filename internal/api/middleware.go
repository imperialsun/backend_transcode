package api

import (
	"context"
	"crypto/subtle"
	"log"
	"strings"

	"demeter-backend/internal/auth"
	"demeter-backend/internal/observability"
	"demeter-backend/internal/rbac"
	"github.com/gofiber/fiber/v2"
)

const claimsContextKey = "claims"

func (a *App) AppAuthRequired() fiber.Handler {
	return a.AuthRequired(auth.SessionTypeApp)
}

func (a *App) AdminAuthRequired() fiber.Handler {
	return a.AuthRequired(auth.SessionTypeAdmin)
}

func (a *App) AuthRequired(sessionType auth.SessionType) fiber.Handler {
	return func(c *fiber.Ctx) error {
		traceID := requestTraceID(c)
		raw := readAccessToken(c, sessionType)
		if raw == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "missing access token"})
		}
		tokenClaims, err := auth.ParseAccessToken(a.Config.JWTSecret, raw)
		if err != nil {
			logAuthAccessDenied(c, traceID, sessionType, "invalid_token", nil, nil, map[string]any{
				"path": c.Path(),
				"ip":   c.IP(),
			})
			return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "invalid access token"})
		}
		claims, status, err := a.resolveLiveClaims(requestContext(c), tokenClaims, sessionType)
		if err != nil {
			if status == fiber.StatusForbidden {
				logAuthAccessDenied(c, traceID, sessionType, "live_forbidden", tokenClaims, nil, map[string]any{
					"path": c.Path(),
					"ip":   c.IP(),
				})
				return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden"})
			}
			if status == fiber.StatusUnauthorized {
				logAuthAccessDenied(c, traceID, sessionType, "invalid_audience", tokenClaims, nil, map[string]any{
					"path": c.Path(),
					"ip":   c.IP(),
				})
				return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "invalid access token"})
			}
			logAuthAccessDenied(c, traceID, sessionType, "resolve_claims_failed", tokenClaims, err, map[string]any{
				"path": c.Path(),
				"ip":   c.IP(),
			})
			return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "failed to resolve authorization context"})
		}
		c.Locals(claimsContextKey, claims)
		return c.Next()
	}
}

func logAuthAccessDenied(c *fiber.Ctx, traceID string, sessionType auth.SessionType, reason string, claims *auth.Claims, err error, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["reason"] = reason
	fields["session"] = sessionType.String()
	if err != nil {
		fields["error"] = err
	}
	userID, orgID := claimsActorIDs(claims)
	log.Print(observability.FormatStepLine("auth", requestRoutePath(c), "access_denied", traceID, userID, orgID, "access denied", fields))
}

func (a *App) resolveLiveClaims(ctx context.Context, tokenClaims *auth.Claims, sessionType auth.SessionType) (*auth.Claims, int, error) {
	if tokenClaims == nil || strings.TrimSpace(tokenClaims.UserID) == "" {
		return nil, fiber.StatusUnauthorized, fiber.ErrUnauthorized
	}
	if !auth.HasAudience(tokenClaims, sessionType) {
		return nil, fiber.StatusUnauthorized, fiber.ErrUnauthorized
	}
	if a.Store == nil {
		return nil, fiber.StatusInternalServerError, fiber.ErrInternalServerError
	}

	user, err := a.Store.GetUserByID(ctx, tokenClaims.UserID)
	if err != nil {
		return nil, fiber.StatusInternalServerError, err
	}
	if user == nil || user.Status != "active" {
		return nil, fiber.StatusForbidden, fiber.ErrForbidden
	}

	org, err := a.Store.GetOrganizationByID(ctx, user.OrganizationID)
	if err != nil {
		return nil, fiber.StatusInternalServerError, err
	}
	if org == nil || org.Status != "active" {
		return nil, fiber.StatusForbidden, fiber.ErrForbidden
	}

	globalRoles, err := a.Store.GetGlobalRoleCodesByUser(ctx, user.ID)
	if err != nil {
		return nil, fiber.StatusInternalServerError, err
	}
	orgRoles, err := a.Store.GetOrganizationRoleCodesByUser(ctx, user.ID)
	if err != nil {
		return nil, fiber.StatusInternalServerError, err
	}
	permissions, err := a.Store.ResolveEffectivePermissions(ctx, user.ID)
	if err != nil {
		return nil, fiber.StatusInternalServerError, err
	}

	return &auth.Claims{
		UserID:           user.ID,
		OrgID:            user.OrganizationID,
		Email:            user.Email,
		GlobalRoles:      globalRoles,
		OrgRoles:         orgRoles,
		Permissions:      permissions,
		CSRFToken:        tokenClaims.CSRFToken,
		RegisteredClaims: tokenClaims.RegisteredClaims,
	}, fiber.StatusOK, nil
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

func RequireAdminCSRF() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if c.Method() == fiber.MethodGet || c.Method() == fiber.MethodHead || c.Method() == fiber.MethodOptions {
			return c.Next()
		}
		claims := MustClaims(c)
		if claims == nil || strings.TrimSpace(claims.CSRFToken) == "" {
			return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "invalid csrf token"})
		}
		candidate := strings.TrimSpace(c.Get(auth.AdminCSRFHeaderName))
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(claims.CSRFToken)) != 1 {
			return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "invalid csrf token"})
		}
		return c.Next()
	}
}

func (a *App) EnforceAdminOrigin() fiber.Handler {
	allowedOrigins := make(map[string]struct{}, len(a.Config.AdminCORSOrigins))
	for _, origin := range a.Config.AdminCORSOrigins {
		allowedOrigins[strings.TrimSpace(origin)] = struct{}{}
	}
	return func(c *fiber.Ctx) error {
		if !strings.HasPrefix(c.Path(), "/api/v1/admin") {
			return c.Next()
		}
		origin := strings.TrimSpace(c.Get(fiber.HeaderOrigin))
		if origin == "" {
			return c.Next()
		}
		if _, ok := allowedOrigins[origin]; ok {
			return c.Next()
		}
		return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{Error: "forbidden origin"})
	}
}

func MustClaims(c *fiber.Ctx) *auth.Claims {
	claims, _ := c.Locals(claimsContextKey).(*auth.Claims)
	return claims
}

func readAccessToken(c *fiber.Ctx, sessionType auth.SessionType) string {
	cookie := strings.TrimSpace(c.Cookies(sessionType.AccessCookieName()))
	if cookie != "" {
		return cookie
	}
	authHeader := strings.TrimSpace(c.Get(fiber.HeaderAuthorization))
	if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
		return strings.TrimSpace(authHeader[7:])
	}
	return ""
}
