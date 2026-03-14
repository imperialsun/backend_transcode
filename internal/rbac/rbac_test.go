package rbac

import "testing"

func TestHasPermissionAndRole(t *testing.T) {
	perms := []string{"foo", "bar"}
	if !HasPermission(perms, "foo") {
		t.Fatal("expected permission foo")
	}
	if HasPermission(perms, "") {
		t.Fatal("expected empty permission to be false")
	}
	if HasPermission(perms, "baz") {
		t.Fatal("expected missing permission to be false")
	}

	if !HasAnyPermission(perms, "baz", "bar") {
		t.Fatal("expected HasAnyPermission to return true for bar")
	}
	if HasAnyPermission(perms) {
		t.Fatal("expected HasAnyPermission with no targets to be false")
	}

	roles := []string{"admin", "user"}
	if !HasRole(roles, "admin") {
		t.Fatal("expected HasRole to return true")
	}
	if HasRole(roles, "") {
		t.Fatal("expected empty role to be false")
	}
	if HasRole(roles, "guest") {
		t.Fatal("expected missing role to be false")
	}
}
