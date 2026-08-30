//go:build linux

package platform

import "errors"

// validateSitesRoot requires an absolute POSIX path.
func validateSitesRoot(root string) error {
	if len(root) == 0 || root[0] != '/' {
		return errors.New("sites root must be an absolute path")
	}
	if root == "/" {
		return errors.New("sites root may not be /")
	}
	return nil
}

// logDirs lists additional directories the panel may read logs from.
func logDirs() []string {
	return []string{"/var/log/nginx"}
}

// defaultSitesRoot is used when -sites-root is not provided. It must match
// the panel's convention (websites.DefaultSitesRootLinux).
func defaultSitesRoot() string { return "/www/wwwroot" }
