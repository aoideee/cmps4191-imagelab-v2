import { getState, setState } from "./state.js";
import { DataService } from "./modules/data-service.js";
import { buildMeasurement } from "./modules/measurements.js";

const fileInput = document.getElementById("file-input");
const chooseImageBtn = document.getElementById("choose-image-btn");
const processBtn = document.getElementById("process-btn");
const tryAgainBtn = document.getElementById("try-again-btn");
const modeInputs = document.querySelectorAll('input[name="observation-mode"]');

let selectionVersion = 0;
let observationGeneration = 0;

window.addEventListener("beforeunload", stopObservation);

for (const input of modeInputs) {
  input.addEventListener("change", () => {
    if (!input.checked) return;

    setState({ observationMode: input.value });
    const { activeJob } = getState();
    if (activeJob?.status_url && !isTerminal(activeJob.status)) {
      startObservation(activeJob.status_url);
    }
  });
}

fileInput.addEventListener("change", async (event) => {
  if (getState().isSubmitting) return;

  const file = event.target.files[0];
  if (!file) return;

  const thisSelection = ++selectionVersion;
  const maxImageSize = 10 * 1024 * 1024;
  const allowedTypes = ["image/jpeg", "image/png"];

  let error = null;
  if (file.size === 0) {
    error = "This file is empty. Choose a JPEG or PNG image.";
  } else if (!allowedTypes.includes(file.type)) {
    error = "Choose a JPEG or PNG image.";
  } else if (file.size > maxImageSize) {
    error = "This image exceeds the 10 MB limit. Choose a smaller image.";
  }

  if (error) {
    fileInput.value = "";
    setState({ selectedFile: null, isValidating: false, uploadError: error });
    return;
  }

  setState({ selectedFile: null, isValidating: true, uploadError: null });
  let checkUrl = null;

  try {
    checkUrl = URL.createObjectURL(file);
    const imageCheck = new Image();
    imageCheck.src = checkUrl;
    await imageCheck.decode();
  } catch {
    if (thisSelection !== selectionVersion) return;
    fileInput.value = "";
    setState({
      selectedFile: null,
      isValidating: false,
      uploadError:
        "This image could not be read. It may be damaged. Choose another JPEG or PNG.",
    });
    return;
  } finally {
    if (checkUrl !== null) URL.revokeObjectURL(checkUrl);
  }

  if (thisSelection !== selectionVersion) return;

  stopObservation();
  setState({
    selectedFile: file,
    isValidating: false,
    uploadError: null,
    activeJob: null,
    variants: [],
    lastObservedVersion: null,
    observationError: false,
    metrics: freshMetrics(),
  });
});

processBtn.addEventListener("click", async () => {
  const { isSubmitting, isValidating, selectedFile, activeJob } = getState();
  if (
    isSubmitting ||
    isValidating ||
    !selectedFile ||
    (activeJob && !isTerminal(activeJob.status))
  ) {
    return;
  }

  stopObservation();
  const requestStart = performance.now();
  setState({ isSubmitting: true, uploadError: null, observationError: false });

  try {
    const accepted = await DataService.uploadImage(selectedFile);
    setState({
      activeJob: accepted,
      lastObservedVersion: null,
      metrics: {
        ...freshMetrics(),
        requestStart,
        ackLatency: performance.now() - requestStart,
      },
    });
    startObservation(accepted.status_url);
  } catch (error) {
    setState({ uploadError: error.message || "Submission error" });
  } finally {
    setState({ isSubmitting: false });
  }
});

chooseImageBtn.addEventListener("click", () => {
  if (getState().isSubmitting) return;
  fileInput.value = "";
  fileInput.click();
});

tryAgainBtn.addEventListener("click", () => {
  const { activeJob } = getState();
  if (activeJob?.status_url) {
    setState({ observationError: false });
    startObservation(activeJob.status_url);
  }
});

function freshMetrics() {
  return {
    requestStart: null,
    ackLatency: null,
    pollingCount: 0,
    completionObservedAt: null,
  };
}

function isTerminal(status) {
  return status === "completed" || status === "failed";
}

function startObservation(statusUrl) {
  stopObservation();

  const generation = observationGeneration;
  const controller = new AbortController();
  setState({ abortController: controller, pollingTimer: null });
  void runObservation(statusUrl, controller, generation);
}

function stopObservation() {
  observationGeneration++;
  const { pollingTimer, abortController } = getState();
  if (pollingTimer !== null) clearTimeout(pollingTimer);
  abortController?.abort();
  setState({ pollingTimer: null, abortController: null });
}

async function runObservation(statusUrl, controller, generation) {
  const isCurrent = () =>
    generation === observationGeneration &&
    getState().abortController === controller &&
    !controller.signal.aborted;

  let initialSnapshot = true;

  try {
    while (isCurrent()) {
      const mode = getState().observationMode;

      if (!initialSnapshot && mode === "short") {
        await delay(1000, controller.signal, (timer) => {
          if (isCurrent()) setState({ pollingTimer: timer });
        });
        if (!isCurrent()) return;
        setState({ pollingTimer: null });
      }

      let requestUrl = statusUrl;
      if (!initialSnapshot && mode === "long") {
        const after = getState().lastObservedVersion;
        requestUrl = `${statusUrl}?after=${encodeURIComponent(after)}&wait=20`;
      }

      setState({
        metrics: {
          ...getState().metrics,
          pollingCount: getState().metrics.pollingCount + 1,
        },
      });

      const jobData = await DataService.fetchJobStatus(
        requestUrl,
        controller.signal,
        30000,
      );
      const responseObservedAt = Date.now();

      if (!isCurrent()) return;

      // 204 is a normal holding-deadline result: retain the last version and
      // immediately issue the next long poll without attempting JSON parsing.
      if (jobData === null) {
        initialSnapshot = false;
        continue;
      }

      const activeJob = getState().activeJob;
      const expectedJobId = activeJob?.job_id ?? activeJob?.id;
      if (jobData.id !== expectedJobId) {
        throw new Error("Status response belongs to a different job");
      }

      const previousVersion = getState().lastObservedVersion;
      if (previousVersion !== null && jobData.version < previousVersion) {
        throw new Error("Status response moved to an older version");
      }

      const mergedJob = { ...jobData, status_url: statusUrl };
      setState({
        activeJob: mergedJob,
        lastObservedVersion: jobData.version,
        variants: jobData.variants || [],
      });

      initialSnapshot = false;
      if (isTerminal(jobData.status)) {
        const metrics = {
          ...getState().metrics,
          completionObservedAt:
            jobData.status === "completed" ? responseObservedAt : null,
        };
        setState({ metrics });
        console.table([buildMeasurement(mergedJob, metrics)]);
        stopObservation();
        return;
      }
    }
  } catch (error) {
    if (!isCurrent() || error.name === "AbortError") return;
    stopObservation();
    setState({ observationError: true });
  }
}

function delay(ms, signal, onTimer) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      signal.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    onTimer(timer);

    function onAbort() {
      clearTimeout(timer);
      reject(new DOMException("Observation cancelled", "AbortError"));
    }

    signal.addEventListener("abort", onAbort, { once: true });
  });
}
