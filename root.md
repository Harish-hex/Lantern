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

