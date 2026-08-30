-- Phase 7: software manager. Software state is detected live from the agent,
-- so no new tables are needed; installs/removals run as background jobs.
ALTER TABLE jobs DROP CONSTRAINT IF EXISTS jobs_type_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_type_check CHECK (type IN (
    'provision_website', 'reconfigure_website', 'delete_website',
    'issue_ssl', 'notify_alert',
    'provision_database', 'delete_database',
    'install_software', 'remove_software'
));
