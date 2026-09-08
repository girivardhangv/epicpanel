package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

const backupsBase = "/srv/epicpanel/backups"

// BackupOutcome is the job result for create_backup.
type BackupOutcome struct {
	ArchivePath string   `json:"archive_path"`
	SizeBytes   int64    `json:"size_bytes"`
	Databases   []string `json:"databases"`
	CreatedAt   string   `json:"created_at"`
}

// CreateBackup tars the site tree (resolving symlinks for the live release)
// and dumps attached databases, producing a single archive:
// <archive>/<backupid>/site.tar.gz + db-<name>.sql.gz + manifest.json
func (e *Executor) CreateBackup(ctx context.Context, backupID, websiteID string, databases []DBClone) (*BackupOutcome, error) {
	if err := uuidCheckErr(backupID); err != nil {
		return nil, fmt.Errorf("invalid backup id")
	}
	if err := uuidCheckErr(websiteID); err != nil {
		return nil, fmt.Errorf("invalid website id")
	}
	siteBase := filepath.Join("/srv/epicpanel/websites", websiteID)
	if _, err := os.Stat(siteBase); err != nil {
		return nil, fmt.Errorf("site tree missing: %w", err)
	}
	archiveDir := filepath.Join(backupsBase, backupID)
	if err := os.MkdirAll(archiveDir, 0o750); err != nil {
		return nil, err
	}

	// Files: tar the whole site tree (including current release via symlink
	// dereference) as site.tar.gz.
	siteTar := filepath.Join(archiveDir, "site.tar.gz")
	if err := e.run(ctx, "tar", "-czf", siteTar, "-h", "-C", filepath.Dir(siteBase), filepath.Base(siteBase)); err != nil {
		return nil, fmt.Errorf("tar site: %w", err)
	}

	var dbs []string
	for _, db := range databases {
		if !safeIdentifier(db.SourceName) {
			return nil, fmt.Errorf("invalid database identifier")
		}
		dumpName := "db-" + db.SourceName + ".sql.gz"
		dumpFile := filepath.Join(archiveDir, dumpName)
		switch db.Engine {
		case "mysql", "mariadb":
			if err := e.waitForMySQL(ctx); err != nil {
				return nil, err
			}
			if err := e.run(ctx, "bash", "-c", fmt.Sprintf("mysqldump -u root --single-transaction %s | gzip > %s", db.SourceName, dumpFile)); err != nil {
				return nil, fmt.Errorf("mysqldump %s: %w", db.SourceName, err)
			}
		case "postgresql":
			if err := e.waitForPostgres(ctx); err != nil {
				return nil, err
			}
			plain := filepath.Join(archiveDir, "db-"+db.SourceName+".sql")
			if err := e.run(ctx, "sudo", "-u", "postgres", "pg_dump", db.SourceName, "-f", plain); err != nil {
				return nil, fmt.Errorf("pg_dump %s: %w", db.SourceName, err)
			}
			if err := e.run(ctx, "gzip", "-f", plain); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unsupported engine %q", db.Engine)
		}
		dbs = append(dbs, dumpName)
	}

	manifest := map[string]any{
		"website_id": websiteID,
		"created_at": time.Now().UTC().Format(time.RFC3339),
		"site_tar":   "site.tar.gz",
		"databases":  dbs,
	}
	mb, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(archiveDir, "manifest.json"), mb, 0o640); err != nil {
		return nil, err
	}

	size := dirSize(archiveDir)
	slog.Info("backup created", "backup", backupID, "size", size, "databases", len(dbs))
	return &BackupOutcome{ArchivePath: archiveDir, SizeBytes: size, Databases: dbs, CreatedAt: time.Now().UTC().Format(time.RFC3339)}, nil
}

// RestoreBackup restores a site tree + databases from an archive.
func (e *Executor) RestoreBackup(ctx context.Context, backupID, websiteID string, databases []DBClone) error {
	if err := uuidCheckErr(backupID); err != nil {
		return fmt.Errorf("invalid backup id")
	}
	archiveDir := filepath.Join(backupsBase, backupID)
	siteTar := filepath.Join(archiveDir, "site.tar.gz")
	if _, err := os.Stat(siteTar); err != nil {
		return fmt.Errorf("archive missing: %w", err)
	}

	siteBase := filepath.Join("/srv/epicpanel/websites", websiteID)
	if _, err := os.Stat(siteBase); err != nil {
		return fmt.Errorf("site tree missing: %w", err)
	}

	// Restore files: extract into the site's parent (archive contains websites/<id>/...).
	if err := e.run(ctx, "tar", "-xzf", siteTar, "-C", filepath.Dir(siteBase)); err != nil {
		return fmt.Errorf("untar site: %w", err)
	}

	for _, db := range databases {
		dumpFile := filepath.Join(archiveDir, "db-"+db.TargetName+".sql.gz")
		if _, err := os.Stat(dumpFile); err != nil {
			continue
		}
		switch db.Engine {
		case "mysql", "mariadb":
			if err := e.waitForMySQL(ctx); err != nil {
				return err
			}
			_ = e.mysqlRootExec(ctx, "CREATE DATABASE IF NOT EXISTS `%s`", db.TargetName)
			if err := e.run(ctx, "bash", "-c", fmt.Sprintf("gunzip -c %s | mysql -u root %s", dumpFile, db.TargetName)); err != nil {
				return fmt.Errorf("mysql restore %s: %w", db.TargetName, err)
			}
		case "postgresql":
			if err := e.waitForPostgres(ctx); err != nil {
				return err
			}
			_ = e.run(ctx, "sudo", "-u", "postgres", "dropdb", "--if-exists", db.TargetName)
			if err := e.run(ctx, "sudo", "-u", "postgres", "createdb", db.TargetName); err != nil {
				return err
			}
			if err := e.run(ctx, "bash", "-c", fmt.Sprintf("gunzip -c %s | sudo -u postgres psql -d %s", dumpFile, db.TargetName)); err != nil {
				return fmt.Errorf("psql restore %s: %w", db.TargetName, err)
			}
		}
	}
	slog.Info("backup restored", "backup", backupID, "website", websiteID)
	return nil
}

func uuidCheckErr(s string) error {
	_, err := uuid.Parse(s)
	return err
}

func dirSize(path string) int64 {
	var total int64
	_ = filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total
}
