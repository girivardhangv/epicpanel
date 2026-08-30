// Package dbops performs managed-database DDL (MySQL/MariaDB and PostgreSQL)
// from the agent using the official Go database drivers — never a shell.
// Identifiers are validated against a strict charset and quoted per engine;
// generated passwords use a symbol-free alphabet so they never need escaping.
package dbops

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Engine identifiers.
const (
	EngineMySQL    = "mysql"
	EnginePostgres = "postgres"
)

// AdminConfig holds the agent's privileged credentials for one engine.
type AdminConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	SSLMode  string // postgres only (disable|require); default disable
}

// Ops is the typed database-administration surface.
type Ops interface {
	Engine() string
	Ping(ctx context.Context) error
	Version(ctx context.Context) (string, error)
	CreateDatabase(ctx context.Context, name string) error
	DropDatabase(ctx context.Context, name string) error
	DatabaseExists(ctx context.Context, name string) (bool, error)
	ListDatabases(ctx context.Context) ([]string, error)
	CreateUser(ctx context.Context, dbName, username, password string) error
	DropUser(ctx context.Context, username string) error
	SetUserPassword(ctx context.Context, username, password string) error
	Grant(ctx context.Context, dbName, username string) error
}

// Identifier validators — lowercase, letter-initial, underscore-safe. This
// charset is safe for both engines and sidesteps case-folding and quoting
// edge cases entirely.
var (
	dbNameRe   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	userNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
)

// ValidateDBName / ValidateUserName are exported for the panel-side mirror.
func ValidateDBName(name string) error {
	if !dbNameRe.MatchString(name) {
		return fmt.Errorf("invalid database name %q (lowercase letters, digits, underscore; start with a letter; max 63)", name)
	}
	return nil
}

func ValidateUserName(name string) error {
	if !userNameRe.MatchString(name) {
		return fmt.Errorf("invalid database user name %q (lowercase letters, digits, underscore; start with a letter; max 32)", name)
	}
	return nil
}

// GeneratePassword returns a 20-char password from a symbol-free alphabet so
// it is safe to embed in DDL without escaping.
func GeneratePassword() (string, error) {
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	out := make([]byte, 20)
	max := big.NewInt(int64(len(alphabet)))
	for i := range out {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		out[i] = alphabet[n.Int64()]
	}
	return string(out), nil
}

// New builds the Ops implementation for an engine.
func New(engine string, cfg AdminConfig) (Ops, error) {
	switch engine {
	case EngineMySQL:
		return &mysqlOps{cfg: cfg}, nil
	case EnginePostgres:
		return &postgresOps{cfg: cfg}, nil
	default:
		return nil, fmt.Errorf("unsupported engine %q", engine)
	}
}

// ---------------------------------------------------------------------------
// MySQL / MariaDB
// ---------------------------------------------------------------------------

type mysqlOps struct{ cfg AdminConfig }

func (o *mysqlOps) Engine() string { return EngineMySQL }

func (o *mysqlOps) dsn(db string) string {
	port := o.cfg.Port
	if port == 0 {
		port = 3306
	}
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?timeout=10s&parseTime=true",
		o.cfg.User, o.cfg.Password, o.cfg.Host, port, db)
}

func (o *mysqlOps) open(db string) (*sql.DB, error) {
	d, err := sql.Open("mysql", o.dsn(db))
	if err != nil {
		return nil, err
	}
	d.SetConnMaxLifetime(30 * time.Second)
	d.SetMaxOpenConns(2)
	return d, nil
}

func (o *mysqlOps) Ping(ctx context.Context) error {
	d, err := o.open("")
	if err != nil {
		return err
	}
	defer d.Close()
	return d.PingContext(ctx)
}

func (o *mysqlOps) Version(ctx context.Context) (string, error) {
	d, err := o.open("")
	if err != nil {
		return "", err
	}
	defer d.Close()
	var v string
	err = d.QueryRowContext(ctx, "SELECT VERSION()").Scan(&v)
	return v, err
}

func (o *mysqlOps) CreateDatabase(ctx context.Context, name string) error {
	if err := ValidateDBName(name); err != nil {
		return err
	}
	return o.exec(ctx, fmt.Sprintf("CREATE DATABASE IF NOT EXISTS `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci", name))
}

