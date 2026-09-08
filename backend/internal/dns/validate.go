// Package dns manages DNS zones and records as desired state. Zones live in
// the panel and are published to the server's authoritative nameserver by an
// agent through the sync_dns_zone job (see PublishPayload).
package dns

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

const (
	// MaxRecordsPerZone caps the records table per zone.
	MaxRecordsPerZone = 100
	minTTL            = 60
	maxTTL            = 604800
)

// NormalizeHostname lowercases, trims space and strips one trailing dot.
// Returns the normalized form; call ValidateHostname for the checks.
func NormalizeHostname(in string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(in)), ".")
}

// ValidateHostname checks RFC-1123-ish hostnames: 1-253 chars, labels of
// 1-63 alnum/hyphen chars not starting or ending with a hyphen, at least two
// labels (mirrors the domains table CHECK). The stored form never carries a
// trailing dot.
func ValidateHostname(in string) error {
	h := NormalizeHostname(in)
	if h == "" {
		return fmt.Errorf("hostname is empty")
	}
	if len(h) > 253 {
		return fmt.Errorf("hostname exceeds 253 characters")
	}
	labels := strings.Split(h, ".")
	if len(labels) < 2 {
		return fmt.Errorf("hostname must have at least two labels")
	}
	for _, l := range labels {
		if l == "" {
			return fmt.Errorf("hostname has an empty label")
		}
		if len(l) > 63 {
			return fmt.Errorf("hostname label %q exceeds 63 characters", l)
		}
		if strings.HasPrefix(l, "-") || strings.HasSuffix(l, "-") {
			return fmt.Errorf("hostname label %q may not start or end with a hyphen", l)
		}
		for _, c := range l {
			if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' {
				continue
			}
			return fmt.Errorf("hostname label %q contains invalid character %q", l, c)
		}
	}
	return nil
}

// ValidateZone checks the zone apex hostname (>= two labels; a panel zone is
// always a real internet domain).
func ValidateZone(domain string) error {
	if err := ValidateHostname(domain); err != nil {
		return fmt.Errorf("zone domain is not a valid hostname: %w", err)
	}
	return nil
}

// ValidateRecord checks a record per DNS type:
//   - A: strict dotted-quad IPv4
//   - AAAA: strict IPv6 (no zone index)
//   - CNAME: hostname; the zone apex ("@") is not allowed
//   - MX: hostname target + priority 0-65535
//   - TXT: <=255 chars, no newlines
//   - NS: hostname
//   - SRV: "weight port target" with valid target
//   - CAA: "flag tag value" with flag 0-255, tag issue|issuewild|iodef
func ValidateRecord(typ, name, value string, ttl, priority int) error {
	if ttl < minTTL || ttl > maxTTL {
		return fmt.Errorf("ttl must be between %d and %d", minTTL, maxTTL)
	}
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
		if err := ValidateHostname(value); err != nil {
			return fmt.Errorf("CNAME target must be a hostname: %w", err)
		}
	case "MX":
		if err := ValidateHostname(value); err != nil {
			return fmt.Errorf("MX target must be a hostname: %w", err)
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
		if err := ValidateHostname(value); err != nil {
			return fmt.Errorf("NS target must be a hostname: %w", err)
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
		if err := ValidateHostname(fields[2]); err != nil {
			return fmt.Errorf("SRV target must be a hostname: %w", err)
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
	default:
		return fmt.Errorf("unsupported record type %q", typ)
	}
	// Priority is only meaningful for MX and SRV; reject it elsewhere.
	if typ != "MX" && typ != "SRV" && priority != 0 {
		return fmt.Errorf("priority is only valid on MX and SRV records")
	}
	return nil
}

// ValidateRecordName checks a record's owner name inside a zone: "@" for the
// apex, an FQDN under the zone (stored as its relative short form), or a
// relative name of DNS labels. Underscore service labels (_dmarc, _sip) and
// a leading wildcard are accepted per RFC 1035/4592 practice. A relative
// multi-label name whose rightmost label is a common TLD is rejected (it is
// almost certainly a paste of a different domain). Returns the normalized
// short form.
func ValidateRecordName(name, zoneDomain string) (string, error) {
	n := NormalizeHostname(name)
	if n == "@" || n == "" {
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
	if n == zoneDomain {
		if wildcard {
			return "", fmt.Errorf("wildcard cannot cover the zone apex; use *.<zone>")
		}
		return "@", nil
	}
	if strings.HasSuffix(n, "."+zoneDomain) {
		n = strings.TrimSuffix(n, "."+zoneDomain)
	}
	labels := strings.Split(n, ".")
	// Paste guard: relative names that end in a TLD are foreign domains.
	if !wildcard && len(labels) > 1 && commonTLDs[labels[len(labels)-1]] {
		return "", fmt.Errorf("record name must be @, a label under the zone, or a hostname under %s", zoneDomain)
	}
	for _, label := range labels {
		if label == "" {
			return "", fmt.Errorf("record name has an empty label")
		}
		if len(label) > 63 {
			return "", fmt.Errorf("record name label %q exceeds 63 characters", label)
		}
		for i := 0; i < len(label); i++ {
			if !isRecordLabelChar(label[i]) {
				return "", fmt.Errorf("record name label %q contains invalid character %q", label, string(label[i]))
			}
		}
	}
	if len(n)+len(zoneDomain)+1 > 253 {
		return "", fmt.Errorf("record name exceeds 253 characters inside the zone")
	}
	if wildcard {
		return "*." + n, nil
	}
	return n, nil
}

func isRecordLabelChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_'
}

// commonTLDs backs the paste guard in ValidateRecordName.
var commonTLDs = map[string]bool{
	"com": true, "net": true, "org": true, "io": true, "dev": true, "app": true,
	"co": true, "uk": true, "de": true, "fr": true, "nl": true, "eu": true,
	"us": true, "ca": true, "au": true, "xyz": true, "info": true, "biz": true,
	"online": true, "site": true, "store": true, "tech": true, "cloud": true,
	"me": true, "tv": true, "ai": true,
}
