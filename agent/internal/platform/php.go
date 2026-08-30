package platform

import "context"

// PHP runtime handler styles.
const (
	HandlerFPM     = "fpm"
	HandlerFastCGI = "fastcgi"
)

// PHPVersion describes one discovered runtime.
type PHPVersion struct {
	Version     string `json:"version"`
	BinaryPath  string `json:"binary_path"`
	ConfigPath  string `json:"config_path,omitempty"`
	HandlerType string `json:"handler_type"` // fpm | fastcgi
	Status      string `json:"status"`       // available | running | stopped
}

// PHPPoolRequest configures the runtime for one website.
type PHPPoolRequest struct {
	SiteSlug string            // filesystem-safe slug
	Version  string            // e.g. "8.4"
	User     string            // per-site OS user the pool runs as (Linux; "" = shared site user)
	Settings map[string]string // validated php_admin_value subset
	Remove   bool              // tear the pool down instead
}

// PHPPoolResult returns the FastCGI address nginx must use.
type PHPPoolResult struct {
	Address string // unix:/run/php/epicpanel-<slug>.sock | 127.0.0.1:<port>
}

// PHPOps is the typed PHP runtime surface. Implementations live in
// php_linux.go / php_windows.go.
type PHPOps interface {
	Versions(ctx context.Context) ([]PHPVersion, error)
	EnsurePool(ctx context.Context, req PHPPoolRequest) (*PHPPoolResult, error)
	RemovePool(ctx context.Context, req PHPPoolRequest) error
}

// PHPValueAllowlist is the validated php_admin_value subset the agent accepts
// in pools. Keys map to value shapes; anything else is rejected before it can
// reach a configuration file.
var PHPValueAllowlist = map[string]string{
	"memory_limit":        "size",
	"upload_max_filesize": "size",
	"post_max_size":       "size",
	"max_execution_time":  "seconds",
	"max_input_time":      "seconds",
}

// ValidPHPSetting reports whether a key/value pair is safe to render.
func ValidPHPSetting(key, value string) bool {
	kind, ok := PHPValueAllowlist[key]
	if !ok {
		return false
	}
	if value == "" || len(value) > 32 {
		return false
	}
	digits := true
	for _, r := range value {
		if r < '0' || r > '9' {
			digits = false
			break
		}
	}
	if digits && value != "0" {
		return true
	}
	if kind == "size" {
		suffixes := []string{"K", "M", "G"}
		for _, sfx := range suffixes {
			if rest := trimSuffix(value, sfx); rest != value && digitsOnly(rest) {
				return rest != "0"
			}
		}
	}
	if kind == "seconds" && digits {
		return value != "0"
	}
	return false
}

func trimSuffix(s, suffix string) string {
	if len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix {
		return s[:len(s)-len(suffix)]
	}
	return s
}

func digitsOnly(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// phpSettingsBlock renders validated settings as php_admin_value lines.
func phpSettingsBlock(settings map[string]string) string {
	out := ""
	for _, key := range sortedKeys(settings) {
		v := settings[key]
		if !ValidPHPSetting(key, v) {
			continue // silently skip anything that failed upstream validation
		}
		out += "php_admin_value[" + key + "] = " + v + "\n"
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// insertion sort: tiny maps, no imports needed
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
