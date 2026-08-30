package software

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// resolvers fetch the current latest release from official upstream metadata
// at install time. No provider hardcodes a version, so future upstream
// releases are installed automatically with zero code changes.

const (
	upstreamNginx      = "https://nginx.org/download/"
	upstreamPHP        = "https://windows.php.net/downloads/releases/"
	upstreamPHPJson    = "https://www.php.net/releases/index.php"
	upstreamPHPDist    = "https://www.php.net/distributions"
	upstreamNodeIdx    = "https://nodejs.org/dist/index.json"
	upstreamNodeDist   = "https://nodejs.org/dist"
	upstreamAdopt      = "https://api.adoptium.net/v3"
	upstreamMariaREST  = "https://downloads.mariadb.org/rest-api/mariadb/"
	upstreamMariaArc   = "https://archive.mariadb.org"
	upstreamRedisRel   = "https://download.redis.io/releases/"
	upstreamApacheDir  = "https://dlcdn.apache.org/httpd/"
)

var (
	reNginxVersion      = regexp.MustCompile(`nginx-(\d+\.\d+\.\d+)\.zip`)
	reNginxTarGz        = regexp.MustCompile(`nginx-(\d+\.\d+\.\d+)\.tar\.gz`)
	rePHPVersion        = regexp.MustCompile(`php-(\d+\.\d+\.\d+)-Win32-vs\d+-x64\.zip`)
	reMariaVersion      = regexp.MustCompile(`mariadb-(\d+\.\d+\.\d+)-winx64\.zip`)
	reMariaBinVersion   = regexp.MustCompile(`mariadb-(\d+\.\d+\.\d+)-linux-systemd-x86_64\.tar\.gz`)
	reRedisVersion      = regexp.MustCompile(`redis-(\d+\.\d+\.\d+)\.tar\.gz`)
	reApacheVersion     = regexp.MustCompile(`httpd-(\d+\.\d+\.\d+)\.tar\.gz`)
)

// httpGet fetches a URL (bounded to 16 MiB) honoring ctx cancellation.
func httpGet(ctx context.Context, url string) ([]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "EpicPanelAgent/0.2.0 (software resolver)")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 16<<20))
}

// ---------------------------------------------------------------------------
// nginx (official prebuilt Windows zip; Linux source tarball → compiled)
// ---------------------------------------------------------------------------

func resolveNginx(ctx context.Context, os OSInfo) (*Release, error) {
	body, err := httpGet(ctx, upstreamNginx)
	if err != nil {
		return nil, err
	}
	if os.Family == "windows" {
		v, ok := maxDotted(reNginxVersion, body)
		if !ok {
			return nil, fmt.Errorf("no nginx release found on %s", upstreamNginx)
		}
		return &Release{
			Version: v,
			URL:     upstreamNginx + "nginx-" + v + ".zip",
			Format:  "zip",
			Prefix:  "nginx-" + v,
		}, nil
	}
	// Linux source tarball
	v, ok := maxDotted(reNginxTarGz, body)
	if !ok {
		return nil, fmt.Errorf("no nginx source tarball found on %s", upstreamNginx)
	}
	return &Release{
		Version: v,
		URL:     upstreamNginx + "nginx-" + v + ".tar.gz",
		Format:  "tar.gz",
		Prefix:  "nginx-" + v,
	}, nil
}

// ---------------------------------------------------------------------------
// PHP (official prebuilt Windows thread-safe build; Linux source → compiled)
// ---------------------------------------------------------------------------

func resolvePHP(ctx context.Context, os OSInfo) (*Release, error) {
	if os.Family == "windows" {
		body, err := httpGet(ctx, upstreamPHP)
		if err != nil {
			return nil, err
		}
		file, v, ok := maxFileAndVersion(rePHPVersion, body)
		if !ok {
			return nil, fmt.Errorf("no PHP release found on %s", upstreamPHP)
		}
		return &Release{
			Version: v,
			URL:     upstreamPHP + file,
			Format:  "zip",
		}, nil
	}
	// Linux source tarball from php.net; version + sha256 from the JSON API.
	return resolvePHPSource(ctx)
}

type phpJSONRelease struct {
	Version string `json:"version"`
	Source  []struct {
		Filename string `json:"filename"`
		SHA256   string `json:"sha256"`
	} `json:"source"`
}

