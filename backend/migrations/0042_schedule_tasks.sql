-- Schedule tasks (Pterodactyl parity): ordered typed tasks per schedule.
-- A schedule keeps its legacy kind/cron columns; tasks are additive and run
-- in sequence order, then time-offset order. Every action maps to an existing
-- agent job type — power -> mc_start/mc_stop/mc_restart/mc_kill, command ->
-- mc_command (allowlist enforced at create AND fire time), backup ->
-- mc_backup_world.

CREATE TABLE IF NOT EXISTS schedule_tasks (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    schedule_id UUID NOT NULL REFERENCES minecraft_schedules (id) ON DELETE CASCADE,
    action TEXT NOT NULL CHECK (action IN ('power', 'command', 'backup')),
    payload JSONB NOT NULL DEFAULT '{}',
    time_offset_seconds INT NOT NULL DEFAULT 0,
    sequence_id INT NOT NULL DEFAULT 0,
    continue_on_failure BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_schedule_tasks_schedule ON schedule_tasks (schedule_id, sequence_id);