package httpapi

import (
	"net"
	"net/http"
	"strings"
	"sync"
)

// Trusted-proxy aware client IP resolution (audit S11): X-Forwarded-For is
// only honored when the direct peer is a configured trusted proxy, and the
// rightmost non-trusted hop wins (leftmost entries are client-controlled).
var (
	trustedMu       sync.RWMutex
	trustedProxies  []*net.IPNet
	trustedLoopback = true // loopback peers are trusted by default (local dev proxies)
)

// ConfigureTrustedProxies parses CIDRs (or bare IPs) whose connections may
// set X-Forwarded-For. Called once at startup from EPICPANEL_TRUSTED_PROXIES.
// "loopback=false" disables the built-in loopback trust.
func ConfigureTrustedProxies(cidrs []string) {
	trustedMu.Lock()
	defer trustedMu.Unlock()
	trustedProxies = trustedProxies[:0]
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if c == "loopback=false" {
			trustedLoopback = false
			continue
		}
		if !strings.Contains(c, "/") {
			c += "/32"
		}
		if _, ipnet, err := net.ParseCIDR(c); err == nil {
			trustedProxies = append(trustedProxies, ipnet)
		}
	}
}

func isTrustedPeer(ip net.IP) bool {
	trustedMu.RLock()
	defer trustedMu.RUnlock()
	if ip.IsLoopback() && trustedLoopback {
		return true
	}
	for _, n := range trustedProxies {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ClientIP resolves the client IP for rate limiting and audit records.
func ClientIP(r *http.Request) string {
	host := remoteHost(r)
	peer := net.ParseIP(host)
	if peer == nil || !isTrustedPeer(peer) {
		return host
	}
	fwd := r.Header.Get("X-Forwarded-For")
	if fwd == "" {
		return host
	}
	parts := strings.Split(fwd, ",")
	// Rightmost non-trusted hop is the client; earlier entries are spoofable.
	for i := len(parts) - 1; i >= 0; i-- {
		cand := strings.TrimSpace(parts[i])
		ip := net.ParseIP(cand)
		if ip == nil {
			return host
		}
		if !isTrustedPeer(ip) {
			return cand
		}
	}
	return host
}

func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
