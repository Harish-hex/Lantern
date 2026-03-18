package server

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/Harish-hex/Lantern/internal/config"
	"github.com/Harish-hex/Lantern/internal/index"
)

type ComputeJob struct {
	ID             string
	Type           string
	Status         string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	TotalTasks     int
	CompletedTasks int
	FailedTasks    int
}

type ComputeTask struct {
	ID         string
	JobID      string
	WorkerID   string
	Status     string
	Attempt    int
	LeaseUntil time.Time
	UpdatedAt  time.Time
	Checksum   string
	Error      string
	Payload    json.RawMessage
}

type ComputeWorker struct {
	ID           string
	Status       string
	LastSeen     time.Time
	LeaseUntil   time.Time
	Capabilities []string
}

type Coordinator struct {
	cfg config.Config
	idx index.Store
	mu  sync.Mutex

	jobs    map[string]*ComputeJob
	tasks   map[string]*ComputeTask
	workers map[string]*ComputeWorker
	queue   []string
}

func NewCoordinator(cfg config.Config, idx index.Store) *Coordinator {
	return &Coordinator{
		cfg:     cfg,
		idx:     idx,
		jobs:    make(map[string]*ComputeJob),
		tasks:   make(map[string]*ComputeTask),
		workers: make(map[string]*ComputeWorker),
		queue:   make([]string, 0),
	}
}

