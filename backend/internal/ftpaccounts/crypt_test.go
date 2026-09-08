package ftpaccounts

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Known-answer vectors verified against glibc crypt (python3 crypt module,
// which defers to the system libc). Mixed published Drepper-suite settings
// and extra pins: these guarantee agent-side chpasswd/useradd accepts our
// hashes byte-for-byte.
func TestSHA512CryptKnownAnswers(t *testing.T) {
	cases := []struct{ password, setting, want string }{
		{"password", "$6$saltstring$",
			"$6$saltstring$adDbXsJjcDlq2662QPgd.tkSOVmnG9Tt3oXl4HR60SusC3AGjirnDenVZp3DGwLwqy6iYKCzannhaX9DR72nN1"},
		{"Hello world!", "$6$saltstring$",
			"$6$saltstring$svn8UoSVapNtMuq1ukKS4tPQd8iKwSMHWjl/O817G3uBnIFNjnQJuesI68u4OTLiBFdcbYEdFCoEOfaS35inz1"},
		{"Hello world!", "$6$rounds=10000$saltstringsaltst$",
			"$6$rounds=10000$saltstringsaltst$OW1/O6BYHV6BcXZu8QVeXbDWra3Oeqh0sbHbbMCVNSnCM/UrjmM0Dp8vOuZeHBy/YTBmSK6H9qs/y3RnOaw5v."},
		{"This is just a test", "$6$rounds=10000$saltstringsaltst$",
			"$6$rounds=10000$saltstringsaltst$vjUJn/k3yM1f7OaKzO2ynECzmC./FdtzJs6dH700GX.YKl..M317P9awjcAo/q5DsaAbC5EpJ/uE1JN018LzJ0"},
		{"Hello world!", "$6$rounds=5000$toolongsaltstrin$",
			"$6$rounds=5000$toolongsaltstrin$iGlL7EUUfzNQx59x3ydJZ.zXPMUu1dOynSEl/vcNhLlas77qD0DzRswhhB6LdrXTz250at0syAfUXra.XrxAI1"},
		{"Hello world!", "$6$rounds=1400$anotherlongsalts$",
			"$6$rounds=1400$anotherlongsalts$5FGyu8c4BZDX4wJgs0Un26YOw2XibT5eTkHF1I1aP3QqStoJI9BHD2YPJYsAjEePVGUyBjdZxcNqMWlrrbIOC."},
		{"a very much longer text to encrypt.  ", "$6$saltstring$",
			"$6$saltstring$V0iM0sRsTYHAnHZeoOyYQa8FIGMA7vhUKJ/CIJb8lBTl3X6mTja33IM88WW8ZSTRdIwzdIdz1pNQj68ZiR/rt1"},
	}
	for _, c := range cases {
		got, err := cryptWithSetting(c.password, c.setting)
		if err != nil {
			t.Errorf("cryptWithSetting(%q, %q): %v", c.password, c.setting, err)
			continue
		}
		if got != c.want {
			t.Errorf("cryptWithSetting(%q, %q) =\n  %q\nwant\n  %q", c.password, c.setting, got, c.want)
		}
	}
}

func TestHashPasswordFormat(t *testing.T) {
	h, err := HashPassword("s3cret-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(h, "$6$") {
		t.Fatalf("hash missing $6$ prefix: %q", h)
	}
	parts := strings.Split(h, "$")
	if len(parts) != 4 {
		t.Fatalf("hash should have 4 $-separated parts: %q", h)
	}
	if len(parts[2]) != 16 {
		t.Fatalf("salt should be 16 chars, got %d", len(parts[2]))
	}
	if len(parts[3]) != 86 {
		t.Fatalf("digest should be 86 chars, got %d", len(parts[3]))
	}
}

func TestHashPasswordSaltUniqueness(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		h, err := HashPassword("same-password")
		if err != nil {
			t.Fatalf("HashPassword: %v", err)
		}
		salt := strings.Split(h, "$")[2]
		if seen[salt] {
			t.Fatalf("duplicate salt after %d hashes: %s", i+1, salt)
		}
		seen[salt] = true
	}
}

