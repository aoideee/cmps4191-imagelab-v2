package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/lewisdalwin/gatekeeper/internal/data"
	"golang.org/x/image/draw"
)

var errPersistVariantProgress = errors.New("persist variant progress")

func (app *application) startImageWorker(ctx context.Context) {
	app.wg.Add(1)
	go func() {
		defer app.wg.Done()
		ticker := time.NewTicker(app.config.workerPollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				app.logger.Info("image worker stopped")
				return
			case <-ticker.C:
				err := app.processNextImageJob(ctx)
				if err != nil && !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, context.Canceled) {
					app.logger.Error("image worker failed", "error", err)
				}
			}
		}
	}()
}

// processNextImageJob orchestrates claiming, processing, and completing an image job.
func (app *application) processNextImageJob(ctx context.Context) error {
	job, err := app.models.Jobs.ClaimNext(ctx, "image_processing")
	if err != nil {
		return err
	}
	app.notifyJobObservers(job.PublicID)
	app.logger.Info(
		"image job change committed",
		"job_id", job.PublicID,
		"version", job.Version,
		"status", job.Status,
		"stage", job.Stage,
		"variants_completed", job.VariantsCompleted,
		"committed_at", time.Now().UTC().Format(time.RFC3339Nano),
	)

	if job.ImageID == nil {
		return app.failJob(ctx, job, "Job is missing associated image record")
	}

	// Artificial delay (configurable via -job-delay) to demonstrate asynchronous job transitions
	if app.config.jobDelay > 0 {
		timer := time.NewTimer(app.config.jobDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}

	img, err := app.models.Images.Get(job.ImageID)
	if err != nil {
		app.logger.Error("failed to load image record", "error", err, "job_id", job.PublicID)
		return app.failJob(ctx, job, "Associated image record could not be loaded")
	}

	// 1. Decode original file from disk
	srcImg, format, err := loadAndDecodeImage(img)
	if err != nil {
		app.logger.Error("failed to load/decode image", "error", err, "job_id", job.PublicID)
		return app.failJob(ctx, job, "Failed to read or decode original image file")
	}

	// 2. Generate each real variant, then transactionally publish its progress.
	_, err = generateVariantsWithProgress(img, srcImg, format, func(variant data.Variant) error {
		progress, err := app.models.Jobs.StoreVariantProgress(ctx, job.ID, variant)
		if err != nil {
			return err
		}

		app.logger.Info(
			"image job change committed",
			"job_id", job.PublicID,
			"version", progress.Version,
			"status", progress.Status,
			"stage", progress.Stage,
			"variants_completed", progress.VariantsCompleted,
			"committed_at", time.Now().UTC().Format(time.RFC3339Nano),
		)
		app.notifyJobObservers(job.PublicID)
		return nil
	})
	if err != nil {
		app.logger.Error("failed to generate variants", "error", err, "job_id", job.PublicID)
		if errors.Is(err, data.ErrVariantCommitUncertain) {
			return err
		}
		message := "Failed to generate image variants"
		if errors.Is(err, errPersistVariantProgress) {
			message = "Failed to save generated image details"
		}
		if failErr := app.failJob(ctx, job, message); failErr != nil {
			return fmt.Errorf("processing failed: %v; recording failure also failed: %w", err, failErr)
		}
		return err
	}

	app.logger.Info("image processing job completed", "job_id", job.PublicID)
	return nil
}

// loadAndDecodeImage opens the original file from the upload directory and decodes its header.
func loadAndDecodeImage(img *data.Image) (image.Image, string, error) {
	origPath := filepath.Join("./uploads", img.ID, img.StoredFilename)

	srcFile, err := os.Open(origPath)
	if err != nil {
		return nil, "", fmt.Errorf("failed to open original file: %w", err)
	}
	defer srcFile.Close()

	srcImg, format, err := image.Decode(srcFile)
	if err != nil {
		return nil, "", fmt.Errorf("unable to decode image header: %w", err)
	}

	return srcImg, format, nil
}

// cropCenterSquare extracts an exact square centered crop from src without distortion.
func cropCenterSquare(src image.Image) image.Image {
	bounds := src.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()
	if w == h {
		return src
	}

	side := w
	if h < side {
		side = h
	}

	x0 := bounds.Min.X + (w-side)/2
	y0 := bounds.Min.Y + (h-side)/2
	cropRect := image.Rect(x0, y0, x0+side, y0+side)

	if sub, ok := src.(interface {
		SubImage(r image.Rectangle) image.Image
	}); ok {
		return sub.SubImage(cropRect)
	}

	cropped := image.NewRGBA(image.Rect(0, 0, side, side))
	draw.Draw(cropped, cropped.Bounds(), src, cropRect.Min, draw.Src)
	return cropped
}

// calculateFit calculates scaled dimensions that fit within maxW and maxH while preserving aspect ratio.
func calculateFit(srcW, srcH, maxW, maxH int) (int, int) {
	if srcW <= 0 || srcH <= 0 {
		return maxW, maxH
	}

	scale := math.Min(float64(maxW)/float64(srcW), float64(maxH)/float64(srcH))
	if scale > 1.0 {
		return srcW, srcH
	}

	w := int(math.Round(float64(srcW) * scale))
	h := int(math.Round(float64(srcH) * scale))
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	return w, h
}

// generateVariants creates thumbnail, preview, and display variants satisfying IMG-01 to IMG-04.
func generateVariants(img *data.Image, srcImg image.Image, format string) ([]data.Variant, error) {
	return generateVariantsWithProgress(img, srcImg, format, nil)
}

func generateVariantsWithProgress(
	img *data.Image,
	srcImg image.Image,
	format string,
	onStored func(data.Variant) error,
) ([]data.Variant, error) {
	variantsDir := filepath.Join("./uploads", img.ID, "variants")
	if err := os.MkdirAll(variantsDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create variants directory: %w", err)
	}

	srcBounds := srcImg.Bounds()
	srcW := srcBounds.Dx()
	srcH := srcBounds.Dy()

	previewW, previewH := calculateFit(srcW, srcH, 800, 600)
	displayW, displayH := calculateFit(srcW, srcH, 1200, 900)

	type variantPlan struct {
		name   string
		width  int
		height int
		source image.Image
	}

	plans := []variantPlan{
		{
			name:   "thumbnail",
			width:  150,
			height: 150,
			source: cropCenterSquare(srcImg),
		},
		{
			name:   "preview",
			width:  previewW,
			height: previewH,
			source: srcImg,
		},
		{
			name:   "display",
			width:  displayW,
			height: displayH,
			source: srcImg,
		},
	}

	ext := ".jpg"
	if format == "png" {
		ext = ".png"
	}

	var variants []data.Variant
	for _, plan := range plans {
		varFileName := fmt.Sprintf("%s-%s%s", img.ID, plan.name, ext)
		varPath := filepath.Join(variantsDir, varFileName)

		resized := resizeImage(plan.source, plan.width, plan.height)

		outFile, err := os.Create(varPath)
		if err != nil {
			return nil, fmt.Errorf("failed to create variant file %s: %w", plan.name, err)
		}

		if format == "png" {
			err = png.Encode(outFile, resized)
		} else {
			err = jpeg.Encode(outFile, resized, &jpeg.Options{Quality: 85})
		}
		closeErr := outFile.Close()

		if err != nil {
			return nil, fmt.Errorf("failed to encode variant %s: %w", plan.name, err)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("failed to close variant %s: %w", plan.name, closeErr)
		}

		fileInfo, err := os.Stat(varPath)
		if err != nil {
			return nil, fmt.Errorf("failed to inspect variant %s: %w", plan.name, err)
		}
		if fileInfo.Size() <= 0 {
			return nil, fmt.Errorf("generated variant %s is empty", plan.name)
		}

		variants = append(variants, data.Variant{
			ImageID:        img.ID,
			Name:           plan.name,
			StoredFilename: varFileName,
			Width:          resized.Bounds().Dx(),
			Height:         resized.Bounds().Dy(),
			Size:           fileInfo.Size(),
			URL:            fmt.Sprintf("/v1/images/%s/variants/%s", img.ID, plan.name),
		})
		if onStored != nil {
			if err := onStored(variants[len(variants)-1]); err != nil {
				return nil, fmt.Errorf("%w for %s: %w", errPersistVariantProgress, plan.name, err)
			}
		}
	}

	return variants, nil
}

// completeJob saves all variant records and the completed state together.
func (app *application) notifyJobObservers(publicID string) {
	if app.observers != nil {
		app.observers.notify(publicID)
	}
}

func (app *application) failJob(ctx context.Context, job *data.Job, message string) error {
	if err := app.models.Jobs.MarkFailed(ctx, job.ID, message); err != nil {
		return err
	}

	app.notifyJobObservers(job.PublicID)
	if snapshot, err := app.models.Jobs.GetByPublicID(job.PublicID); err == nil {
		app.logger.Info(
			"image job change committed",
			"job_id", job.PublicID,
			"version", snapshot.Version,
			"status", snapshot.Status,
			"stage", snapshot.Stage,
			"variants_completed", snapshot.VariantsCompleted,
			"committed_at", time.Now().UTC().Format(time.RFC3339Nano),
		)
	}
	return nil
}

// resizeImage scales an image to width x height using bilinear interpolation.
func resizeImage(src image.Image, width, height int) image.Image {
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.BiLinear.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
	return dst
}