func (c *Coordinator) Restore() error {
	if c == nil || c.idx == nil || !c.cfg.ComputeEnabled {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	jobs, err := c.idx.LoadComputeJobs()
	if err != nil {
		return err
	}
	for _, snap := range jobs {
		c.jobs[snap.ID] = &ComputeJob{
			ID:             snap.ID,
			Type:           snap.Type,
			Status:         snap.Status,
			CreatedAt:      snap.CreatedAt,
			UpdatedAt:      snap.UpdatedAt,
			TotalTasks:     snap.TotalTasks,
			CompletedTasks: snap.CompletedTasks,
			FailedTasks:    snap.FailedTasks,
		}
	}

	tasks, err := c.idx.LoadComputeTasks()
	if err != nil {
		return err
	}
	for _, snap := range tasks {
		c.tasks[snap.ID] = &ComputeTask{
			ID:         snap.ID,
			JobID:      snap.JobID,
			WorkerID:   snap.WorkerID,
			Status:     snap.Status,
			Attempt:    snap.Attempt,
			LeaseUntil: snap.LeaseUntil,
			UpdatedAt:  snap.UpdatedAt,
			Checksum:   snap.Checksum,
			Error:      snap.Error,
			Payload:    snap.Payload,
		}
		if snap.Status == "queued" && !c.jobIsTerminal(snap.JobID) {
			c.queue = append(c.queue, snap.ID)
		}
	}

	workers, err := c.idx.LoadComputeWorkers()
	if err != nil {
		return err
	}
	for _, snap := range workers {
		c.workers[snap.ID] = &ComputeWorker{
			ID:           snap.ID,
			Status:       snap.Status,
			LastSeen:     snap.LastSeen,
			LeaseUntil:   snap.LeaseUntil,
			Capabilities: append([]string(nil), snap.Capabilities...),
		}
	}

	return nil
}

func (c *Coordinator) ReapStaleWorkers(now time.Time) {
	if c == nil || c.idx == nil || !c.cfg.ComputeEnabled {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	for id, worker := range c.workers {
		if now.Sub(worker.LastSeen) <= c.cfg.ComputeLeaseTTL {
			continue
		}
		delete(c.workers, id)
		_ = c.idx.DeleteComputeWorker(id)

		for _, task := range c.tasks {
			if task.WorkerID != id || task.Status != "leased" {
				continue
			}
			if c.jobIsTerminal(task.JobID) {
				task.WorkerID = ""
				task.Status = "failed"
				task.LeaseUntil = time.Time{}
				if task.Error == "" {
					task.Error = fmt.Sprintf("job %s already reached terminal state", task.JobID)
				}
				task.UpdatedAt = now
				if job := c.jobs[task.JobID]; job != nil {
					job.FailedTasks++
					job.UpdatedAt = now
					_ = c.idx.SaveComputeJob(c.toJobSnapshot(job))
				}
				_ = c.idx.SaveComputeTask(c.toTaskSnapshot(task))
				continue
			}
			task.WorkerID = ""
			task.Status = "queued"
			task.LeaseUntil = time.Time{}
			task.UpdatedAt = now
			c.queue = append(c.queue, task.ID)
			_ = c.idx.SaveComputeTask(c.toTaskSnapshot(task))
		}
	}
}

type ComputeJobSubmit struct {
	Type  string            `json:"type"`
	Tasks []json.RawMessage `json:"tasks"`
}

func (c *Coordinator) SubmitJob(jobID string, req ComputeJobSubmit, now time.Time) (*ComputeJob, int, error) {
	if c == nil || !c.cfg.ComputeEnabled {
		return nil, 0, fmt.Errorf("compute disabled")
	}
	if len(req.Tasks) == 0 {
		return nil, 0, fmt.Errorf("job requires at least one task")
	}
	if req.Type == "" {
		req.Type = "generic_v1"
	}
	if jobID == "" {
		jobID = fmt.Sprintf("job_%d", now.UnixNano())
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.jobs[jobID]; exists {
		return nil, 0, fmt.Errorf("job %s already exists", jobID)
	}

	job := &ComputeJob{
		ID:         jobID,
		Type:       req.Type,
		Status:     "queued",
		CreatedAt:  now,
		UpdatedAt:  now,
		TotalTasks: len(req.Tasks),
	}
	c.jobs[jobID] = job

	for i, taskPayload := range req.Tasks {
		if c.cfg.ComputeTaskSizeBytes > 0 && int64(len(taskPayload)) > c.cfg.ComputeTaskSizeBytes {
			delete(c.jobs, jobID)
			return nil, 0, fmt.Errorf("task %d payload too large: %d > %d", i, len(taskPayload), c.cfg.ComputeTaskSizeBytes)
		}
		taskID := fmt.Sprintf("%s_t%04d", jobID, i+1)
		task := &ComputeTask{
			ID:        taskID,
			JobID:     jobID,
			Status:    "queued",
			Attempt:   0,
			UpdatedAt: now,
			Payload:   append(json.RawMessage(nil), taskPayload...),
		}
		c.tasks[taskID] = task
		c.queue = append(c.queue, taskID)
		if c.idx != nil {
			_ = c.idx.SaveComputeTask(c.toTaskSnapshot(task))
		}
	}

	if c.idx != nil {
		_ = c.idx.SaveComputeJob(c.toJobSnapshot(job))
	}

	return job, len(req.Tasks), nil
}

func (c *Coordinator) ClaimTask(workerID string, now time.Time) (*ComputeTask, bool) {
	if c == nil || workerID == "" || !c.cfg.ComputeEnabled {
		return nil, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	worker, ok := c.workers[workerID]
	if !ok {
		return nil, false
	}

	for len(c.queue) > 0 {
		taskID := c.queue[0]
		c.queue = c.queue[1:]

		task := c.tasks[taskID]
		if task == nil || task.Status != "queued" {
			continue
		}
		if c.jobIsTerminal(task.JobID) {
			continue
		}

		task.Status = "leased"
		task.WorkerID = workerID
		task.Attempt++
		task.LeaseUntil = now.Add(c.cfg.ComputeLeaseTTL)
		task.UpdatedAt = now

		worker.Status = "busy"
		worker.LeaseUntil = task.LeaseUntil
		worker.LastSeen = now

		job := c.jobs[task.JobID]
		if job != nil {
			job.Status = "running"
			job.UpdatedAt = now
			if c.idx != nil {
				_ = c.idx.SaveComputeJob(c.toJobSnapshot(job))
			}
		}

		if c.idx != nil {
			_ = c.idx.SaveComputeTask(c.toTaskSnapshot(task))
			_ = c.idx.SaveComputeWorker(c.toWorkerSnapshot(worker))
		}

		copyTask := *task
		if task.Payload != nil {
			copyTask.Payload = append(json.RawMessage(nil), task.Payload...)
		}
		return &copyTask, true
	}

	worker.Status = "ready"
	worker.LastSeen = now
	if c.idx != nil {
		_ = c.idx.SaveComputeWorker(c.toWorkerSnapshot(worker))
	}

	return nil, false
}

func (c *Coordinator) jobIsTerminal(jobID string) bool {
	job := c.jobs[jobID]
	if job == nil {
		return false
	}
	return job.Status == "completed" || job.Status == "failed"
}

func (c *Coordinator) CompleteTask(workerID, taskID, checksum string, now time.Time) (*ComputeTask, *ComputeJob, error) {
	return c.finishTask(workerID, taskID, "completed", "", checksum, now)
}

func (c *Coordinator) FailTask(workerID, taskID, errMsg, checksum string, now time.Time) (*ComputeTask, *ComputeJob, error) {
	if errMsg == "" {
		return nil, nil, fmt.Errorf("missing failure message")
	}
	return c.finishTask(workerID, taskID, "failed", errMsg, checksum, now)
}

func (c *Coordinator) RegisterWorker(workerID string, capabilities []string, now time.Time) {
	if c == nil || workerID == "" || !c.cfg.ComputeEnabled {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	worker := &ComputeWorker{
		ID:           workerID,
		Status:       "ready",
		LastSeen:     now,
		LeaseUntil:   now.Add(c.cfg.ComputeLeaseTTL),
		Capabilities: append([]string(nil), capabilities...),
	}
	c.workers[workerID] = worker

	if c.idx != nil {
		_ = c.idx.SaveComputeWorker(c.toWorkerSnapshot(worker))
	}
}

func (c *Coordinator) HeartbeatWorker(workerID string, now time.Time) bool {
	if c == nil || workerID == "" || !c.cfg.ComputeEnabled {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	worker, ok := c.workers[workerID]
	if !ok {
		return false
	}
	worker.LastSeen = now
	worker.LeaseUntil = now.Add(c.cfg.ComputeLeaseTTL)
	if worker.Status == "" {
		worker.Status = "ready"
	}
	if c.idx != nil {
		_ = c.idx.SaveComputeWorker(c.toWorkerSnapshot(worker))
	}
	return true
}

func (c *Coordinator) finishTask(workerID, taskID, status, errMsg, checksum string, now time.Time) (*ComputeTask, *ComputeJob, error) {
	if c == nil || !c.cfg.ComputeEnabled {
		return nil, nil, fmt.Errorf("compute disabled")
	}
	if workerID == "" {
		return nil, nil, fmt.Errorf("missing worker id")
	}
	if taskID == "" {
		return nil, nil, fmt.Errorf("missing task id")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	task, ok := c.tasks[taskID]
	if !ok || task == nil {
		return nil, nil, fmt.Errorf("task %s not found", taskID)
	}
	if task.Status != "leased" {
		return nil, nil, fmt.Errorf("task %s is not leased", taskID)
	}
	if task.WorkerID != workerID {
		return nil, nil, fmt.Errorf("task %s owned by %s", taskID, task.WorkerID)
	}

	worker, ok := c.workers[workerID]
	if !ok || worker == nil {
		return nil, nil, fmt.Errorf("worker %s not registered", workerID)
	}

	job, ok := c.jobs[task.JobID]
	if !ok || job == nil {
		return nil, nil, fmt.Errorf("job %s not found", task.JobID)
	}

	task.Status = status
	task.LeaseUntil = time.Time{}
	task.UpdatedAt = now
	if checksum != "" {
		task.Checksum = checksum
	}
	if status == "failed" {
		task.Error = errMsg
		job.FailedTasks++
		job.Status = "failed"
		c.failQueuedTasksLocked(job.ID, fmt.Sprintf("job %s failed after task %s", job.ID, task.ID), now)
	} else {
		task.Error = ""
		job.CompletedTasks++
		if job.CompletedTasks == job.TotalTasks {
			job.Status = "completed"
		} else if job.Status != "failed" {
			job.Status = "running"
		}
	}

	worker.Status = "ready"
	worker.LastSeen = now
	worker.LeaseUntil = time.Time{}

	job.UpdatedAt = now

	if c.idx != nil {
		_ = c.idx.SaveComputeTask(c.toTaskSnapshot(task))
		_ = c.idx.SaveComputeJob(c.toJobSnapshot(job))
		_ = c.idx.SaveComputeWorker(c.toWorkerSnapshot(worker))
	}

	return c.cloneTask(task), c.cloneJob(job), nil
}

func (c *Coordinator) toJobSnapshot(job *ComputeJob) *index.ComputeJobSnapshot {
	if job == nil {
		return nil
	}
	return &index.ComputeJobSnapshot{
		ID:             job.ID,
		Type:           job.Type,
		Status:         job.Status,
		CreatedAt:      job.CreatedAt,
		UpdatedAt:      job.UpdatedAt,
		TotalTasks:     job.TotalTasks,
		CompletedTasks: job.CompletedTasks,
		FailedTasks:    job.FailedTasks,
	}
}

func (c *Coordinator) toTaskSnapshot(task *ComputeTask) *index.ComputeTaskSnapshot {
	if task == nil {
		return nil
	}
	return &index.ComputeTaskSnapshot{
		ID:         task.ID,
		JobID:      task.JobID,
		WorkerID:   task.WorkerID,
		Status:     task.Status,
		Attempt:    task.Attempt,
		LeaseUntil: task.LeaseUntil,
		UpdatedAt:  task.UpdatedAt,
		Checksum:   task.Checksum,
		Error:      task.Error,
		Payload:    task.Payload,
	}
}

func (c *Coordinator) toWorkerSnapshot(worker *ComputeWorker) *index.ComputeWorkerSnapshot {
	if worker == nil {
		return nil
	}
	return &index.ComputeWorkerSnapshot{
		ID:           worker.ID,
		Status:       worker.Status,
		LastSeen:     worker.LastSeen,
		LeaseUntil:   worker.LeaseUntil,
		Capabilities: append([]string(nil), worker.Capabilities...),
	}
}

func (c *Coordinator) cloneTask(task *ComputeTask) *ComputeTask {
	if task == nil {
		return nil
	}
	cp := *task
	if task.Payload != nil {
		cp.Payload = append(json.RawMessage(nil), task.Payload...)
	}
	return &cp
}

func (c *Coordinator) cloneJob(job *ComputeJob) *ComputeJob {
	if job == nil {
		return nil
	}
	cp := *job
	return &cp
}

func (c *Coordinator) failQueuedTasksLocked(jobID, reason string, now time.Time) {
	for _, task := range c.tasks {
		if task == nil || task.JobID != jobID || task.Status != "queued" {
			continue
		}
		task.Status = "failed"
		task.Error = reason
		task.LeaseUntil = time.Time{}
		task.UpdatedAt = now

		if job := c.jobs[jobID]; job != nil {
			job.FailedTasks++
		}
		if c.idx != nil {
			_ = c.idx.SaveComputeTask(c.toTaskSnapshot(task))
		}
	}

	filtered := c.queue[:0]
	for _, taskID := range c.queue {
		task := c.tasks[taskID]
		if task == nil || task.JobID == jobID {
			continue
		}
		filtered = append(filtered, taskID)
	}
	c.queue = filtered
}
