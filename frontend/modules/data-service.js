export const API_BASE_URL = "http://localhost:4000";

function hasText(value) {
  return typeof value === "string" && value.trim() !== "";
}

function matchesApiPath(value, expectedPath) {
  if (!hasText(value)) return false;

  try {
    const actual = new URL(value, API_BASE_URL);
    const expected = new URL(expectedPath, API_BASE_URL);
    return actual.href === expected.href;
  } catch {
    return false;
  }
}

export const DataService = {
  async uploadImage(file) {
    const formData = new FormData();
    formData.append("image", file);

    const response = await fetch(`${API_BASE_URL}/v1/images`, {
      method: "POST",
      body: formData,
    });

    if (response.status !== 202) {
      const errData = await response.json().catch(() => ({}));
      throw new Error(
        errData.error || `Upload failed with status ${response.status}`,
      );
    }

        let data;

        try {
          data = await response.json();
        } catch {
          throw new Error(
            "The server returned 202 Accepted, but its reply could not be read. " +
              "The image may already be queued; submitting again may create another job.",
          );
        }

        if (
          !data ||
          !hasText(data.image_id) ||
          !hasText(data.job_id) ||
          data.status !== "queued"
        ) {
          throw new Error(
            "The server returned 202 Accepted without usable job details. " +
              "The image may already be queued; submitting again may create another job.",
          );
        }

        const expectedPath = `/v1/jobs/${encodeURIComponent(data.job_id)}`;

        if (
          !matchesApiPath(data.status_url, expectedPath) ||
          !matchesApiPath(response.headers.get("Location"), expectedPath)
        ) {
          throw new Error(
            "The server returned 202 Accepted without a matching status address. " +
              "The image may already be queued; submitting again may create another job.",
          );
        }

        return data;
  },

  async fetchJobStatus(statusUrl, signal, timeoutMs = 30000) {
    const url = statusUrl.startsWith("http")
      ? statusUrl
      : `${API_BASE_URL}${statusUrl}`;

    // This controller belongs to this individual request.
    const requestController = new AbortController();
    let timedOut = false;

    // Forward deliberate cancellation from the polling loop.
    const cancelRequest = () => {
      requestController.abort();
    };

    if (signal?.aborted) {
      cancelRequest();
    } else {
      signal?.addEventListener("abort", cancelRequest, { once: true });
    }

    const timeoutId = setTimeout(() => {
      timedOut = true;
      requestController.abort();
    }, timeoutMs);

    try {
      const response = await fetch(url, {
        signal: requestController.signal,
      });

      if (response.status === 204) {
        return null;
      }

      if (!response.ok) {
        const errData = await response.json().catch(() => ({}));
        throw new Error(
          errData.error || `Failed to observe job: ${response.status}`,
        );
      }

      const data = await response.json();

      const expectedStates = ["queued", "processing", "completed", "failed"];

      if (!data || typeof data.id !== "string" || data.id.trim() === "") {
        throw new Error("Status response is missing a usable job ID");
      }

      if (!expectedStates.includes(data.status)) {
        throw new Error("Status response contains an unexpected status");
      }

      if (!Number.isInteger(data.version) || data.version < 1) {
        throw new Error("Status response is missing a valid version");
      }

      if (!hasText(data.stage)) {
        throw new Error("Status response is missing a progress stage");
      }

      if (
        !Number.isInteger(data.variants_completed) ||
        data.variants_completed < 0 ||
        data.variants_completed > 3
      ) {
        throw new Error("Status response has invalid variant progress");
      }

	  if (!Number.isInteger(data.simulation_step) || data.simulation_step < 0) {
		throw new Error("Status response has invalid simulated progress");
	  }

            if (data.status === "completed") {
              if (!hasText(data.image_id)) {
                throw new Error("Completed response is missing an image ID");
              }

              if (!Array.isArray(data.variants) || data.variants.length !== 3) {
                throw new Error(
                  "Completed response must contain three variants",
                );
              }

              const requiredNames = new Set([
                "thumbnail",
                "preview",
                "display",
              ]);

              for (const variant of data.variants) {
                if (!variant || !requiredNames.delete(variant.name)) {
                  throw new Error(
                    "Completed response has duplicate or unknown variants",
                  );
                }

                if (
                  !Number.isInteger(variant.width) ||
                  !Number.isInteger(variant.height) ||
                  variant.width <= 0 ||
                  variant.height <= 0
                ) {
                  throw new Error(
                    "Variant dimensions must be positive whole numbers",
                  );
                }

                if (
                  variant.name === "thumbnail" &&
                  (variant.width !== 150 || variant.height !== 150)
                ) {
                  throw new Error("Thumbnail dimensions must be 150 by 150");
                }

                if (
                  variant.name === "preview" &&
                  (variant.width > 800 || variant.height > 600)
                ) {
                  throw new Error(
                    "Preview dimensions exceed the allowed bounds",
                  );
                }

                if (
                  variant.name === "display" &&
                  (variant.width > 1200 || variant.height > 900)
                ) {
                  throw new Error(
                    "Display dimensions exceed the allowed bounds",
                  );
                }

                const expectedPath =
                  `/v1/images/${encodeURIComponent(data.image_id)}` +
                  `/variants/${variant.name}`;

                if (!matchesApiPath(variant.url, expectedPath)) {
                  throw new Error(
                    "Variant URL does not match the completed image",
                  );
                }
              }
            }

      return data;
    } catch (err) {
      // Switching jobs or leaving the page is deliberate cancellation.
      if (signal?.aborted) {
        throw err;
      }

      // A timeout must reach the polling loop as an observation error.
      if (timedOut) {
        const timeoutError = new Error("Status request timed out");
        timeoutError.name = "TimeoutError";
        throw timeoutError;
      }

      throw err;
    } finally {
      clearTimeout(timeoutId);
      signal?.removeEventListener("abort", cancelRequest);
    }
  },
};
