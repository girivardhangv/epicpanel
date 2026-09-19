-- Redis as first-class managed software (Software page install/remove).
-- The live constraint already carries phpmyadmin/adminer (added out-of-band
-- when the dbtools rows shipped); this migration re-states the FULL current
-- list plus redis so fresh installs reproduce it.
ALTER TABLE runtimes DROP CONSTRAINT IF EXISTS runtimes_type_check;
ALTER TABLE runtimes ADD CONSTRAINT runtimes_type_check
    CHECK (type IN ('php', 'node', 'python', 'go', 'apache', 'openlitespeed',
                    'java', 'phpmyadmin', 'adminer', 'redis'));
