-- 0052: allow the governor's denial kind in dynamic_resource_events.
-- ADR-062a introduced the safety governor, whose denials are recorded as
-- kind='governor_denied' — the 0049 CHECK constraint predates it, so every
-- denial insert failed silently (found live under k6 load: the engine tried
-- to scale, the node reserve check refused, and the reason vanished).
ALTER TABLE dynamic_resource_events DROP CONSTRAINT dynamic_resource_events_kind_check;
ALTER TABLE dynamic_resource_events ADD CONSTRAINT dynamic_resource_events_kind_check
    CHECK (kind IN
        ('enable', 'disable', 'scale_up', 'scale_down', 'busy',
         'suspend_attack', 'restore', 'perk_assigned', 'perk_removed',
         'governor_denied'));
