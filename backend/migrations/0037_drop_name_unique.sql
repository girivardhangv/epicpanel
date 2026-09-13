-- Pterodactyl alignment: server NAME is a display label, never an identity.
-- Wings addresses servers by UUID everywhere; Server::$validationRules has no
-- unique rule on name. Our hard UNIQUE(organization_id, name) created a whole
-- class of "name already exists" bugs (including after soft-delete). Drop it
-- for both first-class workloads; UUID stays the identity.
ALTER TABLE minecraft_instances DROP CONSTRAINT IF EXISTS minecraft_instances_organization_id_name_key;
DROP INDEX IF EXISTS uq_mc_org_name_live;

ALTER TABLE bot_instances DROP CONSTRAINT IF EXISTS bot_instances_organization_id_name_key;
DROP INDEX IF EXISTS uq_bot_org_name_live;