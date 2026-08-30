package users

import "testing"

func TestValidateUsername(t *testing.T) {
	valid := []string{"admin", "web-prod-01", "ops.team_2", "Admin.X"}
	for _, u := range valid {
		if err := ValidateUsername(u); err != nil {
			t.Errorf("username %q should be valid, got %v", u, err)
		}
	}
	invalid := []string{
		"", "ab",                       // too short
		"contains space",               // space
		"root;rm -rf /",                // injection attempt
		"ütf8user",                     // non-ascii
	}
	for _, u := range invalid {
		if err := ValidateUsername(u); err == nil {
			t.Errorf("username %q should be rejected", u)
		}
	}
}

func TestValidateEmail(t *testing.T) {
	if ValidateEmail("") {
		t.Error("empty email must be rejected (email optional elsewhere)")
	}
	if !ValidateEmail("ops@example.com") {
		t.Error("standard address accepted")
	}
	if ValidateEmail("missing-at-sign") {
		t.Error("address without @ must be rejected")
	}
	if ValidateEmail("two spaces@example.com") {
		t.Error("whitespace must be rejected")
	}
}
