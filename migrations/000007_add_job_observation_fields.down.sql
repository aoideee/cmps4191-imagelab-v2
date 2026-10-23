BEGIN;

ALTER TABLE jobs
DROP CONSTRAINT jobs_stage_known,
DROP CONSTRAINT jobs_variants_completed_range,
DROP CONSTRAINT jobs_version_positive,
DROP COLUMN variants_completed,
DROP COLUMN stage,
DROP COLUMN version;

COMMIT;
