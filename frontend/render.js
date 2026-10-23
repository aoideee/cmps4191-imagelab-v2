import { emitter } from "./modules/event-emitter.js";
import { API_BASE_URL } from "./modules/data-service.js";
import { buildMeasurement } from "./modules/measurements.js";
let previewFile = null;
let previewUrl = null;

emitter.on("stateChanged", (state) => {
    const measurementsPanel = document.getElementById("measurements-panel");
    const measurementsGrid = document.getElementById("measurements-grid");

    const showMeasurements =
      !state.isSubmitting &&
      !state.isValidating &&
      state.activeJob &&
      ["completed", "failed"].includes(state.activeJob.status);

    measurementsPanel.classList.toggle("hidden", !showMeasurements);
    measurementsGrid.replaceChildren();

    if (showMeasurements) {
      const measurement = buildMeasurement(state.activeJob, state.metrics);

      const formatDuration = (value) => {
        if (!Number.isFinite(value)) return "—";

        return Math.abs(value) < 1000
          ? `${value.toFixed(1)} ms`
          : `${(value / 1000).toFixed(3)} s`;
      };

      const items = [
        {
          label: "Acknowledgement",
          value: formatDuration(measurement.acknowledgement_ms),
          description: "Upload start to validated acceptance reply.",
        },
        {
          label: "Queue wait",
          value: formatDuration(measurement.queue_wait_ms),
          description: "Time waiting for the worker to start.",
        },
        {
          label: "Processing",
          value: formatDuration(measurement.processing_ms),
          description: "Worker start to success or failure.",
        },
        {
          label: "Job duration",
          value: formatDuration(measurement.job_duration_ms),
          description: "Queue entry to successful completion.",
        },
        {
          label: "Status requests",
          value: String(measurement.polling_count),
          description: "Status checks attempted for this job.",
        },
        {
          label: "Detection delay",
          value: formatDuration(measurement.detection_delay_ms),
          description: "Server completion to browser observation.",
        },
      ];

      for (const item of items) {
        const tile = document.createElement("div");
        tile.className = "measurement-tile";

        const label = document.createElement("dt");
        label.textContent = item.label;

        const value = document.createElement("dd");
        value.textContent = item.value;

        const description = document.createElement("dd");
        description.className = "measurement-description";
        description.textContent = item.description;

        tile.append(label, value, description);
        measurementsGrid.appendChild(tile);
      }
    }
  // 1. Target elements from index.html
  const previewImg = document.getElementById("preview-img");
  const fileName = document.getElementById("file-name");
  const fileMeta = document.getElementById("file-meta");
  const processBtn = document.getElementById("process-btn");
  const uploadError = document.getElementById("upload-error");
  const jobId = document.getElementById("job-id");
  const jobStatus = document.getElementById("job-status");
  const tryAgainContainer = document.getElementById("try-again-container");
  const variantsContainer = document.getElementById("variants-container");
  const labelComplete = document.getElementById("label-complete");
  const jobError = document.getElementById("job-error");
  const pollingIndicator = document.getElementById("polling-indicator");
  const jobWorkText = document.getElementById("job-work-text");
  const chooseImageBtn = document.getElementById("choose-image-btn");
  const fileInput = document.getElementById("file-input");
  const modeLabel = document.getElementById("observation-mode-label");
  const jobMode = document.getElementById("job-mode");
  const jobVersion = document.getElementById("job-version");
  const jobStage = document.getElementById("job-stage");
  const jobProgress = document.getElementById("job-progress");

  const modeText =
    state.observationMode === "long"
      ? "Long polling"
      : "Short polling (every 1 second)";
  modeLabel.textContent = modeText;
  jobMode.textContent = modeText;
  jobVersion.textContent = state.lastObservedVersion ?? "--";
  jobStage.textContent =
    state.activeJob?.stage?.replaceAll("_", " ") ?? "--";
  jobProgress.textContent = `${state.activeJob?.variants_completed ?? 0} / 3`;

  // Render Immediate Upload Error Banner (VAL-05)
  if (state.uploadError) {
    uploadError.textContent = state.uploadError;
    uploadError.classList.remove("hidden");
  } else {
    uploadError.textContent = "";
    uploadError.classList.add("hidden");
  }

  // 2. Render File Selection & Preview (UI-05)
  // 2. Render File Selection & Preview (UI-05)
  if (state.selectedFile !== previewFile) {
    if (previewUrl !== null) {
      URL.revokeObjectURL(previewUrl);
      previewUrl = null;
    }

    previewFile = state.selectedFile;

    if (previewFile) {
      previewUrl = URL.createObjectURL(previewFile);
      previewImg.src = previewUrl;
      previewImg.classList.remove("hidden");
    } else {
      previewImg.removeAttribute("src");
      previewImg.classList.add("hidden");
    }
  }

  if (state.selectedFile) {
    fileName.textContent = state.selectedFile.name;
    fileMeta.textContent = `${(state.selectedFile.size / (1024 * 1024)).toFixed(2)} MB • ${state.selectedFile.type}`;
  } else {
    fileName.textContent = "No file selected";
    fileMeta.textContent = "";
  }

  // 3. Render Button State (UI-02, UI-08)
  const isJobActive =
    state.activeJob &&
    (state.activeJob.status === "queued" ||
      state.activeJob.status === "processing");

  // Show this only while the app is observing an unfinished job.
  const isPolling =
    isJobActive && state.abortController !== null && !state.observationError;

  pollingIndicator.classList.toggle("hidden", !isPolling);

  // Explain what is happening to the current job.
  // Give the processing card a clear summary for each visible state.
  const jobCard = document.querySelector(".job-card");
  const summaryIcon = document.getElementById("job-summary-icon");
  const summaryDetail = document.getElementById("job-summary-detail");

  const summaries = {
    idle: {
      title: "Ready when you are.",
      detail: "Choose an image, then select Process image.",
      icon: "○",
    },
    uploading: {
      title: "Sending your original.",
      detail: "Waiting for the server to accept your image.",
      icon: "↑",
    },
    queued: {
      title: "Your image is in line.",
      detail: "The original is stored. Processing will begin shortly.",
      icon: "◷",
    },
    processing: {
      title: "Creating your image set.",
      detail: "Preparing thumbnail, preview, and display versions.",
      icon: "◈",
    },
    completed: {
      title: "Your images are ready.",
      detail: "All three versions are available in the results below.",
      icon: "✓",
    },
    failed: {
      title: "Processing couldn't finish.",
      detail: "See the error below for more information.",
      icon: "!",
    },
    observation: {
      title: "Status checking paused.",
      detail: "Your job may still be running. Try again to reconnect.",
      icon: "↻",
    },
  };

  const displayState = state.isSubmitting
    ? "uploading"
    : state.observationError
      ? "observation"
      : state.activeJob?.status || "idle";

  const summary = summaries[displayState] || summaries.idle;

  jobCard.dataset.status = displayState;
  jobWorkText.textContent = summary.title;
  summaryDetail.textContent = summary.detail;
  summaryIcon.textContent = summary.icon;

  chooseImageBtn.disabled = state.isSubmitting;
  fileInput.disabled = state.isSubmitting;
  processBtn.disabled =
    !state.selectedFile ||
    state.isSubmitting ||
    state.isValidating ||
    isJobActive;

  if (state.isSubmitting) {
    processBtn.textContent = "Uploading...";
  } else if (state.isValidating) {
    processBtn.textContent = "Checking image...";
  } else if (isJobActive) {
    processBtn.textContent = "Processing...";
  } else {
    processBtn.textContent = "Process image";
  }

  // 4. Render Active Job & Timeline Badges (UI-10, UI-11, UI-16)
  const stepUpload = document.getElementById("step-upload");
  const stepStored = document.getElementById("step-stored");
  const stepVariants = document.getElementById("step-variants");
  const stepComplete = document.getElementById("step-complete");
  const timeComplete = document.getElementById("time-complete");

  if (state.activeJob) {
    const status = state.activeJob.status;
    jobId.textContent = `#${state.activeJob.job_id || state.activeJob.id}`;
    jobStatus.textContent = status;
    jobStatus.className = `badge ${status}`;

    const fmt = (ts) => (ts ? new Date(ts).toLocaleTimeString() : "--");
    document.getElementById("time-upload").textContent = state.metrics
      ?.requestStart
      ? "Done"
      : "--";
    document.getElementById("time-stored").textContent = fmt(
      state.activeJob.queued_at,
    );
    document.getElementById("time-variants").textContent = fmt(
      state.activeJob.started_at,
    );

    stepUpload.className = "step-item flex-container completed";
    stepUpload.querySelector(".step-icon").textContent = "✓";

    stepStored.className = "step-item flex-container completed";
    stepStored.querySelector(".step-icon").textContent = "✓";

    // Update Generating Variants step state
    if (status === "completed") {
      stepVariants.className = "step-item flex-container completed";
      stepVariants.querySelector(".step-icon").textContent = "✓";
    } else if (status === "processing") {
      stepVariants.className = "step-item flex-container active";
      stepVariants.querySelector(".step-icon").textContent = "⚙";
    } else if (status === "failed") {
      stepVariants.className = "step-item flex-container failed";
      stepVariants.querySelector(".step-icon").textContent = "✕";
    } else {
      stepVariants.className = "step-item flex-container";
      stepVariants.querySelector(".step-icon").textContent = "○";
    }

    // Update Final Timeline Step State & Error Display
    if (status === "completed") {
      stepComplete.className = "step-item flex-container completed";
      stepComplete.querySelector(".step-icon").textContent = "✓";
      if (labelComplete) labelComplete.textContent = "Complete";
      timeComplete.textContent = fmt(state.activeJob.completed_at);
      timeComplete.style.color = "var(--text-muted)";
      if (jobError) jobError.classList.add("hidden");
    } else if (status === "failed") {
      stepComplete.className = "step-item flex-container failed";
      stepComplete.querySelector(".step-icon").textContent = "✕";
      if (labelComplete) labelComplete.textContent = "Failed";
      timeComplete.textContent = fmt(state.activeJob.failed_at);
      timeComplete.style.color = "var(--status-failed-text)";

      // Display clear, prominent processing error banner
      if (jobError) {
        jobError.textContent = `Processing Error: ${state.activeJob.error_message || "Processing failed"}`;
        jobError.classList.remove("hidden");
      }
    } else {
      stepComplete.className = "step-item flex-container";
      stepComplete.querySelector(".step-icon").textContent = "○";
      if (labelComplete) labelComplete.textContent = "Complete";
      timeComplete.textContent = "Pending";
      timeComplete.style.color = "var(--text-muted)";
      if (jobError) jobError.classList.add("hidden");
    }
  } else {
    jobId.textContent = "#--";
    jobStatus.textContent = "No active job";
    jobStatus.className = "badge";

    // Reset timestamps when no job is active
    document.getElementById("time-upload").textContent = "--";
    document.getElementById("time-stored").textContent = "--";
    document.getElementById("time-variants").textContent = "--";
    timeComplete.textContent = "--";
    timeComplete.style.color = "var(--text-muted)";

    if (labelComplete) labelComplete.textContent = "Complete";
    if (jobError) jobError.classList.add("hidden");

    // Reset timeline step icons and classes
    // Reset timeline step icons and classes.
    [stepUpload, stepStored, stepVariants, stepComplete].forEach((el) => {
      el.className = "step-item flex-container";
      el.querySelector(".step-icon").textContent = "○";
    });
  }

  // 5. Render Observation Error Prompt (POLL-09)
  tryAgainContainer.style.display = state.observationError ? "flex" : "none";

  // 6. Render Dynamically Generated Variant Cards
  if (state.activeJob?.status === "completed" && state.variants?.length) {
    variantsContainer.innerHTML = "";

    state.variants.forEach((variant) => {
      const card = document.createElement("div");
      card.className = "grid-card variant-card flex-container";

      const title = document.createElement("h4");
      title.textContent = variant.name;

      const meta = document.createElement("span");
      meta.textContent = `${variant.width} × ${variant.height}`;

      const img = document.createElement("img");
      img.src = variant.url.startsWith("http")
        ? variant.url
        : `${API_BASE_URL}${variant.url}`;
      img.alt = variant.name;

      const link = document.createElement("a");
      link.href = img.src;
      link.textContent = "View image";
      link.target = "_blank";
      link.rel = "noopener";

      const downloadBtn = document.createElement("button");
      downloadBtn.type = "button";
      downloadBtn.className = "btn-secondary";
      downloadBtn.textContent = "Download";

      downloadBtn.addEventListener("click", async () => {
        downloadBtn.disabled = true;
        downloadBtn.textContent = "Downloading…";

        try {
          const response = await fetch(img.src);

          if (!response.ok) {
            throw new Error("Download failed");
          }

          const blob = await response.blob();
          const downloadUrl = URL.createObjectURL(blob);
          const extension = blob.type === "image/png" ? "png" : "jpg";

          const downloadLink = document.createElement("a");
          downloadLink.href = downloadUrl;
          downloadLink.download = `${variant.name}.${extension}`;

          document.body.appendChild(downloadLink);
          downloadLink.click();
          downloadLink.remove();

          setTimeout(() => URL.revokeObjectURL(downloadUrl), 60000);

          downloadBtn.textContent = "Download";
        } catch (err) {
          downloadBtn.textContent = "Download failed — try again";
        } finally {
          downloadBtn.disabled = false;
        }
      });
      card.append(img, title, meta, link, downloadBtn);
      variantsContainer.appendChild(card);
    });
  } else if (isJobActive) {
    variantsContainer.innerHTML = `
    <p class="empty-state">
      Images are being generated and will appear here when processing completes.
    </p>
  `;
  } else {
    variantsContainer.innerHTML = `
    <p class="empty-state">No images generated yet.</p>
  `;
  }
});
