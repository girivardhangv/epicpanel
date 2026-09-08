package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func dnsZoneTestPayload() DNSZonePayload {
	return DNSZonePayload{
		WebsiteID: "11111111-1111-1111-1111-111111111111",
		Zone: DNSZoneFilePayload{
			Domain:     "example.test",
			TTL:        3600,
			PrimaryNS:  "ns1.example.test",
			AdminEmail: "hostmaster.example.test",
			Refresh:    7200,
			Retry:      1800,
			Expire:     1209600,
			Minimum:    86400,
			Serial:     1720000000,
		},
		Records: []DNSRecordFilePayload{
			{Name: "@", Type: "A", Value: "1.2.3.4", TTL: 3600},
			{Name: "www", Type: "CNAME", Value: "example.test.", TTL: 3600},
			{Name: "mail", Type: "A", Value: "5.6.7.8", TTL: 3600},
			{Name: "@", Type: "MX", Value: "mail.example.test", TTL: 3600, Priority: 10},
			{Name: "@", Type: "TXT", Value: "v=spf1 include:_spf.example.test ~all", TTL: 3600},
			{Name: "_dmarc", Type: "TXT", Value: "v=DMARC1; p=none", TTL: 3600},
			{Name: "_sip._tcp", Type: "SRV", Value: "5 5060 sip.example.test", TTL: 3600, Priority: 5},
			{Name: "@", Type: "CAA", Value: `0 issue "letsencrypt.org"`, TTL: 3600},
			{Name: "*.dev", Type: "A", Value: "9.9.9.9", TTL: 600},
			{Name: "@", Type: "NS", Value: "ns2.example.test", TTL: 86400},
		},
	}
}

