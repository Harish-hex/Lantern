package server

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Harish-hex/Lantern/internal/config"
	"github.com/Harish-hex/Lantern/internal/index"
)

func TestCoordinatorSubmitJobAndClaimTask(t *testing.T) {
	cfg := config.Default()
	cfg.ComputeEnabled = true

	idx, err := index.NewJSONStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}

	c := NewCoordinator(cfg, idx)
	now := time.Now().UTC()
	c.RegisterWorker("worker-1", []string{"edge_tiles_v1"}, now)

	req := ComputeJobSubmit{
		Type: "edge_tiles_v1",
		Tasks: []json.RawMessage{
			json.RawMessage(`{"tile_x":0,"tile_y":0}`),
			json.RawMessage(`{"tile_x":1,"tile_y":0}`),
		},
	}

	job, total, err := c.SubmitJob("job-demo", req, now)
	if err != nil {
		t.Fatalf("SubmitJob: %v", err)
	}
	if total != 2 {
		t.Fatalf("total tasks = %d, want 2", total)
	}
	if job.ID != "job-demo" {
		t.Fatalf("job ID = %q, want job-demo", job.ID)
	}

	task1, ok := c.ClaimTask("worker-1", now.Add(100*time.Millisecond))
	if !ok || task1 == nil {
		t.Fatal("expected first task claim to succeed")
	}
	if task1.Status != "leased" {
		t.Fatalf("task1 status = %q, want leased", task1.Status)
	}

	task2, ok := c.ClaimTask("worker-1", now.Add(200*time.Millisecond))
	if !ok || task2 == nil {
		t.Fatal("expected second task claim to succeed")
	}
	if task2.ID == task1.ID {
		t.Fatalf("task IDs must differ; got %q and %q", task1.ID, task2.ID)
	}

	task3, ok := c.ClaimTask("worker-1", now.Add(300*time.Millisecond))
	if ok || task3 != nil {
		t.Fatal("expected no remaining queued tasks")
	}
}

func TestCoordinatorReapStaleWorkerRequeuesTask(t *testing.T) {
	cfg := config.Default()
	cfg.ComputeEnabled = true
	cfg.ComputeLeaseTTL = 200 * time.Millisecond

	idx, err := index.NewJSONStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}

	c := NewCoordinator(cfg, idx)
	now := time.Now().UTC()
	c.RegisterWorker("worker-a", nil, now)

	_, _, err = c.SubmitJob("job-requeue", ComputeJobSubmit{
		Type:  "edge_tiles_v1",
		Tasks: []json.RawMessage{json.RawMessage(`{"tile_x":0,"tile_y":1}`)},
	}, now)
	if err != nil {
		t.Fatalf("SubmitJob: %v", err)
	}

	first, ok := c.ClaimTask("worker-a", now.Add(10*time.Millisecond))
	if !ok || first == nil {
		t.Fatal("expected initial claim")
	}
	if first.Attempt != 1 {
		t.Fatalf("first attempt = %d, want 1", first.Attempt)
	}

	c.ReapStaleWorkers(now.Add(cfg.ComputeLeaseTTL + 50*time.Millisecond))
	c.RegisterWorker("worker-b", nil, now.Add(cfg.ComputeLeaseTTL+60*time.Millisecond))

	reclaimed, ok := c.ClaimTask("worker-b", now.Add(cfg.ComputeLeaseTTL+70*time.Millisecond))
	if !ok || reclaimed == nil {
		t.Fatal("expected task to be requeued and claimed")
	}
	if reclaimed.ID != first.ID {
		t.Fatalf("reclaimed task ID = %q, want %q", reclaimed.ID, first.ID)
	}
	if reclaimed.Attempt != 2 {
		t.Fatalf("reclaimed attempt = %d, want 2", reclaimed.Attempt)
	}
}

