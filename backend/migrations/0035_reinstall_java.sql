-- Reinstall support for first-class workloads + Java selection parity.
-- mc_reinstall / bot_reinstall are long-running install jobs (30-minute
-- lease in jobs.ClaimNext) and must exist as job types for the API to enqueue
-- them.
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'mc_reinstall';
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'bot_reinstall';

-- Discord: Java is removed as a bot runtime (customer request). The runtime
-- registry no longer offers it; drop the CHECK value so no future row can be
-- created with it and existing foundation rows are converted to failed.
DO $$ BEGIN
    ALTER TABLE bot_instances DROP CONSTRAINT IF EXISTS bot_instances_runtime_check;
    ALTER TABLE bot_instances ADD CONSTRAINT bot_instances_runtime_check
        CHECK (runtime IN ('node', 'python'));
EXCEPTION WHEN others THEN NULL;
END $$;

UPDATE bot_instances SET status = 'failed',
    last_error = 'java runtime removed; recreate this bot with node or python'
WHERE runtime = 'java' AND status <> 'deleted';