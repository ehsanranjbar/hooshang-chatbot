package auth

import "testing"

func TestListIsAdmin(t *testing.T) {
	a := List{IDs: []int64{7, 9}}
	if !a.IsAdmin(-100, 7) || !a.IsAdmin(-200, 9) {
		t.Error("listed ids should be admins in any chat")
	}
	if a.IsAdmin(-100, 8) {
		t.Error("unlisted id should not be admin")
	}
	if (List{}).IsAdmin(-100, 7) {
		t.Error("empty list has no admins")
	}
}

func TestListSatisfiesInterface(t *testing.T) {
	var _ Admins = List{IDs: []int64{1}}
}
