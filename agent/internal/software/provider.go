package software

// Provider describes one managed component: how to detect it, install and
// remove it per package manager, and which service controls it. All command
// arguments are defined here (static), never derived from a request.
type Provider struct {
	Name            string
	DisplayName     string
	Category        string
	Binary          string            // used for presence detection (LookPath)
	VersionArgs     []string          // args to print a version (e.g. {"-v"})
	VersionFromStderr bool            // some tools print version to stderr
	Packages        map[string][]string // manager -> full argv
	Service         string              // systemd unit / Windows service name
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
			Packages: map[string][]string{
				"apt":    {"apt-get", "install", "-y", "nginx"},
				"dnf":    {"dnf", "install", "-y", "nginx"},
				"zypper": {"zypper", "--non-interactive", "install", "nginx"},
				"winget": {"winget", "install", "-e", "--id", "nginxinc.nginx", "--accept-package-agreements", "--accept-source-agreements"},
			},
			Service: "nginx",
		},
		{
			Name: "apache", DisplayName: "Apache", Category: "Web Servers",
			Binary: "apachectl", VersionArgs: []string{"-v"},
			Packages: map[string][]string{
				"apt": {"apt-get", "install", "-y", "apache2"},
				"dnf": {"dnf", "install", "-y", "httpd"},
			},
			Service: "apache2",
		},
		{
			Name: "mariadb", DisplayName: "MariaDB", Category: "Databases",
			Binary: "mariadb", VersionArgs: []string{"--version"},
			Packages: map[string][]string{
				"apt":    {"apt-get", "install", "-y", "mariadb-server"},
				"dnf":    {"dnf", "install", "-y", "mariadb-server"},
				"zypper": {"zypper", "--non-interactive", "install", "mariadb-server"},
				"winget": {"winget", "install", "-e", "--id", "MariaDB.Server", "--accept-package-agreements", "--accept-source-agreements"},
			},
			Service: "mariadb",
		},
		{
			Name: "redis", DisplayName: "Redis", Category: "Databases",
			Binary: "redis-server", VersionArgs: []string{"--version"},
			Packages: map[string][]string{
				"apt":    {"apt-get", "install", "-y", "redis-server"},
				"dnf":    {"dnf", "install", "-y", "redis"},
				"zypper": {"zypper", "--non-interactive", "install", "redis"},
				"winget": {"winget", "install", "-e", "--id", "Redis.Redis", "--accept-package-agreements", "--accept-source-agreements"},
			},
			Service: "redis-server",
		},
		{
			Name: "php", DisplayName: "PHP", Category: "Runtimes",
			Binary: "php", VersionArgs: []string{"-r", "echo PHP_VERSION;"},
			Packages: map[string][]string{
				"apt": {"apt-get", "install", "-y", "php-fpm", "php-cli", "php-mysql", "php-pgsql"},
				"dnf": {"dnf", "install", "-y", "php-fpm", "php-cli", "php-mysqlnd", "php-pgsql"},
				"winget": {"winget", "install", "-e", "--id", "PHP.PHP.8.3", "--accept-package-agreements", "--accept-source-agreements"},
			},
			Service: "php-fpm",
		},
		{
			Name: "node", DisplayName: "Node.js", Category: "Runtimes",
			Binary: "node", VersionArgs: []string{"--version"},
			Packages: map[string][]string{
				"apt":    {"apt-get", "install", "-y", "nodejs", "npm"},
				"dnf":    {"dnf", "install", "-y", "nodejs", "npm"},
				"zypper": {"zypper", "--non-interactive", "install", "nodejs", "npm"},
				"winget": {"winget", "install", "-e", "--id", "OpenJS.NodeJS", "--accept-package-agreements", "--accept-source-agreements"},
			},
			Service: "",
		},
		{
			Name: "java", DisplayName: "Java", Category: "Runtimes",
			Binary: "java", VersionArgs: []string{"-version"}, VersionFromStderr: true,
			Packages: map[string][]string{
				"apt": {"apt-get", "install", "-y", "default-jre"},
				"dnf": {"dnf", "install", "-y", "java-17-openjdk"},
			},
			Service: "",
		},
		{
			Name: "docker", DisplayName: "Docker", Category: "Containers",
			Binary: "docker", VersionArgs: []string{"--version"},
			Packages: map[string][]string{
				"apt": {"apt-get", "install", "-y", "docker.io"},
				"dnf": {"dnf", "install", "-y", "docker"},
				"winget": {"winget", "install", "-e", "--id", "Docker.DockerDesktop", "--accept-package-agreements", "--accept-source-agreements"},
			},
			Service: "docker",
		},
	}
}
