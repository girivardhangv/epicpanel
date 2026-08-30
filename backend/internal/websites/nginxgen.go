// Package websites implements the website hosting engine: path strategy,
// Nginx configuration generation, lifecycle management and provisioning jobs.
package websites

import (
	"strings"
)

// WebServer kinds. Phase 2 ships nginx; the enum keeps the door open for
// Apache/IIS without schema churn.
const (
	WebServerNginx = "nginx"
)

// PHP handler styles (kept identical across operating systems).
const (
	HandlerFPM      = "fpm"      // Linux PHP-FPM over unix socket
	HandlerFastCGI  = "fastcgi"  // Windows php-cgi over TCP loopback
)

// SiteConfig carries every value the generator interpolates into an Nginx
// server block. Every field MUST have been validated before Generate runs —
// the generator is deliberately dumb and never mutations-never escapes input;
// callers guarantee domains came from domains.ValidateAndNormalize and paths
// from ResolveSitePaths.
type SiteConfig struct {
	Domain       string // primary server_name
	Aliases      []string
	IncludeWWW   bool   // also answer for www.<domain>
	DocumentRoot string // absolute path in agent-local OS syntax
	PHPVersion   string // empty => serve static files only
	FastCGIAddr  string // unix:/run/php/....sock or 127.0.0.1:9001
	AccessLog    string
	ErrorLog     string
	IndexFiles   string // optional override of the index directive

	// SSL (Phase 4). When both paths are set the generator emits a 443
	// listener with the certificate and redirects plain HTTP to HTTPS.
	CertPath string
	KeyPath  string
}

// nginxEscape makes a validated path safe for an Nginx directive. Backslashes
// (Windows) are converted to forward slashes which Nginx on Windows accepts.
func nginxEscapePath(p string) string {
	return strings.ReplaceAll(p, "\\", "/")
}

// Generate renders the full server block for a website.
func Generate(c SiteConfig) string {
	var b strings.Builder

	names := []string{c.Domain}
	if c.IncludeWWW {
		names = append(names, "www."+c.Domain)
	}
	names = append(names, c.Aliases...)
	for i, a := range names {
		names[i] = sanitizeServerName(a)
	}

	root := nginxEscapePath(c.DocumentRoot)
	accessLog := firstNonEmpty(nginxEscapePath(c.AccessLog), "/var/log/nginx/access.log")
	errorLog := firstNonEmpty(nginxEscapePath(c.ErrorLog), "/var/log/nginx/error.log")
	index := firstNonEmpty(c.IndexFiles, "index.php index.html")

	// TLS (Phase 4): an HTTPS listener plus an HTTP->HTTPS redirect block.
	// cert/key paths are validated upstream (absolute, under the agent's
	// certs dir); the generator only emits the directives.
	tlsEnabled := c.CertPath != "" && c.KeyPath != ""
	certPath := nginxEscapePath(c.CertPath)
	keyPath := nginxEscapePath(c.KeyPath)

	b.WriteString("# Managed by EpicPanel — manual edits will be overwritten.\n")

	if tlsEnabled {
		b.WriteString("server {\n")
		b.WriteString("    listen 80;\n")
		b.WriteString("    listen [::]:80;\n")
		b.WriteString("    server_name " + strings.Join(names, " ") + ";\n")
		b.WriteString("    return 301 https://$host$request_uri;\n")
		b.WriteString("}\n\n")
		b.WriteString("server {\n")
		b.WriteString("    listen 443 ssl;\n")
		b.WriteString("    listen [::]:443 ssl;\n")
		b.WriteString("    http2 on;\n")
		b.WriteString("    server_name " + strings.Join(names, " ") + ";\n\n")
		b.WriteString("    ssl_certificate " + certPath + ";\n")
		b.WriteString("    ssl_certificate_key " + keyPath + ";\n")
		b.WriteString("    ssl_protocols TLSv1.2 TLSv1.3;\n\n")
	} else {
		b.WriteString("server {\n")
		b.WriteString("    listen 80;\n")
		b.WriteString("    listen [::]:80;\n")
		b.WriteString("    server_name " + strings.Join(names, " ") + ";\n\n")
	}

	b.WriteString("    root " + root + ";\n")
	b.WriteString("    index " + index + ";\n\n")
	// Use the built-in default format: a named "epicpanel" log_format only
	// exists in distro-packaged nginx configs, so referencing it would make
	// validation fail on vanilla Windows/Linux installs.
	b.WriteString("    access_log " + accessLog + ";\n")
	b.WriteString("    error_log  " + errorLog + ";\n\n")

	b.WriteString("    location / {\n")
	if c.PHPVersion != "" {
		b.WriteString("        try_files $uri $uri/ /index.php?$query_string;\n")
	} else {
		b.WriteString("        try_files $uri $uri/ =404;\n")
	}
	b.WriteString("    }\n")

	if c.PHPVersion != "" && c.FastCGIAddr != "" {
		b.WriteString("\n    location ~ [^/]\\.php(/|$) {\n")
		b.WriteString("        include fastcgi_params;\n")
		b.WriteString("        fastcgi_pass " + c.FastCGIAddr + ";\n")
		b.WriteString("        fastcgi_index index.php;\n")
		b.WriteString("        fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;\n")
		if tlsEnabled {
			b.WriteString("        fastcgi_param HTTPS on;\n")
		} else {
			b.WriteString("        fastcgi_param HTTPS off;\n")
		}
		b.WriteString("        try_files $uri =404;\n")
		b.WriteString("    }\n")
	}

	b.WriteString("\n    location ~ /\\.(?!well-known) {\n")
	b.WriteString("        deny all;\n")
	b.WriteString("    }\n")

	b.WriteString("}\n")
	return b.String()
}

// sanitizeServerName guarantees only characters Nginx accepts inside
// server_name can reach the directive. Domains are validated upstream; this is
// defense in depth for values that travel through many layers.
func sanitizeServerName(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-', r == '*':
			b.WriteRune(r)
		}
	}
	return b.String()
}
