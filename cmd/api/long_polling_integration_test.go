package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/lewisdalwin/gatekeeper/internal/data"
)

func TestLongPollingBroadcastsCommittedChange(t *testing.T) {
	if os.Getenv("IMAGELAB_INTEGRATION_TESTS") != "1" {
		t.Skip("set IMAGELAB_INTEGRATION_TESTS=1 to run PostgreSQL tests")
	}

	db := startUploadTestDatabase(t)
	models := data.NewModels(db)
	app := &application{
		models:    models,
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		observers: newObservationHub(),
	}

	image := &data.Image{
		OriginalFilename: "observation.png",
		StoredFilename:   "observation-source.png",
		MediaType:        "image/png",
		Size:             1,
	}
	if err := models.Images.Insert(image); err != nil {
		t.Fatal(err)
	}

	job := &data.Job{ImageID: &image.ID, JobType: "image_processing"}
	if err := models.Jobs.Insert(job); err != nil {
		t.Fatal(err)
	}

	handler := app.routes()
	type result struct {
		recorder *httptest.ResponseRecorder
	}
	results := make(chan result, 2)

	for range 2 {
		go func() {
			r := httptest.NewRequest(
				http.MethodGet,
				"/v1/jobs/"+job.PublicID+"?after=1&wait=2",
				nil,
			)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, r)
			results <- result{recorder: recorder}
		}()
	}

	waitForObservers(t, app.observers, job.PublicID, 2)
	claimed, err := models.Jobs.ClaimNext(context.Background(), "image_processing")
	if err != nil {
		t.Fatal(err)
	}
	app.observers.notify(job.PublicID)

	for range 2 {
		select {
		case got := <-results:
			if got.recorder.Code != http.StatusOK {
				t.Fatalf("status = %d; want 200", got.recorder.Code)
			}
			if got.recorder.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("observation response must disable caching")
			}

			var snapshot struct {
				Status  string `json:"status"`
				Version int64  `json:"version"`
			}
			if err := json.Unmarshal(got.recorder.Body.Bytes(), &snapshot); err != nil {
				t.Fatal(err)
			}
			if snapshot.Status != "processing" || snapshot.Version != claimed.Version {
				t.Fatalf("snapshot = %+v; want processing version %d", snapshot, claimed.Version)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("observer did not receive committed change")
		}
	}

	t.Run("rejects impossible client version", func(t *testing.T) {
		r := httptest.NewRequest(
			http.MethodGet,
			"/v1/jobs/"+job.PublicID+"?after=3&wait=1",
			nil,
		)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, r)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d; want 400", recorder.Code)
		}
	})

	t.Run("returns 204 at unchanged deadline", func(t *testing.T) {
		r := httptest.NewRequest(
			http.MethodGet,
			"/v1/jobs/"+job.PublicID+"?after=2&wait=1",
			nil,
		)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, r)
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("status = %d; want 204", recorder.Code)
		}
		if recorder.Body.Len() != 0 {
			t.Fatal("204 response must not contain a JSON body")
		}
	})
}

func waitForObservers(
	t *testing.T,
	hub *observationHub,
	jobID string,
	want int,
) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		hub.mu.Lock()
		entry := hub.entries[jobID]
		count := 0
		if entry != nil {
			count = entry.waiters
		}
		hub.mu.Unlock()

		if count == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("did not register %d observers", want)
}
