package main

import "sync"

// observationHub broadcasts an in-process change hint to every handler
// currently observing a job. PostgreSQL remains the authoritative state.
type observationHub struct {
	mu      sync.Mutex
	entries map[string]*observationEntry
}

type observationEntry struct {
	changed chan struct{}
	waiters int
}

type observationSubscription struct {
	hub   *observationHub
	jobID string
	entry *observationEntry
	C     <-chan struct{}
	once  sync.Once
}

func newObservationHub() *observationHub {
	return &observationHub{entries: make(map[string]*observationEntry)}
}

func (h *observationHub) subscribe(jobID string) *observationSubscription {
	h.mu.Lock()
	defer h.mu.Unlock()

	entry := h.entries[jobID]
	if entry == nil {
		entry = &observationEntry{changed: make(chan struct{})}
		h.entries[jobID] = entry
	}
	entry.waiters++

	return &observationSubscription{
		hub:   h,
		jobID: jobID,
		entry: entry,
		C:     entry.changed,
	}
}

func (s *observationSubscription) release() {
	s.once.Do(func() {
		s.hub.mu.Lock()
		defer s.hub.mu.Unlock()

		entry := s.hub.entries[s.jobID]
		if entry != s.entry {
			return
		}
		entry.waiters--
		if entry.waiters == 0 {
			delete(s.hub.entries, s.jobID)
		}
	})
}

// notify must only be called after the transaction making the change commits.
func (h *observationHub) notify(jobID string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	entry := h.entries[jobID]
	if entry == nil {
		return
	}

	close(entry.changed)
	entry.changed = make(chan struct{})
}
