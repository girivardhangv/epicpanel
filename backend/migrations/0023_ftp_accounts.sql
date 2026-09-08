-- 0023: Phase 4 FTP/SFTP accounts. Accounts are chrooted per website
-- (SFTP: OpenSSH internal-sftp without shell, FTP: vsftpd). Passwords are
-- stored secretbox-encrypted; sync jobs deliver a one-way SHA-512-crypt
-- hash only.

CREATE TABLE IF NOT EXISTS ftp_accounts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    website_id UUID NOT NULL REFERENCES websites (id) ON DELETE CASCADE,
    organization_id UUID NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    protocol TEXT NOT NULL CHECK (protocol IN ('ftp', 'sftp')),
    label TEXT NOT NULL CHECK (length(label) BETWEEN 1 AND 60),
    user_name TEXT NOT NULL UNIQUE,
    password_enc BYTEA NOT NULL,
    password_fingerprint TEXT NOT NULL DEFAULT '',
    home_subdir TEXT NOT NULL DEFAULT '' CHECK (length(home_subdir) <= 100),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'active', 'failed')),
    error_message TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (website_id, protocol, label)
);

CREATE INDEX IF NOT EXISTS idx_ftp_accounts_website ON ftp_accounts (website_id);

ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'sync_ftp_accounts';
