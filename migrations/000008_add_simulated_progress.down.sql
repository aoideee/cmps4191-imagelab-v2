BEGIN;

ALTER TABLE jobs
DROP COLUMN simulation_step;

COMMIT;
