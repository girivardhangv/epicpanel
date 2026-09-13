package websites

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PHPSettingDef describes one editable php.ini directive surfaced by the
// per-site PHP settings editor (cPanel MultiPHP INI Editor equivalent).
type PHPSettingDef struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Type    string   `json:"type"` // size | int | bool | string | select
	Default string   `json:"default"`
	Min     int      `json:"min,omitempty"`
	Max     int      `json:"max,omitempty"`
	Options []string `json:"options,omitempty"`
	Help    string   `json:"help,omitempty"`
}

// phpSettingCatalog is the fixed allowlist of directives a site may set.
// Anything not listed here is rejected before it can reach php_admin_value.
var phpSettingCatalog = []PHPSettingDef{
	{Key: "memory_limit", Label: "memory_limit", Type: "size", Default: "128M", Help: "Maximum memory a single PHP request may allocate"},
	{Key: "max_execution_time", Label: "max_execution_time", Type: "int", Default: "30", Min: 0, Max: 3600, Help: "Seconds a script may run (0 = unlimited)"},
	{Key: "max_input_time", Label: "max_input_time", Type: "int", Default: "60", Min: -1, Max: 3600, Help: "Seconds allowed to parse request input"},
	{Key: "max_input_vars", Label: "max_input_vars", Type: "int", Default: "1000", Min: 1, Max: 100000, Help: "Maximum number of request variables"},
	{Key: "post_max_size", Label: "post_max_size", Type: "size", Default: "64M", Help: "Maximum size of POST data"},
	{Key: "upload_max_filesize", Label: "upload_max_filesize", Type: "size", Default: "64M", Help: "Maximum size of an uploaded file"},
	{Key: "allow_url_fopen", Label: "allow_url_fopen", Type: "bool", Default: "1", Help: "Allow URL-aware fopen wrappers"},
	{Key: "display_errors", Label: "display_errors", Type: "bool", Default: "0", Help: "Show PHP errors in output (keep off in production)"},
	{Key: "error_reporting", Label: "error_reporting", Type: "string", Default: "E_ALL & ~E_DEPRECATED & ~E_STRICT", Help: "PHP error_reporting bitmask expression"},
	{Key: "date.timezone", Label: "date.timezone", Type: "string", Default: "UTC", Help: "Default timezone (e.g. Asia/Kolkata)"},
	{Key: "short_open_tag", Label: "short_open_tag", Type: "bool", Default: "0", Help: "Allow <? ... ?> short tags"},
	{Key: "disable_functions", Label: "disable_functions", Type: "string", Default: "", Help: "Comma-separated functions to disable (empty = allow all)"},
	{Key: "session.save_path", Label: "session.save_path", Type: "string", Default: "", Help: "Session storage directory (empty = per-site default)"},
	{Key: "upload_tmp_dir", Label: "upload_tmp_dir", Type: "string", Default: "", Help: "Upload scratch directory (empty = per-site default)"},
	{Key: "opcache.enable", Label: "opcache.enable", Type: "bool", Default: "1", Help: "Enable the OPcache bytecode cache"},
	{Key: "opcache.memory_consumption", Label: "opcache.memory_consumption", Type: "int", Default: "64", Min: 8, Max: 1024, Help: "OPcache shared memory size (MB)"},
	{Key: "opcache.max_accelerated_files", Label: "opcache.max_accelerated_files", Type: "int", Default: "4000", Min: 200, Max: 200000, Help: "Maximum number of cached PHP files"},
	{Key: "opcache.validate_timestamps", Label: "opcache.validate_timestamps", Type: "bool", Default: "1", Help: "Revalidate scripts when they change"},
	{Key: "opcache.revalidate_freq", Label: "opcache.revalidate_freq", Type: "int", Default: "2", Min: 0, Max: 3600, Help: "Seconds between script timestamp checks"},
}

var phpSettingByKey = func() map[string]PHPSettingDef {
	m := make(map[string]PHPSettingDef, len(phpSettingCatalog))
	for _, d := range phpSettingCatalog {
		m[d.Key] = d
	}
	return m
}()

var (
	phpSizeRe = regexp.MustCompile(`^[0-9]+[KMG]?$`)
	phpTZRe   = regexp.MustCompile(`^[A-Za-z0-9_+\-]+(/[A-Za-z0-9_+\-]+)*$`)
	phpErrRe  = regexp.MustCompile(`^[A-Za-z0-9_&|~() ]+$`)
	phpFnRe   = regexp.MustCompile(`^[A-Za-z0-9_, ]*$`)
	phpPathRe = regexp.MustCompile(`^[A-Za-z0-9_./\-]*$`)
)

