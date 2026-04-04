package index

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Harish-hex/Lantern/internal/protocol"
)

type SessionSnapshot struct {
	ID               string                 `json:"id"`
	State            string                 `json:"state"`
	CreatedAt        time.Time              `json:"created_at"`
	LastActivity     time.Time              `json:"last_activity"`
	CurrentFileIndex uint32                 `json:"current_file_index"`
	Files            []FileTransferSnapshot `json:"files"`
}

type FileTransferSnapshot struct {
	FileIndex      uint32                `json:"file_index"`
	Metadata       protocol.FileMetadata `json:"metadata"`
	TempPath       string                `json:"temp_path"`
	StoredFileID   string                `json:"stored_file_id,omitempty"`
	ReceivedChunks uint32                `json:"received_chunks"`
	LastAckedSeq   uint32                `json:"last_acked_seq"`
	State          string                `json:"state"`
	StartedAt      time.Time             `json:"started_at"`
}

type StoredFileSnapshot struct {
	ID            string                `json:"id"`
	Path          string                `json:"path"`
	Metadata      protocol.FileMetadata `json:"metadata"`
	DownloadCount int32                 `json:"download_count"`
	MaxDownloads  int                   `json:"max_downloads"`
	ExpiresAt     time.Time             `json:"expires_at"`
}

type ComputeJobSnapshot struct {
	ID                   string                          `json:"id"`
	Type                 string                          `json:"type"`
	TemplateID           string                          `json:"template_id,omitempty"`
	TemplateName         string                          `json:"template_name,omitempty"`
	OutputKind           string                          `json:"output_kind,omitempty"`
	ExecutionProfile     ComputeExecutionProfileSnapshot `json:"execution_profile,omitempty"`
	InputFileIDs         []string                        `json:"input_file_ids,omitempty"`
	Status               string                          `json:"status"`
	Confidence           string                          `json:"confidence,omitempty"`
	NeedsAttentionReason string                          `json:"needs_attention_reason,omitempty"`
	FailureCategory      string                          `json:"failure_category,omitempty"`
	CreatedAt            time.Time                       `json:"created_at"`
	UpdatedAt            time.Time                       `json:"updated_at"`
	StartedAt            time.Time                       `json:"started_at,omitempty"`
	FinishedAt           time.Time                       `json:"finished_at,omitempty"`
	TotalTasks           int                             `json:"total_tasks"`
	CompletedTasks       int                             `json:"completed_tasks"`
	FailedTasks          int                             `json:"failed_tasks"`
	RetryingTasks        int                             `json:"retrying_tasks"`
	Inputs               json.RawMessage                 `json:"inputs,omitempty"`
	Settings             json.RawMessage                 `json:"settings,omitempty"`
	Preflight            ComputePreflightSnapshot        `json:"preflight"`
	Artifacts            []ComputeArtifactSnapshot       `json:"artifacts,omitempty"`
}