// resolvePHPSource fetches the latest stable PHP release info from php.net's
// JSON API and returns the source tarball release. The API returns a map of
// major version -> release; we take the highest major's newest release.
func resolvePHPSource(ctx context.Context) (*Release, error) {
	body, err := httpGet(ctx, upstreamPHPJson+"?json&max=1")
	if err != nil {
		return nil, err
	}
	var payload map[string]phpJSONRelease
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse php json: %w", err)
	}
	bestMajor := ""
	var best phpJSONRelease
	for major, rel := range payload {
		if rel.Version == "" {
			continue
		}
		if bestMajor == "" || compareDotted(major, bestMajor) > 0 {
			bestMajor, best = major, rel
		}
	}
	if best.Version == "" {
		return nil, fmt.Errorf("no stable PHP source release found")
	}
	rel := &Release{Version: best.Version}
	for _, src := range best.Source {
		if src.Filename == "php-"+best.Version+".tar.gz" {
			rel.URL = upstreamPHPDist + "/" + src.Filename
			rel.SHA256 = src.SHA256
			rel.Format = "tar.gz"
			rel.Prefix = "php-" + best.Version
			return rel, nil
		}
	}
	return nil, fmt.Errorf("php source tarball not found for %s", best.Version)
}

// ---------------------------------------------------------------------------
// Node.js (official prebuilt for Windows + Linux)
// ---------------------------------------------------------------------------

type nodeIndexEntry struct {
	Version string `json:"version"`
	LTS     any    `json:"lts"` // bool false, or a string codename for LTS lines
}

func resolveNode(ctx context.Context, os OSInfo) (*Release, error) {
	body, err := httpGet(ctx, upstreamNodeIdx)
	if err != nil {
		return nil, err
	}
	var entries []nodeIndexEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, fmt.Errorf("parse node index: %w", err)
	}
	// Prefer the latest LTS line; fall back to the newest release.
	var chosen *nodeIndexEntry
	for i := range entries {
		if isLTS(entries[i].LTS) {
			chosen = &entries[i]
			break
		}
	}
	if chosen == nil && len(entries) > 0 {
		chosen = &entries[0]
	}
	if chosen == nil {
		return nil, fmt.Errorf("no node releases found")
	}
	v := strings.TrimPrefix(chosen.Version, "v")
	rel := &Release{Version: v}
	// Node's archive filenames keep the "v" prefix: node-v24.20.0-win-x64.zip.
	dist := chosen.Version // e.g. "v24.20.0"
	if os.Family == "windows" {
		rel.URL = fmt.Sprintf("%s/%s/node-%s-win-x64.zip", upstreamNodeDist, dist, dist)
		rel.Format = "zip"
		rel.Prefix = "node-" + dist + "-win-x64"
	} else {
		rel.URL = fmt.Sprintf("%s/%s/node-%s-linux-x64.tar.xz", upstreamNodeDist, dist, dist)
		rel.Format = "tar.xz"
		rel.Prefix = "node-" + dist + "-linux-x64"
		rel.Bin = "bin/node"
	}
	// Best-effort checksum from the official SHASUMS256.txt.
	if sum, err := nodeChecksum(ctx, chosen.Version, rel.URL); err == nil {
		rel.SHA256 = sum
	}
	return rel, nil
}

func isLTS(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t != "" && strings.ToLower(t) != "false"
	}
	return false
}

