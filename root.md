# Lantern — Change Log

All changes made to the project are tracked here. Each entry includes the date, a summary, and affected files.

---

## 2026-03-30 — Backlog Analysis & Implementation Planning

### Changes Made

1. **Backlog status audit** — Reviewed all 13 items in `LANTERN_V1_TEMPLATE_BACKLOG.md` against the codebase. Result: coordinator backend is mature; web UI has zero compute features; no worker executors exist.

2. **Git commit & push** — Staged and committed all pending changes (28 files, 6,332 insertions) with message `feat: template compute coordinator, REST API, and planning docs`. Pushed to `origin/main` as `81c286e`.
   - Modified: `cmd/lantern/main.go`, `internal/client/client.go`, `internal/config/config.go`, `internal/config/config_test.go`, `internal/index/index.go`, `internal/protocol/messages.go`, `internal/server/bridge.go`, `internal/server/compute.go`, `internal/server/compute_test.go`, `internal/server/handler.go`, `internal/server/handler_compute_test.go`, `internal/server/server.go`, `internal/web/server.go`
   - Added: `LANTERN_V1_TEMPLATE_BACKLOG.md`, `LANTERN_V1_TEMPLATE_COMPUTE_PLAN.md`, `WEB_COMPUTE_INTEGRATION_PLAN.md`, `internal/server/compute_templates.go`, `internal/web/server_test.go`, `stitch_exports/` (5 HTML + 5 PNG design exports)

3. **Implementation plan brainstormed** — Created a 4-slice delivery plan covering all 13 backlog items:
   - Slice 1: Dashboard tabs, job wizard, operator controls, overview panel
   - Slice 2: Worker executor framework, Data Processing executor, artifact assembly & download
   - Slice 3: Execution logs, real preflight validation, Image Batch & OCR executors
   - Slice 4: Video Transcode & Render Frames executors, E2E test coverage
   - Key architecture decisions: inline template schemas, coordinator-side zip assembly, `Executor` interface pattern, tab-based single-HTML dashboard

### Files Created This Session
- `root.md` (this file)

---

## 2026-03-30 — Open Questions Resolved

### Decisions

1. **Worker binary → `--worker` flag on existing `cmd/lantern/main.go`** — single binary deployment, simpler for Raspberry Pi targets.
2. **Compute token → Unauthenticated for trusted LAN** — all dashboard API endpoints work without a token by default. `ComputeRequireToken` config stays available but defaults to `false`.
3. **Dashboard design → Current glassmorphism style** — Stitch designs deferred until features are functionally complete.

### Files Changed
- `root.md` — Updated with resolved decisions

### Status
- ✅ Implementation plan finalized — all questions resolved, ready to execute when approved

---

## 2026-03-30 — Slice 1: Compute Dashboard + Job Wizard

### Changes Made

1. **Template Schema** — Added `TemplateFieldDescriptor` struct and `Schema` field to `ComputeTemplate`. Added `Icon` field. All 5 templates now have full schema definitions with field types (string, string[], int, bool, select), labels, defaults, help text, and validation.

2. **Dashboard Tabs** — Replaced flat file-only UI with 4-tab navigation (Files | Jobs | Workers | Overview). Used CSS radio-button-style tab bar matching existing glassmorphism design. Each tab panel lazy-loads data.

3. **Jobs Tab** — Displays all compute jobs with template icon, status pill, progress bar, and task count. Click-to-expand shows task detail table (task ID, status, worker, attempt, error). Retry button for jobs in `needs_attention` state.

4. **Workers Tab** — Shows connected workers with status, capabilities, task count, and reliability score bar. Includes Disable/Enable buttons wired to existing API endpoints.

5. **Overview Tab** — Renders stat cards (active, completed, needs attention, completion rate), failure taxonomy horizontal bar chart, and per-template metrics. "Requeue Stalled" button wired to API.

6. **New Job Wizard** — 3-step modal wizard:
   - Step 1: Template picker cards with icons and descriptions
   - Step 2: Schema-driven dynamic forms (auto-generated from `TemplateFieldDescriptor[]`)
   - Step 3: Preflight review with pass/warn/fail checks and confidence badge
   - Submit wires to `POST /api/compute/jobs` and navigates to Jobs tab

7. **All operator controls** — Retry job, requeue stalled, disable/enable worker — all wired with toast feedback.

### Files Modified
- `internal/server/compute_templates.go` — Added `TemplateFieldDescriptor`, `Icon`, `Schema` to all 5 templates
- `internal/web/static/index.html` — Added ~600 lines of CSS + HTML + JS for complete compute dashboard

### Verification
- ✅ `go build ./...` passes
- ✅ `go test ./...` passes (all 6 test packages)

---

## 2026-03-30 — Slice 2: Artifact Assembly & Executor Framework

### Changes Made

1. **Coordinator artifact assembly pipeline** — Added pluggable artifact assembly to compute coordinator via `ComputeArtifactAssembler` and wired server-level final packaging into job finalization.
   - Jobs now remain in `assembling` until a final artifact exists (`final_package` or fallback `summary_json`).
   - Task artifact kind normalized to `task_package`.
   - Assembly failures now route job state to `needs_attention` with failure reason/category.

2. **Final artifact bundling** — Added zip packaging flow that creates a single downloadable final package containing:
   - `manifest.json` with job/task/artifact metadata
   - all per-task artifact files under `task_artifacts/`
   - generated metadata/checksum for storage registration