type ComputeTaskSnapshot struct {
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

type ComputeWorkerSnapshot struct {
	ID                 string    `json:"id"`
	Status             string    `json:"status"`
	DeviceName         string    `json:"device_name,omitempty"`
	OSInfo             string    `json:"os_info,omitempty"`
	RegistrationIP     string    `json:"registration_ip,omitempty"`
	EnrolledAt         time.Time `json:"enrolled_at,omitempty"`
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

type ComputePreflightSnapshot struct {
	Ready                bool                            `json:"ready"`
	Confidence           string                          `json:"confidence,omitempty"`
	EstimatedTasks       int                             `json:"estimated_tasks"`
	EstimatedOutputBytes int64                           `json:"estimated_output_bytes"`
	RequiredCapabilities []string                        `json:"required_capabilities,omitempty"`
	Checks               []ComputePreflightCheckSnapshot `json:"checks,omitempty"`
}

type ComputeExecutionProfileSnapshot struct {
	ResolvedRenderDevice          string   `json:"resolved_render_device,omitempty"`
	EffectiveRequiredCapabilities []string `json:"effective_required_capabilities,omitempty"`
	RequiresBlender               bool     `json:"requires_blender,omitempty"`
	RequiresCoordinatorFFmpeg     bool     `json:"requires_coordinator_ffmpeg,omitempty"`
	EstimatedOutputBytesTotal     int64    `json:"estimated_output_bytes_total,omitempty"`
	EstimatedOutputBytesPerTask   int64    `json:"estimated_output_bytes_per_task,omitempty"`
}

type ComputePreflightCheckSnapshot struct {
	Code    string `json:"code"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type ComputeArtifactSnapshot struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Kind      string          `json:"kind"`
	SizeBytes int64           `json:"size_bytes"`
	CreatedAt time.Time       `json:"created_at"`
	Summary   json.RawMessage `json:"summary,omitempty"`
}

type Store interface {
	SaveSession(*SessionSnapshot) error
	DeleteSession(id string) error
	LoadSessions() ([]*SessionSnapshot, error)

	SaveStoredFile(*StoredFileSnapshot) error
	DeleteStoredFile(id string) error
	LoadStoredFiles() ([]*StoredFileSnapshot, error)

	SaveComputeJob(*ComputeJobSnapshot) error
	DeleteComputeJob(id string) error
	LoadComputeJobs() ([]*ComputeJobSnapshot, error)

	SaveComputeTask(*ComputeTaskSnapshot) error
	DeleteComputeTask(id string) error
	LoadComputeTasks() ([]*ComputeTaskSnapshot, error)

	SaveComputeWorker(*ComputeWorkerSnapshot) error
	DeleteComputeWorker(id string) error
	LoadComputeWorkers() ([]*ComputeWorkerSnapshot, error)
}

type JSONStore struct {
	baseDir     string
	sessionsDir string
	filesDir    string
	computeDir  string
	jobsDir     string
	tasksDir    string
	workersDir  string
	mu          sync.Mutex
}

func NewJSONStore(baseDir string) (*JSONStore, error) {
	js := &JSONStore{
		baseDir:     baseDir,
		sessionsDir: filepath.Join(baseDir, "sessions"),
		filesDir:    filepath.Join(baseDir, "files"),
		computeDir:  filepath.Join(baseDir, "compute"),
		jobsDir:     filepath.Join(baseDir, "compute", "jobs"),
		tasksDir:    filepath.Join(baseDir, "compute", "tasks"),
		workersDir:  filepath.Join(baseDir, "compute", "workers"),
	}
	for _, dir := range []string{js.baseDir, js.sessionsDir, js.filesDir, js.computeDir, js.jobsDir, js.tasksDir, js.workersDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create index dir %s: %w", dir, err)
		}
	}
	return js, nil
}

func (s *JSONStore) SaveSession(snapshot *SessionSnapshot) error {
	return s.write(filepath.Join(s.sessionsDir, snapshot.ID+".json"), snapshot)
}

func (s *JSONStore) DeleteSession(id string) error {
	return s.remove(filepath.Join(s.sessionsDir, id+".json"))
}

func (s *JSONStore) LoadSessions() ([]*SessionSnapshot, error) {
	var out []*SessionSnapshot
	if err := s.loadDir(s.sessionsDir, func(data []byte) error {
		var snap SessionSnapshot
		if err := json.Unmarshal(data, &snap); err != nil {
			return err
		}
		out = append(out, &snap)
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *JSONStore) SaveStoredFile(snapshot *StoredFileSnapshot) error {
	return s.write(filepath.Join(s.filesDir, snapshot.ID+".json"), snapshot)
}

func (s *JSONStore) DeleteStoredFile(id string) error {
	return s.remove(filepath.Join(s.filesDir, id+".json"))
}

func (s *JSONStore) LoadStoredFiles() ([]*StoredFileSnapshot, error) {
	var out []*StoredFileSnapshot
	if err := s.loadDir(s.filesDir, func(data []byte) error {
		var snap StoredFileSnapshot
		if err := json.Unmarshal(data, &snap); err != nil {
			return err
		}
		out = append(out, &snap)
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *JSONStore) SaveComputeJob(snapshot *ComputeJobSnapshot) error {
	return s.write(filepath.Join(s.jobsDir, snapshot.ID+".json"), snapshot)
}

func (s *JSONStore) DeleteComputeJob(id string) error {
	return s.remove(filepath.Join(s.jobsDir, id+".json"))
}

func (s *JSONStore) LoadComputeJobs() ([]*ComputeJobSnapshot, error) {
	var out []*ComputeJobSnapshot
	if err := s.loadDir(s.jobsDir, func(data []byte) error {
		var snap ComputeJobSnapshot
		if err := json.Unmarshal(data, &snap); err != nil {
			return err
		}
		out = append(out, &snap)
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *JSONStore) SaveComputeTask(snapshot *ComputeTaskSnapshot) error {
	return s.write(filepath.Join(s.tasksDir, snapshot.ID+".json"), snapshot)
}

func (s *JSONStore) DeleteComputeTask(id string) error {
	return s.remove(filepath.Join(s.tasksDir, id+".json"))
}

func (s *JSONStore) LoadComputeTasks() ([]*ComputeTaskSnapshot, error) {
	var out []*ComputeTaskSnapshot
	if err := s.loadDir(s.tasksDir, func(data []byte) error {
		var snap ComputeTaskSnapshot
		if err := json.Unmarshal(data, &snap); err != nil {
			return err
		}
		out = append(out, &snap)
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *JSONStore) SaveComputeWorker(snapshot *ComputeWorkerSnapshot) error {
	return s.write(filepath.Join(s.workersDir, snapshot.ID+".json"), snapshot)
}

func (s *JSONStore) DeleteComputeWorker(id string) error {
	return s.remove(filepath.Join(s.workersDir, id+".json"))
}

func (s *JSONStore) LoadComputeWorkers() ([]*ComputeWorkerSnapshot, error) {
	var out []*ComputeWorkerSnapshot
	if err := s.loadDir(s.workersDir, func(data []byte) error {
		var snap ComputeWorkerSnapshot
		if err := json.Unmarshal(data, &snap); err != nil {
			return err
		}
		out = append(out, &snap)
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *JSONStore) write(path string, v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("write snapshot %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename snapshot %s: %w", path, err)
	}
	return nil
}

func (s *JSONStore) remove(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove snapshot %s: %w", path, err)
	}
	return nil
}

func (s *JSONStore) loadDir(dir string, decode func([]byte) error) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read index dir %s: %w", dir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return fmt.Errorf("read snapshot %s: %w", entry.Name(), err)
		}
		if err := decode(data); err != nil {
			return fmt.Errorf("decode snapshot %s: %w", entry.Name(), err)
		}
	}
	return nil
}