func TestRenderDNSZoneGolden(t *testing.T) {
	payload := dnsZoneTestPayload()
	out, err := renderDNSZoneFile(payload.Zone, payload.Records)
	if err != nil {
		t.Fatal(err)
	}
	want := `; managed by EpicPanel - do not edit
; zone example.test serial 1720000000
$ORIGIN example.test.
$TTL 3600
@	IN	SOA	ns1.example.test. hostmaster.example.test. (
		1720000000	; serial
		7200	; refresh
		1800	; retry
		1209600	; expire
		86400	; minimum
	)
@	3600	IN	A	1.2.3.4
@	3600	IN	CAA	0 issue "letsencrypt.org"
@	3600	IN	MX	10 mail.example.test.
@	86400	IN	NS	ns2.example.test.
@	3600	IN	TXT	"v=spf1 include:_spf.example.test ~all"
*.dev	600	IN	A	9.9.9.9
_dmarc	3600	IN	TXT	"v=DMARC1; p=none"
_sip._tcp	3600	IN	SRV	5 5 5060 sip.example.test.
mail	3600	IN	A	5.6.7.8
www	3600	IN	CNAME	example.test.
`
	if out != want {
		t.Errorf("rendered zone mismatch\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}
}

func TestRenderDNSZoneDeterministic(t *testing.T) {
	payload := dnsZoneTestPayload()
	a, err := renderDNSZoneFile(payload.Zone, payload.Records)
	if err != nil {
		t.Fatal(err)
	}
	reversed := make([]DNSRecordFilePayload, len(payload.Records))
	for i, r := range payload.Records {
		reversed[len(payload.Records)-1-i] = r
	}
	b, err := renderDNSZoneFile(payload.Zone, reversed)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Error("rendering is order-dependent; idempotency would break")
	}
}

func TestRenderDNSZoneTXTQuoting(t *testing.T) {
	out, err := renderDNSZoneFile(DNSZoneFilePayload{Domain: "example.test", TTL: 3600, PrimaryNS: "ns1.example.test", AdminEmail: "h.example.test", Refresh: 1, Retry: 1, Expire: 1, Minimum: 1},
		[]DNSRecordFilePayload{{Name: "@", Type: "TXT", Value: `has "quotes" and \backslash`, TTL: 3600}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"has \"quotes\" and \\backslash"`) {
		t.Errorf("TXT quoting wrong:\n%s", out)
	}
}

func TestDNSZoneValidationMatrix(t *testing.T) {
	cases := []struct {
		name    string
		rec     DNSRecordFilePayload
		wantErr string
	}{
		{name: "ok ipv4", rec: DNSRecordFilePayload{Name: "@", Type: "A", Value: "1.2.3.4", TTL: 3600}},
		{name: "a ipv6 rejected", rec: DNSRecordFilePayload{Name: "@", Type: "A", Value: "::1", TTL: 3600}, wantErr: "IPv4"},
		{name: "a garbage rejected", rec: DNSRecordFilePayload{Name: "@", Type: "A", Value: "999.1.1.1", TTL: 3600}, wantErr: "IPv4"},
		{name: "ok ipv6", rec: DNSRecordFilePayload{Name: "@", Type: "AAAA", Value: "2001:db8::1", TTL: 3600}},
		{name: "aaaa ipv4 rejected", rec: DNSRecordFilePayload{Name: "@", Type: "AAAA", Value: "1.2.3.4", TTL: 3600}, wantErr: "IPv6"},
		{name: "ok cname", rec: DNSRecordFilePayload{Name: "www", Type: "CNAME", Value: "example.test", TTL: 3600}},
		{name: "cname at apex rejected", rec: DNSRecordFilePayload{Name: "@", Type: "CNAME", Value: "other.test", TTL: 3600}, wantErr: "apex"},
		{name: "cname bad target", rec: DNSRecordFilePayload{Name: "www", Type: "CNAME", Value: "not a host", TTL: 3600}, wantErr: "hostname"},
		{name: "ok mx", rec: DNSRecordFilePayload{Name: "@", Type: "MX", Value: "mail.example.test", TTL: 3600, Priority: 10}},
		{name: "mx priority too big", rec: DNSRecordFilePayload{Name: "@", Type: "MX", Value: "mail.example.test", TTL: 3600, Priority: 65536}, wantErr: "priority"},
		{name: "mx negative priority", rec: DNSRecordFilePayload{Name: "@", Type: "MX", Value: "mail.example.test", TTL: 3600, Priority: -1}, wantErr: "priority"},
		{name: "mx stray priority rejected", rec: DNSRecordFilePayload{Name: "@", Type: "A", Value: "1.2.3.4", TTL: 3600, Priority: 5}, wantErr: "priority is only valid"},
		{name: "ok txt", rec: DNSRecordFilePayload{Name: "@", Type: "TXT", Value: "hello world", TTL: 3600}},
		{name: "txt too long", rec: DNSRecordFilePayload{Name: "@", Type: "TXT", Value: strings.Repeat("x", 256), TTL: 3600}, wantErr: "255"},
		{name: "txt newline rejected", rec: DNSRecordFilePayload{Name: "@", Type: "TXT", Value: "a\nb", TTL: 3600}, wantErr: "newline"},
		{name: "ok ns", rec: DNSRecordFilePayload{Name: "@", Type: "NS", Value: "ns1.example.test", TTL: 3600}},
		{name: "ns bad target", rec: DNSRecordFilePayload{Name: "@", Type: "NS", Value: "-bad.example.test", TTL: 3600}, wantErr: "hostname"},
		{name: "ok srv", rec: DNSRecordFilePayload{Name: "_sip._tcp", Type: "SRV", Value: "5 5060 sip.example.test", TTL: 3600, Priority: 5}},
		{name: "srv two fields", rec: DNSRecordFilePayload{Name: "_sip._tcp", Type: "SRV", Value: "5060 sip.example.test", TTL: 3600}, wantErr: "weight port"},
		{name: "srv port out of range", rec: DNSRecordFilePayload{Name: "_sip._tcp", Type: "SRV", Value: "0 70000 host.test", TTL: 3600}, wantErr: "0-65535"},
		{name: "ok caa", rec: DNSRecordFilePayload{Name: "@", Type: "CAA", Value: `0 issue "letsencrypt.org"`, TTL: 3600}},
		{name: "caa bad tag", rec: DNSRecordFilePayload{Name: "@", Type: "CAA", Value: `0 bogus "x"`, TTL: 3600}, wantErr: "CAA tag"},
		{name: "caa two fields", rec: DNSRecordFilePayload{Name: "@", Type: "CAA", Value: `0 issue`, TTL: 3600}, wantErr: "flag tag"},
		{name: "caa flag out of range", rec: DNSRecordFilePayload{Name: "@", Type: "CAA", Value: `300 issue "x"`, TTL: 3600}, wantErr: "0-255"},
		{name: "unsupported type", rec: DNSRecordFilePayload{Name: "@", Type: "PTR", Value: "x", TTL: 3600}, wantErr: "unsupported type"},
		{name: "ttl too low", rec: DNSRecordFilePayload{Name: "@", Type: "A", Value: "1.2.3.4", TTL: 30}, wantErr: "ttl"},
		{name: "ttl too high", rec: DNSRecordFilePayload{Name: "@", Type: "A", Value: "1.2.3.4", TTL: 604801}, wantErr: "ttl"},
		{name: "wildcard not leftmost", rec: DNSRecordFilePayload{Name: "sub.*", Type: "A", Value: "1.2.3.4", TTL: 3600}, wantErr: "leftmost"},
		{name: "underscore label ok", rec: DNSRecordFilePayload{Name: "_acme-challenge", Type: "TXT", Value: "token", TTL: 3600}},
		{name: "fqdn under zone normalized", rec: DNSRecordFilePayload{Name: "WWW.Example.Test.", Type: "A", Value: "1.2.3.4", TTL: 3600}},
		{name: "foreign name rejected", rec: DNSRecordFilePayload{Name: "something.else.test", Type: "A", Value: "1.2.3.4", TTL: 3600}, wantErr: "hostname under"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := dnsZoneValidateRecords("example.test", []DNSRecordFilePayload{tc.rec})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected ok, got: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tc.wantErr, err)
			}
		})
	}
}

func TestDNSZoneZoneHeaderValidation(t *testing.T) {
	base := DNSZoneFilePayload{Domain: "example.test", TTL: 3600, PrimaryNS: "ns1.example.test.", AdminEmail: "hostmaster.example.test.", Refresh: 7200, Retry: 1800, Expire: 1209600, Minimum: 86400}
	if err := dnsZoneValidateZoneHeader(base); err != nil {
		t.Fatalf("base header rejected: %v", err)
	}
	bad := base
	bad.Domain = "not a host"
	if err := dnsZoneValidateZoneHeader(bad); err == nil {
		t.Error("bad domain accepted")
	}
	bad = base
	bad.TTL = 10
	if err := dnsZoneValidateZoneHeader(bad); err == nil {
		t.Error("low ttl accepted")
	}
	bad = base
	bad.PrimaryNS = ""
	if err := dnsZoneValidateZoneHeader(bad); err == nil {
		t.Error("empty primary ns accepted")
	}
	bad = base
	bad.Minimum = 0
	if err := dnsZoneValidateZoneHeader(bad); err == nil {
		t.Error("zero minimum accepted")
	}
}

func TestMergeZoneDeclarations(t *testing.T) {
	root := t.TempDir()
	otherSite := filepath.Join(root, "22222222-2222-2222-2222-222222222222")
	ourSite := filepath.Join(root, "11111111-1111-1111-1111-111111111111")
	if err := os.MkdirAll(otherSite, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(ourSite, 0o750); err != nil {
		t.Fatal(err)
	}
	otherZone := filepath.Join(otherSite, "other.test.zone")
	ourZone := filepath.Join(ourSite, "example.test.zone")
	staleZone := filepath.Join(otherSite, "gone.test.zone")
	foreignZone := filepath.Join(t.TempDir(), "foreign.test.zone")
	for _, p := range []string{otherZone, ourZone, foreignZone} {
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	existing := `// managed by EpicPanel - do not edit
zone "other.test" {
	type master;
	file "` + otherZone + `";
	allow-query { any; };
};
zone "gone.test" {
	type master;
	file "` + staleZone + `";
	allow-query { any; };
};
zone "foreign.test" {
	type master;
	file "` + foreignZone + `";
	allow-query { any; };
};
`
	merged, err := MergeZoneDeclarations(existing, "11111111-1111-1111-1111-111111111111", "example.test", ourZone)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(merged, `zone "other.test"`) || !strings.Contains(merged, otherZone) {
		t.Errorf("live other-site declaration dropped:\n%s", merged)
	}
	if strings.Contains(merged, "gone.test") {
		t.Errorf("stale declaration (file deleted) kept:\n%s", merged)
	}
	if strings.Contains(merged, "foreign.test") {
		t.Errorf("foreign path declaration kept:\n%s", merged)
	}
	if !strings.Contains(merged, `zone "example.test"`) || !strings.Contains(merged, ourZone) {
		t.Errorf("our declaration missing:\n%s", merged)
	}
	if strings.Count(merged, `zone "example.test"`) != 1 {
		t.Errorf("declaration duplicated:\n%s", merged)
	}

	// Upsert: merging again with the same inputs is byte-identical.
	again, err := MergeZoneDeclarations(merged, "11111111-1111-1111-1111-111111111111", "example.test", ourZone)
	if err != nil {
		t.Fatal(err)
	}
	if again != merged {
		t.Error("merge is not idempotent")
	}

	// Sweep mode (empty domain) prunes the site's zones.
	swept, err := MergeZoneDeclarations(merged, "11111111-1111-1111-1111-111111111111", "", ourZone)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(swept, "example.test") {
		t.Errorf("sweep kept our zone:\n%s", swept)
	}
	if !strings.Contains(swept, "other.test") {
		t.Errorf("sweep dropped other site's zone:\n%s", swept)
	}
}

func TestSyncDNSZoneIdempotencyAndSweep(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	bindDir := t.TempDir()
	paths := dnsZonePaths{ZonesRoot: root, IncludePath: filepath.Join(bindDir, "named.conf.epicpanel-zones"), NamedConf: filepath.Join(bindDir, "named.conf")}
	e := NewExecutor()

	payload := dnsZoneTestPayload()
	out, err := e.syncDNSZone(ctx, payload, paths)
	if err != nil {
		t.Fatal(err)
	}
	zonePath := filepath.Join(root, payload.WebsiteID, payload.Zone.Domain+".zone")
	if out.ZoneFile != zonePath {
		t.Errorf("zone file path %q, want %q", out.ZoneFile, zonePath)
	}
	if out.Records != len(payload.Records) {
		t.Errorf("records %d, want %d", out.Records, len(payload.Records))
	}
	if out.Serial != payload.Zone.Serial {
		t.Errorf("serial %d, want %d", out.Serial, payload.Zone.Serial)
	}
	if out.BindPresent {
		t.Error("bind_present must be false without named.conf")
	}
	if out.Reloaded {
		t.Error("reloaded must be false without bind")
	}
	joined := strings.Join(out.Notes, "; ")
	if !strings.Contains(joined, "bind9 not detected") {
		t.Errorf("missing bind-absent note: %v", out.Notes)
	}
	st, err := os.Stat(filepath.Join(root, payload.WebsiteID))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o750 {
		t.Errorf("zone dir perm %v, want 750", st.Mode().Perm())
	}
	zst, err := os.Stat(zonePath)
	if err != nil {
		t.Fatal(err)
	}
	if zst.Mode().Perm() != 0o644 {
		t.Errorf("zone file perm %v, want 644", zst.Mode().Perm())
	}
	inc, err := os.ReadFile(paths.IncludePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(inc), `zone "example.test"`) {
		t.Errorf("include missing our zone:\n%s", inc)
	}

	// Second sync of the same payload: no writes, no validation, no reload.
	out2, err := e.syncDNSZone(ctx, payload, paths)
	if err != nil {
		t.Fatal(err)
	}
	joined2 := strings.Join(out2.Notes, "; ")
	if !strings.Contains(joined2, "zone file unchanged") {
		t.Errorf("second sync did not skip the zone file: %v", out2.Notes)
	}
	if !strings.Contains(joined2, "include file unchanged") {
		t.Errorf("second sync did not skip the include file: %v", out2.Notes)
	}

	// Serial bump changes content; include stays identical.
	payload.Zone.Serial = 1720000001
	out3, err := e.syncDNSZone(ctx, payload, paths)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(out3.Notes, "; "), "zone file unchanged") {
		t.Error("serial bump should rewrite the zone file")
	}
	if !strings.Contains(strings.Join(out3.Notes, "; "), "include file unchanged") {
		t.Error("serial bump must not touch the include file")
	}

	// Removal sweep clears the site's zone files and include entries.
	if err := e.removeDNSZonesForWebsite(ctx, payload.WebsiteID, paths); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(filepath.Join(root, payload.WebsiteID)); len(entries) != 0 {
		t.Error("zone dir not empty after sweep")
	}
	incAfter, err := os.ReadFile(paths.IncludePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(incAfter), "example.test") {
		t.Errorf("include still lists swept zone:\n%s", incAfter)
	}
}

func TestSyncDNSZoneEmptyDomainSkips(t *testing.T) {
	paths := dnsZonePaths{ZonesRoot: t.TempDir(), IncludePath: filepath.Join(t.TempDir(), "inc.conf"), NamedConf: filepath.Join(t.TempDir(), "named.conf")}
	out, err := NewExecutor().syncDNSZone(context.Background(), DNSZonePayload{WebsiteID: "11111111-1111-1111-1111-111111111111"}, paths)
	if err != nil {
		t.Fatal(err)
	}
	if out.ZoneFile != "" || len(out.Notes) == 0 {
		t.Errorf("expected skip note, got %+v", out)
	}
	if _, err := os.Stat(paths.IncludePath); err == nil {
		t.Error("include file written for empty-domain payload")
	}
}

func TestSyncDNSZoneInvalidPayload(t *testing.T) {
	paths := dnsZonePaths{ZonesRoot: t.TempDir(), IncludePath: filepath.Join(t.TempDir(), "inc.conf"), NamedConf: filepath.Join(t.TempDir(), "named.conf")}
	e := NewExecutor()
	if _, err := e.syncDNSZone(context.Background(), DNSZonePayload{WebsiteID: "not-a-uuid", Zone: DNSZoneFilePayload{Domain: "example.test"}}, paths); err == nil {
		t.Error("invalid website id accepted")
	}
	payload := dnsZoneTestPayload()
	payload.Records = []DNSRecordFilePayload{{Name: "@", Type: "A", Value: "nope", TTL: 3600}}
	if _, err := e.syncDNSZone(context.Background(), payload, paths); err == nil {
		t.Error("bad record value accepted")
	}
}

func TestSyncDNSZoneWithFakeNamedCheckconf(t *testing.T) {
	// named-checkzone exists but is not exercised here; what matters is that
	// SwapValidated's validate hook runs and passes, and unchanged content
	// skips validation entirely. Use a fake named-checkconf that fails if the
	// zone file content is flagged (content-based failure injection).
	ctx := context.Background()
	root := t.TempDir()
	bindDir := t.TempDir()
	binDir := t.TempDir()

	fake := "#!/bin/sh\nif grep -q BOGUS \"$2\" 2>/dev/null; then echo bad zone >&2; exit 1; fi\nexit 0\n"
	for _, name := range []string{"named-checkzone", "named-checkconf"} {
		p := filepath.Join(binDir, name)
		if err := os.WriteFile(p, []byte(fake), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	paths := dnsZonePaths{ZonesRoot: root, IncludePath: filepath.Join(bindDir, "named.conf.epicpanel-zones"), NamedConf: filepath.Join(bindDir, "named.conf")}
	e := NewExecutor()

	payload := dnsZoneTestPayload()
	if _, err := e.syncDNSZone(ctx, payload, paths); err != nil {
		t.Fatal(err)
	}
	zonePath := filepath.Join(root, payload.WebsiteID, payload.Zone.Domain+".zone")

	// A bogus record passes agent validation but fails named-checkzone; the
	// previous (good) zone file content must be restored.
	bad := payload
	bad.Zone.Serial++
	bad.Records = []DNSRecordFilePayload{{Name: "@", Type: "TXT", Value: "BOGUS", TTL: 3600}}
	if _, err := e.syncDNSZone(ctx, bad, paths); err == nil {
		t.Fatal("expected validation failure for bogus zone")
	}
	kept, err := os.ReadFile(zonePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(kept), "serial 1720000000") {
		t.Errorf("previous zone file not restored after failed validation:\n%s", kept)
	}
}

func TestDNSZoneIncludeLineHelpers(t *testing.T) {
	conf := 'x'
	_ = conf
	confStr := "options {\n\tdirectory \"/var/cache/bind\";\n};\n"
	if dnsZoneHasIncludeLine(confStr, "/etc/bind/named.conf.epicpanel-zones") {
		t.Error("include line detected in bare config")
	}
	with := dnsZoneWithIncludeLine(confStr, "/etc/bind/named.conf.epicpanel-zones")
	if !dnsZoneHasIncludeLine(with, "/etc/bind/named.conf.epicpanel-zones") {
		t.Error("include line not added")
	}
	if strings.Count(with, "include") != 1 {
		t.Errorf("include line duplicated:\n%s", with)
	}
}
