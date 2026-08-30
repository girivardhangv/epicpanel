//go:build windows

package platform

import (
	"errors"
	"os"
	"regexp"
)

var winDriveRe = regexp.MustCompile(`^[A-Za-z]:[\\/]`)

// validateSitesRoot requires an absolute Windows path (drive letter form).
func validateSitesRoot(root string) error {
	if !winDriveRe.MatchString(root + "\\") {
		return errors.New("sites root must be an absolute Windows path (e.g. C:\\Panel\\Sites)")
	}
	return nil
}

// logDirs lists additional directories the panel may read logs from.
func logDirs() []string {
	dir := os.Getenv("EPICPANEL_NGINX_DIR")
	if dir == "" {
		dir = `C:\nginx`
	}
	return []string{dir + `\logs`}
}

// defaultSitesRoot is used when -sites-root is not provided. It must match
// the panel's convention (websites.DefaultSitesRootWindows).
func defaultSitesRoot() string { return `C:\www\wwwroot` }
