package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

// CloneOutcome is the job result for clone_staging.
type CloneOutcome struct {
	FilesCopied bool     `json:"files_copied"`
	Databases   []string `json:"databases"`
}

// CloneStaging copies the production site's public content (resolving the
// current release) into the staging tree, then clones each attached database
// prod_db -> staging_db via the engine's root socket (credentials never leave
// the box).
func (e *Executor) CloneStaging(ctx context.Context, prodWebsiteID, stagingWebsiteID string, databases []DBClone) (*CloneOutcome, error) {
	if err := uuidParseID(prodWebsiteID); err != nil {
		return nil, fmt.Errorf("invalid production website id")
	}
	if err := uuidParseID(stagingWebsiteID); err != nil {
		return nil, fmt.Errorf("invalid staging website id")
	}

	prodPublic := filepath.Join("/srv/epicpanel/websites", prodWebsiteID, "public")
	stagingBase := filepath.Join("/srv/epicpanel/websites", stagingWebsiteID)
	stagingPublic := filepath.Join(stagingBase, "public")

	// Resolve the current release (public may be a symlink into releases).
	src := prodPublic
	if link, err := os.Readlink(prodPublic); err == nil {
		src = link
	}
	if _, err := os.Stat(src); err != nil {
		return nil, fmt.Errorf("production content missing: %w", err)
	}

	if err := os.RemoveAll(stagingPublic); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(stagingPublic, 0o755); err != nil {
		return nil, err
	}
	if err := copyTree(src, stagingPublic); err != nil {
		return nil, fmt.Errorf("copy files: %w", err)
	}

	// Staging tree must be owned by the staging site's user.
	if uid, gid, err := siteOwnerIDs(stagingWebsiteID); err == nil {
		_ = chownRecursive(stagingBase, uid, gid)
	}

	// Databases: dump prod (root socket auth) and restore into staging db.
	var cloned []string
	for _, db := range databases {
		if !safeIdentifier(db.SourceName) || !safeIdentifier(db.TargetName) || !safeIdentifier(db.User) {
			return nil, fmt.Errorf("invalid database identifiers for clone")
		}
		switch db.Engine {
		case "mysql", "mariadb":
			if err := e.waitForMySQL(ctx); err != nil {
				return nil, err
			}
			dumpFile := filepath.Join("/tmp", "epicpanel-clone-"+db.SourceName+".sql")
			if err := e.run(ctx, "bash", "-c", fmt.Sprintf("mysqldump -u root %s > %s", db.SourceName, dumpFile)); err != nil {
				return nil, fmt.Errorf("mysqldump %s: %w", db.SourceName, err)
			}
			if err := e.mysqlRootExec(ctx, "CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci", db.TargetName); err != nil {
				return nil, err
			}
			if err := e.run(ctx, "bash", "-c", fmt.Sprintf("mysql -u root %s < %s", db.TargetName, dumpFile)); err != nil {
				_ = os.Remove(dumpFile)
				return nil, fmt.Errorf("mysql restore %s: %w", db.TargetName, err)
			}
			// Grant the staging site's db user access to the clone.
			_ = e.mysqlRootExec(ctx, "GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'localhost'", db.TargetName, db.User)
			_ = e.mysqlRootExec(ctx, "FLUSH PRIVILEGES")
			_ = os.Remove(dumpFile)
			cloned = append(cloned, db.SourceName+"->"+db.TargetName)
		case "postgresql":
			if err := e.waitForPostgres(ctx); err != nil {
				return nil, err
			}
			dumpFile := filepath.Join("/tmp", "epicpanel-clone-"+db.SourceName+".sql")
			if err := e.run(ctx, "sudo", "-u", "postgres", "pg_dump", db.SourceName, "-f", dumpFile); err != nil {
				return nil, fmt.Errorf("pg_dump %s: %w", db.SourceName, err)
			}
			_ = e.run(ctx, "sudo", "-u", "postgres", "dropdb", "--if-exists", db.TargetName)
			if err := e.run(ctx, "sudo", "-u", "postgres", "createdb", db.TargetName); err != nil {
				return nil, fmt.Errorf("createdb %s: %w", db.TargetName, err)
			}
			if err := e.run(ctx, "sudo", "-u", "postgres", "psql", "-d", db.TargetName, "-f", dumpFile); err != nil {
				_ = os.Remove(dumpFile)
				return nil, fmt.Errorf("psql restore %s: %w", db.TargetName, err)
			}
			_ = e.run(ctx, "sudo", "-u", "postgres", "psql", "-d", db.TargetName, "-c", fmt.Sprintf("GRANT ALL ON SCHEMA public TO %s", db.User))
			_ = os.Remove(dumpFile)
			cloned = append(cloned, db.SourceName+"->"+db.TargetName)
		default:
			return nil, fmt.Errorf("unsupported engine %q", db.Engine)
		}
	}

	slog.Info("staging cloned", "from", prodWebsiteID, "to", stagingWebsiteID, "databases", len(cloned))
	return &CloneOutcome{FilesCopied: true, Databases: cloned}, nil
}