func TestCoordinatorCompleteTaskUpdatesWorkerAndJobState(t *testing.T) {
	cfg := config.Default()
	cfg.ComputeEnabled = true

	idx, err := index.NewJSONStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}

	c := NewCoordinator(cfg, idx)
	now := time.Now().UTC()
	c.RegisterWorker("worker-1", nil, now)

	_, _, err = c.SubmitJob("job-complete", ComputeJobSubmit{
		Type: "edge_tiles_v1",
		Tasks: []json.RawMessage{
			json.RawMessage(`{"tile_x":0}`),
			json.RawMessage(`{"tile_x":1}`),
		},
	}, now)
	if err != nil {
		t.Fatalf("SubmitJob: %v", err)
	}

	first, ok := c.ClaimTask("worker-1", now.Add(10*time.Millisecond))
	if !ok || first == nil {
		t.Fatal("expected first task claim")
	}

	completedTask, job, err := c.CompleteTask("worker-1", first.ID, "", "sha256:first", now.Add(20*time.Millisecond))
	if err != nil {
		t.Fatalf("CompleteTask(first): %v", err)
	}
	if completedTask.Status != "completed" {
		t.Fatalf("first task status = %q, want completed", completedTask.Status)
	}
	if completedTask.Checksum != "sha256:first" {
		t.Fatalf("first checksum = %q, want sha256:first", completedTask.Checksum)
	}
	if job.Status != "running" {
		t.Fatalf("job status after first completion = %q, want running", job.Status)
	}
	if job.CompletedTasks != 1 || job.FailedTasks != 0 {
		t.Fatalf("job counters after first completion = completed:%d failed:%d, want 1/0", job.CompletedTasks, job.FailedTasks)
	}
	if worker := c.workers["worker-1"]; worker == nil || worker.Status != "ready" {
		t.Fatalf("worker status after first completion = %+v, want ready", worker)
	}

	second, ok := c.ClaimTask("worker-1", now.Add(30*time.Millisecond))
	if !ok || second == nil {
		t.Fatal("expected second task claim")
	}

	_, job, err = c.CompleteTask("worker-1", second.ID, "", "sha256:second", now.Add(40*time.Millisecond))
	if err != nil {
		t.Fatalf("CompleteTask(second): %v", err)
	}
	if job.Status != ComputeJobStatusDone {
		t.Fatalf("job status after second completion = %q, want %s", job.Status, ComputeJobStatusDone)
	}
	if job.CompletedTasks != 2 || job.FailedTasks != 0 {
		t.Fatalf("final job counters = completed:%d failed:%d, want 2/0", job.CompletedTasks, job.FailedTasks)
	}
}

func TestCoordinatorFailTaskFailsJobAndQueuedTasks(t *testing.T) {
	cfg := config.Default()
	cfg.ComputeEnabled = true
	cfg.ComputeRetryMax = 1

	idx, err := index.NewJSONStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}

	c := NewCoordinator(cfg, idx)
	now := time.Now().UTC()
	c.RegisterWorker("worker-1", nil, now)

	_, _, err = c.SubmitJob("job-fail", ComputeJobSubmit{
		Type: "edge_tiles_v1",
		Tasks: []json.RawMessage{
			json.RawMessage(`{"tile_x":0}`),
			json.RawMessage(`{"tile_x":1}`),
		},
	}, now)
	if err != nil {
		t.Fatalf("SubmitJob: %v", err)
	}

	first, ok := c.ClaimTask("worker-1", now.Add(10*time.Millisecond))
	if !ok || first == nil {
		t.Fatal("expected task claim")
	}

	failedTask, job, err := c.FailTask("worker-1", first.ID, "worker crashed", "sha256:failed", now.Add(20*time.Millisecond))
	if err != nil {
		t.Fatalf("FailTask: %v", err)
	}
	if failedTask.Status != ComputeTaskStatusNeedsAttention {
		t.Fatalf("failed task status = %q, want %s", failedTask.Status, ComputeTaskStatusNeedsAttention)
	}
	if failedTask.Error != "worker crashed" {
		t.Fatalf("failed task error = %q, want worker crashed", failedTask.Error)
	}
	if job.Status != ComputeJobStatusNeedsAttention {
		t.Fatalf("job status = %q, want %s", job.Status, ComputeJobStatusNeedsAttention)
	}
	if job.FailedTasks != 2 {
		t.Fatalf("job failed count = %d, want 2", job.FailedTasks)
	}
	if worker := c.workers["worker-1"]; worker == nil || worker.Status != "ready" {
		t.Fatalf("worker status after failure = %+v, want ready", worker)
	}

	for _, task := range c.tasks {
		if task.JobID != "job-fail" {
			continue
		}
		if task.Status != ComputeTaskStatusNeedsAttention {
			t.Fatalf("task %s status = %q, want %s", task.ID, task.Status, ComputeTaskStatusNeedsAttention)
		}
	}

	if next, ok := c.ClaimTask("worker-1", now.Add(30*time.Millisecond)); ok || next != nil {
		t.Fatal("expected no task claim from failed job")
	}
}

