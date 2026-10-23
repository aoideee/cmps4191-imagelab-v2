BEGIN;

ALTER TABLE jobs
ADD COLUMN version BIGINT NOT NULL DEFAULT 1,
ADD COLUMN stage TEXT NOT NULL DEFAULT 'queued',
ADD COLUMN variants_completed INTEGER NOT NULL DEFAULT 0;

ALTER TABLE jobs
ADD CONSTRAINT jobs_version_positive CHECK (version > 0),
ADD CONSTRAINT jobs_variants_completed_range
    CHECK (variants_completed BETWEEN 0 AND 3),
ADD CONSTRAINT jobs_stage_known CHECK (
    stage IN (
        'queued',
        'starting',
        'thumbnail_ready',
        'preview_ready',
        'display_ready'
    )
);

COMMIT;
