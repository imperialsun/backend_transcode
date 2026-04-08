package rbac

import "strings"

// HasPermission reports whether the permission set contains the requested
// permission code.
func HasPermission(permissions []string, target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	for _, permission := range permissions {
		if permission == target {
			return true
		}
	}
	return false
}

// HasAnyPermission reports whether the permission set contains at least one of
// the requested permission codes.
func HasAnyPermission(permissions []string, targets ...string) bool {
	for _, target := range targets {
		if HasPermission(permissions, target) {
			return true
		}
	}
	return false
}

// HasRole reports whether the role list contains the requested role code.
func HasRole(roles []string, role string) bool {
	role = strings.TrimSpace(role)
	if role == "" {
		return false
	}
	for _, r := range roles {
		if r == role {
			return true
		}
	}
	return false
}
