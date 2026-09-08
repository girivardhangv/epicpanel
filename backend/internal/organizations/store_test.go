package organizations

import "testing"

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Acme Hosting":    "acme-hosting",
		"  spaces  here ": "spaces-here",
		"Under_Score":     "under-score",
		"Dots.Name":       "dots-name",
		"CAPS LOCK":       "caps-lock",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidSlug(t *testing.T) {
	valid := []string{"ab", "acme-hosting", "a1-b2", "org-123"}
	invalid := []string{"", "-leading", "trailing-", "A", "a", "has space", "x", "UPPER"}
	for _, s := range valid {
		if !ValidSlug(s) {
			t.Errorf("ValidSlug(%q) = false, want true", s)
		}
	}
	for _, s := range invalid {
		if ValidSlug(s) {
			t.Errorf("ValidSlug(%q) = true, want false", s)
		}
	}
}

func TestValidRole(t *testing.T) {
	for _, r := range []Role{RoleOwner, RoleAdmin, RoleDeveloper, RoleBilling, RoleSupport} {
		if !ValidRole(r) {
			t.Errorf("ValidRole(%q) = false, want true", r)
		}
	}
	if ValidRole(Role("superadmin")) {
		t.Error("unknown role accepted")
	}
}

func TestRoleRankOrdering(t *testing.T) {
	if !(RoleRank[RoleOwner] > RoleRank[RoleAdmin] &&
		RoleRank[RoleAdmin] > RoleRank[RoleDeveloper] &&
		RoleRank[RoleDeveloper] > RoleRank[RoleBilling] &&
		RoleRank[RoleBilling] == RoleRank[RoleSupport]) {
		t.Fatal("role rank ordering is wrong")
	}
}
