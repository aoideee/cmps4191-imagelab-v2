package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lib/pq"
	"time"
)

var ErrVariantCommitUncertain = errors.New(
	"variant commit outcome is uncertain",
)

type JobProgress struct {
	Version           int64
	Status            string
	Stage             string
	VariantsCompleted int
	CompletedAt       *time.Time
}

// StoreVariantProgress persists one real variant and the corresponding job
// representation in the same transaction. The third milestone also completes
// the job so clients never see completed before all metadata is durable.
func (m JobModel) StoreVariantProgress(
	parent context.Context,
	jobID string,
	variant Variant,
) (*JobProgress, error) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()

	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var imageID, status string
	var completed int
	if err := tx.QueryRowContext(ctx, `
		SELECT image_id, status, variants_completed
		FROM jobs
		WHERE id = $1
		FOR UPDATE`, jobID,
	).Scan(&imageID, &status, &completed); err != nil {
		return nil, err
	}
	if status != "processing" {
		return nil, errors.New("only a processing job can record progress")
	}
	if variant.ImageID != imageID {
		return nil, errors.New("variant belongs to a different image")
	}

	expectedNames := [...]string{"thumbnail", "preview", "display"}
	if completed < 0 || completed >= len(expectedNames) {
		return nil, fmt.Errorf("invalid variants_completed value %d", completed)
	}
	if variant.Name != expectedNames[completed] {
		return nil, fmt.Errorf("expected variant %q at progress %d", expectedNames[completed], completed)
	}

	if err := tx.QueryRowContext(ctx, `
		INSERT INTO image_variants (
			image_id, name, stored_filename, width, height, size
		)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, created_at`,
		variant.ImageID,
		variant.Name,
		variant.StoredFilename,
		variant.Width,
		variant.Height,
		variant.Size,
	).Scan(&variant.ID, &variant.CreatedAt); err != nil {
		return nil, err
	}

	completed++
	stages := [...]string{"thumbnail_ready", "preview_ready", "display_ready"}
	progress := &JobProgress{
		Status:            "processing",
		Stage:             stages[completed-1],
		VariantsCompleted: completed,
	}

	if completed < 3 {
		err = tx.QueryRowContext(ctx, `
			UPDATE jobs
			SET variants_completed = $2,
			    stage = $3,
			    version = version + 1
			WHERE id = $1
			RETURNING version`,
			jobID, completed, progress.Stage,
		).Scan(&progress.Version)
	} else {
		variants, loadErr := loadVariantsForResult(ctx, tx, imageID)
		if loadErr != nil {
			return nil, loadErr
		}
		result, marshalErr := json.Marshal(struct {
			Variants []Variant `json:"variants"`
		}{Variants: variants})
		if marshalErr != nil {
			return nil, marshalErr
		}

		progress.Status = "completed"
		err = tx.QueryRowContext(ctx, `
			UPDATE jobs
			SET status = 'completed',
			    result = $2,
			    variants_completed = 3,
			    stage = 'display_ready',
			    completed_at = now(),
			    failed_at = NULL,
			    error_message = NULL,
			    version = version + 1
			WHERE id = $1
			RETURNING version, completed_at`,
			jobID, result,
		).Scan(&progress.Version, &progress.CompletedAt)
	}
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrVariantCommitUncertain, err)
	}
	return progress, nil
}

