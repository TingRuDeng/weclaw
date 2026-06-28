package platform

import "testing"

func TestAccessControlAllowsListedUser(t *testing.T) {
	access := NewAccessControl([]string{" user-1 ", "user-2"})

	if !access.Allowed("user-1") {
		t.Fatalf("listed user should be allowed")
	}
	if !access.Allowed(" user-2 ") {
		t.Fatalf("allowed check should trim user id")
	}
}

func TestAccessControlRejectsUnlistedUser(t *testing.T) {
	access := NewAccessControl([]string{"user-1"})

	if access.Allowed("user-3") {
		t.Fatalf("unlisted user should be rejected")
	}
}

func TestAccessControlEmptyListRejectsAll(t *testing.T) {
	access := NewAccessControl(nil)

	if access.Allowed("user-1") {
		t.Fatalf("empty access list should reject all users")
	}
}

func TestAccessControlAllowedUsersReturnsCopy(t *testing.T) {
	access := NewAccessControl([]string{"user-1"})
	users := access.AllowedUsers()
	users[0] = "changed"

	if !access.Allowed("user-1") || access.Allowed("changed") {
		t.Fatalf("AllowedUsers should not expose mutable internal state")
	}
}

func TestAccessControlSetAllowedUpdatesCopiedValue(t *testing.T) {
	access := NewAccessControl([]string{"user-1"})
	copied := access

	access.SetAllowed([]string{"user-2"})

	if copied.Allowed("user-1") {
		t.Fatal("old user should be rejected after hot update")
	}
	if !copied.Allowed("user-2") {
		t.Fatal("new user should be allowed through copied AccessControl")
	}
}
