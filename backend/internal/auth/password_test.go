package auth

import (
	"strings"
	"testing"
)

func TestHashVerifyRoundTrip(t *testing.T) {
	hash, err := Hash("correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("hash failed: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Fatalf("hash not in PHC argon2id format: %q", hash[:20])
	}
	ok, upgrade, err := Verify(hash, "correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("verify error: %v", err)
	}
	if !ok {
		t.Fatal("correct password rejected")
	}
	if upgrade {
		t.Fatal("freshly generated hash should not need upgrade")
	}

	ok, _, _ = Verify(hash, "wrong-password")
	if ok {
		t.Fatal("wrong password accepted")
	}
}

func TestHashUniqueSalts(t *testing.T) {
	h1, _ := Hash("same-input")
	h2, _ := Hash("same-input")
	if h1 == h2 {
		t.Fatal("two hashes of the same password are identical; salts are broken")
	}
}

func TestTamperedHashFails(t *testing.T) {
	hash, _ := Hash("secret-password-123")

	// Parameter tampering yields a structurally valid hash whose recomputed
	// digest will not match: must report mismatch without error.
	corrupted := strings.Replace(hash, "t=2", "t=3", 1)
	if ok, _, err := Verify(corrupted, "secret-password-123"); err != nil || ok {
		t.Fatalf("tampered parameters must fail verification (ok=%v err=%v)", ok, err)
	}

	garbage := "$argon2id$v=19$m=19456,t=2,p=1$!!!notb64$!!!alsobad"
	if _, _, err := Verify(garbage, "x"); err == nil {
		t.Fatal("garbage hash must return an error")
	}

	if _, _, err := Verify("plaintext", "x"); err == nil {
		t.Fatal("non-PHC string must be rejected as invalid format")
	}
}

func TestNeedsUpgradeWeakerParams(t *testing.T) {
	legacy, err := HashWithParams("legacy-user-password", Params{MemoryKiB: 4096, Time: 1, Threads: 1})
	if err != nil {
		t.Fatalf("legacy hash failed: %v", err)
	}
	ok, upgrade, err := Verify(legacy, "legacy-user-password")
	if err != nil {
		t.Fatalf("verify error: %v", err)
	}
	if !ok {
		t.Fatal("hash generated with weaker params must still verify")
	}
	if !upgrade {
		t.Fatal("weaker-parameter hash must flag needsUpgrade=true")
	}
}

func TestValidatePolicy(t *testing.T) {
	cases := []struct {
		name       string
		pw         string
		minLen     int
		minClasses int
		wantProblems bool
	}{
		{"meets policy", "Str0ng-Passw0rd!x", 12, 3, false},
		{"too short", "Sh0rt!x", 12, 3, true},
		{"single class too long", "aaaaaaaaaaaaaaaaaaaa", 12, 3, true},
		{"two classes", "abcdefgh12345678", 12, 3, true},
		{"three classes OK", "Abcdefgh1!", 10, 3, false},
		{"empty", "", 12, 3, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ValidatePolicy(tc.pw, tc.minLen, tc.minClasses)
			has := len(got) > 0
			if has != tc.wantProblems {
				t.Fatalf("ValidatePolicy(%q) problems=%v, wantProblems=%v", tc.pw, got, tc.wantProblems)
			}
		})
	}
}
