package software

import (
	"context"
	"errors"
)

// Release describes one resolved latest build for a component: the concrete
// download URL and how to lay it out. It is produced at install time by a
// Resolver so the panel always installs the current upstream release — no
// version is hardcoded in the provider, so new upstream versions are picked up
// automatically without any code change.
type Release struct {
	Version string // upstream version, e.g. "1.27.4"
	URL     string // download URL
	SHA256  string // optional hex checksum; verified before extraction
	Format  string // "zip" | "tar.gz" | "tar.xz"
	Prefix  string // top-level dir inside the archive to strip ("" = keep all)
	Bin     string // relative path of the primary binary inside the tree ("" = Binary)
}

// Resolver resolves the current latest Release for a component on the given
// OS. Returning (nil, nil) signals "no self-contained build is available on
// this platform" and the manager falls back to the package manager.
type Resolver func(ctx context.Context, os OSInfo) (*Release, error)

// BuildSpec describes how to compile a component from source on Linux into
// EpicPanel's own directory. It is used for components that publish only
// source tarballs (nginx, PHP, Redis, Apache) — the resulting binaries land in
// our software dir, never in system paths.
type BuildSpec struct {
	ConfigureArgs []string // args after ./configure (prefix handled by the engine)
	MakeFlags     []string // extra flags for `make`, e.g. {"MALLOC=libc"}
	NoConfigure   bool     // true when the component has no ./configure (Redis)
	// CriticalFiles must exist in the extracted source tree (relative to the
	// tree root) before building; a missing file means the download was
	// truncated/corrupt and the build aborts with a clear message.
	CriticalFiles []string
	DepsApt       []string // apt packages required to build
	DepsDnf       []string // dnf packages required to build
	DepsZypper    []string // zypper packages required to build
	VersionArgs   []string // how to print the compiled version
	VersionStderr bool     // version printed to stderr
}

// ErrNotApplicable reports that a resolver has no self-contained build for
// the requested platform (e.g. nginx has no official prebuilt Linux binary).
var ErrNotApplicable = errors.New("no self-contained build available for this platform")

// Provider describes one managed component: how to detect it, how to install
// it (preferring a self-contained, EpicPanel-owned download resolved at
// install time; falling back to the host package manager when no official
// prebuilt archive exists), and how to control its service. All command
// arguments are defined here (static), never derived from a request.
type Provider struct {
	Name              string
	DisplayName       string
	Category          string
	Binary            string              // primary binary name used for detection + version
	VersionArgs       []string            // args to print a version (e.g. {"-v"})
	VersionFromStderr bool                // some tools print version to stderr
	Packages          map[string][]string // manager -> full argv (fallback when no download)
	Service           string              // logical service key
	Resolve           Resolver            // dynamic latest self-contained download
	Build             *BuildSpec          // optional: compile from source (Linux nginx/PHP)
}

// Registry is the allowlist of installable components.
type Registry struct {
	providers map[string]Provider
	order     []string
}

// Default returns the built-in component registry.
func Default() *Registry {
	r := &Registry{providers: map[string]Provider{}}
	for _, p := range builtinProviders() {
		r.providers[p.Name] = p
		r.order = append(r.order, p.Name)
	}
	return r
}

// Get returns a provider by name (allowlist lookup).
func (r *Registry) Get(name string) (Provider, bool) {
	p, ok := r.providers[name]
	return p, ok
}

// Names returns the registered component names in display order.
func (r *Registry) Names() []string { return append([]string{}, r.order...) }

