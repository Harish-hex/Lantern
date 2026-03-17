package server

import (
	"encoding/json"
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
}

func NewCoordinator(cfg config.Config, idx index.Store) *Coordinator {
	return &Coordinator{
		cfg:     cfg,
		idx:     idx,
		jobs:    make(map[string]*ComputeJob),
		tasks:   make(map[string]*ComputeTask),
		workers: make(map[string]*ComputeWorker),
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
			task.WorkerID = ""
			task.Status = "queued"
			task.LeaseUntil = time.Time{}
			task.UpdatedAt = now
			_ = c.idx.SaveComputeTask(c.toTaskSnapshot(task))
		}
	}
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
		_ = c.idx.SaveComputeWorker(&index.ComputeWorkerSnapshot{
			ID:           worker.ID,
			Status:       worker.Status,
			LastSeen:     worker.LastSeen,
			LeaseUntil:   worker.LeaseUntil,
			Capabilities: append([]string(nil), worker.Capabilities...),
		})
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
	if c.idx != nil {
		_ = c.idx.SaveComputeWorker(&index.ComputeWorkerSnapshot{
			ID:           worker.ID,
			Status:       worker.Status,
			LastSeen:     worker.LastSeen,
			LeaseUntil:   worker.LeaseUntil,
			Capabilities: append([]string(nil), worker.Capabilities...),
		})
	}
	return true
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