// PromoteStaging copies staging content back onto production (files via the
// release mechanism: creates a new release from staging public content) and
// restores staging databases over production ones.
func (e *Executor) PromoteStaging(ctx context.Context, stagingWebsiteID, prodWebsiteID string, databases []DBClone) (*CloneOutcome, error) {
	if err := uuidParseID(stagingWebsiteID); err != nil {
		return nil, fmt.Errorf("invalid staging website id")
	}
	if err := uuidParseID(prodWebsiteID); err != nil {
		return nil, fmt.Errorf("invalid production website id")
	}

	stagingPublic := filepath.Join("/srv/epicpanel/websites", stagingWebsiteID, "public")
	src := stagingPublic
	if link, err := os.Readlink(stagingPublic); err == nil {
		src = link
	}
	if _, err := os.Stat(src); err != nil {
		return nil, fmt.Errorf("staging content missing: %w", err)
	}

	// New production release from staging content (atomic switch + rollbackable).
	releasesDir := siteReleasesDir(prodWebsiteID)
	if err := os.MkdirAll(releasesDir, 0o755); err != nil {
		return nil, err
	}
	releaseID, err := randomURLSafe(6)
	if err != nil {
		return nil, err
	}
	releaseDir := filepath.Join(releasesDir, time.Now().UTC().Format("20060102-150405")+"-promote-"+releaseID)
	if err := copyTree(src, releaseDir); err != nil {
		return nil, err
	}
	if uid, gid, err := siteOwnerIDs(prodWebsiteID); err == nil {
		_ = chownRecursive(releaseDir, uid, gid)
	}
	publicLink := filepath.Join("/srv/epicpanel/websites", prodWebsiteID, "public")
	tmpLink := publicLink + ".new"
	_ = os.Remove(tmpLink)
	if err := os.Symlink(releaseDir, tmpLink); err != nil {
		return nil, err
	}
	if err := os.Rename(tmpLink, publicLink); err != nil {
		return nil, err
	}
	e.pruneReleases(releasesDir, 5)

	var promoted []string
	for _, db := range databases {
		if !safeIdentifier(db.SourceName) || !safeIdentifier(db.TargetName) {
			return nil, fmt.Errorf("invalid database identifiers for promote")
		}
		switch db.Engine {
		case "mysql", "mariadb":
			if err := e.waitForMySQL(ctx); err != nil {
				return nil, err
			}
			dumpFile := filepath.Join("/tmp", "epicpanel-promote-"+db.SourceName+".sql")
			if err := e.run(ctx, "bash", "-c", fmt.Sprintf("mysqldump -u root %s > %s", db.SourceName, dumpFile)); err != nil {
				return nil, fmt.Errorf("mysqldump staging %s: %w", db.SourceName, err)
			}
			if err := e.mysqlRootExec(ctx, "CREATE DATABASE IF NOT EXISTS `%s`", db.TargetName); err != nil {
				return nil, err
			}
			if err := e.run(ctx, "bash", "-c", fmt.Sprintf("mysql -u root %s < %s", db.TargetName, dumpFile)); err != nil {
				_ = os.Remove(dumpFile)
				return nil, fmt.Errorf("promote restore %s: %w", db.TargetName, err)
			}
			_ = os.Remove(dumpFile)
			promoted = append(promoted, db.SourceName+"->"+db.TargetName)
		case "postgresql":
			if err := e.waitForPostgres(ctx); err != nil {
				return nil, err
			}
			dumpFile := filepath.Join("/tmp", "epicpanel-promote-"+db.SourceName+".sql")
			if err := e.run(ctx, "sudo", "-u", "postgres", "pg_dump", db.SourceName, "-f", dumpFile); err != nil {
				return nil, err
			}
			_ = e.run(ctx, "sudo", "-u", "postgres", "dropdb", "--if-exists", db.TargetName)
			if err := e.run(ctx, "sudo", "-u", "postgres", "createdb", db.TargetName); err != nil {
				return nil, err
			}
			if err := e.run(ctx, "sudo", "-u", "postgres", "psql", "-d", db.TargetName, "-f", dumpFile); err != nil {
				_ = os.Remove(dumpFile)
				return nil, err
			}
			_ = os.Remove(dumpFile)
			promoted = append(promoted, db.SourceName+"->"+db.TargetName)
		}
	}

	slog.Info("staging promoted", "from", stagingWebsiteID, "to", prodWebsiteID, "databases", len(promoted))
	return &CloneOutcome{FilesCopied: true, Databases: promoted}, nil
}

// DBClone describes one database pair for staging clone/promote.
type DBClone struct {
	Engine     string
	SourceName string // production db (clone) / staging db (promote)
	TargetName string
	User       string // db user of the TARGET site
}

func uuidParseID(s string) error {
	_, err := uuid.Parse(s)
	return err
}