func nodeChecksum(ctx context.Context, distVersion, archiveURL string) (string, error) {
	body, err := httpGet(ctx, upstreamNodeDist+"/"+distVersion+"/SHASUMS256.txt")
	if err != nil {
		return "", err
	}
	// The archive URL ends with the same filename used in SHASUMS256.txt
	// (e.g. node-v24.20.0-win-x64.zip).
	parts := strings.Split(archiveURL, "/")
	fileName := parts[len(parts)-1]
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == fileName {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("checksum for %s not found", fileName)
}

// ---------------------------------------------------------------------------
// Java (Adoptium Temurin official prebuilt JRE)
// ---------------------------------------------------------------------------

type adoptiumRelease struct {
	ReleaseName string `json:"release_name"`
	Version     struct {
		Semver string `json:"semver"`
	} `json:"version"`
	Binary struct {
		Package struct {
			Link     string `json:"link"`
			Checksum string `json:"checksum"`
		} `json:"package"`
	} `json:"binary"`
}

func resolveJava(ctx context.Context, os OSInfo) (*Release, error) {
	// Resolve the current LTS major, so major bumps are picked up automatically.
	major, err := javaLTSCurrent(ctx)
	if err != nil {
		return nil, err
	}
	osName := "windows"
	if os.Family != "windows" {
		osName = "linux"
	}
	url := fmt.Sprintf("%s/assets/latest/%d/hotspot?architecture=x64&heap_size=normal&image_type=jre&os=%s&vendor=eclipse",
		upstreamAdopt, major, osName)
	body, err := httpGet(ctx, url)
	if err != nil {
		return nil, err
	}
	var assets []adoptiumRelease
	if err := json.Unmarshal(body, &assets); err != nil {
		return nil, fmt.Errorf("parse adoptium assets: %w", err)
	}
	if len(assets) == 0 {
		return nil, fmt.Errorf("no java release found for LTS %d on %s", major, osName)
	}
	bin := assets[0]
	link := bin.Binary.Package.Link
	version := bin.Version.Semver
	if version == "" {
		version = bin.ReleaseName // fallback: "jdk-25.0.4.1+1"
	}
	rel := &Release{
		Version: version,
		URL:     link,
		SHA256:  bin.Binary.Package.Checksum,
	}
	switch {
	case strings.HasSuffix(link, ".zip"):
		rel.Format = "zip"
	case strings.HasSuffix(link, ".tar.gz"):
		rel.Format = "tar.gz"
	default:
		return nil, fmt.Errorf("unsupported java archive: %s", link)
	}
	return rel, nil
}

func javaLTSCurrent(ctx context.Context) (int, error) {
	body, err := httpGet(ctx, upstreamAdopt+"/info/available_releases")
	if err != nil {
		return 0, err
	}
	var info struct {
		AvailableLTSReleases []int `json:"available_lts_releases"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return 0, fmt.Errorf("parse adoptium releases: %w", err)
	}
	max := 0
	for _, m := range info.AvailableLTSReleases {
		if m > max {
			max = m
		}
	}
	if max == 0 {
		return 0, fmt.Errorf("no adoptium LTS releases found")
	}
	return max, nil
}

// ---------------------------------------------------------------------------
// MariaDB (official prebuilt Windows zip + Linux bintar tarball)
// ---------------------------------------------------------------------------

type mariadbMajor struct {
	ReleaseID string `json:"release_id"`
	Status    string `json:"release_status"`
}

func resolveMariaDB(ctx context.Context, os OSInfo) (*Release, error) {
	// 1. Latest stable series from the official REST API (e.g. 12.3).
	major, err := mariadbLatestStableMajor(ctx)
	if err != nil {
		return nil, err
	}
	// 2. Latest patch inside that series from the archive listing.
	if os.Family == "windows" {
		dirURL := fmt.Sprintf("%s/mariadb-%s/winx64-packages/", upstreamMariaArc, major)
		body, err := httpGet(ctx, dirURL)
		if err != nil {
			return nil, err
		}
		v, ok := maxDotted(reMariaVersion, body)
		if !ok {
			return nil, fmt.Errorf("no mariadb windows build found in %s", dirURL)
		}
		return &Release{
			Version: v,
			URL:     dirURL + "mariadb-" + v + "-winx64.zip",
			Format:  "zip",
			Prefix:  "mariadb-" + v + "-winx64",
			Bin:     "bin/mariadb",
		}, nil
	}
	dirURL := fmt.Sprintf("%s/mariadb-%s/bintar-linux-systemd-x86_64/", upstreamMariaArc, major)
	body, err := httpGet(ctx, dirURL)
	if err != nil {
		return nil, err
	}
	v, ok := maxDotted(reMariaBinVersion, body)
	if !ok {
		return nil, fmt.Errorf("no mariadb linux build found in %s", dirURL)
	}
	rel := &Release{
		Version: v,
		URL:     dirURL + "mariadb-" + v + "-linux-systemd-x86_64.tar.gz",
		Format:  "tar.gz",
		Prefix:  "mariadb-" + v + "-linux-systemd-x86_64",
		Bin:     "bin/mariadb",
	}
	// Best-effort checksum from the official sha256sums.txt.
	if sum, err := mariadbChecksum(ctx, dirURL, "mariadb-"+v+"-linux-systemd-x86_64.tar.gz"); err == nil {
		rel.SHA256 = sum
	}
	return rel, nil
}

func mariadbLatestStableMajor(ctx context.Context) (string, error) {
	body, err := httpGet(ctx, upstreamMariaREST)
	if err != nil {
		return "", err
	}
	var payload struct {
		MajorReleases []mariadbMajor `json:"major_releases"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("parse mariadb rest api: %w", err)
	}
	best := ""
	for _, m := range payload.MajorReleases {
		if m.Status != "Stable" {
			continue
		}
		if best == "" || compareDotted(m.ReleaseID, best) > 0 {
			best = m.ReleaseID
		}
	}
	if best == "" {
		return "", fmt.Errorf("no stable mariadb series found")
	}
	return best, nil
}

func mariadbChecksum(ctx context.Context, dirURL, fileName string) (string, error) {
	body, err := httpGet(ctx, dirURL+"sha256sums.txt")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == fileName {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("checksum for %s not found", fileName)
}

// ---------------------------------------------------------------------------
// Redis (Linux source tarball → compiled; no official Windows prebuilt)
// ---------------------------------------------------------------------------

func resolveRedis(ctx context.Context, os OSInfo) (*Release, error) {
	if os.Family == "windows" {
		// Windows: no official build; the package manager (winget) is the path.
		return nil, ErrNotApplicable
	}
	body, err := httpGet(ctx, upstreamRedisRel)
	if err != nil {
		return nil, err
	}
	v, ok := maxDotted(reRedisVersion, body)
	if !ok {
		return nil, fmt.Errorf("no redis release found on %s", upstreamRedisRel)
	}
	return &Release{
		Version: v,
		URL:     upstreamRedisRel + "redis-" + v + ".tar.gz",
		Format:  "tar.gz",
		Prefix:  "redis-" + v,
	}, nil
}

// ---------------------------------------------------------------------------
// Apache (Linux source tarball → compiled; Windows has no official prebuilt)
// ---------------------------------------------------------------------------

func resolveApache(ctx context.Context, os OSInfo) (*Release, error) {
	if os.Family == "windows" {
		return nil, ErrNotApplicable
	}
	body, err := httpGet(ctx, upstreamApacheDir)
	if err != nil {
		return nil, err
	}
	v, ok := maxDotted(reApacheVersion, body)
	if !ok {
		return nil, fmt.Errorf("no apache release found on %s", upstreamApacheDir)
	}
	rel := &Release{
		Version: v,
		URL:     upstreamApacheDir + "httpd-" + v + ".tar.gz",
		Format:  "tar.gz",
		Prefix:  "httpd-" + v,
	}
	// Best-effort checksum from the adjacent .sha256 file.
	if sum, err := httpGet(ctx, upstreamApacheDir+"httpd-"+v+".tar.gz.sha256"); err == nil {
		rel.SHA256 = strings.TrimSpace(string(sum))
	}
	return rel, nil
}

// ---------------------------------------------------------------------------
// version helpers
// ---------------------------------------------------------------------------

// maxDotted returns the largest dotted-numeric version found by re in body.
// re must have exactly one capture group holding the version.
func maxDotted(re *regexp.Regexp, body []byte) (string, bool) {
	matches := re.FindAllSubmatch(body, -1)
	best := ""
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		v := string(m[1])
		if best == "" || compareDotted(v, best) > 0 {
			best = v
		}
	}
	return best, best != ""
}

// maxFileAndVersion returns the full matched filename plus its version.
func maxFileAndVersion(re *regexp.Regexp, body []byte) (string, string, bool) {
	matches := re.FindAllSubmatch(body, -1)
	bestFile, best := "", ""
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		file := string(m[0])
		v := string(m[1])
		if best == "" || compareDotted(v, best) > 0 {
			bestFile, best = file, v
		}
	}
	return bestFile, best, best != ""
}

// compareDotted compares numeric dotted versions like "12.3.4" vs "12.3.10".
func compareDotted(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	n := len(as)
	if len(bs) > n {
		n = len(bs)
	}
	for i := 0; i < n; i++ {
		av, bv := 0, 0
		if i < len(as) {
			av, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			bv, _ = strconv.Atoi(bs[i])
		}
		if av != bv {
			if av < bv {
				return -1
			}
			return 1
		}
	}
	return 0
}
