-- App-stack installer jobs: one-click Laravel (composer create-project with the
-- site's selected PHP version) and allowlisted user commands (composer/npm/
-- artisan/node/php...) executed agent-side as the site user.
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'install_laravel';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'site_command';
