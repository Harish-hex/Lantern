package worker

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/Harish-hex/Lantern/internal/client"
	"github.com/Harish-hex/Lantern/internal/protocol"
	"github.com/Harish-hex/Lantern/internal/worker/toolchain"
)

type RunnerConfig struct {
	Host             string
	Port             int
	WorkerID         string
	Token            string
	Heartbeat        time.Duration
	PollInterval     time.Duration
	OneShot          bool
	WorkspaceDir     string
	ToolchainManager *toolchain.Manager
	Registry         *Registry
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

	// TODO: Download inputs from server (if task specifies input files)
	// For now, our data_processing_batch payload often contains raw inline data or file paths

	log.Printf("[worker %s] executing task %s using %s", r.cfg.WorkerID, claim.TaskID, templateID)
	err := executor.Execute(ctx, claim.Payload, outputDir)
	if err != nil {
		return r.failTask(c, claim.TaskID, fmt.Sprintf("execution failed: %v", err), claim.Payload)
	}

	// 2. Archive results
	zipPath := filepath.Join(r.cfg.WorkspaceDir, claim.TaskID+".zip")
	if err := r.zipDir(outputDir, zipPath); err != nil {
		return r.failTask(c, claim.TaskID, fmt.Sprintf("failed to zip output: %v", err), claim.Payload)
	}
	defer os.Remove(zipPath) // Cleanup zip

	// 3. Upload Artifact
	var artifactID string
	fileIDs, err := c.SendFiles([]string{zipPath}, 3600*24, 0, nil) // 24h TTL
	if err == nil && len(fileIDs) > 0 {
		artifactID = fileIDs[0]
	} else {
		log.Printf("[worker %s] warning: failed to upload task artifact: %v", r.cfg.WorkerID, err)
	}

	payloadDigest := sha256.Sum256(claim.Payload)
	checksum := "sha256:" + hex.EncodeToString(payloadDigest[:])

	if err := c.CompleteComputeTask(r.cfg.WorkerID, claim.TaskID, artifactID, checksum, r.cfg.Token); err != nil {
		return r.failTask(c, claim.TaskID, fmt.Sprintf("complete task API call: %v", err), claim.Payload)
	}
	return nil
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

func (r *Runner) failTask(c *client.Client, taskID, msg string, payload []byte) error {
	payloadDigest := sha256.Sum256(payload)
	checksum := "sha256:" + hex.EncodeToString(payloadDigest[:])
	_ = c.FailComputeTask(r.cfg.WorkerID, taskID, msg, checksum, r.cfg.Token)
	return fmt.Errorf("%s", msg)
}
