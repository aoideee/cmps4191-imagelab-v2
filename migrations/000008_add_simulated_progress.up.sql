BEGIN;

ALTER TABLE jobs
ADD COLUMN simulation_step INTEGER NOT NULL DEFAULT 0
    CHECK (simulation_step >= 0);

COMMIT;
