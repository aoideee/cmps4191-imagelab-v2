package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"image"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/lewisdalwin/gatekeeper/internal/data"
)

func (app *application) uploadImageHandler(w http.ResponseWriter, r *http.Request) {
	// 1. Limit the image to 10 MiB, with extra room for form packaging.
	const maxImageSize = 10 << 20
	const maxRequestSize = maxImageSize + (1 << 20)

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestSize)

	if err := r.ParseMultipartForm(maxImageSize); err != nil {
		var sizeError *http.MaxBytesError

		if errors.As(err, &sizeError) {
			app.badRequestResponse(w, r,
				fmt.Errorf("upload request is too large"))
		} else {
			app.badRequestResponse(w, r,
				fmt.Errorf("invalid multipart upload"))
		}
		return
	}
	defer r.MultipartForm.RemoveAll()

	// 2. Require exactly one uploaded file, in the "image" field.
	files := r.MultipartForm.File["image"]
	fileCount := 0
	for _, uploadedFiles := range r.MultipartForm.File {
		fileCount += len(uploadedFiles)
	}

	if len(files) != 1 || fileCount != 1 {
		app.badRequestResponse(w, r,
			fmt.Errorf("provide exactly one file in the 'image' field"))
		return
	}

	header := files[0]

	if header.Size == 0 {
		app.badRequestResponse(w, r,
			fmt.Errorf("image file must not be empty"))
		return
	}

	if header.Size > maxImageSize {
		app.badRequestResponse(w, r,
			fmt.Errorf("file size exceeds 10 MB limit"))
		return
	}

	file, err := header.Open()
	if err != nil {
		app.serverErrorResponse(w, r, err)
		return
	}
	defer file.Close()

	// 3. Decode the contents before accepting the image.
	_, format, err := image.Decode(file)
	if err != nil {
		app.badRequestResponse(w, r,
			fmt.Errorf("upload must be a readable JPEG or PNG image"))
		return
	}

	var mimeType string
	switch format {
	case "jpeg":
		mimeType = "image/jpeg"
	case "png":
		mimeType = "image/png"
	default:
		app.badRequestResponse(w, r,
			fmt.Errorf("only JPEG and PNG images are allowed"))
		return
	}

	// Return to the beginning so the original can be copied in full.
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		app.serverErrorResponse(w, r, err)
		return
	}

	// 4. Persist image record in database to obtain generated image.ID
	extension := ".jpg"
	if mimeType == "image/png" {
		extension = ".png"
	}

	randomBytes := make([]byte, 16)
	if _, err := rand.Read(randomBytes); err != nil {
		app.serverErrorResponse(w, r, err)
		return
	}

	storedFilename := fmt.Sprintf("%x%s", randomBytes, extension)
	image := &data.Image{
		OriginalFilename: header.Filename,
		StoredFilename:   storedFilename,
		MediaType:        mimeType,
		Size:             header.Size,
	}
	// Track only a directory successfully created by this request.
	var createdDir string
	keepOriginal := false

	defer func() {
		if createdDir == "" || keepOriginal {
			return
		}

		if err := os.RemoveAll(createdDir); err != nil {
			app.logger.Error(
				"failed to clean up rejected upload",
				"image_id", image.ID,
				"error", err,
			)
		}
	}()

	saveOriginal := func() error {
		if err := os.MkdirAll("./uploads", 0755); err != nil {
			return err
		}

		imgDir := filepath.Join("./uploads", image.ID)

		// Do not reuse an existing image directory.
		if err := os.Mkdir(imgDir, 0755); err != nil {
			return err
		}
		createdDir = imgDir

		dstPath := filepath.Join(imgDir, storedFilename)
		dst, err := os.OpenFile(
			dstPath,
			os.O_WRONLY|os.O_CREATE|os.O_EXCL,
			0644,
		)
		if err != nil {
			return err
		}

		written, copyErr := io.Copy(dst, file)
		closeErr := dst.Close()

		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if written != header.Size {
			return fmt.Errorf("stored image size does not match upload")
		}

		return nil
	}

	job, err := app.models.Images.AcceptUpload(
		r.Context(), image, saveOriginal,
	)
	if err != nil {
		// If commit confirmation was lost, preserve the input:
		// the database might already have accepted its job.
		if errors.Is(err, data.ErrUploadCommitUncertain) {
			keepOriginal = true
		}

		app.serverErrorResponse(w, r, err)
		return
	}

	// Acceptance succeeded. Keep the original even if sending
	// the response fails or the browser disconnects.
	keepOriginal = true

	// 8. Send 202 Accepted response with polling location header
	statusURL := fmt.Sprintf("/v1/jobs/%s", job.PublicID)
	headers := make(http.Header)
	headers.Set("Location", statusURL)

	response := envelope{
		"image_id":   image.ID,
		"job_id":     job.PublicID,
		"status":     job.Status,
		"status_url": statusURL,
	}

	if err := app.writeJSON(w, http.StatusAccepted, response, headers); err != nil {
		app.serverErrorResponse(w, r, err)
	}
}

