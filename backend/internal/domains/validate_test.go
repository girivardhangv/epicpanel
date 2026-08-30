package domains

import "testing"

func TestNormalize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Example.COM", "example.com"},
		{"  Shop.Example.com. ", "shop.example.com"},
		{"MiXeD.CaSe.Co.UK", "mixed.case.co.uk"},
	}
	for _, c := range cases {
		if got := Normalize(c.in); got != c.want {
			t.Errorf("Normalize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestValidateAccepts(t *testing.T) {
	valid := []string{
		"example.com",
		"www.example.com",
		"sub-domain.example.co.uk",
		"a.b.c.example.io",
		"xn--80ak6aa92e.com", // punycode provided pre-encoded
		"123.example.com",
		"example.museum",
		"*.example.com", // wildcard only when allowed
	}
	for _, d := range valid {
		if err := Validate(d, true); err != nil {
			t.Errorf("Validate(%q, true) unexpected error: %v", d, err)
		}
	}
	if err := Validate("example.com", false); err != nil {
		t.Errorf("plain domain rejected without wildcard: %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		in            string
		allowWildcard bool
		reason        string
	}{
		{"", false, "empty"},
		{"example.com/path", false, "path traversal"},
		{"../etc/passwd", false, "relative traversal"},
		{"..\\windows", false, "windows traversal"},
		{"example.com; rm -rf /", false, "shell metacharacters"},
		{"example.com$(reboot)", false, "command substitution"},
		{"example.com | cat /etc/passwd", false, "pipe injection"},
		{"exa mple.com", false, "whitespace"},
		{"exa\tmple.com", false, "tab"},
		{"exa\nmple.com", false, "newline"},
		{"example.com?", false, "query char"},
		{"example.com:8080", false, "port separator"},
		{"user@example.com", false, "at sign"},
		{"example_.com", false, "underscore"},
		{"exämple.com", false, "non-ascii / IDN must be punycode"},
		{"*.example.com", false, "wildcard disallowed by policy"},
		{"sub.*.example.com", true, "wildcard in wrong position"},
		{"*.*.example.com", true, "double wildcard"},
		{"*", true, "bare wildcard"},
		{"example", false, "no tld"},
		{"-example.com", false, "label leading hyphen"},
		{"example-.com", false, "label trailing hyphen"},
		{"example..com", false, "empty label"},
		{".example.com", false, "leading dot"},
		{"example.c", false, "tld too short"},
		{"example.com-", false, "tld trailing hyphen"},
		{string(make([]byte, 300)) + ".com", false, "overlong"},
	}
	for _, c := range cases {
		err := Validate(c.in, c.allowWildcard)
		if err == nil {
			t.Errorf("Validate(%q, %v) = nil, want error (%s)", c.in, c.allowWildcard, c.reason)
		}
	}
}
