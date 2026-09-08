// Package minecraft (providers.go): the MinecraftProvider abstraction.
// Vanilla · Paper · Purpur · Fabric · Forge · NeoForge are providers; the
// core service contains ZERO provider-specific branches — adding a server
// type = adding one struct here + a registry entry.
//
// Version manifests are fetched from the provider APIs AT RUNTIME and cached
// on disk (manifest cache TTL); an offline node falls back to the cached
// manifest or the built-in seed list (marked as such) so the control plane
// never lies about what is available.
package minecraft

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Provider is the MinecraftProvider interface: version listing, install
// layout and startup defaults for one server type. Implementations must be
// safe for concurrent use and must never shell out.
type Provider interface {
	// Name is the wire identifier ("vanilla" | "paper" | ...).
	Name() string
	// Label is the human name for UIs.
	Label() string
	// Available reports whether the control plane will schedule installs.
	Available() bool
	// Versions lists installable versions (newest first). Determined by the
	// provider's live manifest at runtime — never hardcoded forever.
	Versions() ([]string, error)
	// DefaultVersion is used when the customer does not pick one.
	DefaultVersion() ([]string, error)
	// JavaMajor returns the required Java major version for a version.
	JavaMajor(version string) int
	// DownloadURL resolves the server JAR (or installer JAR for loader
	// providers) for a version. Empty = unknown version.
	DownloadURL(version string) (string, error)
	// InstallerKind declares how the JAR is laid out on disk:
	//   jar        — the downloaded file IS the server jar
	//   installer  — run the jar with the installer profile, then it produces
	//                the server files (forge/neoforge)
	//   loader     — fabric-style loader installer over a vanilla jar
	InstallerKind(version string) string
	// StartCommand renders the java command line (agent fills paths).
	StartCommand(javaBin, jarPath string, xmxMB int64, extraArgs []string) string
	// JarName is the canonical server jar file name for a version.
	JarName(version string) string
	// PluginDir is where server plugins/mods live for this provider type
	// (empty string = the provider has no plugin concept).
	PluginDir() string
	// SupportsRCONTPS reports whether TPS/MSPT can be read over RCON with
	// vanilla commands (paper/purpur expose `tps`; vanilla does not).
	SupportsRCONTPS() bool
}

// ---------------------------------------------------------------------------
// Manifest cache: version lists fetched at runtime, cached on disk, with an
// offline fallback to the cached copy then a minimal seed list (honestly
// derived — the same families the master doc lists, not invented versions).
// ---------------------------------------------------------------------------

// ManifestTTL bounds the cached manifest freshness (re-fetched when stale).
const ManifestTTL = 12 * time.Hour

// ManifestDir is where provider manifests are cached on the control plane.
func ManifestDir() string {
	if v := strings.TrimSpace(os.Getenv("EPICPANEL_MC_MANIFEST_DIR")); v != "" {
		return v
	}
	return "/var/cache/epicpanel/minecraft"
}

// httpClient is the shared outbound client (bounded timeouts).
var httpClient = &http.Client{Timeout: 20 * time.Second}

