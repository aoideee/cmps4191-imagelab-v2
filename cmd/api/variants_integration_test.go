package main

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lewisdalwin/gatekeeper/internal/data"
)

func TestWorkerVariantsIntegration(t *testing.T) {
	if os.Getenv("IMAGELAB_INTEGRATION_TESTS") != "1" {
		t.Skip("set IMAGELAB_INTEGRATION_TESTS=1 to run PostgreSQL tests")
	}

	db := startUploadTestDatabase(t)
	models := data.NewModels(db)

	app := &application{
		models: models,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	for _, scenario := range []string{"success", "third insert fails"} {
		t.Run(scenario, func(t *testing.T) {
			// Clear records from the previous scenario in the temporary database.
			if _, err := db.Exec(
				"TRUNCATE image_variants, jobs, images",
			); err != nil {
				t.Fatal(err)
			}

			t.Chdir(t.TempDir())

			ctx, cancel := context.WithTimeout(
				context.Background(), 10*time.Second,
			)
			defer cancel()

			if scenario == "third insert fails" {
				// Reject display, which the worker saves third.
				_, err := db.Exec(`
					ALTER TABLE image_variants
					ADD CONSTRAINT test_reject_display
					CHECK (name <> 'display')
				`)
				if err != nil {
					t.Fatal(err)
				}

				t.Cleanup(func() {
					_, err := db.Exec(`
						ALTER TABLE image_variants
						DROP CONSTRAINT test_reject_display
					`)
					if err != nil {
						t.Error(err)
					}
				})
			}

			// Create a real PNG for the worker to process.
			var original bytes.Buffer
			picture := image.NewRGBA(image.Rect(0, 0, 300, 200))
			if err := png.Encode(&original, picture); err != nil {
				t.Fatal(err)
			}

			img := &data.Image{
				OriginalFilename: "example.png",
				StoredFilename:   "original-" + scenario + ".png",
				MediaType:        "image/png",
				Size:             int64(original.Len()),
			}

			job, err := models.Images.AcceptUpload(ctx, img, func() error {
				dir := filepath.Join("uploads", img.ID)
				if err := os.MkdirAll(dir, 0755); err != nil {
					return err
				}
				return os.WriteFile(
					filepath.Join(dir, img.StoredFilename),
					original.Bytes(),
					0600,
				)
			})
			if err != nil {
				t.Fatal(err)
			}

			// Run one actual worker job without an artificial delay.
			workErr := app.processNextImageJob(ctx)

			savedJob, err := models.Jobs.GetByPublicID(job.PublicID)
			if err != nil {
				t.Fatal(err)
			}

			var count int
			err = db.QueryRow(`
				SELECT count(*)
				FROM image_variants
				WHERE image_id = $1`,
				img.ID,
			).Scan(&count)
			if err != nil {
				t.Fatal(err)
			}

			if scenario == "third insert fails" {
				if workErr == nil {
					t.Fatal("expected worker completion to report an error")
				}
				if count != 2 {
					t.Fatalf("expected two committed milestones; got %d variant records", count)
				}
				if savedJob.Status != "failed" ||
					savedJob.FailedAt == nil ||
					savedJob.CompletedAt != nil {
					t.Fatal("job must record failure, not completion")
				}
				if len(savedJob.Variants) != 0 {
					t.Fatal("failed job must not advertise results")
				}
				if savedJob.VariantsCompleted != 2 || savedJob.Stage != "preview_ready" {
					t.Fatal("failed job must retain its last truthful progress")
				}
				if savedJob.ErrorMessage == nil ||
					*savedJob.ErrorMessage != "Failed to save generated image details" {
					t.Fatal("expected a client-safe failure message")
				}
				return
			}

			if workErr != nil {
				t.Fatal(workErr)
			}
			if savedJob.Status != "completed" ||
				savedJob.CompletedAt == nil ||
				savedJob.FailedAt != nil {
				t.Fatal("expected successful completion")
			}
			if count != 3 || len(savedJob.Variants) != 3 {
				t.Fatal("expected three database records and three results")
			}
			if savedJob.Version != 5 || savedJob.VariantsCompleted != 3 ||
				savedJob.Stage != "display_ready" {
				t.Fatal("expected claim and three versioned progress milestones")
			}

			for _, variant := range savedJob.Variants {
				var filename string
				var width, height int
				var size int64
				var createdAt time.Time

				err := db.QueryRow(`
					SELECT stored_filename, width, height, size, created_at
					FROM image_variants
					WHERE id = $1 AND image_id = $2 AND name = $3`,
					variant.ID, img.ID, variant.Name,
				).Scan(&filename, &width, &height, &size, &createdAt)
				if err != nil {
					t.Fatal(err)
				}

				path := filepath.Join("uploads", img.ID, "variants", filename)
				contents, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}

				decoded, err := png.Decode(bytes.NewReader(contents))
				if err != nil {
					t.Fatal(err)
				}

				if width != decoded.Bounds().Dx() ||
					height != decoded.Bounds().Dy() ||
					size != int64(len(contents)) {
					t.Fatal("database metadata does not match the actual file")
				}

				if variant.ImageID != img.ID ||
					variant.Width != width ||
					variant.Height != height ||
					variant.Size != size ||
					!variant.CreatedAt.Equal(createdAt) {
					t.Fatal("job result does not match variant record")
				}

				if variant.ID == "" || createdAt.IsZero() {
					t.Fatal("missing database-generated ID or creation time")
				}
			}

			// A late failure update must not overwrite successful completion.
			if err := models.Jobs.MarkFailed(
				ctx, job.ID, "Late failure",
			); err != nil {
				t.Fatal(err)
			}

			after, err := models.Jobs.GetByPublicID(job.PublicID)
			if err != nil {
				t.Fatal(err)
			}
			if after.Status != "completed" ||
				after.FailedAt != nil ||
				after.ErrorMessage != nil {
				t.Fatal("late failure changed a completed job")
			}
		})
	}
}
