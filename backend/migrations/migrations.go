// Package migrations holds canonical SQL migrations for EpicPanel.
// The embedded FS is consumed by the database migrator.
package migrations

import (
	"embed"
	"io/fs"
)

//go:embed *.sql
var files embed.FS

// FS returns the embedded migrations filesystem rooted at this directory.
func FS() fs.FS { return files }
