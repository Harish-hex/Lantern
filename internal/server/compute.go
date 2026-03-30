package server

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Harish-hex/Lantern/internal/config"
	"github.com/Harish-hex/Lantern/internal/index"
)

type ComputeJob struct {
	ID                   string            `json:"id"`
	Type                 string            `json:"type"`
	TemplateID           string            `json:"template_id,omitempty"`
	TemplateName         string            `json:"template_name,omitempty"`
	OutputKind           string            `json:"output_kind,omitempty"`
	Status               string            `json:"status"`
	Confidence           string            `json:"confidence,omitempty"`
	NeedsAttentionReason string            `json:"needs_attention_reason,omitempty"`
	FailureCategory      string            `json:"failure_category,omitempty"`
	CreatedAt            time.Time         `json:"created_at"`
	UpdatedAt            time.Time         `json:"updated_at"`
	StartedAt            time.Time         `json:"started_at,omitempty"`
	FinishedAt           time.Time         `json:"finished_at,omitempty"`
	TotalTasks           int               `json:"total_tasks"`
	CompletedTasks       int               `json:"completed_tasks"`
	FailedTasks          int               `json:"failed_tasks"`
	RetryingTasks        int               `json:"retrying_tasks"`
	Inputs               json.RawMessage   `json:"inputs,omitempty"`
	Settings             json.RawMessage   `json:"settings,omitempty"`
	Preflight            ComputePreflight  `json:"preflight"`
	Artifacts            []ComputeArtifact `json:"artifacts,omitempty"`
}

type ComputeTask struct {
	ID                   string          `json:"id"`
	JobID                string          `json:"job_id"`
	WorkerID             string          `json:"worker_id,omitempty"`
	LastWorkerID         string          `json:"last_worker_id,omitempty"`
	Status               string          `json:"status"`
	Attempt              int             `json:"attempt"`
	LeaseUntil           time.Time       `json:"lease_until,omitempty"`
	UpdatedAt            time.Time       `json:"updated_at"`
	Checksum             string          `json:"checksum,omitempty"`
	Error                string          `json:"error,omitempty"`
	FailureCategory      string          `json:"failure_category,omitempty"`
	RequiredCapabilities []string        `json:"required_capabilities,omitempty"`
	Payload              json.RawMessage `json:"payload,omitempty"`
}

type ComputeWorker struct {
	ID                 string    `json:"id"`
	Status             string    `json:"status"`
	LastSeen           time.Time `json:"last_seen"`
	LeaseUntil         time.Time `json:"lease_until,omitempty"`
	Capabilities       []string  `json:"capabilities,omitempty"`
	CompletedTasks     int       `json:"completed_tasks"`
	FailedTasks        int       `json:"failed_tasks"`
	StaleReassignments int       `json:"stale_reassignments"`
	ReliabilityScore   float64   `json:"reliability_score"`
	Disabled           bool      `json:"disabled"`
	DisabledReason     string    `json:"disabled_reason,omitempty"`
}

type ComputeJobSubmit struct {
	Type     string            `json:"type,omitempty"`
	Template string            `json:"template,omitempty"`
	Inputs   json.RawMessage   `json:"inputs,omitempty"`
	Settings json.RawMessage   `json:"settings,omitempty"`
	Tasks    []json.RawMessage `json:"tasks,omitempty"`
}