func TestCoordinatorRejectsInvalidTaskCompletionTransitions(t *testing.T) {
	cfg := config.Default()
	cfg.ComputeEnabled = true

	idx, err := index.NewJSONStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}

	c := NewCoordinator(cfg, idx)
	now := time.Now().UTC()
	c.RegisterWorker("worker-a", nil, now)
	c.RegisterWorker("worker-b", nil, now)

	_, _, err = c.SubmitJob("job-validate", ComputeJobSubmit{
		Type:  "edge_tiles_v1",
		Tasks: []json.RawMessage{json.RawMessage(`{"tile_x":0}`)},
	}, now)
	if err != nil {
		t.Fatalf("SubmitJob: %v", err)
	}

	task, ok := c.ClaimTask("worker-a", now.Add(10*time.Millisecond))
	if !ok || task == nil {
		t.Fatal("expected task claim")
	}

	if _, _, err := c.CompleteTask("worker-b", task.ID, "", "", now.Add(20*time.Millisecond)); err == nil {
		t.Fatal("expected ownership error")
	}
	if _, _, err := c.CompleteTask("worker-a", "missing-task", "", "", now.Add(20*time.Millisecond)); err == nil {
		t.Fatal("expected missing task error")
	}
	if _, _, err := c.FailTask("worker-a", task.ID, "", "", now.Add(20*time.Millisecond)); err == nil {
		t.Fatal("expected missing failure message error")
	}

	if _, _, err := c.CompleteTask("worker-a", task.ID, "", "sha256:done", now.Add(30*time.Millisecond)); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	if _, _, err := c.FailTask("worker-a", task.ID, "late failure", "", now.Add(40*time.Millisecond)); err == nil {
		t.Fatal("expected terminal state error")
	}
}

func TestCoordinatorRestoreKeepsTerminalTasksOutOfQueue(t *testing.T) {
	cfg := config.Default()
	cfg.ComputeEnabled = true
	cfg.ComputeRetryMax = 1

	idx, err := index.NewJSONStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}

	original := NewCoordinator(cfg, idx)
	now := time.Now().UTC()
	original.RegisterWorker("worker-1", nil, now)

	_, _, err = original.SubmitJob("job-restore", ComputeJobSubmit{
		Type: "edge_tiles_v1",
		Tasks: []json.RawMessage{
			json.RawMessage(`{"tile_x":0}`),
			json.RawMessage(`{"tile_x":1}`),
		},
	}, now)
	if err != nil {
		t.Fatalf("SubmitJob: %v", err)
	}

	task, ok := original.ClaimTask("worker-1", now.Add(10*time.Millisecond))
	if !ok || task == nil {
		t.Fatal("expected task claim")
	}
	if _, _, err := original.FailTask("worker-1", task.ID, "boom", "", now.Add(20*time.Millisecond)); err != nil {
		t.Fatalf("FailTask: %v", err)
	}

	restored := NewCoordinator(cfg, idx)
	if err := restored.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	restored.RegisterWorker("worker-2", nil, now.Add(30*time.Millisecond))

	if next, ok := restored.ClaimTask("worker-2", now.Add(40*time.Millisecond)); ok || next != nil {
		t.Fatal("expected no queued task after restore for failed job")
	}
	if restored.jobs["job-restore"].Status != ComputeJobStatusNeedsAttention {
		t.Fatalf("restored job status = %q, want %s", restored.jobs["job-restore"].Status, ComputeJobStatusNeedsAttention)
	}
}

