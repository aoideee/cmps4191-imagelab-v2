package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/lib/pq"
)

// Variant matches the ImageLab API variant result structure (Section 10.2)
type Variant struct {
	ID             string    `json:"id,omitempty"`
	ImageID        string    `json:"image_id,omitempty"`
	Name           string    `json:"name"`
	StoredFilename string    `json:"-"`
	Width          int       `json:"width"`
	Height         int       `json:"height"`
	Size           int64     `json:"size"`
	CreatedAt      time.Time `json:"created_at"`
	URL            string    `json:"url"`
}
type Job struct {
	ID                string          `json:"-"`
	PublicID          string          `json:"id"`
	ImageID           *string         `json:"image_id,omitempty"`
	JobType           string          `json:"job_type,omitempty"`
	Status            string          `json:"status"`
	Version           int64           `json:"version"`
	Stage             string          `json:"stage"`
	VariantsCompleted int             `json:"variants_completed"`
	Payload           json.RawMessage `json:"payload,omitempty"`
	Result            json.RawMessage `json:"result,omitempty"`
	Variants          []Variant       `json:"variants,omitempty"` // Automatically populated on completion
	ErrorMessage      *string         `json:"error_message,omitempty"`
	QueuedAt          time.Time       `json:"queued_at"` // Spec requires queued_at
	StartedAt         *time.Time      `json:"started_at,omitempty"`
	CompletedAt       *time.Time      `json:"completed_at,omitempty"`
	FailedAt          *time.Time      `json:"failed_at,omitempty"`
}

type JobModel struct {
	DB *sql.DB
}

// Insert creates a durable job record in PostgreSQL (JOB-01)
func (m JobModel) Insert(job *Job) error {
	query := `
		INSERT INTO jobs (image_id, job_type, payload, status)
		VALUES ($1, $2, COALESCE($3, '{}'::jsonb), 'queued')
		RETURNING id, public_id, status, version, stage,
		          variants_completed, created_at`

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := m.DB.QueryRowContext(ctx, query, job.ImageID, job.JobType, job.Payload).Scan(
		&job.ID, &job.PublicID, &job.Status, &job.Version, &job.Stage,
		&job.VariantsCompleted, &job.QueuedAt,
	)
	if err != nil {
		var pgErr *pq.Error
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return ErrRecordNotFound
		}
		return err
	}
	return nil
}

// ClaimNext claims the next queued job for a specific jobType using FOR UPDATE SKIP LOCKED (WRK-02)
func (m JobModel) ClaimNext(ctx context.Context, jobType string) (*Job, error) {
	tx, err := m.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	query := `
		SELECT id, public_id, image_id, job_type,
		       COALESCE(payload, '{}'::jsonb)
		FROM jobs
		WHERE status = 'queued' AND job_type = $1
		ORDER BY created_at
		FOR UPDATE SKIP LOCKED
		LIMIT 1`

	var job Job
	if err := tx.QueryRowContext(ctx, query, jobType).Scan(
		&job.ID, &job.PublicID, &job.ImageID, &job.JobType, &job.Payload,
	); err != nil {
		return nil, err
	}

	if err := tx.QueryRowContext(ctx, `
		UPDATE jobs
		SET status = 'processing',
		    stage = 'starting',
		    started_at = now(),
		    version = version + 1
		WHERE id = $1
		RETURNING started_at, version, stage, variants_completed`,
		job.ID,
	).Scan(&job.StartedAt, &job.Version, &job.Stage, &job.VariantsCompleted); err != nil {
		return nil, err
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}

	job.Status = "processing"
	return &job, nil
}

// GetByPublicID returns current status and unmarshals variant metadata if completed (Section 10)
func (m JobModel) GetByPublicID(publicID string) (*Job, error) {
	query := `
		SELECT id, public_id, image_id, job_type, status,
		       COALESCE(payload, 'null'::jsonb),
		       COALESCE(result, 'null'::jsonb),
		       error_message, version, stage, variants_completed,
		       started_at, completed_at, failed_at, created_at
		FROM jobs WHERE public_id = $1`

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var job Job
	err := m.DB.QueryRowContext(ctx, query, publicID).Scan(
		&job.ID, &job.PublicID, &job.ImageID, &job.JobType, &job.Status,
		&job.Payload, &job.Result, &job.ErrorMessage,
		&job.Version, &job.Stage, &job.VariantsCompleted,
		&job.StartedAt, &job.CompletedAt, &job.FailedAt, &job.QueuedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrRecordNotFound
		}
		return nil, err
	}

	// Extract variants array from result JSONB when job status is completed
	if len(job.Result) > 0 && string(job.Result) != "null" {
		var res struct {
			Variants []Variant `json:"variants"`
		}
		if err := json.Unmarshal(job.Result, &res); err == nil {
			job.Variants = res.Variants
		}
	}

	return &job, nil
}

func (m JobModel) MarkFailed(ctx context.Context, id, message string) error {
	_, err := m.DB.ExecContext(ctx,
		`UPDATE jobs
 SET status = 'failed',
     error_message = $2,
     failed_at = now(),
     completed_at = NULL,
     version = version + 1
 WHERE id = $1 AND status = 'processing'`,
		id, message)
	return err
}