type ComputeJobPreview struct {
	TemplateID   string           `json:"template_id,omitempty"`
	TemplateName string           `json:"template_name,omitempty"`
	OutputKind   string           `json:"output_kind,omitempty"`
	Preflight    ComputePreflight `json:"preflight"`
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

type computeSubmissionBuild struct {
	jobType       string
	templateID    string
	templateName  string
	outputKind    string
	inputs        json.RawMessage
	settings      json.RawMessage
	preflight     ComputePreflight
	compiledTasks []compiledComputeTask
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

	jobSnapshots, err := c.idx.LoadComputeJobs()
	if err != nil {
		return err
	}
	for _, snapshot := range jobSnapshots {
		c.jobs[snapshot.ID] = &ComputeJob{
			ID:                   snapshot.ID,
			Type:                 snapshot.Type,
			TemplateID:           snapshot.TemplateID,
			TemplateName:         snapshot.TemplateName,
			OutputKind:           snapshot.OutputKind,
			Status:               snapshot.Status,
			Confidence:           snapshot.Confidence,
			NeedsAttentionReason: snapshot.NeedsAttentionReason,
			FailureCategory:      snapshot.FailureCategory,
			CreatedAt:            snapshot.CreatedAt,
			UpdatedAt:            snapshot.UpdatedAt,
			StartedAt:            snapshot.StartedAt,
			FinishedAt:           snapshot.FinishedAt,
			TotalTasks:           snapshot.TotalTasks,
			CompletedTasks:       snapshot.CompletedTasks,
			FailedTasks:          snapshot.FailedTasks,
			RetryingTasks:        snapshot.RetryingTasks,
			Inputs:               cloneRawMessage(snapshot.Inputs),
			Settings:             cloneRawMessage(snapshot.Settings),
			Preflight:            preflightFromSnapshot(snapshot.Preflight),
			Artifacts:            artifactsFromSnapshot(snapshot.Artifacts),
		}
	}

	taskSnapshots, err := c.idx.LoadComputeTasks()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, snapshot := range taskSnapshots {
		task := &ComputeTask{
			ID:                   snapshot.ID,
			JobID:                snapshot.JobID,
			WorkerID:             snapshot.WorkerID,
			LastWorkerID:         snapshot.LastWorkerID,
			Status:               snapshot.Status,
			Attempt:              snapshot.Attempt,
			LeaseUntil:           snapshot.LeaseUntil,
			UpdatedAt:            snapshot.UpdatedAt,
			Checksum:             snapshot.Checksum,
			Error:                snapshot.Error,
			FailureCategory:      snapshot.FailureCategory,
			RequiredCapabilities: cloneStrings(snapshot.RequiredCapabilities),
			Payload:              cloneRawMessage(snapshot.Payload),
		}

		if !c.jobIsTerminal(task.JobID) && task.Status == ComputeTaskStatusLeased {
			task.LastWorkerID = task.WorkerID
			task.WorkerID = ""
			task.LeaseUntil = time.Time{}
			if task.Attempt >= c.maxTaskAttempts() {
				task.Status = ComputeTaskStatusNeedsAttention
				if task.Error == "" {
					task.Error = "task lease was lost during coordinator restart"
				}
				if task.FailureCategory == "" {
					task.FailureCategory = ComputeFailureTransientInfrastructure
				}
			} else {
				task.Status = ComputeTaskStatusRetrying
				task.Error = "task lease was lost during coordinator restart; queued for retry"
				task.FailureCategory = ComputeFailureTransientInfrastructure
				c.enqueueTaskLocked(task.ID)
			}
			task.UpdatedAt = now
		} else if !c.jobIsTerminal(task.JobID) && (task.Status == ComputeTaskStatusQueued || task.Status == ComputeTaskStatusRetrying) {
			c.enqueueTaskLocked(task.ID)
		}

		c.tasks[task.ID] = task
	}

	workerSnapshots, err := c.idx.LoadComputeWorkers()
	if err != nil {
		return err
	}
	for _, snapshot := range workerSnapshots {
		worker := &ComputeWorker{
			ID:                 snapshot.ID,
			Status:             snapshot.Status,
			LastSeen:           snapshot.LastSeen,
			LeaseUntil:         snapshot.LeaseUntil,
			Capabilities:       cloneStrings(snapshot.Capabilities),
			CompletedTasks:     snapshot.CompletedTasks,
			FailedTasks:        snapshot.FailedTasks,
			StaleReassignments: snapshot.StaleReassignments,
			ReliabilityScore:   snapshot.ReliabilityScore,
			Disabled:           snapshot.Disabled,
			DisabledReason:     snapshot.DisabledReason,
		}
		c.recomputeWorkerReliabilityLocked(worker)
		if worker.Disabled {
			worker.Status = "disabled"
		} else if worker.Status == "" {
			worker.Status = "ready"
		}
		c.workers[worker.ID] = worker
	}

	c.refreshAllJobsLocked(now)
	c.persistAllLocked()
	return nil
}

func (c *Coordinator) ReapStaleWorkers(now time.Time) {
	if c == nil || !c.cfg.ComputeEnabled {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for _, worker := range c.workers {
		if worker == nil {
			continue
		}
		if now.Sub(worker.LastSeen) <= c.cfg.ComputeLeaseTTL {
			continue
		}

		if worker.Disabled {
			worker.Status = "disabled"
		} else {
			worker.Status = "stale"
		}

		for _, task := range c.tasks {
			if task == nil || task.WorkerID != worker.ID || task.Status != ComputeTaskStatusLeased {
				continue
			}
			c.retryOrFailTaskLocked(task, worker, "worker lease expired", ComputeFailureTransientInfrastructure, now, true)
		}

		c.saveWorkerLocked(worker)
	}
}

func (c *Coordinator) SubmitJob(jobID string, req ComputeJobSubmit, now time.Time) (*ComputeJob, int, error) {
	if c == nil || !c.cfg.ComputeEnabled {
		return nil, 0, fmt.Errorf("compute disabled")
	}

	if jobID == "" {
		jobID = fmt.Sprintf("job_%d", now.UnixNano())
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.jobs[jobID]; exists {
		return nil, 0, fmt.Errorf("job %s already exists", jobID)
	}

	build, err := c.buildSubmissionLocked(req, now, true)
	if err != nil {
		return nil, 0, err
	}

	job := &ComputeJob{
		ID:           jobID,
		Type:         build.jobType,
		TemplateID:   build.templateID,
		TemplateName: build.templateName,
		OutputKind:   build.outputKind,
		Status:       ComputeJobStatusQueued,
		Confidence:   build.preflight.Confidence,
		CreatedAt:    now,
		UpdatedAt:    now,
		TotalTasks:   len(build.compiledTasks),
		Inputs:       cloneRawMessage(build.inputs),
		Settings:     cloneRawMessage(build.settings),
		Preflight:    clonePreflight(build.preflight),
	}
	c.jobs[jobID] = job

	for index, compiledTask := range build.compiledTasks {
		if c.cfg.ComputeTaskSizeBytes > 0 && int64(len(compiledTask.Payload)) > c.cfg.ComputeTaskSizeBytes {
			delete(c.jobs, jobID)
			return nil, 0, fmt.Errorf("task %d payload too large: %d > %d", index, len(compiledTask.Payload), c.cfg.ComputeTaskSizeBytes)
		}

		taskID := fmt.Sprintf("%s_t%04d", jobID, index+1)
		task := &ComputeTask{
			ID:                   taskID,
			JobID:                jobID,
			Status:               ComputeTaskStatusQueued,
			UpdatedAt:            now,
			RequiredCapabilities: cloneStrings(compiledTask.RequiredCapabilities),
			Payload:              cloneRawMessage(compiledTask.Payload),
		}
		c.tasks[taskID] = task
		c.enqueueTaskLocked(taskID)
		c.saveTaskLocked(task)
	}

	c.refreshJobLocked(jobID, now)
	c.saveJobLocked(job)
	return c.cloneJob(job), len(build.compiledTasks), nil
}

func (c *Coordinator) PreviewJob(req ComputeJobSubmit, now time.Time) (*ComputeJobPreview, error) {
	if c == nil || !c.cfg.ComputeEnabled {
		return nil, fmt.Errorf("compute disabled")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	build, err := c.buildSubmissionLocked(req, now, false)
	if err != nil {
		return nil, err
	}

	return &ComputeJobPreview{
		TemplateID:   build.templateID,
		TemplateName: build.templateName,
		OutputKind:   build.outputKind,
		Preflight:    clonePreflight(build.preflight),
	}, nil
}

func (c *Coordinator) ClaimTask(workerID string, now time.Time) (*ComputeTask, bool) {
	if c == nil || workerID == "" || !c.cfg.ComputeEnabled {
		return nil, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	worker, ok := c.workers[workerID]
	if !ok || worker == nil || worker.Disabled {
		return nil, false
	}

	skipped := make([]string, 0)
	for len(c.queue) > 0 {
		taskID := c.queue[0]
		c.queue = c.queue[1:]

		task := c.tasks[taskID]
		if task == nil {
			continue
		}
		if task.Status != ComputeTaskStatusQueued && task.Status != ComputeTaskStatusRetrying {
			continue
		}
		if c.jobIsTerminal(task.JobID) {
			continue
		}
		if !c.workerCanClaimTaskLocked(worker, task, now) {
			skipped = append(skipped, taskID)
			continue
		}

		c.queue = append(skipped, c.queue...)

		task.Status = ComputeTaskStatusLeased
		task.WorkerID = workerID
		task.Attempt++
		task.LeaseUntil = now.Add(c.cfg.ComputeLeaseTTL)
		task.UpdatedAt = now

		worker.Status = "busy"
		worker.LastSeen = now
		worker.LeaseUntil = task.LeaseUntil

		c.refreshJobLocked(task.JobID, now)
		c.saveTaskLocked(task)
		c.saveWorkerLocked(worker)
		if job := c.jobs[task.JobID]; job != nil {
			c.saveJobLocked(job)
		}

		return c.cloneTask(task), true
	}

	c.queue = append(c.queue, skipped...)
	if worker.Disabled {
		worker.Status = "disabled"
	} else {
		worker.Status = "ready"
	}
	worker.LastSeen = now
	c.saveWorkerLocked(worker)
	return nil, false
}

func (c *Coordinator) CompleteTask(workerID, taskID, checksum string, now time.Time) (*ComputeTask, *ComputeJob, error) {
	return c.finishTask(workerID, taskID, ComputeTaskStatusCompleted, "", checksum, now)
}

func (c *Coordinator) FailTask(workerID, taskID, errMsg, checksum string, now time.Time) (*ComputeTask, *ComputeJob, error) {
	if errMsg == "" {
		return nil, nil, fmt.Errorf("missing failure message")
	}
	return c.finishTask(workerID, taskID, ComputeTaskStatusNeedsAttention, errMsg, checksum, now)
}

func (c *Coordinator) RegisterWorker(workerID string, capabilities []string, now time.Time) {
	if c == nil || workerID == "" || !c.cfg.ComputeEnabled {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	worker := c.workers[workerID]
	if worker == nil {
		worker = &ComputeWorker{ID: workerID}
		c.workers[workerID] = worker
	}
	worker.Capabilities = cloneStrings(capabilities)
	worker.LastSeen = now
	worker.LeaseUntil = now.Add(c.cfg.ComputeLeaseTTL)
	if worker.Disabled {
		worker.Status = "disabled"
	} else if c.workerHasActiveLeaseLocked(workerID) {
		worker.Status = "busy"
	} else {
		worker.Status = "ready"
	}
	c.recomputeWorkerReliabilityLocked(worker)
	c.saveWorkerLocked(worker)
}

func (c *Coordinator) HeartbeatWorker(workerID string, now time.Time) bool {
	if c == nil || workerID == "" || !c.cfg.ComputeEnabled {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	worker, ok := c.workers[workerID]
	if !ok || worker == nil {
		return false
	}
	worker.LastSeen = now
	worker.LeaseUntil = now.Add(c.cfg.ComputeLeaseTTL)
	if worker.Disabled {
		worker.Status = "disabled"
	} else if c.workerHasActiveLeaseLocked(workerID) {
		worker.Status = "busy"
	} else {
		worker.Status = "ready"
	}
	c.saveWorkerLocked(worker)
	return true
}

func (c *Coordinator) JobState(jobID string) (*ComputeJob, []*ComputeTask, bool) {
	if c == nil || jobID == "" || !c.cfg.ComputeEnabled {
		return nil, nil, false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	job := c.jobs[jobID]
	if job == nil {
		return nil, nil, false
	}

	tasks := make([]*ComputeTask, 0, job.TotalTasks)
	for _, task := range c.tasks {
		if task == nil || task.JobID != jobID {
			continue
		}
		tasks = append(tasks, c.cloneTask(task))
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	return c.cloneJob(job), tasks, true
}

func (c *Coordinator) JobsSnapshot() []*ComputeJob {
	if c == nil || !c.cfg.ComputeEnabled {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	out := make([]*ComputeJob, 0, len(c.jobs))
	for _, job := range c.jobs {
		if job == nil {
			continue
		}
		out = append(out, c.cloneJob(job))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out
}

func (c *Coordinator) WorkersSnapshot() []*ComputeWorker {
	if c == nil || !c.cfg.ComputeEnabled {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	out := make([]*ComputeWorker, 0, len(c.workers))
	for _, worker := range c.workers {
		if worker == nil {
			continue
		}
		out = append(out, c.cloneWorker(worker))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastSeen.Equal(out[j].LastSeen) {
			return out[i].ID < out[j].ID
		}
		return out[i].LastSeen.After(out[j].LastSeen)
	})
	return out
}

func (c *Coordinator) Overview(now time.Time) *ComputeOverview {
	if c == nil || !c.cfg.ComputeEnabled {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	overview := &ComputeOverview{
		GeneratedAt:    now,
		FailureBuckets: map[string]int{},
	}

	templateMetrics := make(map[string]*ComputeTemplateMetric)
	for _, template := range BuiltInComputeTemplates() {
		templateMetrics[template.ID] = &ComputeTemplateMetric{
			TemplateID:   template.ID,
			TemplateName: template.Name,
		}
	}

	for _, job := range c.jobs {
		if job == nil {
			continue
		}
		overview.StartedJobs++
		switch job.Status {
		case ComputeJobStatusDone:
			overview.CompletedJobs++
		case ComputeJobStatusNeedsAttention:
			overview.NeedsAttentionJobs++
		default:
			overview.ActiveJobs++
		}

		metricKey := job.TemplateID
		if metricKey == "" {
			metricKey = job.Type
		}
		metric := templateMetrics[metricKey]
		if metric == nil {
			metric = &ComputeTemplateMetric{
				TemplateID:   metricKey,
				TemplateName: metricKey,
			}
			templateMetrics[metricKey] = metric
		}
		metric.StartedJobs++
		if job.Status == ComputeJobStatusDone {
			metric.CompletedJobs++
		}
		if job.Status == ComputeJobStatusNeedsAttention {
			metric.NeedsAttentionJobs++
		}
	}

	for _, task := range c.tasks {
		if task == nil {
			continue
		}
		if task.Status == ComputeTaskStatusQueued || task.Status == ComputeTaskStatusRetrying {
			overview.QueueDepth++
		}
		if task.Status == ComputeTaskStatusNeedsAttention {
			category := task.FailureCategory
			if category == "" {
				category = ComputeFailureUnknown
			}
			overview.FailureBuckets[category]++
		}
	}

	if overview.StartedJobs > 0 {
		overview.GlobalCompletionRate = float64(overview.CompletedJobs) * 100 / float64(overview.StartedJobs)
	}

	metrics := make([]ComputeTemplateMetric, 0, len(templateMetrics))
	for _, metric := range templateMetrics {
		if metric.StartedJobs > 0 {
			metric.CompletionRate = float64(metric.CompletedJobs) * 100 / float64(metric.StartedJobs)
		}
		metrics = append(metrics, *metric)
	}
	sort.Slice(metrics, func(i, j int) bool {
		if metrics[i].StartedJobs == metrics[j].StartedJobs {
			return metrics[i].TemplateName < metrics[j].TemplateName
		}
		return metrics[i].StartedJobs > metrics[j].StartedJobs
	})
	overview.TemplateMetrics = metrics
	return overview
}

func (c *Coordinator) RetryJob(jobID string, now time.Time) (*ComputeJob, error) {
	if c == nil || !c.cfg.ComputeEnabled {
		return nil, fmt.Errorf("compute disabled")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	job := c.jobs[jobID]
	if job == nil {
		return nil, fmt.Errorf("job %s not found", jobID)
	}
	if job.Status != ComputeJobStatusNeedsAttention && job.Status != ComputeJobStatusDone {
		return nil, fmt.Errorf("job %s is not in a retryable terminal state", jobID)
	}

	preflight, err := c.rebuildPreflightLocked(job, now)
	if err != nil {
		return nil, err
	}
	if !preflight.Ready {
		return nil, fmt.Errorf("%s", preflightFailureMessage(preflight))
	}

	for _, task := range c.tasks {
		if task == nil || task.JobID != jobID {
			continue
		}
		if task.WorkerID != "" {
			if worker := c.workers[task.WorkerID]; worker != nil {
				if worker.Disabled {
					worker.Status = "disabled"
				} else {
					worker.Status = "ready"
				}
				worker.LeaseUntil = time.Time{}
				c.saveWorkerLocked(worker)
			}
		}
		c.removeTaskFromQueueLocked(task.ID)
		task.WorkerID = ""
		task.LastWorkerID = ""
		task.Status = ComputeTaskStatusQueued
		task.Attempt = 0
		task.LeaseUntil = time.Time{}
		task.Checksum = ""
		task.Error = ""
		task.FailureCategory = ""
		task.UpdatedAt = now
		c.enqueueTaskLocked(task.ID)
		c.saveTaskLocked(task)
	}

	job.Status = ComputeJobStatusQueued
	job.Confidence = preflight.Confidence
	job.NeedsAttentionReason = ""
	job.FailureCategory = ""
	job.UpdatedAt = now
	job.StartedAt = time.Time{}
	job.FinishedAt = time.Time{}
	job.CompletedTasks = 0
	job.FailedTasks = 0
	job.RetryingTasks = 0
	job.Artifacts = nil
	job.Preflight = clonePreflight(preflight)
	c.refreshJobLocked(jobID, now)
	c.saveJobLocked(job)
	return c.cloneJob(job), nil
}

func (c *Coordinator) RequeueStalledTasks(now time.Time) ([]string, error) {
	if c == nil || !c.cfg.ComputeEnabled {
		return nil, fmt.Errorf("compute disabled")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	requeued := make([]string, 0)
	for _, task := range c.tasks {
		if task == nil || task.Status != ComputeTaskStatusLeased {
			continue
		}
		if task.LeaseUntil.IsZero() || now.Before(task.LeaseUntil) {
			continue
		}
		worker := c.workers[task.WorkerID]
		c.retryOrFailTaskLocked(task, worker, "task lease expired", ComputeFailureTransientInfrastructure, now, true)
		requeued = append(requeued, task.ID)
	}
	sort.Strings(requeued)
	return requeued, nil
}

func (c *Coordinator) SetWorkerDisabled(workerID string, disabled bool, reason string, now time.Time) (*ComputeWorker, error) {
	if c == nil || !c.cfg.ComputeEnabled {
		return nil, fmt.Errorf("compute disabled")
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	worker := c.workers[workerID]
	if worker == nil {
		return nil, fmt.Errorf("worker %s not found", workerID)
	}

	worker.Disabled = disabled
	if disabled {
		if strings.TrimSpace(reason) == "" {
			reason = "disabled by operator"
		}
		worker.DisabledReason = reason
		worker.Status = "disabled"
		worker.LeaseUntil = time.Time{}
		for _, task := range c.tasks {
			if task == nil || task.WorkerID != worker.ID || task.Status != ComputeTaskStatusLeased {
				continue
			}
			c.retryOrFailTaskLocked(task, worker, fmt.Sprintf("worker disabled: %s", reason), ComputeFailureTransientInfrastructure, now, false)
		}
	} else {
		worker.DisabledReason = ""
		if c.workerHasActiveLeaseLocked(worker.ID) {
			worker.Status = "busy"
		} else {
			worker.Status = "ready"
		}
	}

	c.recomputeWorkerReliabilityLocked(worker)
	c.saveWorkerLocked(worker)
	return c.cloneWorker(worker), nil
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
	if task.Status != ComputeTaskStatusLeased {
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

	if status == ComputeTaskStatusCompleted {
		task.Status = ComputeTaskStatusCompleted
		task.LastWorkerID = workerID
		task.LeaseUntil = time.Time{}
		task.UpdatedAt = now
		task.Checksum = checksum
		task.Error = ""
		task.FailureCategory = ""
		worker.CompletedTasks++
	} else {
		c.retryOrFailTaskLocked(task, worker, errMsg, classifyComputeFailure(errMsg), now, false)
	}

	if worker.Disabled {
		worker.Status = "disabled"
	} else if c.workerHasActiveLeaseLocked(worker.ID) {
		worker.Status = "busy"
	} else {
		worker.Status = "ready"
	}
	worker.LastSeen = now
	worker.LeaseUntil = time.Time{}
	c.recomputeWorkerReliabilityLocked(worker)

	c.refreshJobLocked(task.JobID, now)
	job = c.jobs[task.JobID]
	if job != nil && job.Status == ComputeJobStatusAssembling {
		c.finalizeJobLocked(job, now)
	}

	c.saveTaskLocked(task)
	c.saveWorkerLocked(worker)
	if job != nil {
		c.saveJobLocked(job)
	}

	return c.cloneTask(task), c.cloneJob(job), nil
}

func (c *Coordinator) buildSubmissionLocked(req ComputeJobSubmit, now time.Time, requireReady bool) (*computeSubmissionBuild, error) {
	templateID := req.Template
	if templateID == "" {
		templateID = req.Type
	}

	if len(req.Tasks) == 0 {
		if strings.TrimSpace(templateID) == "" {
			templateID = "data_processing_batch"
		}
		compilation, err := compileTemplateSubmission(templateID, req.Inputs, req.Settings)
		if err != nil {
			return nil, err
		}
		preflight := c.buildPreflightLocked(compilation.Template.Name, compilation.Template.RequiredCapabilities, len(compilation.Tasks), compilation.EstimatedOutputBytes, now)
		if requireReady && !preflight.Ready {
			return nil, fmt.Errorf("%s", preflightFailureMessage(preflight))
		}
		return &computeSubmissionBuild{
			jobType:       compilation.Template.ID,
			templateID:    compilation.Template.ID,
			templateName:  compilation.Template.Name,
			outputKind:    compilation.Template.OutputKind,
			inputs:        cloneRawMessage(req.Inputs),
			settings:      cloneRawMessage(req.Settings),
			preflight:     preflight,
			compiledTasks: compilation.Tasks,
		}, nil
	}

	jobType := strings.TrimSpace(req.Type)
	if jobType == "" {
		jobType = "generic_v1"
	}

	requiredCapabilities := []string{}
	if jobType != "generic_v1" {
		requiredCapabilities = []string{jobType}
	}
	compiledTasks := make([]compiledComputeTask, 0, len(req.Tasks))
	for _, taskPayload := range req.Tasks {
		compiledTasks = append(compiledTasks, compiledComputeTask{
			Payload:              cloneRawMessage(taskPayload),
			RequiredCapabilities: cloneStrings(requiredCapabilities),
		})
	}

	preflight := c.buildPreflightLocked(jobType, requiredCapabilities, len(compiledTasks), int64(len(compiledTasks))*512*1024, now)
	if requireReady && !preflight.Ready {
		return nil, fmt.Errorf("%s", preflightFailureMessage(preflight))
	}

	return &computeSubmissionBuild{
		jobType:       jobType,
		inputs:        cloneRawMessage(req.Inputs),
		settings:      cloneRawMessage(req.Settings),
		preflight:     preflight,
		compiledTasks: compiledTasks,
	}, nil
}

func (c *Coordinator) buildPreflightLocked(displayName string, requiredCapabilities []string, taskCount int, estimatedOutputBytes int64, now time.Time) ComputePreflight {
	checks := []ComputePreflightCheck{
		{
			Code:    "task_plan",
			Status:  "pass",
			Message: fmt.Sprintf("%d chunk(s) prepared for execution", taskCount),
		},
	}

	if taskCount <= 0 {
		checks = append(checks, ComputePreflightCheck{
			Code:    "task_plan",
			Status:  "fail",
			Message: "job requires at least one task chunk",
		})
	}

	if c.cfg.ComputeArtifactBudgetBytes > 0 && estimatedOutputBytes > c.cfg.ComputeArtifactBudgetBytes {
		checks = append(checks, ComputePreflightCheck{
			Code:    "artifact_budget",
			Status:  "fail",
			Message: fmt.Sprintf("estimated output %d bytes exceeds artifact budget %d bytes", estimatedOutputBytes, c.cfg.ComputeArtifactBudgetBytes),
		})
	} else {
		checks = append(checks, ComputePreflightCheck{
			Code:    "artifact_budget",
			Status:  "pass",
			Message: fmt.Sprintf("estimated output %d bytes within budget", estimatedOutputBytes),
		})
	}

	healthyWorkers := 0
	matchingWorkers := 0
	for _, worker := range c.workers {
		if worker == nil || worker.Disabled || !c.workerHealthyLocked(worker, now) {
			continue
		}
		healthyWorkers++
		if c.workerCanRunCapabilitiesLocked(worker, requiredCapabilities) {
			matchingWorkers++
		}
	}

	switch {
	case healthyWorkers == 0:
		checks = append(checks, ComputePreflightCheck{
			Code:    "capability_match",
			Status:  "fail",
			Message: fmt.Sprintf("no healthy workers are available for %s", displayName),
		})
	case len(requiredCapabilities) > 0 && matchingWorkers == 0:
		checks = append(checks, ComputePreflightCheck{
			Code:    "capability_match",
			Status:  "fail",
			Message: fmt.Sprintf("no healthy workers advertise %s", strings.Join(requiredCapabilities, ", ")),
		})
	case matchingWorkers <= 1:
		checks = append(checks, ComputePreflightCheck{
			Code:    "capability_match",
			Status:  "warn",
			Message: "only one healthy worker is currently eligible; retries may be slower",
		})
	default:
		checks = append(checks, ComputePreflightCheck{
			Code:    "capability_match",
			Status:  "pass",
			Message: fmt.Sprintf("%d healthy workers can execute this template", matchingWorkers),
		})
	}

	if taskCount > 64 {
		checks = append(checks, ComputePreflightCheck{
			Code:    "chunk_guardrail",
			Status:  "warn",
			Message: "large chunk count may increase queue tail latency on 2-5 worker clusters",
		})
	}

	preflight := ComputePreflight{
		Ready:                true,
		EstimatedTasks:       taskCount,
		EstimatedOutputBytes: estimatedOutputBytes,
		RequiredCapabilities: cloneStrings(requiredCapabilities),
		Checks:               clonePreflightChecks(checks),
	}

	warnings := 0
	for _, check := range checks {
		if check.Status == "fail" {
			preflight.Ready = false
		}
		if check.Status == "warn" {
			warnings++
		}
	}

	switch {
	case !preflight.Ready:
		preflight.Confidence = "Low"
	case matchingWorkers >= 2 && warnings == 0:
		preflight.Confidence = "High"
	default:
		preflight.Confidence = "Medium"
	}

	return preflight
}

func (c *Coordinator) rebuildPreflightLocked(job *ComputeJob, now time.Time) (ComputePreflight, error) {
	if job == nil {
		return ComputePreflight{}, fmt.Errorf("job not found")
	}

	requiredCapabilities := make([]string, 0)
	for _, task := range c.tasks {
		if task == nil || task.JobID != job.ID {
			continue
		}
		requiredCapabilities = append(requiredCapabilities, task.RequiredCapabilities...)
	}

	displayName := job.TemplateName
	if displayName == "" {
		displayName = job.Type
	}
	return c.buildPreflightLocked(displayName, uniqueSortedStrings(requiredCapabilities), job.TotalTasks, job.Preflight.EstimatedOutputBytes, now), nil
}

func (c *Coordinator) retryOrFailTaskLocked(task *ComputeTask, worker *ComputeWorker, errMsg, category string, now time.Time, stale bool) {
	if task == nil {
		return
	}

	task.LastWorkerID = task.WorkerID
	task.WorkerID = ""
	task.LeaseUntil = time.Time{}
	task.UpdatedAt = now
	task.Checksum = ""
	task.Error = errMsg
	task.FailureCategory = category

	if stale && worker != nil {
		worker.StaleReassignments++
	} else if worker != nil {
		worker.FailedTasks++
	}

	if task.Attempt < c.maxTaskAttempts() && !computeFailureTerminal(category) {
		task.Status = ComputeTaskStatusRetrying
		c.enqueueTaskLocked(task.ID)
		c.saveTaskLocked(task)
		if worker != nil {
			c.maybeQuarantineWorkerLocked(worker, now)
		}
		return
	}

	task.Status = ComputeTaskStatusNeedsAttention
	c.removeTaskFromQueueLocked(task.ID)
	if worker != nil {
		c.maybeQuarantineWorkerLocked(worker, now)
	}
	c.markJobNeedsAttentionLocked(task.JobID, task.ID, errMsg, category, now)
	c.saveTaskLocked(task)
}

func (c *Coordinator) markJobNeedsAttentionLocked(jobID, originTaskID, reason, category string, now time.Time) {
	job := c.jobs[jobID]
	if job == nil {
		return
	}
	job.NeedsAttentionReason = reason
	job.FailureCategory = category
	job.Status = ComputeJobStatusNeedsAttention

	for _, task := range c.tasks {
		if task == nil || task.JobID != jobID || task.ID == originTaskID {
			continue
		}
		if task.Status == ComputeTaskStatusCompleted || task.Status == ComputeTaskStatusNeedsAttention {
			continue
		}
		if task.WorkerID != "" {
			if worker := c.workers[task.WorkerID]; worker != nil {
				if worker.Disabled {
					worker.Status = "disabled"
				} else {
					worker.Status = "ready"
				}
				worker.LeaseUntil = time.Time{}
				c.saveWorkerLocked(worker)
			}
		}
		task.LastWorkerID = task.WorkerID
		task.WorkerID = ""
		task.Status = ComputeTaskStatusNeedsAttention
		task.LeaseUntil = time.Time{}
		task.Error = reason
		task.FailureCategory = category
		task.UpdatedAt = now
		c.removeTaskFromQueueLocked(task.ID)
		c.saveTaskLocked(task)
	}

	c.refreshJobLocked(jobID, now)
	c.saveJobLocked(job)
}

func (c *Coordinator) refreshAllJobsLocked(now time.Time) {
	jobIDs := make([]string, 0, len(c.jobs))
	for jobID := range c.jobs {
		jobIDs = append(jobIDs, jobID)
	}
	sort.Strings(jobIDs)
	for _, jobID := range jobIDs {
		c.refreshJobLocked(jobID, now)
		job := c.jobs[jobID]
		if job != nil && job.Status == ComputeJobStatusAssembling {
			c.finalizeJobLocked(job, now)
		}
	}
}

func (c *Coordinator) refreshJobLocked(jobID string, now time.Time) {
	job := c.jobs[jobID]
	if job == nil {
		return
	}

	completed := 0
	failed := 0
	retrying := 0
	leased := 0
	queued := 0
	firstFailureReason := ""
	firstFailureCategory := ""

	for _, task := range c.tasks {
		if task == nil || task.JobID != jobID {
			continue
		}

		switch task.Status {
		case ComputeTaskStatusCompleted:
			completed++
		case ComputeTaskStatusNeedsAttention:
			failed++
			if firstFailureReason == "" {
				firstFailureReason = task.Error
				firstFailureCategory = task.FailureCategory
			}
		case ComputeTaskStatusRetrying:
			retrying++
		case ComputeTaskStatusLeased:
			leased++
		case ComputeTaskStatusQueued:
			queued++
		}
	}

	job.CompletedTasks = completed
	job.FailedTasks = failed
	job.RetryingTasks = retrying
	job.UpdatedAt = now

	switch {
	case failed > 0:
		job.Status = ComputeJobStatusNeedsAttention
		job.NeedsAttentionReason = firstFailureReason
		job.FailureCategory = firstFailureCategory
	case completed == job.TotalTasks && job.TotalTasks > 0:
		if len(job.Artifacts) == 0 {
			job.Status = ComputeJobStatusAssembling
		} else {
			job.Status = ComputeJobStatusDone
			if job.FinishedAt.IsZero() {
				job.FinishedAt = now
			}
		}
	case retrying > 0:
		job.Status = ComputeJobStatusRetrying
		if job.StartedAt.IsZero() {
			job.StartedAt = now
		}
	case leased > 0:
		job.Status = ComputeJobStatusRunning
		if job.StartedAt.IsZero() {
			job.StartedAt = now
		}
	case queued == job.TotalTasks:
		job.Status = ComputeJobStatusQueued
	default:
		job.Status = ComputeJobStatusRunning
		if job.StartedAt.IsZero() {
			job.StartedAt = now
		}
	}
}

func (c *Coordinator) finalizeJobLocked(job *ComputeJob, now time.Time) {
	if job == nil {
		return
	}

	type artifactTaskSummary struct {
		ID       string `json:"id"`
		Checksum string `json:"checksum,omitempty"`
		Attempt  int    `json:"attempt"`
	}

	taskSummaries := make([]artifactTaskSummary, 0, job.TotalTasks)
	for _, task := range c.tasks {
		if task == nil || task.JobID != job.ID {
			continue
		}
		taskSummaries = append(taskSummaries, artifactTaskSummary{
			ID:       task.ID,
			Checksum: task.Checksum,
			Attempt:  task.Attempt,
		})
	}
	sort.Slice(taskSummaries, func(i, j int) bool { return taskSummaries[i].ID < taskSummaries[j].ID })

	summary := mustMarshalRaw(map[string]any{
		"job_id":        job.ID,
		"template_id":   job.TemplateID,
		"template_name": job.TemplateName,
		"completed_at":  now,
		"tasks":         taskSummaries,
	})

	namePrefix := job.TemplateID
	if namePrefix == "" {
		namePrefix = job.Type
	}
	job.Artifacts = []ComputeArtifact{
		{
			ID:        fmt.Sprintf("%s_artifact_01", job.ID),
			Name:      fmt.Sprintf("%s-%s-summary.json", job.ID, namePrefix),
			Kind:      "summary_json",
			SizeBytes: int64(len(summary)),
			CreatedAt: now,
			Summary:   summary,
		},
	}
	job.Status = ComputeJobStatusDone
	job.FinishedAt = now
	job.UpdatedAt = now
	c.saveJobLocked(job)
}

func (c *Coordinator) maxTaskAttempts() int {
	if c.cfg.ComputeRetryMax <= 0 {
		return 1
	}
	return c.cfg.ComputeRetryMax
}

func (c *Coordinator) workerHealthyLocked(worker *ComputeWorker, now time.Time) bool {
	if worker == nil || worker.Disabled {
		return false
	}
	threshold := c.cfg.ComputeLeaseTTL
	if heartbeatWindow := 2 * c.cfg.ComputeHeartbeat; heartbeatWindow > threshold {
		threshold = heartbeatWindow
	}
	if threshold <= 0 {
		threshold = time.Minute
	}
	return now.Sub(worker.LastSeen) <= threshold
}

func (c *Coordinator) workerCanRunCapabilitiesLocked(worker *ComputeWorker, requiredCapabilities []string) bool {
	if worker == nil || worker.Disabled {
		return false
	}
	if len(requiredCapabilities) == 0 || len(worker.Capabilities) == 0 {
		return true
	}
	capabilitySet := make(map[string]struct{}, len(worker.Capabilities))
	for _, capability := range worker.Capabilities {
		capabilitySet[capability] = struct{}{}
	}
	for _, required := range requiredCapabilities {
		if _, ok := capabilitySet[required]; !ok {
			return false
		}
	}
	return true
}

func (c *Coordinator) workerCanClaimTaskLocked(worker *ComputeWorker, task *ComputeTask, now time.Time) bool {
	if worker == nil || task == nil || worker.Disabled {
		return false
	}
	if !c.workerCanRunCapabilitiesLocked(worker, task.RequiredCapabilities) {
		return false
	}
	if task.LastWorkerID == worker.ID && c.otherEligibleWorkerExistsLocked(task, worker.ID, now) {
		return false
	}
	return true
}

func (c *Coordinator) otherEligibleWorkerExistsLocked(task *ComputeTask, excludeWorkerID string, now time.Time) bool {
	for _, worker := range c.workers {
		if worker == nil || worker.ID == excludeWorkerID {
			continue
		}
		if !c.workerHealthyLocked(worker, now) {
			continue
		}
		if c.workerCanRunCapabilitiesLocked(worker, task.RequiredCapabilities) {
			return true
		}
	}
	return false
}

func (c *Coordinator) maybeQuarantineWorkerLocked(worker *ComputeWorker, now time.Time) {
	if worker == nil || worker.Disabled {
		return
	}
	threshold := c.cfg.ComputeWorkerQuarantineThreshold
	if threshold <= 0 {
		threshold = 4
	}
	if worker.FailedTasks+worker.StaleReassignments < threshold {
		return
	}
	worker.Disabled = true
	worker.DisabledReason = fmt.Sprintf("auto-quarantined after %d reliability events", worker.FailedTasks+worker.StaleReassignments)
	worker.Status = "disabled"
	worker.LeaseUntil = time.Time{}
	worker.LastSeen = now
}

func (c *Coordinator) workerHasActiveLeaseLocked(workerID string) bool {
	for _, task := range c.tasks {
		if task == nil {
			continue
		}
		if task.WorkerID == workerID && task.Status == ComputeTaskStatusLeased {
			return true
		}
	}
	return false
}

func (c *Coordinator) enqueueTaskLocked(taskID string) {
	for _, queuedID := range c.queue {
		if queuedID == taskID {
			return
		}
	}
	c.queue = append(c.queue, taskID)
}

func (c *Coordinator) removeTaskFromQueueLocked(taskID string) {
	filtered := c.queue[:0]
	for _, queuedID := range c.queue {
		if queuedID == taskID {
			continue
		}
		filtered = append(filtered, queuedID)
	}
	c.queue = filtered
}

func (c *Coordinator) jobIsTerminal(jobID string) bool {
	job := c.jobs[jobID]
	if job == nil {
		return false
	}
	return job.Status == ComputeJobStatusDone || job.Status == ComputeJobStatusNeedsAttention
}

func (c *Coordinator) saveJobLocked(job *ComputeJob) {
	if c.idx == nil || job == nil {
		return
	}
	_ = c.idx.SaveComputeJob(c.toJobSnapshot(job))
}

func (c *Coordinator) saveTaskLocked(task *ComputeTask) {
	if c.idx == nil || task == nil {
		return
	}
	_ = c.idx.SaveComputeTask(c.toTaskSnapshot(task))
}

func (c *Coordinator) saveWorkerLocked(worker *ComputeWorker) {
	if c.idx == nil || worker == nil {
		return
	}
	_ = c.idx.SaveComputeWorker(c.toWorkerSnapshot(worker))
}

func (c *Coordinator) persistAllLocked() {
	if c.idx == nil {
		return
	}
	for _, job := range c.jobs {
		c.saveJobLocked(job)
	}
	for _, task := range c.tasks {
		c.saveTaskLocked(task)
	}
	for _, worker := range c.workers {
		c.saveWorkerLocked(worker)
	}
}

func (c *Coordinator) recomputeWorkerReliabilityLocked(worker *ComputeWorker) {
	if worker == nil {
		return
	}
	totalEvents := worker.CompletedTasks + worker.FailedTasks + worker.StaleReassignments
	if totalEvents == 0 {
		worker.ReliabilityScore = 100
		return
	}
	worker.ReliabilityScore = float64(worker.CompletedTasks) * 100 / float64(totalEvents)
}

func (c *Coordinator) toJobSnapshot(job *ComputeJob) *index.ComputeJobSnapshot {
	if job == nil {
		return nil
	}
	return &index.ComputeJobSnapshot{
		ID:                   job.ID,
		Type:                 job.Type,
		TemplateID:           job.TemplateID,
		TemplateName:         job.TemplateName,
		OutputKind:           job.OutputKind,
		Status:               job.Status,
		Confidence:           job.Confidence,
		NeedsAttentionReason: job.NeedsAttentionReason,
		FailureCategory:      job.FailureCategory,
		CreatedAt:            job.CreatedAt,
		UpdatedAt:            job.UpdatedAt,
		StartedAt:            job.StartedAt,
		FinishedAt:           job.FinishedAt,
		TotalTasks:           job.TotalTasks,
		CompletedTasks:       job.CompletedTasks,
		FailedTasks:          job.FailedTasks,
		RetryingTasks:        job.RetryingTasks,
		Inputs:               cloneRawMessage(job.Inputs),
		Settings:             cloneRawMessage(job.Settings),
		Preflight:            preflightToSnapshot(job.Preflight),
		Artifacts:            artifactsToSnapshot(job.Artifacts),
	}
}

func (c *Coordinator) toTaskSnapshot(task *ComputeTask) *index.ComputeTaskSnapshot {
	if task == nil {
		return nil
	}
	return &index.ComputeTaskSnapshot{
		ID:                   task.ID,
		JobID:                task.JobID,
		WorkerID:             task.WorkerID,
		LastWorkerID:         task.LastWorkerID,
		Status:               task.Status,
		Attempt:              task.Attempt,
		LeaseUntil:           task.LeaseUntil,
		UpdatedAt:            task.UpdatedAt,
		Checksum:             task.Checksum,
		Error:                task.Error,
		FailureCategory:      task.FailureCategory,
		RequiredCapabilities: cloneStrings(task.RequiredCapabilities),
		Payload:              cloneRawMessage(task.Payload),
	}
}

func (c *Coordinator) toWorkerSnapshot(worker *ComputeWorker) *index.ComputeWorkerSnapshot {
	if worker == nil {
		return nil
	}
	return &index.ComputeWorkerSnapshot{
		ID:                 worker.ID,
		Status:             worker.Status,
		LastSeen:           worker.LastSeen,
		LeaseUntil:         worker.LeaseUntil,
		Capabilities:       cloneStrings(worker.Capabilities),
		CompletedTasks:     worker.CompletedTasks,
		FailedTasks:        worker.FailedTasks,
		StaleReassignments: worker.StaleReassignments,
		ReliabilityScore:   worker.ReliabilityScore,
		Disabled:           worker.Disabled,
		DisabledReason:     worker.DisabledReason,
	}
}

func (c *Coordinator) cloneTask(task *ComputeTask) *ComputeTask {
	if task == nil {
		return nil
	}
	cp := *task
	cp.RequiredCapabilities = cloneStrings(task.RequiredCapabilities)
	cp.Payload = cloneRawMessage(task.Payload)
	return &cp
}

func (c *Coordinator) cloneJob(job *ComputeJob) *ComputeJob {
	if job == nil {
		return nil
	}
	cp := *job
	cp.Inputs = cloneRawMessage(job.Inputs)
	cp.Settings = cloneRawMessage(job.Settings)
	cp.Preflight = clonePreflight(job.Preflight)
	cp.Artifacts = cloneArtifacts(job.Artifacts)
	return &cp
}

func (c *Coordinator) cloneWorker(worker *ComputeWorker) *ComputeWorker {
	if worker == nil {
		return nil
	}
	cp := *worker
	cp.Capabilities = cloneStrings(worker.Capabilities)
	return &cp
}

func clonePreflight(preflight ComputePreflight) ComputePreflight {
	cp := preflight
	cp.RequiredCapabilities = cloneStrings(preflight.RequiredCapabilities)
	cp.Checks = clonePreflightChecks(preflight.Checks)
	return cp
}

func clonePreflightChecks(checks []ComputePreflightCheck) []ComputePreflightCheck {
	if len(checks) == 0 {
		return nil
	}
	return append([]ComputePreflightCheck(nil), checks...)
}

func cloneArtifacts(artifacts []ComputeArtifact) []ComputeArtifact {
	if len(artifacts) == 0 {
		return nil
	}
	out := make([]ComputeArtifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		cp := artifact
		cp.Summary = cloneRawMessage(artifact.Summary)
		out = append(out, cp)
	}
	return out
}

func preflightFailureMessage(preflight ComputePreflight) string {
	failures := make([]string, 0)
	for _, check := range preflight.Checks {
		if check.Status == "fail" {
			failures = append(failures, check.Message)
		}
	}
	if len(failures) == 0 {
		return "compute preflight failed"
	}
	return strings.Join(failures, "; ")
}

func preflightToSnapshot(preflight ComputePreflight) index.ComputePreflightSnapshot {
	checks := make([]index.ComputePreflightCheckSnapshot, 0, len(preflight.Checks))
	for _, check := range preflight.Checks {
		checks = append(checks, index.ComputePreflightCheckSnapshot{
			Code:    check.Code,
			Status:  check.Status,
			Message: check.Message,
		})
	}
	return index.ComputePreflightSnapshot{
		Ready:                preflight.Ready,
		Confidence:           preflight.Confidence,
		EstimatedTasks:       preflight.EstimatedTasks,
		EstimatedOutputBytes: preflight.EstimatedOutputBytes,
		RequiredCapabilities: cloneStrings(preflight.RequiredCapabilities),
		Checks:               checks,
	}
}

func preflightFromSnapshot(snapshot index.ComputePreflightSnapshot) ComputePreflight {
	checks := make([]ComputePreflightCheck, 0, len(snapshot.Checks))
	for _, check := range snapshot.Checks {
		checks = append(checks, ComputePreflightCheck{
			Code:    check.Code,
			Status:  check.Status,
			Message: check.Message,
		})
	}
	return ComputePreflight{
		Ready:                snapshot.Ready,
		Confidence:           snapshot.Confidence,
		EstimatedTasks:       snapshot.EstimatedTasks,
		EstimatedOutputBytes: snapshot.EstimatedOutputBytes,
		RequiredCapabilities: cloneStrings(snapshot.RequiredCapabilities),
		Checks:               checks,
	}
}

func artifactsToSnapshot(artifacts []ComputeArtifact) []index.ComputeArtifactSnapshot {
	if len(artifacts) == 0 {
		return nil
	}
	out := make([]index.ComputeArtifactSnapshot, 0, len(artifacts))
	for _, artifact := range artifacts {
		out = append(out, index.ComputeArtifactSnapshot{
			ID:        artifact.ID,
			Name:      artifact.Name,
			Kind:      artifact.Kind,
			SizeBytes: artifact.SizeBytes,
			CreatedAt: artifact.CreatedAt,
			Summary:   cloneRawMessage(artifact.Summary),
		})
	}
	return out
}

func artifactsFromSnapshot(snapshots []index.ComputeArtifactSnapshot) []ComputeArtifact {
	if len(snapshots) == 0 {
		return nil
	}
	out := make([]ComputeArtifact, 0, len(snapshots))
	for _, snapshot := range snapshots {
		out = append(out, ComputeArtifact{
			ID:        snapshot.ID,
			Name:      snapshot.Name,
			Kind:      snapshot.Kind,
			SizeBytes: snapshot.SizeBytes,
			CreatedAt: snapshot.CreatedAt,
			Summary:   cloneRawMessage(snapshot.Summary),
		})
	}
	return out
}
