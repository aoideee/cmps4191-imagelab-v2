import test from "node:test";
import assert from "node:assert/strict";
import { DataService } from "./data-service.js";

// Simulate a request that never finishes unless its signal is aborted.
function waitingFetch(_url, { signal }) {
  return new Promise((resolve, reject) => {
    const rejectOnAbort = () => {
      reject(new DOMException("Request cancelled", "AbortError"));
    };

    if (signal.aborted) {
      rejectOnAbort();
      return;
    }

    signal.addEventListener("abort", rejectOnAbort, { once: true });
  });
}

test("a stalled status request becomes a TimeoutError", async (t) => {
  t.mock.method(globalThis, "fetch", waitingFetch);

  const controller = new AbortController();

  await assert.rejects(
    DataService.fetchJobStatus("/v1/jobs/test-job", controller.signal, 20),
    { name: "TimeoutError" },
  );

  // Timing out one request must not abort the observation controller.
  assert.equal(controller.signal.aborted, false);
});

test("deliberate cancellation remains an AbortError", async (t) => {
  t.mock.method(globalThis, "fetch", waitingFetch);

  const controller = new AbortController();

  const request = DataService.fetchJobStatus(
    "/v1/jobs/test-job",
    controller.signal,
    1000,
  );

  controller.abort();

  await assert.rejects(request, { name: "AbortError" });
});

test("a successful status response is returned normally", async (t) => {
  const expected = {
    id: "test-job",
    status: "processing",
	version: 3,
	stage: "thumbnail_ready",
	variants_completed: 1,
  };

  t.mock.method(globalThis, "fetch", async () => ({
    ok: true,
    json: async () => expected,
  }));

  const controller = new AbortController();

  const result = await DataService.fetchJobStatus(
    "/v1/jobs/test-job",
    controller.signal,
    1000,
  );

  assert.deepEqual(result, expected);
});

function completedJob() {
  return {
    id: "test-job",
    image_id: "test-image",
    status: "completed",
	version: 5,
	stage: "display_ready",
	variants_completed: 3,
    variants: [
      {
        name: "thumbnail",
        width: 150,
        height: 150,
        url: "/v1/images/test-image/variants/thumbnail",
      },
      {
        name: "preview",
        width: 800,
        height: 600,
        url: "/v1/images/test-image/variants/preview",
      },
      {
        name: "display",
        width: 1200,
        height: 900,
        url: "/v1/images/test-image/variants/display",
      },
    ],
  };
}

test("a 204 long-poll deadline returns no snapshot", async (t) => {
  t.mock.method(globalThis, "fetch", async () => ({
	status: 204,
	ok: true,
	json: async () => {
	  throw new Error("204 body must not be parsed");
	},
  }));

  const result = await DataService.fetchJobStatus(
	"/v1/jobs/test-job?after=2&wait=20",
  );

  assert.equal(result, null);
});

test("a status response without a durable version is rejected", async (t) => {
  const reply = completedJob();
  delete reply.version;

  t.mock.method(globalThis, "fetch", async () => ({
	status: 200,
	ok: true,
	json: async () => reply,
  }));

  await assert.rejects(
	DataService.fetchJobStatus("/v1/jobs/test-job"),
	/missing a valid version/,
  );
});

test("a valid completed job is returned normally", async (t) => {
  const expected = completedJob();

  t.mock.method(globalThis, "fetch", async () => ({
    ok: true,
    json: async () => expected,
  }));

  const result = await DataService.fetchJobStatus("/v1/jobs/test-job");

  assert.deepEqual(result, expected);
});

const invalidCompletedCases = [
  {
    name: "missing variant",
    change: (job) => job.variants.pop(),
    error: /must contain three variants/,
  },
  {
    name: "duplicate variant",
    change: (job) => {
      job.variants[2] = { ...job.variants[0] };
    },
    error: /duplicate or unknown variants/,
  },
  {
    name: "zero width",
    change: (job) => {
      job.variants[1].width = 0;
    },
    error: /positive whole numbers/,
  },
  {
    name: "oversized preview",
    change: (job) => {
      job.variants[1].width = 801;
    },
    error: /Preview dimensions exceed/,
  },
  {
    name: "download address belongs to another image",
    change: (job) => {
      job.variants[1].url = "/v1/images/another-image/variants/preview";
    },
    error: /URL does not match/,
  },
];

for (const example of invalidCompletedCases) {
  test(`completed response rejects ${example.name}`, async (t) => {
    const reply = completedJob();
    example.change(reply);

    t.mock.method(globalThis, "fetch", async () => ({
      ok: true,
      json: async () => reply,
    }));

    await assert.rejects(
      DataService.fetchJobStatus("/v1/jobs/test-job"),
      example.error,
    );
  });
}

function acceptedUpload() {
  return {
    image_id: "test-image",
    job_id: "test-job",
    status: "queued",
    status_url: "/v1/jobs/test-job",
  };
}

test("a valid upload acknowledgement is returned normally", async (t) => {
  const expected = acceptedUpload();

  t.mock.method(globalThis, "fetch", async () => ({
    status: 202,
    headers: new Headers({ Location: "/v1/jobs/test-job" }),
    json: async () => expected,
  }));

  const result = await DataService.uploadImage(
    new Blob(["test"], { type: "image/png" }),
  );

  assert.deepEqual(result, expected);
});

test("an accepted upload with missing job details warns about resubmitting", async (t) => {
  const reply = acceptedUpload();
  delete reply.job_id;

  t.mock.method(globalThis, "fetch", async () => ({
    status: 202,
    headers: new Headers({ Location: "/v1/jobs/test-job" }),
    json: async () => reply,
  }));

  await assert.rejects(
    DataService.uploadImage(new Blob(["test"])),
    /without usable job details.*submitting again may create another job/,
  );
});

test("an accepted upload rejects a mismatched Location", async (t) => {
  t.mock.method(globalThis, "fetch", async () => ({
    status: 202,
    headers: new Headers({ Location: "/v1/jobs/wrong-job" }),
    json: async () => acceptedUpload(),
  }));

  await assert.rejects(
    DataService.uploadImage(new Blob(["test"])),
    /without a matching status address/,
  );
});
