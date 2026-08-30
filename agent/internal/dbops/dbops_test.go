package dbops

import (
	"strings"
	"testing"
)

func TestValidateDBName(t *testing.T) {
	for _, v := range []string{"app", "my_app", "a1", strings.Repeat("z", 63)} {
		if err := ValidateDBName(v); err != nil {
			t.Errorf("ValidateDBName(%q) unexpected: %v", v, err)
		}
	}
	for _, v := range []string{"", "1a", "_a", "A", "a-b", "a.b", "a b", "a;drop table", strings.Repeat("a", 64)} {
		if err := ValidateDBName(v); err == nil {
			t.Errorf("ValidateDBName(%q) = nil, want error", v)
		}
	}
}

func TestValidateUserName(t *testing.T) {
	if err := ValidateUserName("appuser"); err != nil {
		t.Errorf("valid user rejected: %v", err)
	}
	for _, v := range []string{"", "1u", "U", "a-b", strings.Repeat("u", 33)} {
		if err := ValidateUserName(v); err == nil {
			t.Errorf("ValidateUserName(%q) = nil, want error", v)
		}
	}
}

func TestGeneratePasswordSafe(t *testing.T) {
	for i := 0; i < 50; i++ {
		p, err := GeneratePassword()
		if err != nil {
			t.Fatal(err)
		}
		if len(p) != 20 {
			t.Fatalf("password length = %d, want 20", len(p))
		}
		// Must be embeddable in DDL without escaping.
		if !isSafePassword(p) {
			t.Fatalf("generated password not safe: %q", p)
		}
	}
}

func TestIsSafePassword(t *testing.T) {
	if !isSafePassword("abcXYZ123") {
		t.Error("alphanumeric should be safe")
	}
	for _, bad := range []string{"", "a'b", "a\\b", "a b", "a;b", strings.Repeat("x", 65)} {
		if isSafePassword(bad) {
			t.Errorf("isSafePassword(%q) = true, want false", bad)
		}
	}
}

func TestNewEngine(t *testing.T) {
	if _, err := New(EngineMySQL, AdminConfig{Host: "127.0.0.1", User: "root"}); err != nil {
		t.Errorf("mysql ops: %v", err)
	}
	if _, err := New(EnginePostgres, AdminConfig{Host: "127.0.0.1", User: "postgres"}); err != nil {
		t.Errorf("postgres ops: %v", err)
	}
	if _, err := New("oracle", AdminConfig{}); err == nil {
		t.Error("unsupported engine must error")
	}
}