// PHPSettingsCatalog returns the editable directive definitions.
func PHPSettingsCatalog() []PHPSettingDef { return phpSettingCatalog }

// NormalizePHPSettings validates and canonicalizes a settings map: unknown
// keys and malformed values are rejected, empty values mean "unset".
func NormalizePHPSettings(in map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range in {
		def, ok := phpSettingByKey[k]
		if !ok {
			return nil, fmt.Errorf("unsupported PHP setting %q", k)
		}
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		norm, err := normalizePHPValue(def, v)
		if err != nil {
			return nil, err
		}
		out[k] = norm
	}
	return out, nil
}

func normalizePHPValue(def PHPSettingDef, v string) (string, error) {
	switch def.Type {
	case "size":
		if !phpSizeRe.MatchString(strings.ToUpper(v)) {
			return "", fmt.Errorf("%s must be a size like 128M or 1048576", def.Key)
		}
		return strings.ToUpper(v), nil
	case "int":
		n, err := strconv.Atoi(v)
		if err != nil {
			return "", fmt.Errorf("%s must be an integer", def.Key)
		}
		if n < def.Min || n > def.Max {
			return "", fmt.Errorf("%s must be between %d and %d", def.Key, def.Min, def.Max)
		}
		return strconv.Itoa(n), nil
	case "bool":
		switch strings.ToLower(v) {
		case "1", "on", "true", "yes":
			return "On", nil
		case "0", "off", "false", "no":
			return "Off", nil
		}
		return "", fmt.Errorf("%s must be On or Off", def.Key)
	case "select":
		for _, o := range def.Options {
			if strings.EqualFold(o, v) {
				return o, nil
			}
		}
		return "", fmt.Errorf("%s has an unsupported value %q", def.Key, v)
	default:
		switch def.Key {
		case "date.timezone":
			if !phpTZRe.MatchString(v) {
				return "", fmt.Errorf("date.timezone is not a valid timezone")
			}
		case "error_reporting":
			if !phpErrRe.MatchString(v) {
				return "", fmt.Errorf("error_reporting contains unsupported characters")
			}
		case "disable_functions":
			if !phpFnRe.MatchString(v) {
				return "", fmt.Errorf("disable_functions contains unsupported characters")
			}
		case "session.save_path", "upload_tmp_dir":
			if !phpPathRe.MatchString(v) {
				return "", fmt.Errorf("%s must be a relative-safe path", def.Key)
			}
		}
		if len(v) > 512 {
			return "", fmt.Errorf("%s is too long", def.Key)
		}
		return v, nil
	}
}

// PHPSettingsStore persists per-site PHP INI overrides.
type PHPSettingsStore struct {
	Pool *pgxpool.Pool
}

// Get returns the site's settings; an absent row is an empty map, not an error.
func (s *PHPSettingsStore) Get(ctx context.Context, websiteID uuid.UUID) (map[string]string, error) {
	var raw []byte
	err := s.Pool.QueryRow(ctx, `SELECT settings FROM website_php_settings WHERE website_id = $1`, websiteID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Set validates and upserts the site's settings, returning the normalized map.
func (s *PHPSettingsStore) Set(ctx context.Context, websiteID uuid.UUID, settings map[string]string) (map[string]string, error) {
	norm, err := NormalizePHPSettings(settings)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(norm)
	if err != nil {
		return nil, err
	}
	_, err = s.Pool.Exec(ctx, `
		INSERT INTO website_php_settings (website_id, settings, updated_at)
		VALUES ($1, $2::jsonb, now())
		ON CONFLICT (website_id) DO UPDATE SET settings = $2::jsonb, updated_at = now()
	`, websiteID, raw)
	if err != nil {
		return nil, err
	}
	return norm, nil
}

// PHPSettingsView is the API response for the settings editor.
type PHPSettingsView struct {
	WebsiteID uuid.UUID         `json:"website_id"`
	Settings  map[string]string `json:"settings"`
	Catalog   []PHPSettingDef   `json:"catalog"`
	UpdatedAt time.Time         `json:"updated_at,omitempty"`
}

// requestTimeoutFor derives the FPM request_terminate_timeout from a site's
// max_execution_time override (plus 60s grace). A missing value keeps the
// 300s default; "0" means unlimited and maps to 0 (FPM treats 0 as no limit).
func requestTimeoutFor(settings map[string]string) int {
	raw, ok := settings["max_execution_time"]
	if !ok {
		return 300
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 300
	}
	if n <= 0 {
		return 0
	}
	return n + 60
}