package worker

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Harish-hex/Lantern/internal/client"
	"github.com/Harish-hex/Lantern/internal/protocol"
	"github.com/Harish-hex/Lantern/internal/worker/toolchain"
)

// defaultTaskTimeout is the maximum time a single task execution may take.
const defaultTaskTimeout = 5 * time.Minute

// logBatchSize controls how many lines to accumulate before flushing to the coordinator.
const logBatchSize = 10

// logBatchInterval controls the maximum age of an unflushed batch.
const logBatchInterval = 500 * time.Millisecond

// defaultTaskZipThresholdBytes is the output-size threshold above which task
// results are archived before upload.
const defaultTaskZipThresholdBytes int64 = 8 * 1024 * 1024

type RunnerConfig struct {
	Host                  string
	Port                  int
	HTTPPort              int
	WorkerID              string
	Token                 string
	Heartbeat             time.Duration
	PollInterval          time.Duration
	OneShot               bool
	TaskTimeout           time.Duration
	TaskZipThresholdBytes int64
	WorkspaceDir          string
	ToolchainManager      *toolchain.Manager
	Registry              *Registry
}

type Runner struct {
	cfg RunnerConfig
}

func NewRunner(cfg RunnerConfig) *Runner {
	if cfg.WorkspaceDir == "" {
		cfg.WorkspaceDir = filepath.Join(os.TempDir(), "lantern_worker")
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 2 * time.Second
	}
	if cfg.Heartbeat == 0 {
		cfg.Heartbeat = 5 * time.Second
	}
	if cfg.TaskTimeout == 0 {
		cfg.TaskTimeout = defaultTaskTimeout
	}
	if cfg.TaskZipThresholdBytes <= 0 {
		cfg.TaskZipThresholdBytes = defaultTaskZipThresholdBytes
	}
	return &Runner{cfg: cfg}
}

func (r *Runner) Run(ctx context.Context) error {
	c, err := client.New(r.cfg.Host, r.cfg.Port)
	if err != nil {
		return fmt.Errorf("connect to server: %w", err)
	}
	defer c.Close()

	if err := os.MkdirAll(r.cfg.WorkspaceDir, 0755); err != nil {
		return fmt.Errorf("create workspace dir: %w", err)
	}

	caps := r.cfg.Registry.Capabilities()
	if err := c.RegisterWorker(r.cfg.WorkerID, r.cfg.Token, caps); err != nil {
		return fmt.Errorf("register worker: %w", err)
	}
	log.Printf("[worker %s] registered with capabilities: %v", r.cfg.WorkerID, caps)

	nextHeartbeat := time.Now()
	completed := 0

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		now := time.Now()
		if now.After(nextHeartbeat) {
			if err := c.HeartbeatWorker(r.cfg.WorkerID, r.cfg.Token); err != nil {
				log.Printf("[worker %s] heartbeat failed: %v", r.cfg.WorkerID, err)
			}
			nextHeartbeat = now.Add(r.cfg.Heartbeat)
		}

		resp, err := c.ClaimComputeTask(r.cfg.WorkerID, r.cfg.Token)
		if err != nil {
			log.Printf("[worker %s] claim failed: %v", r.cfg.WorkerID, err)
			time.Sleep(r.cfg.PollInterval)
			continue
		}
		if resp.Type == protocol.CtrlNAK {
			// No tasks
			time.Sleep(r.cfg.PollInterval)
			continue
		}

		log.Printf("[worker %s] claimed task %s (job %s)", r.cfg.WorkerID, resp.TaskID, resp.JobID)
		err = r.executeTask(ctx, c, resp)
		if err != nil {
			log.Printf("[worker %s] task %s failed: %v", r.cfg.WorkerID, resp.TaskID, err)
		} else {
			completed++
			log.Printf("[worker %s] task %s completed successfully", r.cfg.WorkerID, resp.TaskID)
		}

		if r.cfg.OneShot && completed >= 1 {
			log.Printf("[worker %s] oneshot mode complete", r.cfg.WorkerID)
			return nil
		}
	}
}

