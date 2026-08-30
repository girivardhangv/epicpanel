package websites

import "github.com/jackc/pgx/v5/pgconn"

// isUniqueViolation reports PostgreSQL SQLSTATE 23505.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if e, ok := err.(*pgconn.PgError); ok {
		pgErr = e
	}
	return pgErr != nil && pgErr.Code == "23505"
}