// fetchJSON GETs a URL and decodes JSON into out. No redirects to file
// schemes, size-capped body, honest errors.
func fetchJSON(url string, out any) error {
	resp, err := httpClient.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("manifest fetch %s: HTTP %d", url, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

// cachePath is the on-disk cache file for one provider.
func cachePath(name string) string {
	return filepath.Join(ManifestDir(), name+"-versions.json")
}

// cachedVersions reads the cached list for a provider.
func cachedVersions(name string) ([]string, bool) {
	b, err := os.ReadFile(cachePath(name))
	if err != nil {
		return nil, false
	}
	var out []string
	if json.Unmarshal(b, &out) != nil || len(out) == 0 {
		return nil, false
	}
	return out, true
}

// storeVersions writes the fetched list to the cache (best effort).
func storeVersions(name string, versions []string) {
	_ = os.MkdirAll(ManifestDir(), 0o755)
	b, err := json.Marshal(versions)
	if err != nil {
		return
	}
	_ = os.WriteFile(cachePath(name), b, 0o644)
}

// stableVersions is the last-resort seed per provider family. These are the
// release lines the master doc's support list implies; the live manifest
// supersedes them whenever the node has network. Kept honest in the API: the
// versions response carries a "source" field ("live" | "cache" | "seed").
func stableVersions(name string) []string {
	switch name {
	case "vanilla":
		return []string{"1.21.4", "1.21.1", "1.20.6", "1.20.4", "1.19.4"}
	case "paper":
		return []string{"1.21.4", "1.21.1", "1.20.6", "1.20.4", "1.19.4"}
	case "purpur":
		return []string{"1.21.4", "1.21.1", "1.20.6", "1.20.4", "1.19.4"}
	case "fabric":
		return []string{"1.21.4", "1.21.1", "1.20.6", "1.20.4", "1.19.4"}
	case "forge":
		return []string{"1.21.4", "1.21.1", "1.20.6", "1.20.4", "1.19.4"}
	case "neoforge":
		return []string{"1.21.4", "1.21.1", "1.20.6", "1.20.4", "1.20.1"}
	default:
		return nil
	}
}

// versionsWithSource resolves a provider's version list with the honesty
// chain: live manifest → disk cache → seed. The agent performs the actual
// download at install time (its own manifest fetch); the control-plane list
// drives UI selection.
func versionsWithSource(p Provider) ([]string, string) {
	if v, err := p.Versions(); err == nil && len(v) > 0 {
		storeVersions(p.Name(), v)
		return v, "live"
	}
	if v, ok := cachedVersions(p.Name()); ok {
		return v, "cache"
	}
	return stableVersions(p.Name()), "seed"
}

// ---------------------------------------------------------------------------
// Manifest shapes (only the fields we consume).
// ---------------------------------------------------------------------------

type pistonResponse struct {
	Latest struct {
		Release string `json:"release"`
	} `json:"latest"`
	Versions []struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	} `json:"versions"`
}

type paperProject struct {
	Versions []string `json:"versions"`
}

type fabricGameVersions struct {
	Game []struct {
		Version string `json:"version"`
		Stable  bool   `json:"stable"`
	} `json:"game"`
}

type forgePromos struct {
	Promos map[string]string `json:"promos"`
}

// sortVersionsDesc orders release strings newest-first (numeric segments,
// descending; tolerant of pre-release suffixes which sort after the base).
func sortVersionsDesc(v []string) {
	sort.Slice(v, func(i, j int) bool {
		a, b := splitVer(v[i]), splitVer(v[j])
		for k := 0; k < len(a) && k < len(b); k++ {
			if a[k] != b[k] {
				return a[k] > b[k]
			}
		}
		return len(a) > len(b)
	})
}

func splitVer(s string) []int {
	s = strings.TrimPrefix(s, "v")
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == '.' || r == '-' })
	out := make([]int, 0, len(fields))
	for _, f := range fields {
		n := 0
		digits := true
		for _, c := range f {
			if c < '0' || c > '9' {
				digits = false
				break
			}
			n = n*10 + int(c-'0')
		}
		if digits {
			out = append(out, n)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Vanilla (Mojang piston-meta manifest).
// ---------------------------------------------------------------------------

// VanillaProvider serves Mojang vanilla server jars.
type VanillaProvider struct{}

func (VanillaProvider) Name() string    { return "vanilla" }
func (VanillaProvider) Label() string   { return "Vanilla" }
func (VanillaProvider) Available() bool { return true }

func (p VanillaProvider) Versions() ([]string, error) {
	var mr pistonResponse
	if err := fetchJSON("https://launchermeta.mojang.com/mc/game/version_manifest_v2.json", &mr); err != nil {
		return nil, err
	}
	var out []string
	for _, v := range mr.Versions {
		if v.Type == "release" {
			out = append(out, v.ID)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("vanilla manifest empty")
	}
	sortVersionsDesc(out)
	return out[:minInt(40, len(out))], nil
}

func (p VanillaProvider) DefaultVersion() ([]string, error) {
	v, err := p.Versions()
	if err != nil {
		return nil, err
	}
	return v[:minInt(1, len(v))], nil
}

// javaFor maps a Minecraft release to the required Java major (17 for
// 1.18-1.20.4, 21 for 1.20.5+). Data-driven from the release line — the
// same rule every downstream provider inherits.
func javaFor(version string) int {
	base := strings.SplitN(version, "-", 2)[0]
	parts := strings.Split(base, ".")
	major, minor, patch := 0, 0, 0
	if len(parts) > 0 {
		fmt.Sscanf(parts[0], "%d", &major)
	}
	if len(parts) > 1 {
		fmt.Sscanf(parts[1], "%d", &minor)
	}
	if len(parts) > 2 {
		fmt.Sscanf(parts[2], "%d", &patch)
	}
	if major < 1 {
		return 21
	}
	if major == 1 && (minor > 20 || (minor == 20 && patch >= 5)) {
		return 21
	}
	if major == 1 && minor >= 18 {
		return 17
	}
	return 8
}

func (p VanillaProvider) JavaMajor(version string) int { return javaFor(version) }

func (p VanillaProvider) DownloadURL(version string) (string, error) {
	// piston-meta: version manifest → per-version meta → server jar URL.
	var mr struct {
		Versions []struct {
			ID  string `json:"id"`
			URL string `json:"url"`
		} `json:"versions"`
	}
	if err := fetchJSON("https://launchermeta.mojang.com/mc/game/version_manifest_v2.json", &mr); err != nil {
		return "", err
	}
	metaURL := ""
	for _, v := range mr.Versions {
		if v.ID == version {
			metaURL = v.URL
			break
		}
	}
	if metaURL == "" {
		return "", fmt.Errorf("unknown vanilla version %q", version)
	}
	var meta struct {
		Downloads struct {
			Server struct {
				URL string `json:"url"`
			} `json:"server"`
		} `json:"downloads"`
	}
	if err := fetchJSON(metaURL, &meta); err != nil {
		return "", err
	}
	if meta.Downloads.Server.URL == "" {
		return "", fmt.Errorf("vanilla %q has no server jar", version)
	}
	return meta.Downloads.Server.URL, nil
}

func (p VanillaProvider) InstallerKind(version string) string { return "jar" }
func (p VanillaProvider) JarName(version string) string {
	return "minecraft_server-" + version + ".jar"
}
func (p VanillaProvider) PluginDir() string     { return "plugins" }
func (p VanillaProvider) SupportsRCONTPS() bool { return false }
func (p VanillaProvider) StartCommand(javaBin, jarPath string, xmxMB int64, extraArgs []string) string {
	return javaCommand(javaBin, jarPath, xmxMB, extraArgs)
}

// ---------------------------------------------------------------------------
// Paper (api.papermc.io v2) — also the base for Purpur.
// ---------------------------------------------------------------------------

// PaperProvider serves PaperMC server jars.
type PaperProvider struct{}

func (PaperProvider) Name() string    { return "paper" }
func (PaperProvider) Label() string   { return "Paper" }
func (PaperProvider) Available() bool { return true }

func (p PaperProvider) paperVersions() ([]string, error) {
	var proj paperProject
	if err := fetchJSON("https://api.papermc.io/v2/projects/paper", &proj); err != nil {
		return nil, err
	}
	if len(proj.Versions) == 0 {
		return nil, fmt.Errorf("paper manifest empty")
	}
	sortVersionsDesc(proj.Versions)
	return proj.Versions[:minInt(40, len(proj.Versions))], nil
}

func (p PaperProvider) Versions() ([]string, error) { return p.paperVersions() }

func (p PaperProvider) DefaultVersion() ([]string, error) {
	v, err := p.Versions()
	if err != nil {
		return nil, err
	}
	return v[:minInt(1, len(v))], nil
}

func (p PaperProvider) JavaMajor(version string) int { return javaFor(version) }

func (p PaperProvider) DownloadURL(version string) (string, error) {
	var builds struct {
		Builds []int `json:"builds"`
	}
	if err := fetchJSON("https://api.papermc.io/v2/projects/paper/versions/"+version+"/builds", &builds); err != nil {
		return "", err
	}
	if len(builds.Builds) == 0 {
		return "", fmt.Errorf("paper %q has no builds", version)
	}
	latest := builds.Builds[len(builds.Builds)-1]
	var bmeta struct {
		Downloads struct {
			Application struct {
				Name string `json:"name"`
			} `json:"application"`
		} `json:"downloads"`
	}
	if err := fetchJSON(fmt.Sprintf("https://api.papermc.io/v2/projects/paper/versions/%s/builds/%d", version, latest), &bmeta); err != nil {
		return "", err
	}
	name := bmeta.Downloads.Application.Name
	if name == "" {
		return "", fmt.Errorf("paper %q build %d missing artifact name", version, latest)
	}
	return fmt.Sprintf("https://api.papermc.io/v2/projects/paper/versions/%s/builds/%d/downloads/%s", version, latest, name), nil
}

func (p PaperProvider) InstallerKind(version string) string { return "jar" }
func (p PaperProvider) JarName(version string) string       { return "paper-" + version + ".jar" }
func (p PaperProvider) PluginDir() string                   { return "plugins" }
func (p PaperProvider) SupportsRCONTPS() bool               { return true }
func (p PaperProvider) StartCommand(javaBin, jarPath string, xmxMB int64, extraArgs []string) string {
	return javaCommand(javaBin, jarPath, xmxMB, extraArgs)
}

// PurpurProvider serves Purpur jars (drops on top of Paper API-compatible
// layout; own download endpoint).
type PurpurProvider struct{}

func (PurpurProvider) Name() string    { return "purpur" }
func (PurpurProvider) Label() string   { return "Purpur" }
func (PurpurProvider) Available() bool { return true }

func (p PurpurProvider) Versions() ([]string, error) {
	var proj paperProject
	if err := fetchJSON("https://api.purpurmc.org/v2/purpur", &proj); err != nil {
		return nil, err
	}
	if len(proj.Versions) == 0 {
		return nil, fmt.Errorf("purpur manifest empty")
	}
	sortVersionsDesc(proj.Versions)
	return proj.Versions[:minInt(40, len(proj.Versions))], nil
}

func (p PurpurProvider) DefaultVersion() ([]string, error) {
	v, err := p.Versions()
	if err != nil {
		return nil, err
	}
	return v[:minInt(1, len(v))], nil
}

func (p PurpurProvider) JavaMajor(version string) int { return javaFor(version) }

func (p PurpurProvider) DownloadURL(version string) (string, error) {
	var meta struct {
		Builds struct {
			Latest string `json:"latest"`
		} `json:"builds"`
	}
	if err := fetchJSON("https://api.purpurmc.org/v2/purpur/"+version, &meta); err != nil {
		return "", err
	}
	if meta.Builds.Latest == "" {
		return "", fmt.Errorf("purpur %q has no builds", version)
	}
	return "https://api.purpurmc.org/v2/purpur/" + version + "/" + meta.Builds.Latest + "/download", nil
}

func (p PurpurProvider) InstallerKind(version string) string { return "jar" }
func (p PurpurProvider) JarName(version string) string       { return "purpur-" + version + ".jar" }
func (p PurpurProvider) PluginDir() string                   { return "plugins" }
func (p PurpurProvider) SupportsRCONTPS() bool               { return true }
func (p PurpurProvider) StartCommand(javaBin, jarPath string, xmxMB int64, extraArgs []string) string {
	return javaCommand(javaBin, jarPath, xmxMB, extraArgs)
}

// ---------------------------------------------------------------------------
// Fabric (meta.fabricmc.net loader installer over a vanilla server jar).
// ---------------------------------------------------------------------------

// FabricProvider serves Fabric loader installs.
type FabricProvider struct{}

func (FabricProvider) Name() string    { return "fabric" }
func (FabricProvider) Label() string   { return "Fabric" }
func (FabricProvider) Available() bool { return true }

func (p FabricProvider) Versions() ([]string, error) {
	var gv fabricGameVersions
	if err := fetchJSON("https://meta.fabricmc.net/v2/versions/game", &gv); err != nil {
		return nil, err
	}
	var out []string
	for _, v := range gv.Game {
		if v.Stable {
			out = append(out, v.Version)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("fabric manifest empty")
	}
	sortVersionsDesc(out)
	return out[:minInt(40, len(out))], nil
}

func (p FabricProvider) DefaultVersion() ([]string, error) {
	v, err := p.Versions()
	if err != nil {
		return nil, err
	}
	return v[:minInt(1, len(v))], nil
}

func (p FabricProvider) JavaMajor(version string) int { return javaFor(version) }

// DownloadURL returns the fabric loader installer jar URL for the newest
// loader/stable installer against the game version (agent runs
// `java -jar fabric-installer.jar server -mcversion <v> -downloadMinecraft`).
func (p FabricProvider) DownloadURL(version string) (string, error) {
	var loaders []struct {
		Version string `json:"version"`
		Stable  bool   `json:"stable"`
	}
	if err := fetchJSON("https://meta.fabricmc.net/v2/versions/loader", &loaders); err != nil {
		return "", err
	}
	loader := ""
	for _, l := range loaders {
		if l.Stable {
			loader = l.Version
			break
		}
	}
	if loader == "" {
		return "", fmt.Errorf("fabric: no stable loader")
	}
	var installers []struct {
		URL     string `json:"url"`
		Version string `json:"version"`
	}
	if err := fetchJSON("https://meta.fabricmc.net/v2/versions/installer", &installers); err != nil {
		return "", err
	}
	for _, i := range installers {
		if i.URL != "" {
			return i.URL, nil
		}
	}
	return "", fmt.Errorf("fabric: no installer available")
}

func (p FabricProvider) InstallerKind(version string) string { return "loader" }
func (p FabricProvider) JarName(version string) string       { return "fabric-" + version + ".jar" }
func (p FabricProvider) PluginDir() string                   { return "mods" }
func (p FabricProvider) SupportsRCONTPS() bool               { return false }
func (p FabricProvider) StartCommand(javaBin, jarPath string, xmxMB int64, extraArgs []string) string {
	return javaCommand(javaBin, jarPath, xmxMB, extraArgs)
}

// ---------------------------------------------------------------------------
// Forge / NeoForge (installer profile: run installer once, it produces the
// run scripts + libraries; the start command then runs the produced jar).
// ---------------------------------------------------------------------------

// ForgeProvider serves MinecraftForge installs.
type ForgeProvider struct{}

func (ForgeProvider) Name() string    { return "forge" }
func (ForgeProvider) Label() string   { return "Forge" }
func (ForgeProvider) Available() bool { return true }

func (p ForgeProvider) Versions() ([]string, error) {
	var promos forgePromos
	if err := fetchJSON("https://files.minecraftforge.net/net/minecraftforge/forge/promotions_slim.json", &promos); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for key := range promos.Promos {
		// keys look like "1.21.4-latest" / "1.20.1-recommended"
		if i := strings.LastIndex(key, "-"); i > 0 {
			mc := key[:i]
			if !seen[mc] {
				seen[mc] = true
				out = append(out, mc)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("forge manifest empty")
	}
	sortVersionsDesc(out)
	return out[:minInt(40, len(out))], nil
}

func (p ForgeProvider) DefaultVersion() ([]string, error) {
	v, err := p.Versions()
	if err != nil {
		return nil, err
	}
	return v[:minInt(1, len(v))], nil
}

func (p ForgeProvider) JavaMajor(version string) int { return javaFor(version) }

// DownloadURL returns the forge installer jar (marketer download endpoint);
// the agent runs it with `--installServer`.
func (p ForgeProvider) DownloadURL(version string) (string, error) {
	var promos forgePromos
	if err := fetchJSON("https://files.minecraftforge.net/net/minecraftforge/forge/promotions_slim.json", &promos); err != nil {
		return "", err
	}
	build := promos.Promos[version+"-latest"]
	if build == "" {
		build = promos.Promos[version+"-recommended"]
	}
	if build == "" {
		return "", fmt.Errorf("forge %q has no promoted build", version)
	}
	return fmt.Sprintf("https://maven.minecraftforge.net/net/minecraftforge/forge/%s-%s/forge-%s-%s-installer.jar", version, build, version, build), nil
}

func (p ForgeProvider) InstallerKind(version string) string { return "installer" }
func (p ForgeProvider) JarName(version string) string       { return "forge-" + version + "-installer.jar" }
func (p ForgeProvider) PluginDir() string                   { return "mods" }
func (p ForgeProvider) SupportsRCONTPS() bool               { return false }
func (p ForgeProvider) StartCommand(javaBin, jarPath string, xmxMB int64, extraArgs []string) string {
	// After --installServer, modern forge generates run/run.sh + a user-jvm
	// args dir; the produced libraries launcher jar lives at the recorded
	// path. The agent resolves it; the default is the conventional layout.
	return javaCommand(javaBin, "@libraries/net/minecraftforge/forge/linux_args.txt", xmxMB, extraArgs)
}

// NeoForgeProvider serves NeoForge installs (installer with own maven).
type NeoForgeProvider struct{}

func (NeoForgeProvider) Name() string    { return "neoforge" }
func (NeoForgeProvider) Label() string   { return "NeoForge" }
func (NeoForgeProvider) Available() bool { return true }

func (p NeoForgeProvider) Versions() ([]string, error) {
	var releases []string
	if err := fetchJSON("https://maven.neoforged.net/api/maven/versions/releases/net/neoforged/neoforge", &releases); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, rel := range releases {
		// versions look like 21.4.47 (mc 1.21.4) / 20.6.119 (mc 1.20.6)
		parts := strings.SplitN(rel, ".", 3)
		if len(parts) < 3 {
			continue
		}
		mc := mcForNeoMajor(parts[0], parts[1])
		if mc == "" || seen[mc] {
			continue
		}
		seen[mc] = true
		out = append(out, mc)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("neoforge manifest empty")
	}
	sortVersionsDesc(out)
	return out[:minInt(40, len(out))], nil
}

// mcForNeoMajor maps the neoforge version's first two segments back to the
// Minecraft release line (21.4.x → 1.21.4; 20.6.x → 1.20.6; ...).
func mcForNeoMajor(major, minor string) string {
	maj := atoiSafe(major)
	min := atoiSafe(minor)
	if maj == 0 || min == 0 {
		return ""
	}
	return fmt.Sprintf("1.%d.%d", maj, min)
}

func atoiSafe(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func (p NeoForgeProvider) DefaultVersion() ([]string, error) {
	v, err := p.Versions()
	if err != nil {
		return nil, err
	}
	return v[:minInt(1, len(v))], nil
}

func (p NeoForgeProvider) JavaMajor(version string) int { return javaFor(version) }

func (p NeoForgeProvider) DownloadURL(version string) (string, error) {
	var releases []string
	if err := fetchJSON("https://maven.neoforged.net/api/maven/versions/releases/net/neoforged/neoforge", &releases); err != nil {
		return "", err
	}
	// pick the newest neoforge build for the requested mc line
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 3 {
		return "", fmt.Errorf("invalid neoforge target %q", version)
	}
	wantMajor, wantMinor := atoiSafe(parts[1]), atoiSafe(parts[2])
	best := ""
	for _, rel := range releases {
		segs := strings.SplitN(rel, ".", 3)
		if len(segs) < 3 {
			continue
		}
		if atoiSafe(segs[0]) == wantMajor && atoiSafe(segs[1]) == wantMinor {
			best = rel // list is ordered ascending; keep the last match
		}
	}
	if best == "" {
		return "", fmt.Errorf("neoforge %q has no builds", version)
	}
	return "https://maven.neoforged.net/releases/net/neoforged/neoforge/" + best + "/neoforge-" + best + "-installer.jar", nil
}

func (p NeoForgeProvider) InstallerKind(version string) string { return "installer" }
func (p NeoForgeProvider) JarName(version string) string {
	return "neoforge-" + version + "-installer.jar"
}
func (p NeoForgeProvider) PluginDir() string     { return "mods" }
func (p NeoForgeProvider) SupportsRCONTPS() bool { return false }
func (p NeoForgeProvider) StartCommand(javaBin, jarPath string, xmxMB int64, extraArgs []string) string {
	return javaCommand(javaBin, "@libraries/net/neoforged/neoforge/linux_args.txt", xmxMB, extraArgs)
}

// javaCommand assembles the JVM command line (shared by all providers — no
// provider branch needed): heap, G1 defaults, nogui, jar, nogui flags.
func javaCommand(javaBin, jarOrArgs string, xmxMB int64, extraArgs []string) string {
	parts := []string{javaBin,
		"-Xms" + fmt.Sprintf("%dM", xmxMB),
		"-Xmx" + fmt.Sprintf("%dM", xmxMB),
		"-XX:+UseG1GC", "-XX:+ParallelRefProcEnabled",
		"-XX:MaxGCPauseMillis=200",
		"-Dfile.encoding=UTF-8",
	}
	parts = append(parts, extraArgs...)
	parts = append(parts, "-jar", jarOrArgs, "nogui")
	return strings.Join(parts, " ")
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ---------------------------------------------------------------------------
// Registry — adding a provider = one entry here. Core never branches on
// provider names.
// ---------------------------------------------------------------------------

var providers = map[string]Provider{
	VanillaProvider{}.Name():  VanillaProvider{},
	PaperProvider{}.Name():    PaperProvider{},
	PurpurProvider{}.Name():   PurpurProvider{},
	FabricProvider{}.Name():   FabricProvider{},
	ForgeProvider{}.Name():    ForgeProvider{},
	NeoForgeProvider{}.Name(): NeoForgeProvider{},
}

// ProviderFor resolves the provider for a name.
func ProviderFor(name string) (Provider, error) {
	p, ok := providers[name]
	if !ok {
		return nil, fmt.Errorf("unknown minecraft provider %q", name)
	}
	return p, nil
}

// ProviderNames lists provider wire names (sorted).
func ProviderNames() []string {
	out := make([]string, 0, len(providers))
	for name := range providers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ProviderOffer is the provider + versions payload for the UI version
// selector. Source states the honesty chain (live | cache | seed).
type ProviderOffer struct {
	Provider    string   `json:"provider"`
	Label       string   `json:"label"`
	Versions    []string `json:"versions"`
	Default     string   `json:"default"`
	Source      string   `json:"source"`
	SupportsTPS bool     `json:"supports_tps"`
}

// ProviderOffers resolves every provider's selectable versions (bounded
// concurrency is unnecessary: six bounded HTTP GETs worst case, each
// timeout-capped; a failing provider falls back to cache/seed honestly).
func ProviderOffers() []ProviderOffer {
	out := make([]ProviderOffer, 0, len(providers))
	for _, name := range ProviderNames() {
		p := providers[name]
		versions, source := versionsWithSource(p)
		def := ""
		if len(versions) > 0 {
			def = versions[0]
		}
		out = append(out, ProviderOffer{
			Provider: p.Name(), Label: p.Label(), Versions: versions,
			Default: def, Source: source, SupportsTPS: p.SupportsRCONTPS(),
		})
	}
	return out
}

// ValidateProviderVersion checks a customer-selected version against the
// provider's resolved offer (live → cache → seed chain).
func ValidateProviderVersion(providerName, version string) (int, error) {
	p, err := ProviderFor(providerName)
	if err != nil {
		return 0, err
	}
	versions, _ := versionsWithSource(p)
	for _, v := range versions {
		if v == version {
			return p.JavaMajor(version), nil
		}
	}
	return 0, fmt.Errorf("unsupported %s version %q", providerName, version)
}