func TestCoordinatorJobStateReturnsSnapshot(t *testing.T) {
	cfg := config.Default()
	cfg.ComputeEnabled = true

	idx, err := index.NewJSONStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}

	c := NewCoordinator(cfg, idx)
	now := time.Now().UTC()
	c.RegisterWorker("worker-state", nil, now)

	_, _, err = c.SubmitJob("job-state", ComputeJobSubmit{
		Type: "generic_v1",
		Tasks: []json.RawMessage{
			json.RawMessage(`{"task":1}`),
			json.RawMessage(`{"task":2}`),
		},
	}, now)
	if err != nil {
		t.Fatalf("SubmitJob: %v", err)
	}

	if _, _, ok := c.JobState("missing"); ok {
		t.Fatal("expected missing job state lookup to fail")
	}

	job, tasks, ok := c.JobState("job-state")
	if !ok {
		t.Fatal("expected job-state snapshot")
	}
	if job.ID != "job-state" {
		t.Fatalf("job id = %q, want job-state", job.ID)
	}
	if len(tasks) != 2 {
		t.Fatalf("task count = %d, want 2", len(tasks))
	}

	claimed, ok := c.ClaimTask("worker-state", now.Add(10*time.Millisecond))
	if !ok || claimed == nil {
		t.Fatal("expected claim")
	}
	if _, _, err := c.CompleteTask("worker-state", claimed.ID, "", "sha256:ok", now.Add(20*time.Millisecond)); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	job, tasks, ok = c.JobState("job-state")
	if !ok {
		t.Fatal("expected job-state snapshot after completion")
	}
	if job.CompletedTasks != 1 {
		t.Fatalf("completed tasks = %d, want 1", job.CompletedTasks)
	}
	if len(tasks) != 2 {
		t.Fatalf("task count after completion = %d, want 2", len(tasks))
	}
}

func TestCoordinatorTemplateSubmissionCompilesBuiltIns(t *testing.T) {
	cfg := config.Default()
	cfg.ComputeEnabled = true

	idx, err := index.NewJSONStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}

	c := NewCoordinator(cfg, idx)
	now := time.Now().UTC()
	c.RegisterWorker("worker-render-a", []string{"render_frames", "render_device:cpu", "tool:blender"}, now)
	c.RegisterWorker("worker-render-b", []string{"render_frames", "render_device:cpu", "tool:blender"}, now)

	job, total, err := c.SubmitJob("job-template", ComputeJobSubmit{
		Template: "render_frames",
		Inputs:   json.RawMessage(`{"scene_bundle_id":"bundle-demo.zip","scene_file":"demo.blend","frame_start":1,"frame_end":7,"chunk_size":3}`),
		Settings: json.RawMessage(`{"stitched_output":false,"output_format":"png"}`),
	}, now)
	if err != nil {
		t.Fatalf("SubmitJob(template): %v", err)
	}
	if total != 3 {
		t.Fatalf("compiled task count = %d, want 3", total)
	}
	if job.TemplateID != "render_frames" {
		t.Fatalf("template id = %q, want render_frames", job.TemplateID)
	}
	if job.Preflight.Confidence != "High" {
		t.Fatalf("preflight confidence = %q, want High", job.Preflight.Confidence)
	}
}

