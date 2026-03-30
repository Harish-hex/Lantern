# Web Compute Integration Plan

This document explains the most efficient way to integrate compute visibility into the Lantern web interface while keeping risk low and delivery fast.

## Objective

Deliver a user-friendly web experience for compute operations by exposing read-only visibility into jobs, tasks, and workers first.

## Scope for First Release

Included:
- Read-only compute APIs over HTTP
- Dashboard panels for jobs, tasks, and workers
- Polling-based updates for quick implementation

Not included (later phases):
- Web-based job submission
- Web-based worker control
- Cancel/retry controls in browser
- New auth model changes

## Why This Order Is Efficient

1. Reuses existing compute coordinator and CLI behavior without introducing protocol risk.
2. Delivers immediate user value: people can see what is running and where failures happen.
3. Keeps write operations in CLI while observability stabilizes.
4. Minimizes refactor cost before adding richer controls.

## Implementation Phases

## Phase 1: Backend Read Surface

Goal: expose stable snapshots of compute state to the web layer.

Tasks:
1. Add coordinator read accessors for:
- list jobs
- get job plus tasks
- list workers
2. Add bridge methods that return safe snapshots (not mutable internal maps).
3. Define DTO response shapes with derived values:
- job progress percentage
- worker freshness indicator

Likely files:
- [internal/server/compute.go](internal/server/compute.go)
- [internal/server/bridge.go](internal/server/bridge.go)

## Phase 2: HTTP Compute Endpoints

Goal: make compute state available through web APIs.

Endpoints:
1. GET /api/compute/jobs
2. GET /api/compute/jobs/{jobID}
3. GET /api/compute/workers

Tasks:
1. Register routes in web server.
2. Implement handlers with consistent JSON error format.
3. Return 503 when compute is disabled.
4. Keep endpoints read-only.

Likely files:
- [internal/web/server.go](internal/web/server.go)

## Phase 3: Dashboard UI (Read-Only)

Goal: present compute status clearly in the existing web app.

UI sections:
1. Jobs panel: status, totals, progress.
2. Job detail panel: task rows with state, worker, attempt.
3. Worker panel: status, capabilities, last seen.

Tasks:
1. Add compute markup sections.
2. Add fetch logic for all compute endpoints.
3. Render state with clear status badges.
4. Poll every 2 to 3 seconds.

Likely files:
- [internal/web/static/index.html](internal/web/static/index.html)

## Phase 4: Optional Live Updates (Recommended)

Goal: reduce polling pressure and improve responsiveness.

Tasks:
1. Extend SSE event schema for compute events.
2. Publish events on coordinator transitions:
- job submitted
- task leased
- task completed or failed
- worker registered or heartbeat
3. Patch UI rows incrementally on event receipt.

Likely files:
- [internal/web/hub.go](internal/web/hub.go)
- [internal/server/handler.go](internal/server/handler.go)
- [internal/web/static/index.html](internal/web/static/index.html)

## Phase 5: Testing and Validation

Automated:
1. Add handler tests for compute API status codes and payload shape.
2. Add snapshot tests for coordinator list and detail accessors.
3. Run go test ./...

Manual smoke test:
1. Start server with HTTP enabled.
2. Submit a job via CLI.
3. Run one worker via CLI.
4. Open dashboard and verify queued to leased to completed transitions.
5. Restart server and confirm persisted states remain visible.

## Execution Order and Dependencies

Critical path:
1. Phase 1
2. Phase 2
3. Phase 3
4. Validation

Parallel work:
1. UI skeleton can start once endpoint contract is finalized.
2. SSE extension can be built after Phase 2 without blocking initial release.

## Definition of Done for This Milestone

1. Compute jobs, tasks, and workers are visible in web dashboard.
2. Status transitions are visible within expected refresh interval.
3. API and UI tests pass.
4. Full test suite passes.
5. No regressions to file transfer dashboard behavior.

## Next Milestone After This

After read-only visibility is stable:
1. Add web job submission.
2. Add optional cancel/retry controls.
3. Add token-protected write endpoints.
