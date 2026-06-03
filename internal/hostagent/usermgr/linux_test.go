package usermgr

import (
	"reflect"
	"testing"
)

func TestUseraddArgs_Defaults(t *testing.T) {
	m := &LinuxUserManager{}
	got := m.useraddArgs("cindy")
	want := []string{"--create-home", "--shell", "/bin/bash", "--gid", "molma", "cindy"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("useraddArgs default: got %v, want %v", got, want)
	}
}

func TestUseraddArgs_Overrides(t *testing.T) {
	m := &LinuxUserManager{Shell: "/usr/bin/zsh", PrimaryGroup: "users"}
	got := m.useraddArgs("dave")
	want := []string{"--create-home", "--shell", "/usr/bin/zsh", "--gid", "users", "dave"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("useraddArgs overrides: got %v, want %v", got, want)
	}
}

func TestUpsertPassword_EmptySlug(t *testing.T) {
	m := &LinuxUserManager{}
	if err := m.UpsertPassword("", "pw"); err == nil {
		t.Error("want error for empty slug")
	}
}

func TestDeleteUser_EmptySlug(t *testing.T) {
	m := &LinuxUserManager{}
	if err := m.DeleteUser(""); err == nil {
		t.Error("want error for empty slug")
	}
}

func TestSetRole_EmptySlug(t *testing.T) {
	m := &LinuxUserManager{}
	if err := m.SetRole("", "admin"); err == nil {
		t.Error("want error for empty slug")
	}
}

func TestSetRole_BadRole(t *testing.T) {
	m := &LinuxUserManager{}
	if err := m.SetRole("cindy", "superuser"); err == nil {
		t.Error("want error for bad role")
	}
}

func TestParseGroupMembership(t *testing.T) {
	const content = `# comment line ignored
root:x:0:
sudo:x:27:alice,bob,cindy
molma:x:3000:
nogroup:x:65534:
`
	cases := []struct {
		slug, group string
		want        bool
	}{
		{"alice", "sudo", true},
		{"bob", "sudo", true},
		{"cindy", "sudo", true},
		{"dave", "sudo", false},         // not listed
		{"alice", "molma", false},       // listed in sudo, not molma
		{"alice", "nonexistent", false}, // group not present
		{"", "sudo", false},
	}
	for _, c := range cases {
		got := parseGroupMembership([]byte(content), c.slug, c.group)
		if got != c.want {
			t.Errorf("parseGroupMembership(%q,%q) = %v, want %v", c.slug, c.group, got, c.want)
		}
	}
}

// Trailing-whitespace / empty-member-field corner cases observed in practice.
func TestParseGroupMembership_EdgeCases(t *testing.T) {
	const content = "sudo:x:27: alice , bob \nempty:x:99:\n"
	if !parseGroupMembership([]byte(content), "alice", "sudo") {
		t.Error("alice with leading-space should match")
	}
	if !parseGroupMembership([]byte(content), "bob", "sudo") {
		t.Error("bob with surrounding whitespace should match")
	}
	if parseGroupMembership([]byte(content), "anyone", "empty") {
		t.Error("empty member list should not match")
	}
	// Regression: strings.Split("",",") returns [""], so without the empty-slug
	// guard a "" lookup against the empty member list of "empty:" would match.
	if parseGroupMembership([]byte(content), "", "empty") {
		t.Error("empty slug must never match, even against an empty member list")
	}
}
