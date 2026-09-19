package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// CreateDatabase installs the engine if needed and creates the database and
// user with all privileges scoped to that database only. Idempotent: existing
// db/user short-circuit. SQL is built ONLY from control-plane-validated
// identifiers (regex ^ep_[a-z0-9_]+$) — no free-form SQL ever reaches here.
func (e *Executor) CreateDatabase(ctx context.Context, engine, dbName, dbUser string) (string, error) {
	if !safeIdentifier(dbName) || !safeIdentifier(dbUser) {
		return "", fmt.Errorf("invalid database identifiers")
	}
	password, err := generatePassword()
	if err != nil {
		return "", err
	}

	switch engine {
	case "mysql", "mariadb":
		err = e.createMySQLDB(ctx, dbName, dbUser, password)
	case "postgresql":
		err = e.createPostgresDB(ctx, dbName, dbUser, password)
	default:
		return "", fmt.Errorf("unsupported database engine %q", engine)
	}
	if err != nil {
		return "", err
	}
	slog.Info("database created", "engine", engine, "db", dbName, "user", dbUser)
	return password, nil
}

func (e *Executor) createMySQLDB(ctx context.Context, dbName, dbUser, password string) error {
	if err := e.InstallDatabaseEngine(ctx, "mariadb"); err != nil {
		return err
	}
	// Wait for the socket to accept queries (fresh install may still be starting).
	if err := e.waitForMySQL(ctx); err != nil {
		return err
	}

	// Idempotency checks via information_schema.
	var dbExists, userExists bool
	err := e.mysqlRootQueryRow(ctx, &dbExists,
		`SELECT COUNT(*)>0 FROM information_schema.schemata WHERE schema_name='%s'`, dbName)
	if err != nil {
		return fmt.Errorf("check db: %w", err)
	}
	err = e.mysqlRootQueryRow(ctx, &userExists,
		`SELECT COUNT(*)>0 FROM mysql.user WHERE user='%s' AND host='localhost'`, dbUser)
	if err != nil {
		return fmt.Errorf("check user: %w", err)
	}

	if !dbExists {
		if err := e.mysqlRootExec(ctx, "CREATE DATABASE `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci", dbName); err != nil {
			return fmt.Errorf("create database: %w", err)
		}
	}
	if !userExists {
		if err := e.mysqlRootExec(ctx, "CREATE USER '%s'@'localhost' IDENTIFIED BY '%s'", dbUser, password); err != nil {
			// Race: user created between check and create.
			if !strings.Contains(err.Error(), "already exists") {
				return fmt.Errorf("create user: %w", err)
			}
		}
	} else {
		if err := e.mysqlRootExec(ctx, "ALTER USER '%s'@'localhost' IDENTIFIED BY '%s'", dbUser, password); err != nil {
			return fmt.Errorf("reset user password: %w", err)
		}
	}
	if err := e.mysqlRootExec(ctx, "GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'localhost'", dbName, dbUser); err != nil {
		return fmt.Errorf("grant: %w", err)
	}
	if err := e.mysqlRootExec(ctx, "FLUSH PRIVILEGES"); err != nil {
		return fmt.Errorf("flush: %w", err)
	}
	return nil
}

func (e *Executor) createPostgresDB(ctx context.Context, dbName, dbUser, password string) error {
	if err := e.InstallDatabaseEngine(ctx, "postgresql"); err != nil {
		return err
	}
	if err := e.waitForPostgres(ctx); err != nil {
		return err
	}

	var exists bool
	if err := e.psqlRootQueryRow(ctx, &exists, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname='%s')", dbName); err != nil {
		return fmt.Errorf("check db: %w", err)
	}
	if !exists {
		if err := e.run(ctx, "sudo", "-u", "postgres", "createdb", dbName); err != nil {
			if !strings.Contains(err.Error(), "already exists") {
				return fmt.Errorf("createdb: %w", err)
			}
		}
	}

	var roleExists bool
	if err := e.psqlRootQueryRow(ctx, &roleExists, "SELECT COUNT(*)>0 FROM pg_roles WHERE rolname='%s'", dbUser); err != nil {
		return fmt.Errorf("check role: %w", err)
	}
	if roleExists {
		if err := e.run(ctx, "sudo", "-u", "postgres", "psql", "-c",
			fmt.Sprintf("ALTER ROLE %s WITH LOGIN PASSWORD '%s'", dbUser, password)); err != nil {
			return fmt.Errorf("alter role: %w", err)
		}
	} else {
		if err := e.run(ctx, "sudo", "-u", "postgres", "psql", "-c",
			fmt.Sprintf("CREATE ROLE %s WITH LOGIN PASSWORD '%s'", dbUser, password)); err != nil {
			if !strings.Contains(err.Error(), "already exists") {
				return fmt.Errorf("create role: %w", err)
			}
		}
	}
	if err := e.run(ctx, "sudo", "-u", "postgres", "psql", "-c",
		fmt.Sprintf("GRANT ALL PRIVILEGES ON DATABASE %s TO %s", dbName, dbUser)); err != nil {
		return fmt.Errorf("grant: %w", err)
	}
	// PG15+: ensure the role can use the public schema of this db.
	if err := e.run(ctx, "sudo", "-u", "postgres", "psql", "-d", dbName, "-c",
		fmt.Sprintf("GRANT ALL ON SCHEMA public TO %s", dbUser)); err != nil {
		return fmt.Errorf("grant schema: %w", err)
	}
	return nil
}

