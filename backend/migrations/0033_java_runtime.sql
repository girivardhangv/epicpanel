-- Java as a first-class managed runtime (Software page + Minecraft auto-install).
-- Version format already allows single-major ("21", "17", "8") via the 0018
-- regex ^[0-9]+(\.[0-9]+)?$ — only the type CHECK needs java.
-- install_runtime / remove_runtime payloads carry "type":"java" as plain JSON
-- text; the job_type enum needs no change.
ALTER TABLE runtimes DROP CONSTRAINT IF EXISTS runtimes_type_check;
ALTER TABLE runtimes ADD CONSTRAINT runtimes_type_check
    CHECK (type IN ('php', 'node', 'python', 'go', 'apache', 'openlitespeed', 'java'));