func TestCoordinatorPreviewTemplateSubmissionDoesNotPersistState(t *testing.T) {
	cfg := config.Default()
	cfg.ComputeEnabled = true

	idx, err := index.NewJSONStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}

	c := NewCoordinator(cfg, idx)
	now := time.Now().UTC()
	c.RegisterWorker("worker-preview-a", []string{"render_frames", "render_device:cpu", "tool:blender"}, now)
	c.RegisterWorker("worker-preview-b", []string{"render_frames", "render_device:cpu", "tool:blender"}, now)

	preview, err := c.PreviewJob(ComputeJobSubmit{
		Template: "render_frames",
		Inputs:   json.RawMessage(`{"scene_bundle_id":"bundle-preview.zip","scene_file":"preview.blend","frame_start":1,"frame_end":10,"chunk_size":5}`),
		Settings: json.RawMessage(`{"stitched_output":false,"output_format":"png"}`),
	}, now)
	if err != nil {
		t.Fatalf("PreviewJob: %v", err)
	}
	if preview.TemplateID != "render_frames" {
		t.Fatalf("template id = %q, want render_frames", preview.TemplateID)
	}
	if preview.Preflight.Ready != true {
		t.Fatalf("preflight ready = %v, want true", preview.Preflight.Ready)
	}
	if len(c.JobsSnapshot()) != 0 {
		t.Fatalf("jobs persisted after preview = %d, want 0", len(c.JobsSnapshot()))
	}
	if _, _, ok := c.JobState("job-preview"); ok {
		t.Fatal("unexpected persisted job state after preview")
	}
}

func TestCoordinatorPreviewTemplateSubmissionAllowsNotReadyPreflight(t *testing.T) {
	cfg := config.Default()
	cfg.ComputeEnabled = true

	idx, err := index.NewJSONStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}

	c := NewCoordinator(cfg, idx)
	now := time.Now().UTC()

	preview, err := c.PreviewJob(ComputeJobSubmit{
		Template: "render_frames",
		Inputs:   json.RawMessage(`{"scene_bundle_id":"bundle-preview.zip","scene_file":"preview.blend","frame_start":1,"frame_end":5,"chunk_size":2}`),
		Settings: json.RawMessage(`{"stitched_output":false,"output_format":"png"}`),
	}, now)
	if err != nil {
		t.Fatalf("PreviewJob: %v", err)
	}
	// Preflight should be ready even without workers — jobs queue and wait.
	if !preview.Preflight.Ready {
		t.Fatal("expected ready preflight — jobs should queue even without healthy workers")
	}
	// But it should have a warning about no healthy workers.
	hasCapWarning := false
	for _, check := range preview.Preflight.Checks {
		if check.Code == "capability_match" && check.Status == "warn" {
			hasCapWarning = true
		}
	}
	if !hasCapWarning {
		t.Fatal("expected a capability_match warning when no healthy workers exist")
	}
	if len(c.JobsSnapshot()) != 0 {
		t.Fatalf("jobs persisted after preview = %d, want 0", len(c.JobsSnapshot()))
	}
}

func TestCoordinatorPreviewTemplateSubmissionRejectsInvalidTemplate(t *testing.T) {
	cfg := config.Default()
	cfg.ComputeEnabled = true

	idx, err := index.NewJSONStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}

	c := NewCoordinator(cfg, idx)
	_, err = c.PreviewJob(ComputeJobSubmit{Template: "missing_template"}, time.Now().UTC())
	if err == nil {
		t.Fatal("expected invalid template error")
	}
}

func TestCoordinatorPreviewTemplateSubmissionRejectsInvalidInputShape(t *testing.T) {
	cfg := config.Default()
	cfg.ComputeEnabled = true

	idx, err := index.NewJSONStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}

	c := NewCoordinator(cfg, idx)
	_, err = c.PreviewJob(ComputeJobSubmit{
		Template: "render_frames",
		Inputs:   json.RawMessage(`{"scene":"broken.blend","frame_start":"bad","frame_end":10,"chunk_size":2}`),
		Settings: json.RawMessage(`{"stitched_output":true,"output_format":"png"}`),
	}, time.Now().UTC())
	if err == nil {
		t.Fatal("expected invalid input shape error")
	}
}

