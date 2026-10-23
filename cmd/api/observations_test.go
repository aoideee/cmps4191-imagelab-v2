package main

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestObservationHubBroadcastsAndReleases(t *testing.T) {
	hub := newObservationHub()
	first := hub.subscribe("job-1")
	second := hub.subscribe("job-1")

	hub.notify("job-1")

	for name, signal := range map[string]<-chan struct{}{
		"first":  first.C,
		"second": second.C,
	} {
		select {
		case <-signal:
		case <-time.After(time.Second):
			t.Fatalf("%s observer was not notified", name)
		}
	}

	first.release()
	second.release()
	if len(hub.entries) != 0 {
		t.Fatalf("expected released hub entries; got %d", len(hub.entries))
	}
}

func TestParseObservationQuery(t *testing.T) {
	tests := []struct {
		name     string
		target   string
		wantLong bool
		wantWait time.Duration
		wantErr  bool
	}{
		{name: "prompt", target: "/v1/jobs/id"},
		{name: "default wait", target: "/v1/jobs/id?after=7", wantLong: true, wantWait: 20 * time.Second},
		{name: "explicit wait", target: "/v1/jobs/id?after=0&wait=3", wantLong: true, wantWait: 3 * time.Second},
		{name: "wait alone", target: "/v1/jobs/id?wait=3", wantErr: true},
		{name: "negative after", target: "/v1/jobs/id?after=-1", wantErr: true},
		{name: "wait too long", target: "/v1/jobs/id?after=1&wait=21", wantErr: true},
		{name: "fractional wait", target: "/v1/jobs/id?after=1&wait=1.5", wantErr: true},
		{name: "unknown parameter", target: "/v1/jobs/id?after=1&other=x", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", tc.target, nil)
			_, wait, longPolling, err := parseObservationQuery(r)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v; wantErr %v", err, tc.wantErr)
			}
			if longPolling != tc.wantLong {
				t.Errorf("longPolling = %v; want %v", longPolling, tc.wantLong)
			}
			if wait != tc.wantWait {
				t.Errorf("wait = %v; want %v", wait, tc.wantWait)
			}
		})
	}
}