func builtinProviders() []Provider {
	return []Provider{
		{
			Name: "nginx", DisplayName: "Nginx", Category: "Web Servers",
			Binary: "nginx", VersionArgs: []string{"-v"}, VersionFromStderr: true,
			Service: "nginx",
			Resolve: resolveNginx,
			Build: &BuildSpec{
				ConfigureArgs: []string{
					"--with-http_ssl_module",
					"--with-http_v2_module",
					"--with-http_v3_module",
					"--with-http_gzip_static_module",
					"--with-http_stub_status_module",
					"--with-threads",
					"--with-file-aio",
					"--without-select_module",
					"--without-poll_module",
				},
				CriticalFiles: []string{"configure", "auto/options"},
				DepsApt:       []string{"build-essential", "libpcre2-dev", "libssl-dev", "zlib1g-dev", "libgd-dev"},
				DepsDnf:    []string{"gcc", "make", "pcre2-devel", "openssl-devel", "zlib-devel", "gd-devel"},
				DepsZypper: []string{"gcc", "make", "pcre2-devel", "libopenssl-devel", "zlib-devel", "gd-devel"},
				VersionArgs: []string{"-v"}, VersionStderr: true,
			},
			Packages: map[string][]string{
				"apt":    {"apt-get", "install", "-y", "nginx"},
				"dnf":    {"dnf", "install", "-y", "nginx"},
				"zypper": {"zypper", "--non-interactive", "install", "nginx"},
				"winget": {"winget", "install", "-e", "--id", "nginxinc.nginx", "--accept-package-agreements", "--accept-source-agreements"},
			},
		},
		{
			Name: "apache", DisplayName: "Apache", Category: "Web Servers",
			Binary: "apachectl", VersionArgs: []string{"-v"},
			Service: "apache2",
			Resolve: resolveApache,
			Build: &BuildSpec{
				ConfigureArgs: []string{
					"--enable-so",
					"--enable-ssl",
					"--enable-mods-shared=all",
					"--with-mpm=prefork",
					"--with-included-apr",
				},
				CriticalFiles: []string{"configure"},
				DepsApt:       []string{"build-essential", "libtool", "libpcre2-dev", "libssl-dev", "zlib1g-dev", "libxml2-dev"},
				DepsDnf:    []string{"gcc", "make", "libtool", "pcre2-devel", "openssl-devel", "zlib-devel", "libxml2-devel", "apr-devel", "apr-util-devel"},
				DepsZypper: []string{"gcc", "make", "libtool", "pcre2-devel", "libopenssl-devel", "zlib-devel", "libxml2-devel", "libapr1-devel", "libapr-util1-devel"},
			},
			Packages: map[string][]string{
				"apt": {"apt-get", "install", "-y", "apache2"},
				"dnf": {"dnf", "install", "-y", "httpd"},
			},
		},
		{
			Name: "mariadb", DisplayName: "MariaDB", Category: "Databases",
			Binary: "mariadb", VersionArgs: []string{"--version"},
			Service: "mariadb",
			Resolve: resolveMariaDB,
			Packages: map[string][]string{
				"apt":    {"apt-get", "install", "-y", "mariadb-server"},
				"dnf":    {"dnf", "install", "-y", "mariadb-server"},
				"zypper": {"zypper", "--non-interactive", "install", "mariadb-server"},
				"winget": {"winget", "install", "-e", "--id", "MariaDB.Server", "--accept-package-agreements", "--accept-source-agreements"},
			},
		},
		{
			Name: "redis", DisplayName: "Redis", Category: "Databases",
			Binary: "redis-server", VersionArgs: []string{"--version"},
			Service: "redis-server",
			Resolve: resolveRedis,
			Build: &BuildSpec{
				NoConfigure: true,
				MakeFlags:   []string{"MALLOC=libc"},
				DepsApt:    []string{"build-essential", "libsystemd-dev"},
				DepsDnf:    []string{"gcc", "make", "systemd-devel"},
				DepsZypper: []string{"gcc", "make", "systemd-devel"},
				VersionArgs: []string{"--version"}, VersionStderr: false,
			},
			Packages: map[string][]string{
				"apt":    {"apt-get", "install", "-y", "redis-server"},
				"dnf":    {"dnf", "install", "-y", "redis"},
				"zypper": {"zypper", "--non-interactive", "install", "redis"},
				"winget": {"winget", "install", "-e", "--id", "Redis.Redis", "--accept-package-agreements", "--accept-source-agreements"},
			},
		},
		{
			Name: "php", DisplayName: "PHP", Category: "Runtimes",
			Binary: "php", VersionArgs: []string{"-r", "echo PHP_VERSION;"},
			Service: "php-fpm",
			Resolve: resolvePHP,
			Build: &BuildSpec{
				ConfigureArgs: []string{
					"--enable-fpm",
					"--with-fpm-user=epicpanel-sites",
					"--with-fpm-group=epicpanel-sites",
					"--with-pdo-mysql=mysqlnd",
					"--with-mysqli=mysqlnd",
					"--with-pdo-pgsql",
					"--with-curl",
					"--with-openssl",
					"--with-zlib",
					"--with-readline",
					"--with-zip",
					"--enable-mbstring",
					"--enable-opcache",
					"--disable-cgi",
				},
				CriticalFiles: []string{"configure"},
				DepsApt:       []string{"build-essential", "autoconf", "libxml2-dev", "libsqlite3-dev", "libcurl4-openssl-dev", "libssl-dev", "libonig-dev", "libzip-dev", "zlib1g-dev", "libreadline-dev", "pkg-config"},
				DepsDnf:    []string{"gcc", "make", "autoconf", "libxml2-devel", "sqlite-devel", "libcurl-devel", "openssl-devel", "oniguruma-devel", "libzip-devel", "zlib-devel", "readline-devel", "pkgconfig"},
				DepsZypper: []string{"gcc", "make", "autoconf", "libxml2-devel", "sqlite3-devel", "libcurl-devel", "libopenssl-devel", "oniguruma-devel", "libzip-devel", "zlib-devel", "readline-devel", "pkg-config"},
				VersionArgs: []string{"-r", "echo PHP_VERSION;"}, VersionStderr: false,
			},
			Packages: map[string][]string{
				"apt": {"apt-get", "install", "-y", "php-fpm", "php-cli", "php-mysql", "php-pgsql"},
				"dnf": {"dnf", "install", "-y", "php-fpm", "php-cli", "php-mysqlnd", "php-pgsql"},
				"winget": {"winget", "install", "-e", "--id", "PHP.PHP.8.3", "--accept-package-agreements", "--accept-source-agreements"},
			},
		},
		{
			Name: "node", DisplayName: "Node.js", Category: "Runtimes",
			Binary: "node", VersionArgs: []string{"--version"},
			Resolve: resolveNode,
			Packages: map[string][]string{
				"apt":    {"apt-get", "install", "-y", "nodejs", "npm"},
				"dnf":    {"dnf", "install", "-y", "nodejs", "npm"},
				"zypper": {"zypper", "--non-interactive", "install", "nodejs", "npm"},
				"winget": {"winget", "install", "-e", "--id", "OpenJS.NodeJS", "--accept-package-agreements", "--accept-source-agreements"},
			},
		},
		{
			Name: "java", DisplayName: "Java", Category: "Runtimes",
			Binary: "java", VersionArgs: []string{"-version"}, VersionFromStderr: true,
			Resolve: resolveJava,
			Packages: map[string][]string{
				"apt": {"apt-get", "install", "-y", "default-jre"},
				"dnf": {"dnf", "install", "-y", "java-17-openjdk"},
			},
		},
		{
			Name: "docker", DisplayName: "Docker", Category: "Containers",
			Binary: "docker", VersionArgs: []string{"--version"},
			Service: "docker",
			Packages: map[string][]string{
				"apt": {"apt-get", "install", "-y", "docker.io"},
				"dnf": {"dnf", "install", "-y", "docker"},
				"winget": {"winget", "install", "-e", "--id", "Docker.DockerDesktop", "--accept-package-agreements", "--accept-source-agreements"},
			},
		},
	}
}
