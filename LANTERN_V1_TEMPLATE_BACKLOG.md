# Lantern v1 Template Compute Backlog

This backlog tracks the remaining work after the current template-first compute foundation.

Scoring:
- `Value`: 1-10, where 10 means highest product impact for Lantern v1.
- `Time`: 1-10, where 10 means highest implementation effort / longest time.
- `Priority`: 1-10, a rough balance of value vs. time. Higher means "do sooner."

## Ranked TODO List

- [ ] `Priority 10` Build the web `New Job` wizard with a Jobs-first flow.
  Value: 10
  Time: 5
  Why: This is the biggest gap between the product plan and what users can currently do in the UI.

- [ ] `Priority 9` Add schema-driven per-template forms with defaults and guided validation.
  Value: 9
  Time: 5
  Why: The built-in templates exist, but users still do not get the simple guided input flow the plan calls for.

- [ ] `Priority 9` Expose the remaining operator controls in the web dashboard.
  Value: 8
  Time: 4
  Why: Retry job is wired, but stalled-task requeue and worker disable/enable still need visible UI actions.

- [ ] `Priority 8` Upgrade preflight from heuristics to real validation.
  Value: 9
  Time: 6
  Why: This directly supports the "predictable completion time" goal and improves trust before job start.

- [ ] `Priority 8` Build real artifact assembly and a proper final download surface per template.
  Value: 10
  Time: 8
  Why: Users need real output packages, not only summary artifacts.

- [ ] `Priority 8` Persist retained execution logs and implement full-log download.
  Value: 8
  Time: 5
  Why: This closes an important operator and debugging gap in the current dashboard.

- [ ] `Priority 8` Use the compute overview API to render completion, failure taxonomy, and per-template metrics in the dashboard.
  Value: 7
  Time: 4
  Why: The backend data exists, but the operator dashboard still does not fully reflect the v1 plan.

- [ ] `Priority 7` Add end-to-end web/API coverage for template submission, retry, worker controls, and dashboard data.
  Value: 7
  Time: 4
  Why: The compute coordinator is covered better than the HTTP/UI path right now.

- [ ] `Priority 7` Implement the `Data Processing Batch` worker executor.
  Value: 8
  Time: 6
  Why: This is one of the fastest paths to a real useful end-to-end template.

- [ ] `Priority 7` Implement the `Image Batch Pipeline` worker executor.
  Value: 8
  Time: 7
  Why: High practical value for small teams and easier to validate than render/transcode flows.

- [ ] `Priority 7` Implement the `Document OCR Batch` worker executor.
  Value: 8
  Time: 7
  Why: Strong business utility and a good fit for the local back-office workloads described in the plan.

- [ ] `Priority 6` Implement the `Video Transcode Batch` worker executor.
  Value: 9
  Time: 9
  Why: Very valuable, but more expensive because of codec/tooling/runtime concerns.

- [ ] `Priority 6` Implement the `Render Frames` worker executor.
  Value: 9
  Time: 10
  Why: Very high value for creative workloads, but likely the most expensive and integration-heavy template.

## Recommended Next Slice

1. Build the web `New Job` wizard.
2. Add schema-driven template forms.
3. Finish the missing operator controls in the dashboard.
4. Implement one real end-to-end template first: `Data Processing Batch`.

## Notes

- The scheduler/template foundation is already in place.
- The biggest remaining v1 gap is not coordinator logic; it is the user-facing submission flow plus real template execution and output delivery.