func (r *Runner) executeTask(ctx context.Context, c *client.Client, claim *protocol.ControlPayload) error {
	// 1. Identify executor
	if len(claim.Capabilities) == 0 {
		return r.failTask(c, claim.TaskID, "task missing capabilities", claim.Payload)
	}

	// Usually capability [0] is the template ID
	templateID := claim.Capabilities[0]
	executor, ok := r.cfg.Registry.Get(templateID)
	if !ok {
		return r.failTask(c, claim.TaskID, fmt.Sprintf("no executor found for %s", templateID), claim.Payload)
	}

	taskDir := filepath.Join(r.cfg.WorkspaceDir, claim.TaskID)
	inputDir := filepath.Join(taskDir, "input")
	outputDir := filepath.Join(taskDir, "output")
	if err := os.MkdirAll(inputDir, 0755); err != nil {
		return r.failTask(c, claim.TaskID, fmt.Sprintf("failed to create input dir: %v", err), claim.Payload)
	}
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return r.failTask(c, claim.TaskID, fmt.Sprintf("failed to create output dir: %v", err), claim.Payload)
	}
	defer os.RemoveAll(taskDir) // Cleanup

	// 1b. Resolve file IDs in payload → download to inputDir, rewrite payload paths.
	resolvedPayload, resolveErr := r.resolveFileInputs(claim.Payload, inputDir)
	if resolveErr != nil {
		log.Printf("[worker %s] file resolve warning: %v", r.cfg.WorkerID, resolveErr)
		// Non-fatal — fall through with original payload; executor will try paths as-is.
		resolvedPayload = claim.Payload
	}
	claim.Payload = resolvedPayload

	// 2. Build a log writer that batches lines and sends them to the coordinator.
	lw := newTaskLogWriter(c, r.cfg.WorkerID, claim.TaskID, r.cfg.Token)

	// 3. Apply task timeout
	taskCtx, cancel := context.WithTimeout(ctx, r.cfg.TaskTimeout)
	defer cancel()

	log.Printf("[worker %s] executing task %s using %s", r.cfg.WorkerID, claim.TaskID, templateID)
	lw.writeLine(fmt.Sprintf("[LANTERN] Task %s started — executor: %s", claim.TaskID, templateID))

	// 4. Execute — provide the log writer to executors that support it
	var execErr error
	if le, ok := executor.(LoggableExecutor); ok {
		execErr = le.ExecuteWithLog(taskCtx, claim.Payload, outputDir, lw)
	} else {
		execErr = executor.Execute(taskCtx, claim.Payload, outputDir)
	}

	if execErr != nil {
		lw.writeLine(fmt.Sprintf("[LANTERN] Task failed: %v", execErr))
		lw.flush(c) // final flush before failing
		return r.failTask(c, claim.TaskID, fmt.Sprintf("execution failed: %v", execErr), claim.Payload)
	}

	lw.writeLine(fmt.Sprintf("[LANTERN] Task %s execution complete — packaging results", claim.TaskID))
	lw.flush(c)

	artifactUploadPath, usedZip, packageErr := r.selectTaskArtifactUploadPath(outputDir, claim.TaskID)
	if packageErr != nil {
		return r.failTask(c, claim.TaskID, fmt.Sprintf("failed to package output: %v", packageErr), claim.Payload)
	}
	if usedZip {
		defer os.Remove(artifactUploadPath)
		lw.writeLine(fmt.Sprintf("[LANTERN] Task %s packaged as zip (threshold=%d bytes)", claim.TaskID, r.cfg.TaskZipThresholdBytes))
	} else {
		lw.writeLine(fmt.Sprintf("[LANTERN] Task %s uploaded direct artifact: %s", claim.TaskID, filepath.Base(artifactUploadPath)))
	}
	lw.flush(c)

	// 6. Upload Artifact
	var artifactID string
	artifactID, uploadErr := r.uploadTaskArtifact(artifactUploadPath)
	if uploadErr != nil {
		log.Printf("[worker %s] warning: failed to upload task artifact: %v", r.cfg.WorkerID, uploadErr)
	}

	payloadDigest := sha256.Sum256(claim.Payload)
	checksum := "sha256:" + hex.EncodeToString(payloadDigest[:])

	if err := c.CompleteComputeTask(r.cfg.WorkerID, claim.TaskID, artifactID, checksum, r.cfg.Token); err != nil {
		return r.failTask(c, claim.TaskID, fmt.Sprintf("complete task API call: %v", err), claim.Payload)
	}
	return nil
}

// LoggableExecutor is an optional interface executors may implement to receive a log writer.
// If an executor implements this, ExecuteWithLog is called instead of Execute.
type LoggableExecutor interface {
	ExecuteWithLog(ctx context.Context, payload []byte, outputDir string, lw *TaskLogWriter) error
}

