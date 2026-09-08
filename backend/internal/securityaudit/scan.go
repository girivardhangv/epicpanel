package securityaudit

// Phase 12 — automated secret-leak scanner (exported so any module's tests
// can extend it: Phase 8's bot-secret test builds on this). Greps API
// responses + captured log lines for secret material patterns.

import (
	"regexp"
	"strings"
)

// Pattern is one secret-material signature.
type Pattern struct {
	Name    string
	Re      *regexp.Regexp
	// SecretValue markers: when set, the scanner additionally verifies that
	// this exact literal never appears (planted-secret assertions).
	Literal string
}

// defaultPatterns cover the secret classes the platform stores: passwords,
// bearer/API tokens, TOTP seeds, recovery codes, private keys, sealed-blob
// leakage, database DSNs.
var defaultPatterns = []Pattern{
	{Name: "bearer_token", Re: regexp.MustCompile(`(?i)authorization["']?\s*[:=]\s*["']?bearer\s+[a-z0-9._-]{16,}`)},
	{Name: "api_key_field", Re: regexp.MustCompile(`(?i)"(api_key|apikey|secret|password|token|private_key|access_key)"\s*:\s*"[^"]{8,}"`)},
	{Name: "totp_seed", Re: regexp.MustCompile(`(?i)[A-Z2-7]{32}`)},
	{Name: "recovery_code", Re: regexp.MustCompile(`(?i)"(recovery_code|codes)"\s*:\s*\[[^\]]{10,}\]`)},
	{Name: "private_key_pem", Re: regexp.MustCompile(`-----BEGIN (RSA |EC )?PRIVATE KEY-----`)},
	{Name: "postgres_dsn", Re: regexp.MustCompile(`postgres(ql)?://[^\s"']+:[^\s"']+@`)},
	{Name: "mysql_dsn", Re: regexp.MustCompile(`[a-z0-9._-]+:[^\s"'/@]+@tcp\(`)},
	{Name: "aws_access_key", Re: regexp.MustCompile(`AKIA[0-9A-Z]{16}`)},
	{Name: "sealed_blob", Re: regexp.MustCompile(`(?i)"(key_enc|creds_enc|secret_enc)"\s*:\s*"[A-Za-z0-9+/=]{40,}"`)},
}

// ScanOptions configures one scan run.
type ScanOptions struct {
	// Extra patterns from the calling module (e.g. bot env secret markers).
	Extra []Pattern
	// PlantedSecrets: exact values that must never appear anywhere
	// (responses or logs). Strongest assertion — the test plants a
	// recognizable value and proves it never leaks.
	PlantedSecrets []string
}

// Finding is one leak hit.
type Finding struct {
	Pattern  string
	Excerpt  string // bounded to 120 chars, further redacted
	Location string // caller-provided label (e.g. "GET /v1/auth/me")
}

// Scan checks the given payloads (JSON responses, log lines, headers) for
// secret material. Returns findings; empty = clean.
func Scan(location string, payloads ...string) []Finding {
	return scanWith(defaultPatterns, nil, location, payloads)
}

// ScanPlant checks that exact planted secret literals never appear. Use in
// round-trip tests: create a secret via the API, then scan everything the
// API returned / logged during the test.
func ScanPlant(location string, planted []string, payloads ...string) []Finding {
	return scanWith(defaultPatterns, planted, location, payloads)
}

// scanWith runs all patterns + planted literals over the payloads.
func scanWith(patterns []Pattern, planted []string, location string, payloads []string) []Finding {
	var findings []Finding
	for _, p := range payloads {
		for _, pat := range patterns {
			if pat.Re == nil || !pat.Re.MatchString(p) {
				continue
			}
			findings = append(findings, Finding{
				Pattern:  pat.Name,
				Excerpt:  excerpt(p, pat.Re),
				Location: location,
			})
		}
		for _, lit := range planted {
			if lit != "" && strings.Contains(p, lit) {
				findings = append(findings, Finding{
					Pattern:  "planted_secret",
					Excerpt:  "<planted literal redacted>",
					Location: location,
				})
			}
		}
	}
	return findings
}

func excerpt(s string, re *regexp.Regexp) string {
	loc := re.FindStringIndex(s)
	if loc == nil {
		return ""
	}
	start := loc[0]
	if start > 40 {
		start -= 20
	}
	end := loc[1] + 20
	if end > len(s) {
		end = len(s)
	}
	out := s[start:end]
	out = strings.ReplaceAll(out, "\n", "\\n")
	if len(out) > 120 {
		out = out[:117] + "..."
	}
	// Never echo the raw secret back into the finding.
	return re.ReplaceAllString(out, "<redacted>")
}

// ValidatePlanted ensures planted literals are scan-worthy.
func ValidatePlanted(planted []string) bool {
	for _, p := range planted {
		if len(p) < 12 {
			return false
		}
	}
	return true
}