func TestCoordinatorTransientFailureRetriesAndReassigns(t *testing.T) {
	cfg := config.Default()
	cfg.ComputeEnabled = true
	cfg.ComputeRetryMax = 3

	idx, err := index.NewJSONStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}

	c := NewCoordinator(cfg, idx)
	now := time.Now().UTC()
	c.RegisterWorker("worker-a", []string{"edge_tiles_v1"}, now)
	c.RegisterWorker("worker-b", []string{"edge_tiles_v1"}, now)

	_, _, err = c.SubmitJob("job-retry", ComputeJobSubmit{
		Type:  "edge_tiles_v1",
		Tasks: []json.RawMessage{json.RawMessage(`{"tile_x":0,"tile_y":0}`)},
	}, now)
	if err != nil {
		t.Fatalf("SubmitJob: %v", err)
	}

	first, ok := c.ClaimTask("worker-a", now.Add(10*time.Millisecond))
	if !ok || first == nil {
		t.Fatal("expected initial claim")
	}

	task, job, err := c.FailTask("worker-a", first.ID, "connection timeout", "", now.Add(20*time.Millisecond))
	if err != nil {
		t.Fatalf("FailTask(retry): %v", err)
	}
	if task.Status != ComputeTaskStatusRetrying {
		t.Fatalf("task status after retryable failure = %q, want %s", task.Status, ComputeTaskStatusRetrying)
	}
	if job.Status != ComputeJobStatusRetrying {
		t.Fatalf("job status after retryable failure = %q, want %s", job.Status, ComputeJobStatusRetrying)
	}

	if sameWorkerTask, ok := c.ClaimTask("worker-a", now.Add(30*time.Millisecond)); ok || sameWorkerTask != nil {
		t.Fatal("expected same worker to be skipped when another eligible worker exists")
	}

	reassigned, ok := c.ClaimTask("worker-b", now.Add(40*time.Millisecond))
	if !ok || reassigned == nil {
		t.Fatal("expected second worker to claim retried task")
	}
	if reassigned.ID != first.ID {
		t.Fatalf("reassigned task id = %q, want %q", reassigned.ID, first.ID)
	}
	if reassigned.Attempt != 2 {
		t.Fatalf("reassigned attempt = %d, want 2", reassigned.Attempt)
	}
}

func TestCoordinatorRetryJobResetsTerminalState(t *testing.T) {
	cfg := config.Default()
	cfg.ComputeEnabled = true
	cfg.ComputeRetryMax = 1

	idx, err := index.NewJSONStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}

	c := NewCoordinator(cfg, idx)
	now := time.Now().UTC()
	c.RegisterWorker("worker-reset-a", []string{"data_processing_batch"}, now)
	c.RegisterWorker("worker-reset-b", []string{"data_processing_batch"}, now)

	_, _, err = c.SubmitJob("job-reset", ComputeJobSubmit{
		Template: "data_processing_batch",
		Inputs:   json.RawMessage(`{"datasets":["customers.csv"]}`),
	}, now)
	if err != nil {
		t.Fatalf("SubmitJob: %v", err)
	}

	task, ok := c.ClaimTask("worker-reset-a", now.Add(10*time.Millisecond))
	if !ok || task == nil {
		t.Fatal("expected claim")
	}
	if _, job, err := c.FailTask("worker-reset-a", task.ID, "invalid schema", "", now.Add(20*time.Millisecond)); err != nil {
		t.Fatalf("FailTask: %v", err)
	} else if job.Status != ComputeJobStatusNeedsAttention {
		t.Fatalf("job status = %q, want %s", job.Status, ComputeJobStatusNeedsAttention)
	}

	retried, err := c.RetryJob("job-reset", now.Add(30*time.Millisecond))
	if err != nil {
		t.Fatalf("RetryJob: %v", err)
	}
	if retried.Status != ComputeJobStatusQueued {
		t.Fatalf("retried status = %q, want %s", retried.Status, ComputeJobStatusQueued)
	}

	reclaimed, ok := c.ClaimTask("worker-reset-b", now.Add(40*time.Millisecond))
	if !ok || reclaimed == nil {
		t.Fatal("expected retried job task claim")
	}
	if reclaimed.Attempt != 1 {
		t.Fatalf("reclaimed attempt = %d, want 1", reclaimed.Attempt)
	}
}

