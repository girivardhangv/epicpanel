package agent

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ============================================================================
// DNS ZONE PUBLISH (sync_dns_zone) — panel-managed authoritative zones.
//
// Desired state for one BIND zone: the zone file is rendered RFC1035-style
// under /var/lib/epicpanel/dns-zones/<website_id>/<domain>.zone and the zone
// is upserted into the global include file /etc/bind/named.conf.epicpanel-zones.
// BIND is optional: without it the deliverable files are still written.
// Payload tags mirror dns.PublishPayload / dns.ZoneFile / dns.RecordFile.
// ============================================================================

const (
	dnsZonesRootDefault  = "/var/lib/epicpanel/dns-zones"
	dnsZoneIncludePath   = "/etc/bind/named.conf.epicpanel-zones"
	dnsZoneNamedConfPath = "/etc/bind/named.conf"
	dnsZoneMinTTL        = 60
	dnsZoneMaxTTL        = 7 * 24 * 3600
)

// dnsZonePaths groups every filesystem location the DNS ops touch so tests
// can redirect them into temp directories.
type dnsZonePaths struct {
	ZonesRoot   string
	IncludePath string
	NamedConf   string
}

func defaultDNSZonePaths() dnsZonePaths {
	return dnsZonePaths{
		ZonesRoot:   dnsZonesRootDefault,
		IncludePath: dnsZoneIncludePath,
		NamedConf:   dnsZoneNamedConfPath,
	}
}

// DNSZonePayload matches dns.PublishPayload on the wire.
type DNSZonePayload struct {
	WebsiteID string                 `json:"website_id"`
	Zone      DNSZoneFilePayload     `json:"zone"`
	Records   []DNSRecordFilePayload `json:"records"`
}

// DNSZoneFilePayload matches dns.ZoneFile (SOA-carrying zone header).
type DNSZoneFilePayload struct {
	Domain     string `json:"domain"`
	TTL        int    `json:"ttl"`
	PrimaryNS  string `json:"primary_ns"`
	AdminEmail string `json:"admin_email"`
	Refresh    int    `json:"refresh"`
	Retry      int    `json:"retry"`
	Expire     int    `json:"expire"`
	Minimum    int    `json:"minimum"`
	Serial     int64  `json:"serial"`
}

// DNSRecordFilePayload matches dns.RecordFile.
type DNSRecordFilePayload struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Value    string `json:"value"`
	TTL      int    `json:"ttl"`
	Priority int    `json:"priority,omitempty"`
}

// DNSZoneOutcome is the job result reported to the control plane.
type DNSZoneOutcome struct {
	ZoneFile    string   `json:"zone_file"`
	Records     int      `json:"records"`
	Serial      int64    `json:"serial"`
	BindPresent bool     `json:"bind_present"`
	Reloaded    bool     `json:"reloaded"`
	Notes       []string `json:"notes,omitempty"`
}

// SyncDNSZone renders and publishes one zone as desired state.
func (e *Executor) SyncDNSZone(ctx context.Context, payload DNSZonePayload) (*DNSZoneOutcome, error) {
	return e.syncDNSZone(ctx, payload, defaultDNSZonePaths())
}

