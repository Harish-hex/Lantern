# Lantern v1 Template-First Compute Plan

## 1. Understanding Summary

- Lantern should feel like a simple local job platform, not a low-level compute control dashboard.
- Primary audience is solo creators and small teams on LAN (2-5 workers), with general-purpose use beyond media workflows.
- UX should be template-first with predefined workflows, not raw task payload authoring.
- Default product goal is predictable completion time.
- Main success metric is job completion rate.
- Trust model for v1 is trusted LAN with shared access key.
- Reliability should be strong by default: retries and reassignment unless failure is unrecoverable.

## 2. Confirmed Assumptions

- v1 ships with 5 built-in templates.
- Primary operator is one technical owner, with occasional support from a second person.
- Most users should complete job submission in a few guided steps with defaults.
- v1 does not include enterprise auth/roles or zero-trust security.
- v1 focuses on stability and completion outcomes over broad feature surface.

## 3. Decision Log

1. Decision: Use template-first UX as the core model.
   - Alternatives: Auto-detect-first, plugin-first extensibility.
   - Why chosen: Lowest complexity and highest reliability for early users.

2. Decision: Optimize for predictable completion time.
   - Alternatives: Max throughput or lowest setup friction as top objective.
   - Why chosen: Better fit for trust, usability, and stable expectations.

3. Decision: Target 2-5 workers for first production profile.
   - Alternatives: 6-20 or 20+ as primary profile.
   - Why chosen: Matches real deployment reality and reduces scheduler complexity in v1.

4. Decision: Trusted LAN security model with shared key.
   - Alternatives: Per-user roles or mutual auth from day one.
   - Why chosen: Fastest route to adoption and operational simplicity.

5. Decision: Reliability by default with auto retries and reassignment.
   - Alternatives: Best effort or strict deterministic correctness mode by default.
   - Why chosen: Directly supports the job completion rate metric.

6. Decision: Ship exactly 5 templates in v1.
   - Alternatives: 3 templates or 8+ templates.
   - Why chosen: Balanced scope and usability.

7. Decision: Main success metric is job completion rate.
   - Alternatives: Time-to-first-job or pure throughput.
   - Why chosen: Most meaningful user outcome metric for power-sharing workloads.

## 4. Recommended v1 Product Design

### 4.1 Product Positioning

Lantern v1 is a Local Job Hub. Users submit a job using a template and receive a final artifact. Internal task chunking, leasing, retries, and reassignment are system-managed and mostly hidden.

### 4.2 Default User Flow

1. Open Jobs page.
2. Click New Job.
3. Choose one of 5 templates.
4. Provide required inputs and minimal settings.
5. Review preflight and start.
6. Track progress in plain-language statuses.
7. Download final output artifact.

### 4.3 Built-in v1 Templates (5)

1. Render Frames
   - Best for: frame-range render workloads.
   - Output: frame archive and optional stitched output.

2. Video Transcode Batch
   - Best for: converting many source videos to standard presets.
   - Output: transformed video batch package.

3. Image Batch Pipeline
   - Best for: resize/format conversion/watermark/optimization.
   - Output: processed image bundle.

4. Data Processing Batch
   - Best for: CSV/JSON transformation and enrichment.
   - Output: transformed datasets and summary report.

5. Document OCR Batch
   - Best for: PDF/image text extraction and normalization.
   - Output: text/JSON extraction package with logs.

## 5. Execution and Scheduling Model

### 5.1 Internal Mechanics

- Every template compiles into chunks with clear retry policy.
- Workers claim chunks by capability match.
- Lease expiry returns stale chunks to queue.
- Retries are reassigned to different workers when possible.
- Finalization stage assembles complete job artifacts.

### 5.2 Predictability and Completion Controls

- Preflight validation before job acceptance.
- Capability checks against currently healthy workers.
- Storage and artifact budget checks.
- Guardrails for unsupported input combinations.
- Confidence indicator before start (High/Medium/Low).

### 5.3 Status Language

Use plain statuses:
- Queued
- Running
- Retrying
- Assembling
- Done
- Needs Attention

Avoid exposing internal protocol details in default UI.

## 6. Observability and Operations

### 6.1 Completion Metric Stack

1. Global completion rate = completed / started jobs.
2. Per-template completion rate.
3. Failure taxonomy buckets:
   - transient infrastructure
   - capability mismatch
   - invalid input
   - resource exhaustion

### 6.2 Operator Dashboard (v1)

- Active jobs and queue depth.
- Jobs needing attention.
- Worker health and flakiness indicator.
- One-click actions: retry job, requeue stalled chunks, disable unstable worker.

### 6.3 v1 Targets

- Job completion rate >= 95% for valid jobs on healthy 2-5 worker clusters.
- Median time from input submission to start < 2 minutes.
- Manual intervention rate < 10% of jobs.

## 7. High-Value Power Sharing Scenarios

1. Small creative studio batch processing
   - Rendering, transcoding, denoising, proxy generation.

2. Local AI data pipelines
   - Preprocessing, embedding generation, batch inference.

3. Engineering simulation and CAD workflows
   - Parameter sweeps, variant exports, mesh processing.

4. Software team LAN compute tasks
   - Test matrix execution, static analysis, artifact builds.

5. Operations and document back-office workloads
   - OCR batches, document conversion, log analysis, integrity verification.

## 8. Risks and Mitigations

1. Risk: Template mismatch with user workloads.
   - Mitigation: Track per-template completion and drop-off, refine defaults quickly.

2. Risk: Flaky workers reduce completion reliability.
   - Mitigation: Worker reliability score, auto-quarantine after repeated failures.

3. Risk: Tail latency from large chunks.
   - Mitigation: Moderate chunk sizing and reassignment of stragglers.

4. Risk: User confusion on failure causes.
   - Mitigation: Plain-language errors with direct remediation guidance.

5. Risk: Storage bottlenecks during final artifact assembly.
   - Mitigation: Preflight storage budgeting and cleanup policy.

## 9. Implementation Handoff Plan

### Phase A: UX and Template Shell
- Add Jobs-first UI and 3-step New Job wizard.
- Implement template registry and per-template input schema.
- Add preflight checks and confidence indicator.

### Phase B: Reliability-Oriented Scheduler Integration
- Map templates to chunk plans.
- Add retry/reassignment policy and worker reliability weighting.
- Implement standardized status model.

### Phase C: Artifact Finalization and Delivery
- Build artifact assembly per template.
- Add final download surface and retained logs.

### Phase D: Metrics and Operator Controls
- Add completion dashboards and failure taxonomy.
- Add one-click remediation actions.

## 10. Out of Scope for v1

- Enterprise identity/roles.
- Untrusted network hardening and mutual auth.
- Marketplace-style extensible plugin ecosystem.
- Broad auto-detect for arbitrary workloads.