func (e *Executor) psqlRootQueryRow(ctx context.Context, dst *bool, format string, args ...any) error {
	sqlText := fmt.Sprintf(format, args...)
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, "sudo", "-u", "postgres", "psql", "-tAc", sqlText)
	out, err := cmd.Output()
	if err != nil {
		return err
	}
	*dst = strings.TrimSpace(string(out)) == "t" || strings.TrimSpace(string(out)) == "1"
	return nil
}

// DeleteDatabase drops the database and the user (idempotent).
func (e *Executor) DeleteDatabase(ctx context.Context, engine, dbName, dbUser string) error {
	if !safeIdentifier(dbName) || !safeIdentifier(dbUser) {
		return fmt.Errorf("invalid database identifiers")
	}
	switch engine {
	case "mysql", "mariadb":
		if err := e.waitForMySQL(ctx); err == nil {
			_ = e.mysqlRootExec(ctx, "DROP DATABASE IF EXISTS `%s`", dbName)
			_ = e.mysqlRootExec(ctx, "DROP USER IF EXISTS '%s'@'localhost'", dbUser)
			_ = e.mysqlRootExec(ctx, "FLUSH PRIVILEGES")
		}
	case "postgresql":
		if err := e.waitForPostgres(ctx); err == nil {
			_ = e.run(ctx, "sudo", "-u", "postgres", "dropdb", "--if-exists", dbName)
			_ = e.run(ctx, "sudo", "-u", "postgres", "psql", "-c", fmt.Sprintf("DROP ROLE IF EXISTS %s", dbUser))
		}
	default:
		return fmt.Errorf("unsupported database engine %q", engine)
	}
	slog.Info("database deleted", "engine", engine, "db", dbName)
	return nil
}

// InstallDatabaseEngine installs the engine packages via apt (idempotent).
func (e *Executor) InstallDatabaseEngine(ctx context.Context, engine string) error {
	switch engine {
	case "mysql", "mariadb":
		// Both halves must be present: on rpm distros the client can exist
		// alone (el9 ships `mariadb` client / `mariadb-server` separately),
		// and a client-only box would pass the old probe with no server to
		// create databases in.
		client := execExists("mariadb") || execExists("mysql")
		server := execExists("mariadbd") || execExists("mysqld")
		if client && server {
			return nil
		}
		pkgs := []string{"mariadb-server", "mariadb-client"}
		if e.pm.Name() != "apt" {
			pkgs = []string{"mariadb-server"}
		}
		return e.aptInstall(ctx, pkgs, "", engine)
	case "postgresql":
		if execExists("psql") && execExists("postgres") {
			return nil
		}
		// postgresql-contrib is Debian packaging; rpm distros bundle the
		// contrib modules into the server package (postgresql-server).
		pkgs := []string{"postgresql", "postgresql-contrib"}
		if e.pm.Name() != "apt" {
			pkgs = []string{"postgresql-server"}
		}
		return e.aptInstall(ctx, pkgs, "", engine)
	default:
		return fmt.Errorf("unsupported engine %q", engine)
	}
}

func execExists(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

func safeIdentifier(s string) bool {
	if len(s) < 4 || len(s) > 63 {
		return false
	}
	if !strings.HasPrefix(s, "ep_") {
		return false
	}
	for _, c := range s[3:] {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_':
		default:
			return false
		}
	}
	return true
}

func generatePassword() (string, error) {
	return randomURLSafe(24)
}

// waitForMySQL pings until the server answers (max ~60s).
func (e *Executor) waitForMySQL(ctx context.Context) error {
	for i := 0; i < 30; i++ {
		if err := e.mysqlRootExec(ctx, "SELECT 1"); err == nil {
			return nil
		}
		sleepCtx(ctx, 2*1000*1000*1000)
	}
	return fmt.Errorf("mysql did not become ready in time")
}

func (e *Executor) waitForPostgres(ctx context.Context) error {
	for i := 0; i < 30; i++ {
		if err := e.run(ctx, "sudo", "-u", "postgres", "psql", "-tAc", "SELECT 1"); err == nil {
			return nil
		}
		sleepCtx(ctx, 2*1000*1000*1000)
	}
	return fmt.Errorf("postgres did not become ready in time")
}