func (e *Executor) syncDNSZone(ctx context.Context, payload DNSZonePayload, paths dnsZonePaths) (*DNSZoneOutcome, error) {
	if _, err := uuid.Parse(payload.WebsiteID); err != nil {
		return nil, fmt.Errorf("invalid website id: %w", err)
	}
	out := &DNSZoneOutcome{Serial: payload.Zone.Serial}
	if strings.TrimSpace(payload.Zone.Domain) == "" {
		out.Notes = append(out.Notes, "zone domain empty; nothing to sync")
		return out, nil
	}
	zone := payload.Zone
	records := append([]DNSRecordFilePayload(nil), payload.Records...)
	if err := dnsZoneValidateZoneHeader(zone); err != nil {
		return nil, err
	}
	if err := dnsZoneValidateRecords(zone.Domain, records); err != nil {
		return nil, err
	}
	out.Records = len(records)

	bindPresent := fileExists(paths.NamedConf)
	out.BindPresent = bindPresent
	if !bindPresent {
		out.Notes = append(out.Notes, "bind9 not detected; zone files written without named integration")
	}

	content, err := renderDNSZoneFile(zone, records)
	if err != nil {
		return nil, err
	}

	zoneDir := filepath.Join(paths.ZonesRoot, payload.WebsiteID)
	if err := os.MkdirAll(zoneDir, 0o750); err != nil {
		return nil, fmt.Errorf("create zone dir: %w", err)
	}
	// MkdirAll keeps existing modes; enforce 0750 so a pre-created loose dir
	// is tightened (zone files may contain internal hostnames).
	_ = os.Chmod(zoneDir, 0o750)
	zonePath := filepath.Join(zoneDir, zone.Domain+".zone")
	out.ZoneFile = zonePath

	zoneChanged := false
	if prev, readErr := os.ReadFile(zonePath); readErr == nil && string(prev) == content {
		out.Notes = append(out.Notes, "zone file unchanged; skipped write and validation")
	} else {
		validate := func() error {
			if !dnsZoneLookPath("named-checkzone") {
				return nil
			}
			return ValidateCmd(ctx, 120*time.Second, "named-checkzone", zone.Domain, zonePath)
		}
		if err := SwapValidated(zonePath, []byte(content), 0o644, validate); err != nil {
			return nil, fmt.Errorf("zone file: %w", err)
		}
		zoneChanged = true
		if !dnsZoneLookPath("named-checkzone") {
			out.Notes = append(out.Notes, "named-checkzone not found; zone file written without validation")
		}
	}

	existingInclude, _ := os.ReadFile(paths.IncludePath)
	newZone := !dnsZoneIncludeHasZone(string(existingInclude), zone.Domain)
	merged, err := MergeZoneDeclarations(string(existingInclude), payload.WebsiteID, zone.Domain, zonePath)
	if err != nil {
		return nil, fmt.Errorf("merge zone declarations: %w", err)
	}
	includeChanged := string(existingInclude) != merged
	if includeChanged {
		if err := os.MkdirAll(filepath.Dir(paths.IncludePath), 0o755); err != nil {
			return nil, fmt.Errorf("create bind config dir: %w", err)
		}
		if err := SwapValidated(paths.IncludePath, []byte(merged), 0o644, e.dnsZoneValidateInclude(ctx, paths)); err != nil {
			return nil, fmt.Errorf("include file: %w", err)
		}
	} else {
		out.Notes = append(out.Notes, "include file unchanged; skipped rewrite")
	}

	confChanged := false
	if bindPresent {
		confChanged, err = e.dnsZoneEnsureNamedConfInclude(ctx, paths)
		if err != nil {
			return nil, fmt.Errorf("named.conf include: %w", err)
		}
	}

	if bindPresent && dnsZoneLookPath("rndc") && (zoneChanged || includeChanged || confChanged) {
		args := []string{"reload", zone.Domain}
		if newZone || confChanged {
			args = []string{"reconfig"}
		}
		if err := e.run(ctx, "rndc", args...); err != nil {
			out.Notes = append(out.Notes, "rndc "+args[0]+" failed: "+err.Error())
		} else {
			out.Reloaded = true
		}
	}

	slog.Info("dns zone synced", "website", payload.WebsiteID, "domain", zone.Domain, "serial", zone.Serial, "records", len(records))
	return out, nil
}

// RemoveDNSZonesForWebsite deletes every zone file under the website's zone
// directory and rebuilds the include file without those zones.
func (e *Executor) RemoveDNSZonesForWebsite(ctx context.Context, websiteID string) error {
	return e.removeDNSZonesForWebsite(ctx, websiteID, defaultDNSZonePaths())
}

