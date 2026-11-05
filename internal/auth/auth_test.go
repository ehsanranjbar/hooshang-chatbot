package auth

import "testing"

func TestOwnerIsAdmin(t *testing.T) {
	a := Owner{ID: 7}
	if !a.IsAdmin(-100, 7) {
		t.Error("owner should be admin in any chat")
	}
	if a.IsAdmin(-100, 8) {
		t.Error("non-owner should not be admin")
	}
}

func TestOwnerSatisfiesInterface(t *testing.T) {
	var _ Admins = Owner{ID: 1}
}
