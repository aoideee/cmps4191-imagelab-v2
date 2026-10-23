import { emitter } from "./modules/event-emitter.js";

const state = {
  selectedFile: null,
  isValidating: false,
  isSubmitting: false, // Double-submit guard
  activeJob: null, // Stores status, job_id, image_id, timestamps
  uploadError: null,
  variants: [],
  observationMode: "long",
  lastObservedVersion: null,
  pollingTimer: null,
  abortController: null, // For cancelling fetch requests on reset
  observationError: false, // Distinguishes GET failure from job failure[cite: 1]
  metrics: {
    requestStart: null,
    ackLatency: null,
    pollingCount: 0,
    completionObservedAt: null,
  },
};

export function getState() {
  return state;
}

export function setState(updates) {
  Object.assign(state, updates);
  emitter.emit("stateChanged", state);
}