func (e *Executor) removeDNSZonesForWebsite(ctx context.Context, websiteID string, paths dnsZonePaths) error {
	if _, err := uuid.Parse(websiteID); err != nil {
		return fmt.Errorf("invalid website id: %w", err)
	}
	zoneDir := filepath.Join(paths.ZonesRoot, websiteID)
	entries, err := os.ReadDir(zoneDir)
	if err == nil {
		for _, entry := range entries {
			if rmErr := os.RemoveAll(filepath.Join(zoneDir, entry.Name())); rmErr != nil {
				return fmt.Errorf("remove %s: %w", entry.Name(), rmErr)
			}
		}
	}

	existingInclude, _ := os.ReadFile(paths.IncludePath)
	sweepPath := filepath.Join(paths.ZonesRoot, websiteID, ".sweep")
	merged, err := MergeZoneDeclarations(string(existingInclude), websiteID, "", sweepPath)
	if err != nil {
		return fmt.Errorf("merge zone declarations: %w", err)
	}
	if string(existingInclude) == merged {
		return nil
	}
	bindPresent := fileExists(paths.NamedConf)
	if bindPresent {
		if err := SwapValidated(paths.IncludePath, []byte(merged), 0o644, e.dnsZoneValidateInclude(ctx, paths)); err != nil {
			return fmt.Errorf("include file: %w", err)
		}
		if dnsZoneLookPath("rndc") {
			_ = e.run(ctx, "rndc", "reconfig")
		}
		slog.Info("dns zones removed", "website", websiteID)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(paths.IncludePath), 0o755); err != nil {
		return fmt.Errorf("create bind config dir: %w", err)
	}
	if err := AtomicWriteFile(paths.IncludePath, []byte(merged), 0o644); err != nil {
		return fmt.Errorf("include file: %w", err)
	}
	slog.Info("dns zones removed", "website", websiteID)
	return nil
}

// dnsZoneValidateInclude validates the freshly written include file through
// named-checkconf on a temp merged copy of named.conf. Without BIND (no
// config, no binary) it is a no-op.
func (e *Executor) dnsZoneValidateInclude(ctx context.Context, paths dnsZonePaths) func() error {
	return func() error {
		if !dnsZoneLookPath("named-checkconf") || !fileExists(paths.NamedConf) {
			return nil
		}
		conf, err := os.ReadFile(paths.NamedConf)
		if err != nil {
			return err
		}
		return e.dnsZoneCheckConfContent(ctx, paths.NamedConf, dnsZoneWithIncludeLine(string(conf), paths.IncludePath))
	}
}

