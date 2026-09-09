package securityaudit

import (
	"strings"
	"testing"
)

// TestScanDetectsSecretMaterial verifies each default pattern fires on a
// representative payload (the scanner's own contract).
func TestScanDetectsSecretMaterial(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{"bearer", `log: Authorization: Bearer skliveabcdef1234567890`, "bearer_token"},
		{"api_key_field", `{"api_key": "abcd1234efgh5678"}`, "api_key_field"},
		{"totp_seed", `{"secret": "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"}`, "totp_seed"},
		{"recovery_codes", `{"codes": ["abcd-efgh", "ijkl-mnop", "qrst-uvwx"]}`, "recovery_code"},
		{"private_key", `-----BEGIN RSA PRIVATE KEY-----`, "private_key_pem"},
		{"postgres_dsn", `conn=postgres://user:hunter2@db.local:5432/app`, "postgres_dsn"},
		{"aws_key", `key=AKIAIOSFODNN7EXAMPLE`, "aws_access_key"},
		{"sealed_blob", `{"creds_enc": "AAECAwQFBgcICRARAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="}`, "sealed_blob"},
	}
	for _, tc := range cases {
		findings := Scan("test:"+tc.name, tc.payload)
		found := false
		for _, f := range findings {
			if f.Pattern == tc.want {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: pattern %s not detected in %q (findings=%v)", tc.name, tc.want, tc.payload, findings)
		}
	}
}

// TestScanPlantedLiteral is the core guarantee: an exact secret value planted
// in any payload is reported, and the finding never echoes the value back.
func TestScanPlantedLiteral(t *testing.T) {
	secret := "PLANTED-SECRET-ZZ9QXK7T4M"
	payloads := []string{
		`{"token": "` + secret + `"}`,
		"2026/09/09 handler: response body " + secret + " end",
		`{"nested": {"deep": ["` + secret + `"]}}`,
	}
	for i, p := range payloads {
		f := ScanPlant("payload-"+string(rune('0'+i)), []string{secret}, p)
		if len(f) == 0 {
			t.Errorf("payload %d does not trigger any finding", i)
		}
		for _, fi := range f {
			if strings.Contains(fi.Excerpt, secret) {
				t.Errorf("finding excerpt leaks the secret itself: %q", fi.Excerpt)
			}
		}
	}
	if !strings.Contains(payloads[0], "token") {
		t.Fatal("sanity: payload fixture changed")
	}
	// A payload containing the literal verbatim MUST carry a planted_secret
	// finding (not just a generic pattern hit).
	direct := ScanPlant("direct", []string{secret}, payloads[2])
	hasPlanted := false
	for _, f := range direct {
		if f.Pattern == "planted_secret" {
			hasPlanted = true
		}
	}
	if !hasPlanted {
		t.Fatalf("payload 2 contains the literal verbatim but no planted_secret finding: %v", direct)
	}
}

// TestCleanPayloads produces no false positives on benign content.
func TestCleanPayloads(t *testing.T) {
	clean := []string{
		`{"status": "ok", "user": {"id": "abc", "email": "a@b.c"}, "expires_at": "2026-10-01T00:00:00Z"}`,
		`GET /v1/auth/me 200 12ms request_id=abc-123`,
		`{"websites": [], "total": 0}`,
		// short base64-looking blobs below the sealed threshold are fine
		`{"key_enc": "c2hvcnQ="}`,
	}
	if findings := Scan("clean", clean...); len(findings) != 0 {
		t.Errorf("false positives on benign payloads: %v", findings)
	}
}

// TestValidatePlanted guards the API: literals below 12 chars are rejected
// (too short to be a real secret; scanning them invites false positives).
func TestValidatePlanted(t *testing.T) {
	if ValidatePlanted([]string{"short"}) {
		t.Error("ValidatePlanted accepted a too-short literal")
	}
	if !ValidatePlanted([]string{"long-enough-secret-value"}) {
		t.Error("ValidatePlanted rejected a valid literal")
	}
}

// TestExcerptRedaction ensures pattern matches are replaced in excerpts even
// when surrounding context is preserved for debugging.
func TestExcerptRedaction(t *testing.T) {
	payload := `{"password": "supersecretvalue99"}`
	findings := Scan("redact", payload)
	for _, f := range findings {
		if strings.Contains(f.Excerpt, "supersecretvalue99") {
			t.Errorf("excerpt echoes secret: %q", f.Excerpt)
		}
	}
}