3. **Worker executor payload compatibility** — Updated `data_processing_batch` executor to support both compiled template payloads and legacy payload structure.
   - Added compiled payload parsing (`template`, `dataset`, `transforms`, `summary_report`).
   - Added transform pipeline helpers and deterministic output naming.

4. **Dashboard API contract fixes** — Updated web UI request/response mapping for compute flows.
   - Job preflight/submit now sends `template` (not `template_id`).
   - Worker empty-state command text corrected to `lantern worker`.

### Files Modified
- `internal/server/compute.go`
- `internal/server/compute_test.go`
- `internal/server/server.go`
- `internal/server/compute_artifacts.go` [NEW]
- `internal/web/static/index.html`
- `internal/worker/data_processing.go`
- `internal/worker/data_processing_test.go` [NEW]

---

## 2026-03-31 — Distributed Compute Stabilization

### Changes Made

1. **Preflight Submission Gate Fix** — Modified `internal/server/compute.go` (capability_match check) from a hard `fail` to `warn`.
   - Allows job submission and queuing even when no workers are currently online or healthy.

2. **File Transmission Protocol Fix (Critical)** — Fixed a hang in `internal/client/client.go` file upload loop.
   - Switched from `io.EOF` detection to explicit chunk counting (`seq < totalChunks`).
   - Fixed "perfect-multiple" buffer boundary hang (256 KB files).
   - Ensured `FlagLastChunk` is correctly set on final packets, including for 0-byte or singular-chunk transfers.

3. **Integrity Check Alignment** — Unified hash format across client/server.
   - Standardized checksum string to `sha256:<hex>` format in `internal/client/client.go`.
   - Implemented `io.Writer` interface for `SHA256Hasher` in `internal/protocol/checksum.go` for cleaner `io.Copy` usage.

4. **Worker Payload & Format Stability** — Fixed `image_batch_pipeline` crashes.
   - Added support for both singular `image` and plural `images` keys in payloads.
   - Removed unsupported `avif`/`webp` encoders (missing in stdlib); added automatic `png` fallback.
   - Updated default template format to `png`.

5. **Reliability & Maintenance** — Improved worker quarantine visibility.
   - Verified worker binary synchronization.
   - Cleaned up unused imports and improved log reporting for artifact integrity failures.

### Files Modified
- `internal/server/compute.go`
- `internal/server/compute_templates.go`
- `internal/client/client.go`
- `internal/protocol/checksum.go`
- `internal/worker/image_batch.go`
- `internal/worker/runner.go`

### Status
- ✅ End-to-end execution loop (Submit → Claim → Run → Upload → Complete) is now stable and reliable.
- ✅ Jobs can be submitted to an empty queue and will be processed immediately upon worker registration.

---

## 2026-03-31 — Dashboard Operator Cleanup Controls

### Changes Made

1. **Job removal from dashboard** — Added API + UI flow to remove compute jobs directly from the Jobs tab.
   - New endpoint: `DELETE /api/compute/jobs/{jobId}`.
   - Removes job state, task records, queue references, and associated compute artifacts.
   - Dashboard now shows a **Remove** button per job with confirmation and toast feedback.

2. **Worker removal from dashboard** — Added API + UI flow to remove workers directly from the Workers tab.
   - New endpoint: `DELETE /api/compute/workers/{workerId}`.
   - Requeues any currently leased tasks before deleting worker state.
   - Dashboard now shows a **Remove** button per worker with confirmation and toast feedback.

3. **Coverage for new APIs** — Added web handler tests to verify delete behavior for both jobs and workers.

### Files Modified
- `internal/server/compute.go`
- `internal/server/bridge.go`
- `internal/web/server.go`
- `internal/web/server_test.go`
- `internal/web/static/index.html`

### Verification
- ✅ `go test ./internal/server ./internal/web` passes

---

## 2026-03-31 — Runtime Hang Fix + Artifact Output Correctness

### Changes Made

1. **Upload completion deadlock fix (critical)** — Fixed a session completion deadlock that caused workers to stay on “executing task” and never fully progress.
   - In session completion, removed nested `sess.mu` re-lock path.
   - Adjusted completion flow so `COMPLETE` is sent before potentially slow persistence.

2. **Artifact upload ID collision fix** — Updated worker artifact upload path to use a dedicated client session per task artifact upload.
   - Prevents multiple task artifacts from reusing the same session-derived file ID.
   - Eliminates artifact overwrite in final package assembly.

3. **Wizard compute submission contract fix** — Corrected wizard payload key from `template_id` to `template` for preflight and submit.
   - Prevents unintended fallback to `data_processing_batch` when selecting other templates.

4. **Wizard file field UX + behavior fix** — Extended wizard file input handling for `file[]` schema fields.
   - Drag-and-drop and browse support now works for `file[]` (not only `string[]`).
   - Dropped/selected files are uploaded and their resulting Lantern file IDs are injected into the field.
   - This ensures workers download the correct uploaded files instead of operating on placeholder paths.

5. **Worker empty-state command correction** — Updated dashboard hint text from `lantern --worker` to `lantern worker`.

### Files Modified
- `internal/server/handler.go`
- `internal/worker/runner.go`
- `internal/web/static/index.html`

### Verification
- ✅ `go test ./internal/worker ./internal/server ./internal/web` passes
- ✅ `go build -o ./bin/lantern ./cmd/lantern` passes
- ✅ Runtime validation confirmed job progression to `done` with `final_package` artifact output