func (app *application) getJobHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	after, wait, longPolling, err := parseObservationQuery(r)
	if err != nil {
		app.badRequestResponse(w, r, err)
		return
	}

	if !longPolling {
		job, err := app.models.Jobs.GetByPublicID(r.PathValue("id"))
		if err != nil {
			app.writeJobLookupError(w, r, err)
			return
		}
		app.writeJobSnapshot(w, r, job)
		return
	}

	deadline := time.NewTimer(wait)
	defer deadline.Stop()

	jobID := r.PathValue("id")
	for {
		// Capture the signal before reading PostgreSQL. A commit between the
		// read and wait closes this channel, so the change cannot be missed.
		subscription := app.observers.subscribe(jobID)
		job, err := app.models.Jobs.GetByPublicID(jobID)
		if err != nil {
			subscription.release()
			app.writeJobLookupError(w, r, err)
			return
		}

		if after > job.Version {
			subscription.release()
			app.badRequestResponse(w, r, fmt.Errorf(
				"after cannot exceed the current job version",
			))
			return
		}

		if job.Status == "completed" || job.Status == "failed" || job.Version > after {
			subscription.release()
			app.writeJobSnapshot(w, r, job)
			return
		}

		select {
		case <-r.Context().Done():
			subscription.release()
			return
		case <-subscription.C:
			subscription.release()
			// A notification is only a hint; loop and read PostgreSQL again.
		case <-deadline.C:
			subscription.release()

			// Recheck authoritative state before deciding that nothing changed.
			job, err := app.models.Jobs.GetByPublicID(jobID)
			if err != nil {
				app.writeJobLookupError(w, r, err)
				return
			}
			if job.Status == "completed" || job.Status == "failed" || job.Version > after {
				app.writeJobSnapshot(w, r, job)
				return
			}

			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
}

func parseObservationQuery(r *http.Request) (int64, time.Duration, bool, error) {
	query := r.URL.Query()
	for key := range query {
		if key != "after" && key != "wait" {
			return 0, 0, false, fmt.Errorf("unknown observation parameter %q", key)
		}
		if len(query[key]) != 1 {
			return 0, 0, false, fmt.Errorf("observation parameter %q must appear once", key)
		}
	}

	afterText, hasAfter := query["after"]
	waitText, hasWait := query["wait"]
	if !hasAfter {
		if hasWait {
			return 0, 0, false, fmt.Errorf("wait requires after")
		}
		return 0, 0, false, nil
	}

	after, err := strconv.ParseInt(afterText[0], 10, 64)
	if err != nil || after < 0 {
		return 0, 0, false, fmt.Errorf("after must be a nonnegative integer")
	}

	waitSeconds := int64(20)
	if hasWait {
		waitSeconds, err = strconv.ParseInt(waitText[0], 10, 64)
		if err != nil || waitSeconds < 1 || waitSeconds > 20 {
			return 0, 0, false, fmt.Errorf("wait must be an integer from 1 to 20")
		}
	}

	return after, time.Duration(waitSeconds) * time.Second, true, nil
}

func (app *application) writeJobLookupError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, data.ErrRecordNotFound) {
		app.notFoundResponse(w, r)
		return
	}
	app.serverErrorResponse(w, r, err)
}

func (app *application) writeJobSnapshot(w http.ResponseWriter, r *http.Request, job *data.Job) {
	response := envelope{
		"id":                 job.PublicID,
		"image_id":           job.ImageID,
		"status":             job.Status,
		"version":            job.Version,
		"stage":              job.Stage,
		"variants_completed": job.VariantsCompleted,
		"variants":           job.Variants,
		"queued_at":          job.QueuedAt,
		"started_at":         job.StartedAt,
		"completed_at":       job.CompletedAt,
		"failed_at":          job.FailedAt,
		"error_message":      job.ErrorMessage,
	}

	if err := app.writeJSON(w, http.StatusOK, response, nil); err != nil {
		app.serverErrorResponse(w, r, err)
	}
}

func (app *application) getVariantHandler(w http.ResponseWriter, r *http.Request) {
	imageID := r.PathValue("id")
	variantName := r.PathValue("name")

	switch variantName {
	case "thumbnail", "preview", "display":
	default:
		app.notFoundResponse(w, r)
		return
	}

	variant, mediaType, err := app.models.Images.GetCompletedVariant(
		r.Context(), imageID, variantName,
	)
	if err != nil {
		if errors.Is(err, data.ErrRecordNotFound) {
			app.notFoundResponse(w, r)
			return
		}
		app.serverErrorResponse(w, r, err)
		return
	}

	// Stored filenames must be simple filenames, not directory paths.
	filename := variant.StoredFilename
	if filename == "" || filename == "." || filename == ".." ||
		filepath.Base(filename) != filename {
		app.serverErrorResponse(
			w, r, fmt.Errorf("invalid stored variant filename"),
		)
		return
	}

	variantPath := filepath.Join(
		"./uploads", variant.ImageID, "variants", filename,
	)

	file, err := os.Open(variantPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			app.notFoundResponse(w, r)
			return
		}
		app.serverErrorResponse(w, r, err)
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		app.serverErrorResponse(w, r, err)
		return
	}
	if !info.Mode().IsRegular() {
		app.notFoundResponse(w, r)
		return
	}

	w.Header().Set("Content-Type", mediaType)
	http.ServeContent(w, r, filename, info.ModTime(), file)
}