func loadVariantsForResult(ctx context.Context, tx *sql.Tx, imageID string) ([]Variant, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, image_id, name, stored_filename, width, height, size, created_at
		FROM image_variants
		WHERE image_id = $1
		ORDER BY CASE name
			WHEN 'thumbnail' THEN 1
			WHEN 'preview' THEN 2
			WHEN 'display' THEN 3
		END`, imageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var variants []Variant
	for rows.Next() {
		var variant Variant
		if err := rows.Scan(
			&variant.ID,
			&variant.ImageID,
			&variant.Name,
			&variant.StoredFilename,
			&variant.Width,
			&variant.Height,
			&variant.Size,
			&variant.CreatedAt,
		); err != nil {
			return nil, err
		}
		variant.URL = fmt.Sprintf("/v1/images/%s/variants/%s", imageID, variant.Name)
		variants = append(variants, variant)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(variants) != 3 {
		return nil, fmt.Errorf("completion requires exactly three variants; got %d", len(variants))
	}
	return variants, nil
}

func (m JobModel) CompleteWithVariants(
	parent context.Context,
	jobID string,
	variants []Variant,
) error {
	// Require exactly one of each expected variant.
	if len(variants) != 3 {
		return errors.New("completion requires exactly three variants")
	}

	seen := make(map[string]bool)
	for _, variant := range variants {
		switch variant.Name {
		case "thumbnail", "preview", "display":
		default:
			return fmt.Errorf("unknown variant name %q", variant.Name)
		}

		if seen[variant.Name] {
			return fmt.Errorf("duplicate variant name %q", variant.Name)
		}
		seen[variant.Name] = true
	}

	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()

	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Lock the job while preparing its completion.
	var imageID string
	var status string

	err = tx.QueryRowContext(ctx, `
		SELECT image_id, status
		FROM jobs
		WHERE id = $1
		FOR UPDATE`,
		jobID,
	).Scan(&imageID, &status)
	if err != nil {
		return err
	}
	if status != "processing" {
		return errors.New("only a processing job can be completed")
	}

	// Work on a copy so a rollback does not leave generated IDs
	// in the caller's variant list.
	saved := append([]Variant(nil), variants...)

	for i := range saved {
		variant := &saved[i]

		if variant.ImageID != imageID {
			return errors.New("variant belongs to a different image")
		}

		err = tx.QueryRowContext(ctx, `
			INSERT INTO image_variants (
				image_id, name, stored_filename, width, height, size
			)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id, created_at`,
			variant.ImageID,
			variant.Name,
			variant.StoredFilename,
			variant.Width,
			variant.Height,
			variant.Size,
		).Scan(&variant.ID, &variant.CreatedAt)
		if err != nil {
			return err
		}
	}

	// Preserve the existing job-response format, including the
	// IDs and creation times returned by PostgreSQL.
	result, err := json.Marshal(struct {
		Variants []Variant `json:"variants"`
	}{
		Variants: saved,
	})
	if err != nil {
		return err
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE jobs
		SET status = 'completed',
		    result = $2,
		    variants_completed = 3,
		    stage = 'display_ready',
		    completed_at = now(),
		    failed_at = NULL,
		    error_message = NULL,
		    version = version + 1
		WHERE id = $1`,
		jobID,
		result,
	)
	if err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: %v", ErrVariantCommitUncertain, err)
	}

	return nil
}

// GetCompletedVariant returns a known variant only when its image
// has a successfully completed processing job.
func (m ImageModel) GetCompletedVariant(
	parent context.Context,
	imageID string,
	name string,
) (*Variant, string, error) {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()

	query := `
		SELECT v.id, v.image_id, v.name, v.stored_filename,
		       v.width, v.height, v.size, v.created_at,
		       i.media_type
		FROM image_variants v
		JOIN images i ON i.id = v.image_id
		WHERE v.image_id = $1
		  AND v.name = $2
		  AND EXISTS (
		      SELECT 1
		      FROM jobs j
		      WHERE j.image_id = v.image_id
		        AND j.job_type = 'image_processing'
		        AND j.status = 'completed'
		  )`

	var variant Variant
	var mediaType string

	err := m.DB.QueryRowContext(ctx, query, imageID, name).Scan(
		&variant.ID,
		&variant.ImageID,
		&variant.Name,
		&variant.StoredFilename,
		&variant.Width,
		&variant.Height,
		&variant.Size,
		&variant.CreatedAt,
		&mediaType,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", ErrRecordNotFound
		}

		// An incorrectly formatted UUID cannot identify an image.
		var pgErr *pq.Error
		if errors.As(err, &pgErr) && pgErr.Code == "22P02" {
			return nil, "", ErrRecordNotFound
		}

		return nil, "", err
	}

	return &variant, mediaType, nil
}
