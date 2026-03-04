package rbac

import "strings"

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

func HasAnyPermission(permissions []string, targets ...string) bool {
	for _, target := range targets {
		if HasPermission(permissions, target) {
			return true
		}
	}
	return false
}

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
