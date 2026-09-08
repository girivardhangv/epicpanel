package dns

import (
	"strings"
	"testing"
)

func TestValidateHostname(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"plain", "example.com", false},
		{"uppercase normalized ok", "EXAMPLE.COM", false},
		{"trailing dot ok", "example.com.", false},
		{"multi label", "a.b.c.example.com", false},
		{"hyphen inside label", "my-site.example.com", false},
		{"single label", "localhost", true},
		{"empty", "", true},
		{"spaces", "exa mple.com", true},
		{"leading hyphen", "-example.com", true},
		{"trailing hyphen", "example-.com", true},
		{"underscore", "not_a_host.example.com", true},
		{"empty label", "example..com", true},
		{"too long total", "a." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 63) + "." + strings.Repeat("e", 63) + ".com", true},
		{"label too long", strings.Repeat("a", 64) + ".com", true},
		{"unicode", "exämple.com", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateHostname(tt.in)
			if tt.wantErr != (err != nil) {
				t.Fatalf("ValidateHostname(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
		})
	}
}

func TestValidateZone(t *testing.T) {
	if err := ValidateZone("Example.COM."); err != nil {
		t.Fatalf("valid zone rejected: %v", err)
	}
	if err := ValidateZone("localhost"); err == nil {
		t.Fatal("single-label zone accepted")
	}
	if err := ValidateZone(""); err == nil {
		t.Fatal("empty zone accepted")
	}
}

func TestValidateRecordName(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		zone    string
		want    string
		wantErr bool
	}{
		{"apex at", "@", "example.com", "@", false},
		{"apex empty", "", "example.com", "@", false},
		{"apex fqdn", "example.com", "example.com", "@", false},
		{"sub fqdn", "www.example.com", "example.com", "www", false},
		{"deep fqdn", "a.b.example.com", "example.com", "a.b", false},
		{"short label", "www", "example.com", "www", false},
		{"deep short", "a.b", "example.com", "a.b", false},
		{"wildcard", "*.www", "example.com", "*.www", false},
		{"service label", "_dmarc", "example.com", "_dmarc", false},
		{"srv name", "_sip._tcp", "example.com", "_sip._tcp", false},
		{"trailing dot", "www.", "example.com", "www", false},
		{"uppercase", "WWW", "example.com", "www", false},
		{"other domain", "www.other.com", "example.com", "", true},
		{"invalid chars", "ww w", "example.com", "", true},
		{"wildcard not first", "www.*", "example.com", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateRecordName(tt.in, tt.zone)
			if tt.wantErr != (err != nil) {
				t.Fatalf("ValidateRecordName(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Fatalf("ValidateRecordName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestValidateRecord(t *testing.T) {
	tests := []struct {
		name     string
		typ      string
		rname    string
		value    string
		ttl      int
		priority int
		wantErr  bool
	}{
		{"A ok", "A", "www", "192.0.2.10", 3600, 0, false},
		{"A bad ip", "A", "www", "999.1.2.3", 3600, 0, true},
		{"A ipv6 rejected", "A", "www", "2001:db8::1", 3600, 0, true},
		{"A mapped v4 rejected", "A", "www", "::ffff:192.0.2.1", 3600, 0, true},
		{"AAAA ok", "AAAA", "www", "2001:db8::1", 3600, 0, false},
		{"AAAA v4 rejected", "AAAA", "www", "192.0.2.1", 3600, 0, true},
		{"AAAA garbage", "AAAA", "www", "db8::garbage", 3600, 0, true},
		{"CNAME ok", "CNAME", "www", "target.example.com.", 3600, 0, false},
		{"CNAME at apex", "CNAME", "@", "target.example.com", 3600, 0, true},
		{"CNAME bad target", "CNAME", "www", "not a host", 3600, 0, true},
		{"MX ok", "MX", "@", "mail.example.com", 3600, 10, false},
		{"MX priority too big", "MX", "@", "mail.example.com", 3600, 65536, true},
		{"MX negative priority", "MX", "@", "mail.example.com", 3600, -1, true},
		{"MX bad target", "MX", "@", "mail..example.com", 3600, 10, true},
		{"TXT ok", "TXT", "@", "v=spf1 include:example.com ~all", 3600, 0, false},
		{"TXT newline", "TXT", "@", "line1\nline2", 3600, 0, true},
		{"TXT too long", "TXT", "@", strings.Repeat("x", 256), 3600, 0, true},
		{"TXT empty", "TXT", "@", "", 3600, 0, true},
		{"TXT at 255", "TXT", "@", strings.Repeat("x", 255), 3600, 0, false},
		{"NS ok", "NS", "@", "ns1.example.com.", 3600, 0, false},
		{"NS bad", "NS", "@", "-bad-", 3600, 0, true},
		{"SRV ok", "SRV", "_sip._tcp", "10 5060 sip.example.com", 3600, 10, false},
		{"SRV bad fields", "SRV", "_sip._tcp", "10 5060", 3600, 10, true},
		{"SRV bad port", "SRV", "_sip._tcp", "10 99999 sip.example.com", 3600, 10, true},
		{"SRV bad target", "SRV", "_sip._tcp", "10 5060 -bad-", 3600, 10, true},
		{"CAA ok", "CAA", "@", "0 issue letsencrypt.org", 3600, 0, false},
		{"CAA quoted ok", "CAA", "@", `0 issue "letsencrypt.org"`, 3600, 0, false},
		{"CAA flag too big", "CAA", "@", "256 issue letsencrypt.org", 3600, 0, true},
		{"CAA bad tag", "CAA", "@", "0 issuee letsencrypt.org", 3600, 0, true},
		{"CAA missing value", "CAA", "@", "0 issue", 3600, 0, true},
		{"unknown type", "HINFO", "@", "x", 3600, 0, true},
		{"ttl too low", "A", "www", "192.0.2.1", 59, 0, true},
		{"ttl too high", "A", "www", "192.0.2.1", 604801, 0, true},
		{"ttl at minimum", "A", "www", "192.0.2.1", 60, 0, false},
		{"priority on A", "A", "www", "192.0.2.1", 3600, 5, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRecord(tt.typ, tt.rname, tt.value, tt.ttl, tt.priority)
			if tt.wantErr != (err != nil) {
				t.Fatalf("ValidateRecord(%s,%s,%s,%d,%d) err = %v, wantErr %v", tt.typ, tt.rname, tt.value, tt.ttl, tt.priority, err, tt.wantErr)
			}
		})
	}
}