func (o *mysqlOps) DropDatabase(ctx context.Context, name string) error {
	if err := ValidateDBName(name); err != nil {
		return err
	}
	return o.exec(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS `%s`", name))
}

func (o *mysqlOps) DatabaseExists(ctx context.Context, name string) (bool, error) {
	d, err := o.open("")
	if err != nil {
		return false, err
	}
	defer d.Close()
	var n int
	err = d.QueryRowContext(ctx,
		`SELECT count(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = ?`, name).Scan(&n)
	return n > 0, err
}

func (o *mysqlOps) ListDatabases(ctx context.Context) ([]string, error) {
	d, err := o.open("")
	if err != nil {
		return nil, err
	}
	defer d.Close()
	rows, err := d.QueryContext(ctx,
		`SELECT SCHEMA_NAME FROM information_schema.SCHEMATA
		 WHERE SCHEMA_NAME NOT IN ('information_schema','mysql','performance_schema','sys')
		 ORDER BY SCHEMA_NAME`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// CreateUser creates the user for both localhost and 127.0.0.1 (socket vs TCP)
// and grants it access to the database.
func (o *mysqlOps) CreateUser(ctx context.Context, dbName, username, password string) error {
	if err := ValidateDBName(dbName); err != nil {
		return err
	}
	if err := ValidateUserName(username); err != nil {
		return err
	}
	if !isSafePassword(password) {
		return errors.New("password contains disallowed characters")
	}
	for _, host := range []string{"localhost", "127.0.0.1"} {
		if err := o.exec(ctx, fmt.Sprintf(
			"CREATE USER IF NOT EXISTS '%s'@'%s' IDENTIFIED BY '%s'", username, host, password)); err != nil {
			return err
		}
		if err := o.exec(ctx, fmt.Sprintf(
			"GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'%s'", dbName, username, host)); err != nil {
			return err
		}
	}
	return o.exec(ctx, "FLUSH PRIVILEGES")
}

func (o *mysqlOps) DropUser(ctx context.Context, username string) error {
	if err := ValidateUserName(username); err != nil {
		return err
	}
	for _, host := range []string{"localhost", "127.0.0.1"} {
		if err := o.exec(ctx, fmt.Sprintf("DROP USER IF EXISTS '%s'@'%s'", username, host)); err != nil {
			return err
		}
	}
	return nil
}

func (o *mysqlOps) SetUserPassword(ctx context.Context, username, password string) error {
	if err := ValidateUserName(username); err != nil {
		return err
	}
	if !isSafePassword(password) {
		return errors.New("password contains disallowed characters")
	}
	for _, host := range []string{"localhost", "127.0.0.1"} {
		if err := o.exec(ctx, fmt.Sprintf(
			"ALTER USER '%s'@'%s' IDENTIFIED BY '%s'", username, host, password)); err != nil {
			return err
		}
	}
	return nil
}

func (o *mysqlOps) Grant(ctx context.Context, dbName, username string) error {
	if err := ValidateDBName(dbName); err != nil {
		return err
	}
	if err := ValidateUserName(username); err != nil {
		return err
	}
	for _, host := range []string{"localhost", "127.0.0.1"} {
		if err := o.exec(ctx, fmt.Sprintf(
			"GRANT ALL PRIVILEGES ON `%s`.* TO '%s'@'%s'", dbName, username, host)); err != nil {
			return err
		}
	}
	return nil
}

func (o *mysqlOps) exec(ctx context.Context, q string) error {
	d, err := o.open("")
	if err != nil {
		return err
	}
	defer d.Close()
	_, err = d.ExecContext(ctx, q)
	return err
}

// ---------------------------------------------------------------------------
// PostgreSQL
// ---------------------------------------------------------------------------

type postgresOps struct{ cfg AdminConfig }

func (o *postgresOps) Engine() string { return EnginePostgres }

func (o *postgresOps) dsn(db string) string {
	ssl := o.cfg.SSLMode
	if ssl == "" {
		ssl = "disable"
	}
	port := o.cfg.Port
	if port == 0 {
		port = 5432
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s&connect_timeout=10",
		o.cfg.User, o.cfg.Password, o.cfg.Host, port, db, ssl)
}

func (o *postgresOps) open(db string) (*sql.DB, error) {
	d, err := sql.Open("pgx", o.dsn(db))
	if err != nil {
		return nil, err
	}
	d.SetConnMaxLifetime(30 * time.Second)
	d.SetMaxOpenConns(2)
	return d, nil
}

func (o *postgresOps) Ping(ctx context.Context) error {
	d, err := o.open("postgres")
	if err != nil {
		return err
	}
	defer d.Close()
	return d.PingContext(ctx)
}

func (o *postgresOps) Version(ctx context.Context) (string, error) {
	d, err := o.open("postgres")
	if err != nil {
		return "", err
	}
	defer d.Close()
	var v string
	err = d.QueryRowContext(ctx, "SELECT version()").Scan(&v)
	return v, err
}

// CREATE/DROP DATABASE cannot run inside a transaction; database/sql Exec is
// autocommit, so this is safe.
func (o *postgresOps) CreateDatabase(ctx context.Context, name string) error {
	if err := ValidateDBName(name); err != nil {
		return err
	}
	exists, err := o.DatabaseExists(ctx, name)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	return o.exec(ctx, fmt.Sprintf(`CREATE DATABASE "%s"`, name))
}

func (o *postgresOps) DropDatabase(ctx context.Context, name string) error {
	if err := ValidateDBName(name); err != nil {
		return err
	}
	return o.exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS "%s"`, name))
}

func (o *postgresOps) DatabaseExists(ctx context.Context, name string) (bool, error) {
	d, err := o.open("postgres")
	if err != nil {
		return false, err
	}
	defer d.Close()
	var n int
	err = d.QueryRowContext(ctx, `SELECT count(*) FROM pg_database WHERE datname = $1`, name).Scan(&n)
	return n > 0, err
}

func (o *postgresOps) ListDatabases(ctx context.Context) ([]string, error) {
	d, err := o.open("postgres")
	if err != nil {
		return nil, err
	}
	defer d.Close()
	rows, err := d.QueryContext(ctx,
		`SELECT datname FROM pg_database WHERE NOT datistemplate AND datname <> 'postgres' ORDER BY datname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (o *postgresOps) CreateUser(ctx context.Context, dbName, username, password string) error {
	if err := ValidateDBName(dbName); err != nil {
		return err
	}
	if err := ValidateUserName(username); err != nil {
		return err
	}
	if !isSafePassword(password) {
		return errors.New("password contains disallowed characters")
	}
	// Role is global in Postgres; create if absent, then grant on the DB.
	if err := o.exec(ctx, fmt.Sprintf(
		`DO $$ BEGIN CREATE ROLE "%s" LOGIN PASSWORD '%s'; EXCEPTION WHEN duplicate_object THEN NULL; END $$;`,
		username, password)); err != nil {
		return err
	}
	return o.Grant(ctx, dbName, username)
}

func (o *postgresOps) DropUser(ctx context.Context, username string) error {
	if err := ValidateUserName(username); err != nil {
		return err
	}
	return o.exec(ctx, fmt.Sprintf(`DROP ROLE IF EXISTS "%s"`, username))
}

func (o *postgresOps) SetUserPassword(ctx context.Context, username, password string) error {
	if err := ValidateUserName(username); err != nil {
		return err
	}
	if !isSafePassword(password) {
		return errors.New("password contains disallowed characters")
	}
	return o.exec(ctx, fmt.Sprintf(`ALTER ROLE "%s" WITH LOGIN PASSWORD '%s'`, username, password))
}

func (o *postgresOps) Grant(ctx context.Context, dbName, username string) error {
	if err := ValidateDBName(dbName); err != nil {
		return err
	}
	if err := ValidateUserName(username); err != nil {
		return err
	}
	if err := o.exec(ctx, fmt.Sprintf(`GRANT ALL PRIVILEGES ON DATABASE "%s" TO "%s"`, dbName, username)); err != nil {
		return err
	}
	// Give the role schema ownership so it can create tables (PG15+ needs this).
	return o.exec(ctx, fmt.Sprintf(`ALTER DATABASE "%s" OWNER TO "%s"`, dbName, username))
}

func (o *postgresOps) exec(ctx context.Context, q string) error {
	d, err := o.open("postgres")
	if err != nil {
		return err
	}
	defer d.Close()
	_, err = d.ExecContext(ctx, q)
	return err
}

// ---------------------------------------------------------------------------
// shared helpers
// ---------------------------------------------------------------------------

// isSafePassword ensures the generated password contains no characters that
// would need escaping inside a quoted DDL literal.
func isSafePassword(p string) bool {
	if p == "" || len(p) > 64 {
		return false
	}
	for _, r := range p {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
