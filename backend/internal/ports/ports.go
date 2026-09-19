// Package ports defines the private backend port ranges for proxy-mode web
// servers (nginx edge + Apache/OpenLiteSpeed backend). Ranges are configurable
// via environment so deployments can adapt them without code changes; they
// must be identical on the control plane (allocation) and the agent (verify).
package ports

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Range is an inclusive port range.
type Range struct {
	Min int
	Max int
}

// Contains reports whether p lies within the range.
func (r Range) Contains(p int) bool { return p >= r.Min && p <= r.Max }

// Defaults — never bind public :80/:443 here; nginx owns those. Ranges are
// disjoint by design: backend ports (apache/OLS) and app ports (node/python/
// go processes) are allocated independently and must never collide.
var (
	defaultApache = Range{Min: 6600, Max: 6999}
	defaultOLS    = Range{Min: 7100, Max: 7499}
	defaultApp    = Range{Min: 8100, Max: 8499}
)

// Apache returns the Apache backend range (EPICPANEL_APACHE_PORT_RANGE "min-max").
func Apache() Range { return parse("EPICPANEL_APACHE_PORT_RANGE", defaultApache) }

// OLS returns the OpenLiteSpeed backend range (EPICPANEL_OLS_PORT_RANGE "min-max").
func OLS() Range { return parse("EPICPANEL_OLS_PORT_RANGE", defaultOLS) }

// App returns the app-process range (EPICPANEL_APP_PORT_RANGE "min-max") —
// the loopback ports nginx proxies to for node/python/go websites.
func App() Range { return parse("EPICPANEL_APP_PORT_RANGE", defaultApp) }

func parse(env string, fallback Range) Range {
	v := strings.TrimSpace(os.Getenv(env))
	if v == "" {
		return fallback
	}
	parts := strings.SplitN(v, "-", 2)
	if len(parts) != 2 {
		return fallback
	}
	min, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	max, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil || min < 1024 || max > 65535 || min > max {
		return fallback
	}
	return Range{Min: min, Max: max}
}

// Allocates picks the lowest port in r not present in used. Returns 0 when
// the range is exhausted.
func Allocates(r Range, used map[int]bool) int {
	for p := r.Min; p <= r.Max; p++ {
		if !used[p] {
			return p
		}
	}
	return 0
}

func (r Range) String() string { return fmt.Sprintf("%d-%d", r.Min, r.Max) }
