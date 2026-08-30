package domains

import "github.com/jackc/pgx/v5/pgconn"

// isUniqueViolation reports PostgreSQL SQLSTATE 23505.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if ok := errorsAsPg(err, &pgErr); ok {
		return pgErr.Code == "23505"
	}
	return false
}

// isForeignKeyViolation reports PostgreSQL SQLSTATE 23503.
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	if ok := errorsAsPg(err, &pgErr); ok {
		return pgErr.Code == "23503"
	}
	return false
}

func errorsAsPg(err error, target **pgconn.PgError) bool {
	for err != nil {
		if e, ok := err.(*pgconn.PgError); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
