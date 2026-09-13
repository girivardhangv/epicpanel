-- System actors legitimately have no user row: billing auto-provisioning creates
-- workloads with created_by = uuid.Nil (there is no human actor). 0019 relaxed
-- this for runtimes/databases but the newer first-class workload tables kept the
-- NOT NULL, so every billed Minecraft/bot provision failed the FK with 23503.
ALTER TABLE minecraft_instances ALTER COLUMN created_by DROP NOT NULL;
ALTER TABLE bot_instances ALTER COLUMN created_by DROP NOT NULL;