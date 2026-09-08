ALTER TABLE websites ADD COLUMN web_server TEXT NOT NULL DEFAULT 'nginx' CHECK (web_server IN ('nginx', 'openlitespeed', 'apache', 'none'));