// TaskLogWriter collects log lines and batches them to the coordinator.
// It is NOT goroutine-safe; it is owned by a single task execution.
type TaskLogWriter struct {
	mu        sync.Mutex
	workerID  string
	taskID    string
	token     string
	buf       []string
	lastFlush time.Time
}

func newTaskLogWriter(c *client.Client, workerID, taskID, token string) *TaskLogWriter {
	lw := &TaskLogWriter{
		workerID:  workerID,
		taskID:    taskID,
		token:     token,
		buf:       make([]string, 0, logBatchSize),
		lastFlush: time.Now(),
	}
	return lw
}

// Write implements io.Writer so the TaskLogWriter can capture executor stdout/stderr.
func (lw *TaskLogWriter) Write(p []byte) (int, error) {
	lines := strings.Split(strings.TrimRight(string(p), "\n"), "\n")
	lw.mu.Lock()
	lw.buf = append(lw.buf, lines...)
	lw.mu.Unlock()
	return len(p), nil
}

// writeLine appends a single log line.
func (lw *TaskLogWriter) writeLine(line string) {
	lw.mu.Lock()
	lw.buf = append(lw.buf, line)
	lw.mu.Unlock()
}

// maybeFlush sends the buffered lines to the coordinator if the batch is full
// or enough time has elapsed since the last flush.
func (lw *TaskLogWriter) maybeFlush(c *client.Client) {
	lw.mu.Lock()
	shouldFlush := len(lw.buf) >= logBatchSize || time.Since(lw.lastFlush) >= logBatchInterval
	if !shouldFlush || len(lw.buf) == 0 {
		lw.mu.Unlock()
		return
	}
	lines := lw.buf
	lw.buf = make([]string, 0, logBatchSize)
	lw.lastFlush = time.Now()
	lw.mu.Unlock()

	if err := c.SendTaskLog(lw.workerID, lw.taskID, lw.token, lines); err != nil {
		log.Printf("[logwriter] failed to send log batch: %v", err)
	}
}

// flush forcibly sends any remaining buffered lines.
func (lw *TaskLogWriter) flush(c *client.Client) {
	lw.mu.Lock()
	lines := lw.buf
	lw.buf = make([]string, 0, logBatchSize)
	lw.lastFlush = time.Now()
	lw.mu.Unlock()

	if len(lines) == 0 {
		return
	}
	if err := c.SendTaskLog(lw.workerID, lw.taskID, lw.token, lines); err != nil {
		log.Printf("[logwriter] failed to flush log batch: %v", err)
	}
}

// Writer returns an io.Writer view of the TaskLogWriter (for piping subprocess output).
func (lw *TaskLogWriter) Writer() io.Writer {
	return &lockedWriter{lw: lw}
}

// lockedWriter wraps TaskLogWriter as a plain io.Writer (no-op buffering approach).
type lockedWriter struct {
	lw  *TaskLogWriter
	buf bytes.Buffer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	return w.lw.Write(p)
}

func (r *Runner) zipDir(source, target string) error {
	zipfile, err := os.Create(target)
	if err != nil {
		return err
	}
	defer zipfile.Close()

	archive := zip.NewWriter(zipfile)
	defer archive.Close()

	return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if path == source {
			return nil
		}

		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}

		header.Name, err = filepath.Rel(source, path)
		if err != nil {
			return err
		}

		if info.IsDir() {
			header.Name += "/"
		} else {
			header.Method = zip.Deflate
		}

		writer, err := archive.CreateHeader(header)
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()

		_, err = io.Copy(writer, file)
		return err
	})
}

func (r *Runner) selectTaskArtifactUploadPath(outputDir, taskID string) (string, bool, error) {
	totalBytes, nonSummaryFiles, err := scanTaskOutput(outputDir)
	if err != nil {
		return "", false, err
	}

	if len(nonSummaryFiles) == 1 && totalBytes <= r.cfg.TaskZipThresholdBytes {
		return nonSummaryFiles[0], false, nil
	}

	zipPath := filepath.Join(r.cfg.WorkspaceDir, taskID+".zip")
	if err := r.zipDir(outputDir, zipPath); err != nil {
		return "", false, err
	}
	return zipPath, true, nil
}

func scanTaskOutput(outputDir string) (int64, []string, error) {
	var totalBytes int64
	nonSummaryFiles := make([]string, 0, 2)

	err := filepath.Walk(outputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		totalBytes += info.Size()
		if !strings.EqualFold(info.Name(), "summary.json") {
			nonSummaryFiles = append(nonSummaryFiles, path)
		}
		return nil
	})
	if err != nil {
		return 0, nil, err
	}

	return totalBytes, nonSummaryFiles, nil
}

