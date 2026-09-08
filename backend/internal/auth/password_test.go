package auth

import "testing"

func TestHashAndCheckPassword(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "" {
		t.Fatal("empty hash")
	}
	if !CheckPassword(hash, "correct horse battery staple") {
		t.Fatal("correct password rejected")
	}
	if CheckPassword(hash, "wrong password") {
		t.Fatal("wrong password accepted")
	}
}

func TestHashPasswordUniqueSalts(t *testing.T) {
	h1, _ := HashPassword("same-password")
	h2, _ := HashPassword("same-password")
	if h1 == h2 {
		t.Fatal("two hashes of the same password are identical; salts missing")
	}
}

func TestSessionTokenHashDeterministic(t *testing.T) {
	if hashToken("abc") != hashToken("abc") {
		t.Fatal("hashToken not deterministic")
	}
	if hashToken("abc") == hashToken("abd") {
		t.Fatal("hashToken collision on different inputs")
	}
}
