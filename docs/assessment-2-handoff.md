# Assessment 2 pair-member handoff

## Completed implementation foundation

- Durable positive job versions, stages, real variant counts, and separate simulated progress.
- Prompt snapshots and version-based long polling on the same endpoint.
- Validation, 20-second maximum holds, empty `204` deadlines, cancellation, and no-store responses.
- Broadcast wakeups for every in-process observer, followed by a PostgreSQL recheck.
- Post-commit notifications and server logs with job ID, version, state/progress, and wall-clock time.
- Short/long UI controls, one request at a time, initial prompt snapshot, 30-second browser timeout, mode switching, terminal stop, and same-job retry.
- Disabled-by-default quiet and simulated workload controls.
- Structured browser console records prefixed `IMAGELAB_OBSERVATION`.
- Unit, race, frontend, PostgreSQL, two-observer, deadline, and progress integration coverage.

## Intentionally left for the pair member

This is the remaining approximately 25% of the project workflow. Do not replace the implementation or change the controls between short- and long-poll runs.

1. Write a prediction before each condition.
2. Choose one input image and fixed transformation settings.
3. Run A, B, C1, C2, and C3 in both modes at least three times: 30 runs total.
4. Alternate which mode runs first; begin with an empty worker queue and one foreground observer.
5. For C3, add about 200 ms round-trip latency with documented browser/network tooling and record the actual value plus a prompt-GET timing check.
6. Save raw browser `IMAGELAB_OBSERVATION` lines, server commit logs, and evidence screenshots/traces. Account for every started request.
7. Fill `docs/assessment-2-summary.csv`. Preserve unexpected results.
8. Calculate median and minimum-maximum request count and detection delay for every condition/mode.
9. Produce two labelled charts: request count by condition and completion-detection delay by condition.
10. Write the 2-3 page report covering predictions, method, results, interpretation, limitations, recommendation, and the questions in the brief.
11. Record the 20-30 minute narrated demonstration and add the unlisted YouTube link.

## Run configurations

| Condition | API flags | Additional setup |
| --- | --- | --- |
| A quiet | `-job-delay=30s` | Simulation disabled |
| B real | no workload flags | Ordinary real processing |
| C1 slow | `-simulation-interval=2s -simulation-duration=10s` | No artificial latency |
| C2 fast | `-simulation-interval=200ms -simulation-duration=10s` | No artificial latency |
| C3 fast + latency | same as C2 | Document about 200 ms added RTT |

Restart the API with the chosen flags and record the startup configuration for every batch. Use the same configuration for both modes.

## Required manual acceptance evidence

- Older `after` returns a newer snapshot promptly.
- A matching active version visibly remains pending and wakes on commit.
- An update between requests is returned immediately.
- An unchanged hold returns `204`, followed by another request without an error.
- Two tabs both discover the same change.
- Completed and failed jobs stop both modes.
- Switching mode or image cancels the old request without stopping worker progress.
- Offline/retrieval failure preserves the last snapshot; Try again performs no new upload.
- Real counts match stored variants; simulated progress is visibly labelled.

## Timestamp calculation

Use browser `Date.now()` at response receipt minus the matching server
`committed_at` wall-clock value for the terminal job/version. Do not subtract
`performance.now()` from a server timestamp. State clock resolution and local
scheduling uncertainty in the report.
