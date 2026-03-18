package server

import (
	"encoding/json"
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

	completedTask, job, err := c.CompleteTask("worker-1", first.ID, "sha256:first", now.Add(20*time.Millisecond))
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

	_, job, err = c.CompleteTask("worker-1", second.ID, "sha256:second", now.Add(40*time.Millisecond))
	if err != nil {
		t.Fatalf("CompleteTask(second): %v", err)
	}
	if job.Status != "completed" {
		t.Fatalf("job status after second completion = %q, want completed", job.Status)
	}
	if job.CompletedTasks != 2 || job.FailedTasks != 0 {
		t.Fatalf("final job counters = completed:%d failed:%d, want 2/0", job.CompletedTasks, job.FailedTasks)
	}
}

func TestCoordinatorFailTaskFailsJobAndQueuedTasks(t *testing.T) {
	cfg := config.Default()
	cfg.ComputeEnabled = true

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
	if failedTask.Status != "failed" {
		t.Fatalf("failed task status = %q, want failed", failedTask.Status)
	}
	if failedTask.Error != "worker crashed" {
		t.Fatalf("failed task error = %q, want worker crashed", failedTask.Error)
	}
	if job.Status != "failed" {
		t.Fatalf("job status = %q, want failed", job.Status)
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
		if task.Status != "failed" {
			t.Fatalf("task %s status = %q, want failed", task.ID, task.Status)
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

	if _, _, err := c.CompleteTask("worker-b", task.ID, "", now.Add(20*time.Millisecond)); err == nil {
		t.Fatal("expected ownership error")
	}
	if _, _, err := c.CompleteTask("worker-a", "missing-task", "", now.Add(20*time.Millisecond)); err == nil {
		t.Fatal("expected missing task error")
	}
	if _, _, err := c.FailTask("worker-a", task.ID, "", "", now.Add(20*time.Millisecond)); err == nil {
		t.Fatal("expected missing failure message error")
	}

	if _, _, err := c.CompleteTask("worker-a", task.ID, "sha256:done", now.Add(30*time.Millisecond)); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	if _, _, err := c.FailTask("worker-a", task.ID, "late failure", "", now.Add(40*time.Millisecond)); err == nil {
		t.Fatal("expected terminal state error")
	}
}

func TestCoordinatorRestoreKeepsTerminalTasksOutOfQueue(t *testing.T) {
	cfg := config.Default()
	cfg.ComputeEnabled = true

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
	if restored.jobs["job-restore"].Status != "failed" {
		t.Fatalf("restored job status = %q, want failed", restored.jobs["job-restore"].Status)
	}
}