func (r *Runner) failTask(c *client.Client, taskID, msg string, payload []byte) error {
	payloadDigest := sha256.Sum256(payload)
	checksum := "sha256:" + hex.EncodeToString(payloadDigest[:])
	_ = c.FailComputeTask(r.cfg.WorkerID, taskID, msg, checksum, r.cfg.Token)
	return fmt.Errorf("%s", msg)
}

// uploadTaskArtifact uses a dedicated client session so each task artifact
// receives a unique file ID and cannot overwrite previous task uploads.
func (r *Runner) uploadTaskArtifact(path string) (string, error) {
	uploader, err := client.New(r.cfg.Host, r.cfg.Port)
	if err != nil {
		return "", fmt.Errorf("create artifact uploader: %w", err)
	}
	defer uploader.Close()

	fileIDs, err := uploader.SendFiles([]string{path}, 3600*24, 0, nil)
	if err != nil {
		return "", err
	}
	if len(fileIDs) == 0 || strings.TrimSpace(fileIDs[0]) == "" {
		return "", fmt.Errorf("artifact upload returned empty file id")
	}
	return fileIDs[0], nil
}

// lanternFileIDPattern matches known Lantern file ID formats.
// Supported examples:
// - Content-addressed IDs: "7aff719efccac0c2_0"
// - Web upload IDs:       "ws_1774975169059567000_0"
var lanternFileIDPattern = regexp.MustCompile(`^(?:[a-f0-9]{8,}_\d+|ws_\d+_\d+)$`)

// resolveFileInputs scans a JSON payload for string values that look like
// Lantern file IDs, downloads them via the HTTP API to inputDir, and rewrites
// the values to local file paths. This bridges uploaded files → executor.
func (r *Runner) resolveFileInputs(payload json.RawMessage, inputDir string) (json.RawMessage, error) {
	httpPort := r.cfg.HTTPPort
	if httpPort == 0 {
		httpPort = r.cfg.Port + 1 // convention: HTTP port = TCP port + 1
	}

	var generic map[string]any
	if err := json.Unmarshal(payload, &generic); err != nil {
		return payload, nil // not a JSON object — pass through
	}

	changed := false
	for key, val := range generic {
		switch v := val.(type) {
		case string:
			if lanternFileIDPattern.MatchString(v) {
				localPath, err := r.downloadFileHTTP(v, inputDir, httpPort)
				if err != nil {
					log.Printf("[worker] resolve %s=%s: download failed: %v", key, v, err)
					continue
				}
				generic[key] = localPath
				changed = true
			}
		case []any:
			newSlice := make([]any, len(v))
			copy(newSlice, v)
			for i, elem := range v {
				if s, ok := elem.(string); ok && lanternFileIDPattern.MatchString(s) {
					localPath, err := r.downloadFileHTTP(s, inputDir, httpPort)
					if err != nil {
						log.Printf("[worker] resolve %s[%d]=%s: download failed: %v", key, i, s, err)
						continue
					}
					newSlice[i] = localPath
					changed = true
				}
			}
			if changed {
				generic[key] = newSlice
			}
		}
	}

	if !changed {
		return payload, nil
	}

	out, err := json.Marshal(generic)
	if err != nil {
		return payload, fmt.Errorf("re-marshal payload: %w", err)
	}
	return out, nil
}

// downloadFileHTTP fetches a file from the Lantern HTTP API and saves it to destDir.
// Returns the local file path on success.
func (r *Runner) downloadFileHTTP(fileID, destDir string, httpPort int) (string, error) {
	url := fmt.Sprintf("http://%s:%d/api/files/%s", r.cfg.Host, httpPort, fileID)
	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}

	// Determine filename from Content-Disposition or fall back to fileID.
	filename := fileID
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if idx := strings.Index(cd, "filename="); idx >= 0 {
			name := cd[idx+len("filename="):]
			name = strings.Trim(name, "\"' ")
			if name != "" {
				filename = name
			}
		}
	}

	localPath := filepath.Join(destDir, filename)
	f, err := os.Create(localPath)
	if err != nil {
		return "", fmt.Errorf("create %s: %w", localPath, err)
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		return "", fmt.Errorf("write %s: %w", localPath, err)
	}

	log.Printf("[worker] downloaded file %s → %s", fileID, localPath)
	return localPath, nil
}
