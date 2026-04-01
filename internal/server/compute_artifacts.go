package server

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Harish-hex/Lantern/internal/protocol"
)

type computeArtifactManifest struct {
	JobID        string                `json:"job_id"`
	TemplateID   string                `json:"template_id,omitempty"`
	TemplateName string                `json:"template_name,omitempty"`
	OutputKind   string                `json:"output_kind,omitempty"`
	CompletedAt  time.Time             `json:"completed_at"`
	TaskCount    int                   `json:"task_count"`
	Tasks        []computeTaskManifest `json:"tasks"`
	Artifacts    []computeFileManifest `json:"artifacts"`
}

type computeTaskManifest struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Attempt  int    `json:"attempt"`
	Checksum string `json:"checksum,omitempty"`
	WorkerID string `json:"worker_id,omitempty"`
}

type computeFileManifest struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Size     int64  `json:"size"`
	Checksum string `json:"checksum,omitempty"`
}

func (s *Server) assembleComputeArtifacts(job *ComputeJob, tasks []*ComputeTask, now time.Time) ([]ComputeArtifact, error) {
	if s == nil || s.storage == nil || job == nil || len(job.Artifacts) == 0 {
		return nil, nil
	}

	sourceFiles := make([]*StoredFile, 0, len(job.Artifacts))
	manifestFiles := make([]computeFileManifest, 0, len(job.Artifacts))
	for _, artifact := range job.Artifacts {
		sf := s.storage.GetFile(artifact.ID)
		if sf == nil {
			return nil, fmt.Errorf("artifact %s is missing from storage", artifact.ID)
		}
		sourceFiles = append(sourceFiles, sf)
		manifestFiles = append(manifestFiles, computeFileManifest{
			ID:       sf.ID,
			Name:     sf.Metadata.Filename,
			Kind:     artifact.Kind,
			Size:     sf.Metadata.Size,
			Checksum: sf.Metadata.ChecksumFull,
		})
	}

	manifestTasks := make([]computeTaskManifest, 0, len(tasks))
	for _, task := range tasks {
		if task == nil {
			continue
		}
		manifestTasks = append(manifestTasks, computeTaskManifest{
			ID:       task.ID,
			Status:   task.Status,
			Attempt:  task.Attempt,
			Checksum: task.Checksum,
			WorkerID: task.LastWorkerID,
		})
	}

	manifest := computeArtifactManifest{
		JobID:        job.ID,
		TemplateID:   job.TemplateID,
		TemplateName: job.TemplateName,
		OutputKind:   job.OutputKind,
		CompletedAt:  now,
		TaskCount:    len(manifestTasks),
		Tasks:        manifestTasks,
		Artifacts:    manifestFiles,
	}

	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal compute manifest: %w", err)
	}

	tempPath := filepath.Join(s.cfg.TempDir, fmt.Sprintf("%s-final-package.zip.tmp", job.ID))
	if err := os.MkdirAll(filepath.Dir(tempPath), 0o755); err != nil {
		return nil, fmt.Errorf("create artifact temp dir: %w", err)
	}
	if err := writeComputeArtifactPackage(tempPath, manifestBytes, sourceFiles); err != nil {
		return nil, err
	}

	meta, err := generatedFileMetadata(tempPath, computeFinalPackageName(job), "application/zip", s.cfg.ChunkSize)
	if err != nil {
		_ = os.Remove(tempPath)
		return nil, err
	}

	ttlSeconds := s.cfg.TTLDefault
	if ttlSeconds < 24*3600 {
		ttlSeconds = 24 * 3600
	}

	fileID := fmt.Sprintf("%s_final_package", job.ID)
	sf, err := s.storage.MoveToStorage(tempPath, fileID, meta, ttlSeconds, 0)
	if err != nil {
		_ = os.Remove(tempPath)
		return nil, fmt.Errorf("store final compute artifact: %w", err)
	}

	// Final package is the canonical retained artifact for completed jobs.
	// Remove per-task source artifacts to keep the generic file space uncluttered.
	for _, source := range sourceFiles {
		if source == nil || source.ID == "" || source.ID == sf.ID {
			continue
		}
		s.storage.DeleteFile(source.ID)
	}

	return []ComputeArtifact{
		{
			ID:        sf.ID,
			Name:      sf.Metadata.Filename,
			Kind:      "final_package",
			SizeBytes: sf.Metadata.Size,
			CreatedAt: now,
			Summary:   manifestBytes,
		},
	}, nil
}

func writeComputeArtifactPackage(target string, manifest []byte, sourceFiles []*StoredFile) error {
	file, err := os.Create(target)
	if err != nil {
		return fmt.Errorf("create final artifact package: %w", err)
	}
	defer file.Close()

	archive := zip.NewWriter(file)
	defer archive.Close()

	writer, err := archive.Create("manifest.json")
	if err != nil {
		return fmt.Errorf("create manifest.json: %w", err)
	}
	if _, err := writer.Write(manifest); err != nil {
		return fmt.Errorf("write manifest.json: %w", err)
	}

	for _, sf := range sourceFiles {
		if sf == nil {
			continue
		}
		if err := addStoredFileToZip(archive, "task_artifacts/"+sf.Metadata.Filename, sf.Path); err != nil {
			return err
		}
	}

	return nil
}

func addStoredFileToZip(archive *zip.Writer, name, sourcePath string) error {
	reader, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open artifact source %s: %w", sourcePath, err)
	}
	defer reader.Close()

	writer, err := archive.Create(filepath.ToSlash(name))
	if err != nil {
		return fmt.Errorf("create zip entry %s: %w", name, err)
	}
	if _, err := io.Copy(writer, reader); err != nil {
		return fmt.Errorf("copy artifact %s into zip: %w", name, err)
	}
	return nil
}

func computeFinalPackageName(job *ComputeJob) string {
	base := strings.TrimSpace(job.TemplateID)
	if base == "" {
		base = strings.TrimSpace(job.Type)
	}
	if base == "" {
		base = "compute-job"
	}
	base = strings.ReplaceAll(base, " ", "-")
	return fmt.Sprintf("%s-%s-package.zip", job.ID, base)
}

func generatedFileMetadata(path, filename, mimeType string, chunkSize uint32) (protocol.FileMetadata, error) {
	file, err := os.Open(path)
	if err != nil {
		return protocol.FileMetadata{}, fmt.Errorf("open generated artifact %s: %w", path, err)
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return protocol.FileMetadata{}, fmt.Errorf("stat generated artifact %s: %w", path, err)
	}

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return protocol.FileMetadata{}, fmt.Errorf("hash generated artifact %s: %w", path, err)
	}

	if chunkSize == 0 {
		chunkSize = 256 * 1024
	}
	totalChunks := uint32(stat.Size() / int64(chunkSize))
	if stat.Size()%int64(chunkSize) != 0 {
		totalChunks++
	}
	if totalChunks == 0 {
		totalChunks = 1
	}

	return protocol.FileMetadata{
		Filename:     filename,
		Size:         stat.Size(),
		MimeType:     mimeType,
		ChecksumFull: "sha256:" + hex.EncodeToString(hasher.Sum(nil)),
		ChunkSize:    chunkSize,
		TotalChunks:  totalChunks,
		FileIndex:    0,
		TotalFiles:   1,
	}, nil
}