func TestVerifyPassword(t *testing.T) {
	h, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !VerifyPassword(h, "correct horse battery staple") {
		t.Error("VerifyPassword rejected the correct password")
	}
	if VerifyPassword(h, "wrong password") {
		t.Error("VerifyPassword accepted a wrong password")
	}
	if VerifyPassword("not-a-crypt-hash", "x") {
		t.Error("VerifyPassword accepted a malformed hash")
	}
	if !VerifyPassword("$6$saltstring$adDbXsJjcDlq2662QPgd.tkSOVmnG9Tt3oXl4HR60SusC3AGjirnDenVZp3DGwLwqy6iYKCzannhaX9DR72nN1", "password") {
		t.Error("VerifyPassword rejected the published test vector")
	}
}

func TestDeriveUserName(t *testing.T) {
	cases := []struct {
		websiteID string
		label     string
		want      string
	}{
		{"0b9d4f6a-1111-2222-3333-444455556666", "backups", "ep-ftp-0b9d4f6a-backups"},
		{"0b9d4f6a-1111-2222-3333-444455556666", "My Site! Files", "ep-ftp-0b9d4f6a-my-site-files"},
		{"0b9d4f6a-1111-2222-3333-444455556666", "Über-FTP", "ep-ftp-0b9d4f6a-ber-ftp"},
		{"0b9d4f6a-1111-2222-3333-444455556666", "---", "ep-ftp-0b9d4f6a-account"},
		{"0b9d4f6a-1111-2222-3333-444455556666", "an extremely long label that goes on and on forever and ever", stringsPrefix32("ep-ftp-0b9d4f6a-")},
	}
	for _, c := range cases {
		id := mustUUID(t, c.websiteID)
		got := DeriveUserName(id, c.label)
		if got != c.want {
			t.Errorf("DeriveUserName(%s, %q) = %q, want %q", c.websiteID, c.label, got, c.want)
		}
		if len(got) < 2 || len(got) > 32 {
			t.Errorf("derived name %q violates length 2-32", got)
		}
		if !strings.HasPrefix(got, "ep-") {
			t.Errorf("derived name %q lacks ep- prefix", got)
		}
		for _, r := range got[3:] {
			alnum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_'
			if !alnum {
				t.Errorf("derived name %q contains invalid rune %q", got, r)
			}
		}
	}
}

func mustUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	if err != nil {
		t.Fatalf("bad uuid %q: %v", s, err)
	}
	return id
}

func stringsPrefix32(prefix string) string {
	name := prefix + "an-extremely-long-label-th"
	if len(name) > 32 {
		name = name[:32]
	}
	return strings.TrimRight(name, "-")
}

func TestValidateHomeSubdir(t *testing.T) {
	valid := map[string]string{
		"":            "",
		"  ":          "",
		"ftp/backups": "ftp/backups",
		"a//b/./c":    "a/b/c",
		"single":      "single",
	}
	for in, want := range valid {
		got, err := ValidateHomeSubdir(in)
		if err != nil || got != want {
			t.Errorf("ValidateHomeSubdir(%q) = %q, %v; want %q, nil", in, got, err, want)
		}
	}
	invalid := []string{"/abs", "..", "../escape", "a/../../escape", "a/../..", "ends/..", strings.Repeat("x", 101)}
	for _, in := range invalid {
		if got, err := ValidateHomeSubdir(in); err == nil {
			t.Errorf("ValidateHomeSubdir(%q) = %q, want error", in, got)
		}
	}
}

func TestHomeDirFor(t *testing.T) {
	id := mustUUID(t, "0b9d4f6a-1111-2222-3333-444455556666")
	if got := HomeDirFor(id, ""); got != "/srv/epicpanel/websites/0b9d4f6a-1111-2222-3333-444455556666" {
		t.Errorf("HomeDirFor base = %q", got)
	}
	if got := HomeDirFor(id, "ftp/backups"); got != "/srv/epicpanel/websites/0b9d4f6a-1111-2222-3333-444455556666/ftp/backups" {
		t.Errorf("HomeDirFor subdir = %q", got)
	}
}
