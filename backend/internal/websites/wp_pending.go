package websites

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WPPending holds the admin details of a requested WordPress install until
// its auto-created database becomes ready (the install job chains off that
// event, because the DB credential only exists after the agent reports it).
type WPPending struct {
	DBID       uuid.UUID `json:"db_id"`
	WebsiteID  uuid.UUID `json:"website_id"`
	Title      string    `json:"title"`
	AdminUser  string    `json:"admin_user"`
	AdminEmail string    `json:"admin_email"`
}

type WPPendingStore struct {
	Pool *pgxpool.Pool
}

const wpPendingCols = `db_id, website_id, title, admin_user, admin_email`

func scanWPPending(row pgx.Row) (*WPPending, error) {
	var w WPPending
	err := row.Scan(&w.DBID, &w.WebsiteID, &w.Title, &w.AdminUser, &w.AdminEmail)
	if err != nil {
		return nil, err
	}
	return &w, nil
}

func (s *WPPendingStore) Create(ctx context.Context, p WPPending) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO wp_pending (db_id, website_id, title, admin_user, admin_email)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (db_id) DO UPDATE SET website_id = $2, title = $3, admin_user = $4, admin_email = $5
	`, p.DBID, p.WebsiteID, p.Title, p.AdminUser, p.AdminEmail)
	return err
}

func (s *WPPendingStore) Get(ctx context.Context, dbID uuid.UUID) (*WPPending, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+wpPendingCols+` FROM wp_pending WHERE db_id = $1`, dbID)
	w, err := scanWPPending(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return w, err
}

func (s *WPPendingStore) Delete(ctx context.Context, dbID uuid.UUID) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM wp_pending WHERE db_id = $1`, dbID)
	return err
}