// dnsZoneCheckConfContent runs named-checkconf against content staged as a
// temp file next to named.conf so relative paths resolve identically.
func (e *Executor) dnsZoneCheckConfContent(ctx context.Context, namedConfPath, content string) error {
	if !dnsZoneLookPath("named-checkconf") {
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(namedConfPath), ".named.conf.epicpanel-check.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return ValidateCmd(ctx, 120*time.Second, "named-checkconf", tmpName)
}

// dnsZoneEnsureNamedConfInclude appends the include line to named.conf when
// missing. Returns whether the file changed.
func (e *Executor) dnsZoneEnsureNamedConfInclude(ctx context.Context, paths dnsZonePaths) (bool, error) {
	conf, err := os.ReadFile(paths.NamedConf)
	if err != nil {
		return false, err
	}
	if dnsZoneHasIncludeLine(string(conf), paths.IncludePath) {
		return false, nil
	}
	merged := dnsZoneWithIncludeLine(string(conf), paths.IncludePath)
	if dnsZoneLookPath("named-checkconf") {
		if err := e.dnsZoneCheckConfContent(ctx, paths.NamedConf, merged); err != nil {
			return false, err
		}
	}
	validate := func() error {
		if !dnsZoneLookPath("named-checkconf") {
			return nil
		}
		return ValidateCmd(ctx, 120*time.Second, "named-checkconf")
	}
	if err := SwapValidated(paths.NamedConf, []byte(merged), 0o644, validate); err != nil {
		return false, err
	}
	slog.Info("dns include added to named.conf", "include", paths.IncludePath)
	return true, nil
}

func dnsZoneLookPath(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

func dnsZoneHasIncludeLine(conf, includePath string) bool {
	re := regexp.MustCompile(`(?m)^\s*include\s+"` + regexp.QuoteMeta(includePath) + `"\s*;`)
	return re.MatchString(conf)
}

func dnsZoneWithIncludeLine(conf, includePath string) string {
	if dnsZoneHasIncludeLine(conf, includePath) {
		return conf
	}
	if conf != "" && !strings.HasSuffix(conf, "\n") {
		conf += "\n"
	}
	return conf + "\n// managed by EpicPanel\ninclude \"" + includePath + "\";\n"
}

// ============================================================================
// Include file merge
// ============================================================================

type dnsZoneDeclaration struct {
	Name string
	File string
}

var (
	dnsZoneDeclRe     = regexp.MustCompile(`zone\s+"([^"]+)"\s*\{([^}]*)\};`)
	dnsZoneDeclFileRe = regexp.MustCompile(`file\s+"([^"]+)"\s*;`)
)

func parseDNSZoneDeclarations(content string) []dnsZoneDeclaration {
	var out []dnsZoneDeclaration
	for _, m := range dnsZoneDeclRe.FindAllStringSubmatch(content, -1) {
		fm := dnsZoneDeclFileRe.FindStringSubmatch(m[2])
		if fm == nil {
			continue
		}
		out = append(out, dnsZoneDeclaration{Name: m[1], File: fm[1]})
	}
	return out
}

func dnsZoneIncludeHasZone(content, domain string) bool {
	for _, d := range parseDNSZoneDeclarations(content) {
		if d.Name == domain {
			return true
		}
	}
	return false
}

// MergeZoneDeclarations rebuilds the global BIND include file content: it
// keeps declarations whose zone file path sits under the panel zones root and
// still exists on disk, drops stale and foreign entries, and upserts the
// declaration for domain at zoneFilePath. An empty domain is sweep mode: only
// pruning happens. The zones root is derived from zoneFilePath
// (<root>/<website_id>/<domain>.zone). Output is deterministically ordered.
func MergeZoneDeclarations(existing, websiteID, domain, zoneFilePath string) (string, error) {
	if zoneFilePath == "" {
		return "", fmt.Errorf("zone file path required")
	}
	zonesRoot := filepath.Dir(filepath.Dir(zoneFilePath))
	siteDir := ""
	if domain == "" && websiteID != "" {
		// Sweep mode: force-drop this website's declarations even if their
		// zone files still exist on disk.
		siteDir = filepath.Join(zonesRoot, websiteID)
	}
	kept := map[string]string{}
	for _, d := range parseDNSZoneDeclarations(existing) {
		if d.Name == domain {
			continue
		}
		if !dnsZonePathUnder(d.File, zonesRoot) {
			continue
		}
		if siteDir != "" && dnsZonePathUnder(d.File, siteDir) {
			continue
		}
		if !fileExists(d.File) {
			continue
		}
		kept[d.Name] = d.File
	}
	if domain != "" {
		kept[domain] = zoneFilePath
	}
	names := make([]string, 0, len(kept))
	for n := range kept {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteString("// managed by EpicPanel - do not edit\n")
	for _, n := range names {
		fmt.Fprintf(&b, "zone %q {\n\ttype master;\n\tfile %q;\n\tallow-query { any; };\n};\n", n, kept[n])
	}
	return b.String(), nil
}

func dnsZonePathUnder(path, root string) bool {
	if root == "" {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absRoot, abs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// ============================================================================
// Rendering and validation
// ============================================================================

// renderDNSZoneFile renders a classic RFC1035 zone file. Output depends only
// on the payload (records sorted by name, type, value) so equal payloads
// produce byte-identical files.
func renderDNSZoneFile(zone DNSZoneFilePayload, records []DNSRecordFilePayload) (string, error) {
	sorted := append([]DNSRecordFilePayload(nil), records...)
	sort.SliceStable(sorted, func(i, j int) bool {
		ni, nj := dnsZoneSortName(sorted[i].Name), dnsZoneSortName(sorted[j].Name)
		if ni != nj {
			return ni < nj
		}
		if sorted[i].Type != sorted[j].Type {
			return sorted[i].Type < sorted[j].Type
		}
		return sorted[i].Value < sorted[j].Value
	})
	var b strings.Builder
	fmt.Fprintf(&b, "; managed by EpicPanel - do not edit\n")
	fmt.Fprintf(&b, "; zone %s serial %d\n", zone.Domain, zone.Serial)
	fmt.Fprintf(&b, "$ORIGIN %s.\n", zone.Domain)
	fmt.Fprintf(&b, "$TTL %d\n", zone.TTL)
	fmt.Fprintf(&b, "@\tIN\tSOA\t%s %s (\n", dnsZoneFQDN(zone.PrimaryNS), dnsZoneFQDN(zone.AdminEmail))
	fmt.Fprintf(&b, "\t\t%d\t; serial\n", zone.Serial)
	fmt.Fprintf(&b, "\t\t%d\t; refresh\n", zone.Refresh)
	fmt.Fprintf(&b, "\t\t%d\t; retry\n", zone.Retry)
	fmt.Fprintf(&b, "\t\t%d\t; expire\n", zone.Expire)
	fmt.Fprintf(&b, "\t\t%d\t; minimum\n", zone.Minimum)
	b.WriteString("\t)\n")
	for _, r := range sorted {
		rdata, err := dnsZoneRenderRDATA(r)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "%s\t%d\tIN\t%s\t%s\n", r.Name, r.TTL, r.Type, rdata)
	}
	return b.String(), nil
}

// dnsZoneSortName orders the apex first, then wildcard, then labels.
func dnsZoneSortName(name string) string {
	if name == "@" {
		return ""
	}
	return name
}

func dnsZoneRenderRDATA(r DNSRecordFilePayload) (string, error) {
	switch r.Type {
	case "A", "AAAA":
		return r.Value, nil
	case "CNAME", "NS":
		return dnsZoneFQDN(r.Value), nil
	case "MX":
		return fmt.Sprintf("%d %s", r.Priority, dnsZoneFQDN(r.Value)), nil
	case "SRV":
		fields := strings.Fields(r.Value)
		return fmt.Sprintf("%d %s %s", r.Priority, fields[0]+" "+fields[1], dnsZoneFQDN(fields[2])), nil
	case "TXT":
		return dnsZoneQuoteTXT(r.Value), nil
	case "CAA":
		fields := strings.Fields(r.Value)
		return fmt.Sprintf("%s %s %s", fields[0], fields[1], dnsZoneQuoteTXT(strings.Trim(fields[2], `"`))), nil
	}
	return "", fmt.Errorf("unsupported record type %q", r.Type)
}

func dnsZoneFQDN(s string) string {
	return strings.TrimSuffix(strings.TrimSpace(s), ".") + "."
}

func dnsZoneQuoteTXT(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	return `"` + v + `"`
}

func dnsZoneValidateZoneHeader(z DNSZoneFilePayload) error {
	if !dnsZoneValidHostname(z.Domain, true) {
		return fmt.Errorf("zone domain %q is not a valid hostname", z.Domain)
	}
	if z.TTL < dnsZoneMinTTL || z.TTL > dnsZoneMaxTTL {
		return fmt.Errorf("zone ttl must be between %d and %d", dnsZoneMinTTL, dnsZoneMaxTTL)
	}
	if !dnsZoneValidHostname(z.PrimaryNS, false) {
		return fmt.Errorf("soa primary ns %q is not a valid hostname", z.PrimaryNS)
	}
	if !dnsZoneValidHostname(z.AdminEmail, false) {
		return fmt.Errorf("soa admin email %q is not a valid hostname form", z.AdminEmail)
	}
	for name, v := range map[string]int{"refresh": z.Refresh, "retry": z.Retry, "expire": z.Expire, "minimum": z.Minimum} {
		if v <= 0 {
			return fmt.Errorf("soa %s must be positive", name)
		}
	}
	if z.Serial < 0 {
		return fmt.Errorf("soa serial must not be negative")
	}
	return nil
}

var dnsZoneRecordTypes = map[string]bool{
	"A": true, "AAAA": true, "CNAME": true, "MX": true,
	"TXT": true, "NS": true, "SRV": true, "CAA": true,
}

func dnsZoneValidateRecords(domain string, records []DNSRecordFilePayload) error {
	seen := map[string]bool{}
	for i := range records {
		r := &records[i]
		name, err := dnsZoneNormalizeRecordName(r.Name, domain)
		if err != nil {
			return fmt.Errorf("record %d: %w", i+1, err)
		}
		r.Name = name
		r.Type = strings.ToUpper(strings.TrimSpace(r.Type))
		if !dnsZoneRecordTypes[r.Type] {
			return fmt.Errorf("record %d (%s): unsupported type %q", i+1, name, r.Type)
		}
		if r.TTL < dnsZoneMinTTL || r.TTL > dnsZoneMaxTTL {
			return fmt.Errorf("record %d (%s %s): ttl must be between %d and %d", i+1, name, r.Type, dnsZoneMinTTL, dnsZoneMaxTTL)
		}
		key := name + "|" + r.Type + "|" + r.Value
		if seen[key] {
			return fmt.Errorf("record %d: duplicate %s %s %s", i+1, name, r.Type, r.Value)
		}
		seen[key] = true
		if err := dnsZoneValidateRecordValue(r.Type, name, r.Value, r.Priority); err != nil {
			return fmt.Errorf("record %d (%s %s): %w", i+1, name, r.Type, err)
		}
	}
	return nil
}

// dnsZoneNormalizeRecordName mirrors the control plane's record-name rules:
// "@"/empty for the apex, relative labels (underscores allowed for service
// records), a leftmost wildcard, and full names under the zone are normalized
// to the zone-relative short form.
func dnsZoneNormalizeRecordName(name, domain string) (string, error) {
	n := strings.ToLower(strings.TrimSpace(name))
	n = strings.TrimSuffix(n, ".")
	if n == "" || n == "@" {
		return "@", nil
	}
	wildcard := false
	if strings.Contains(n, "*") {
		if !strings.HasPrefix(n, "*.") || strings.Contains(strings.TrimPrefix(n, "*."), "*") {
			return "", fmt.Errorf("wildcard is only allowed as the leftmost label")
		}
		wildcard = true
		n = strings.TrimPrefix(n, "*.")
	}
	if n == domain {
		if wildcard {
			return "", fmt.Errorf("wildcard cannot cover the zone apex")
		}
		return "@", nil
	}
	if strings.HasSuffix(n, "."+domain) {
		n = strings.TrimSuffix(n, "."+domain)
	}
	if n == "" {
		return "@", nil
	}
	labels := strings.Split(n, ".")
	// Paste guard: relative multi-label names ending in a common TLD are
	// almost certainly a foreign domain (mirrors the control plane).
	if !wildcard && len(labels) > 1 && dnsZoneCommonTLDs[labels[len(labels)-1]] {
		return "", fmt.Errorf("record name must be @, a label under the zone, or a hostname under %s", domain)
	}
	for _, label := range labels {
		if label == "" {
			return "", fmt.Errorf("empty label in record name")
		}
		if len(label) > 63 {
			return "", fmt.Errorf("label %q exceeds 63 characters", label)
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
				continue
			}
			return "", fmt.Errorf("label %q contains invalid character %q", label, string(c))
		}
	}
	if wildcard {
		n = "*." + n
	}
	if len(n)+len(domain)+1 > 253 {
		return "", fmt.Errorf("record name exceeds 253 characters inside the zone")
	}
	return n, nil
}

// dnsZoneValidateRecordValue mirrors the control plane's per-type checks.
func dnsZoneValidateRecordValue(typ, name, value string, priority int) error {
	value = strings.TrimSpace(value)
	switch typ {
	case "A":
		ip := net.ParseIP(value)
		if ip == nil || ip.To4() == nil || strings.Contains(value, ":") {
			return fmt.Errorf("A record requires a valid IPv4 address")
		}
	case "AAAA":
		ip := net.ParseIP(value)
		if ip == nil || ip.To4() != nil || strings.Contains(value, "%") {
			return fmt.Errorf("AAAA record requires a valid IPv6 address")
		}
	case "CNAME":
		if name == "@" {
			return fmt.Errorf("CNAME is not allowed at the zone apex")
		}
		if !dnsZoneValidHostname(value, false) {
			return fmt.Errorf("CNAME target must be a hostname")
		}
	case "MX":
		if !dnsZoneValidHostname(value, false) {
			return fmt.Errorf("MX target must be a hostname")
		}
		if priority < 0 || priority > 65535 {
			return fmt.Errorf("MX priority must be 0-65535")
		}
	case "TXT":
		if value == "" {
			return fmt.Errorf("TXT record value is empty")
		}
		if strings.ContainsAny(value, "\n\r") {
			return fmt.Errorf("TXT record may not contain newlines")
		}
		if len(value) > 255 {
			return fmt.Errorf("TXT record exceeds 255 characters")
		}
	case "NS":
		if !dnsZoneValidHostname(value, false) {
			return fmt.Errorf("NS target must be a hostname")
		}
	case "SRV":
		fields := strings.Fields(value)
		if len(fields) != 3 {
			return fmt.Errorf("SRV value must be \"weight port target\"")
		}
		weight, err1 := strconv.Atoi(fields[0])
		port, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil || weight < 0 || weight > 65535 || port < 0 || port > 65535 {
			return fmt.Errorf("SRV weight and port must be 0-65535")
		}
		if !dnsZoneValidHostname(fields[2], false) {
			return fmt.Errorf("SRV target must be a hostname")
		}
		if priority < 0 || priority > 65535 {
			return fmt.Errorf("SRV priority must be 0-65535")
		}
	case "CAA":
		fields := strings.Fields(value)
		if len(fields) != 3 {
			return fmt.Errorf("CAA value must be \"flag tag value\"")
		}
		flag, err := strconv.Atoi(fields[0])
		if err != nil || flag < 0 || flag > 255 {
			return fmt.Errorf("CAA flag must be 0-255")
		}
		switch fields[1] {
		case "issue", "issuewild", "iodef":
		default:
			return fmt.Errorf("CAA tag must be issue, issuewild or iodef")
		}
		if fields[2] == "" {
			return fmt.Errorf("CAA value part is empty")
		}
	}
	if typ != "MX" && typ != "SRV" && priority != 0 {
		return fmt.Errorf("priority is only valid on MX and SRV records")
	}
	return nil
}

// dnsZoneCommonTLDs backs the record-name paste guard.
var dnsZoneCommonTLDs = map[string]bool{
	"com": true, "net": true, "org": true, "io": true, "dev": true, "app": true,
	"co": true, "uk": true, "de": true, "fr": true, "nl": true, "eu": true,
	"us": true, "ca": true, "au": true, "xyz": true, "info": true, "biz": true,
	"online": true, "site": true, "store": true, "tech": true, "cloud": true,
	"me": true, "tv": true, "ai": true, "test": true,
}

// dnsZoneValidHostname checks RFC-1123-ish hostnames; requireTwoLabels is set
// for zone apexes. A single trailing dot is tolerated and ignored.
func dnsZoneValidHostname(s string, requireTwoLabels bool) bool {
	h := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
	if h == "" || len(h) > 253 {
		return false
	}
	labels := strings.Split(h, ".")
	if requireTwoLabels && len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 {
			return false
		}
		if l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
				continue
			}
			return false
		}
	}
	return true
}