func TestCoordinatorAssemblesFinalArtifactPackage(t *testing.T) {
	cfg := config.Default()
	cfg.ComputeEnabled = true
	base := t.TempDir()
	cfg.StorageDir = filepath.Join(base, "storage")
	cfg.TempDir = filepath.Join(base, "tmp")
	cfg.IndexDir = filepath.Join(base, "index")

	srv, err := New(cfg)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	now := time.Now().UTC()
	srv.compute.RegisterWorker("worker-package", nil, now)

	_, _, err = srv.compute.SubmitJob("job-package", ComputeJobSubmit{
		Type: "generic_v1",
		Tasks: []json.RawMessage{
			json.RawMessage(`{"task":1}`),
			json.RawMessage(`{"task":2}`),
		},
	}, now)
	if err != nil {
		t.Fatalf("SubmitJob: %v", err)
	}

	taskOne, ok := srv.compute.ClaimTask("worker-package", now.Add(10*time.Millisecond))
	if !ok || taskOne == nil {
		t.Fatal("expected first task claim")
	}
	artifactOne := createStoredArtifactForTest(t, srv, "artifact-one", "task-one.zip", []byte("task-one"), now)
	if _, _, err := srv.compute.CompleteTask("worker-package", taskOne.ID, artifactOne.ID, "sha256:first", now.Add(20*time.Millisecond)); err != nil {
		t.Fatalf("CompleteTask(first): %v", err)
	}

	taskTwo, ok := srv.compute.ClaimTask("worker-package", now.Add(30*time.Millisecond))
	if !ok || taskTwo == nil {
		t.Fatal("expected second task claim")
	}
	artifactTwo := createStoredArtifactForTest(t, srv, "artifact-two", "task-two.zip", []byte("task-two"), now)
	_, job, err := srv.compute.CompleteTask("worker-package", taskTwo.ID, artifactTwo.ID, "sha256:second", now.Add(40*time.Millisecond))
	if err != nil {
		t.Fatalf("CompleteTask(second): %v", err)
	}

	if job.Status != ComputeJobStatusDone {
		t.Fatalf("job status = %q, want %s", job.Status, ComputeJobStatusDone)
	}
	if len(job.Artifacts) != 1 {
		t.Fatalf("job artifacts len = %d, want 1", len(job.Artifacts))
	}
	if job.Artifacts[0].Kind != "final_package" {
		t.Fatalf("final artifact kind = %q, want final_package", job.Artifacts[0].Kind)
	}

	finalFile := srv.storage.GetFile(job.Artifacts[0].ID)
	if finalFile == nil {
		t.Fatalf("final stored file %s not found", job.Artifacts[0].ID)
	}

	reader, err := zip.OpenReader(finalFile.Path)
	if err != nil {
		t.Fatalf("zip.OpenReader(%s): %v", finalFile.Path, err)
	}
	defer reader.Close()

	entries := make(map[string]bool)
	for _, file := range reader.File {
		entries[file.Name] = true
	}
	for _, required := range []string{"manifest.json", "task_artifacts/task-one.zip", "task_artifacts/task-two.zip"} {
		if !entries[required] {
			t.Fatalf("final package missing %s", required)
		}
	}
}

func createStoredArtifactForTest(t *testing.T, srv *Server, fileID, filename string, content []byte, now time.Time) *StoredFile {
	t.Helper()

	tempPath := filepath.Join(srv.cfg.TempDir, fileID+".tmp")
	if err := os.WriteFile(tempPath, content, 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", tempPath, err)
	}

	meta, err := generatedFileMetadata(tempPath, filename, "application/zip", srv.cfg.ChunkSize)
	if err != nil {
		t.Fatalf("generatedFileMetadata(%s): %v", tempPath, err)
	}

	sf, err := srv.storage.MoveToStorage(tempPath, fileID, meta, srv.cfg.TTLDefault, 0)
	if err != nil {
		t.Fatalf("MoveToStorage(%s): %v", fileID, err)
	}
	sf.ExpiresAt = now.Add(24 * time.Hour)
	return sf
}
