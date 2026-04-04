package server

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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

	if job.TemplateID == "render_frames" {
		if err := s.writeRenderFramesArtifactPackage(tempPath, manifestBytes, sourceFiles, job); err != nil {
			return nil, err
		}
	} else {
		if err := writeComputeArtifactPackage(tempPath, manifestBytes, sourceFiles); err != nil {
			return nil, err
		}
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

func (s *Server) writeRenderFramesArtifactPackage(target string, manifest []byte, sourceFiles []*StoredFile, job *ComputeJob) error {
	inputs := renderFramesInputs{FrameStart: 1, FrameEnd: 1}
	settings := renderFramesSettings{OutputFormat: "png", Fps: 24, VideoCodec: "libx264", StitchedOutput: true}
	if err := decodeOptionalJSON(job.Inputs, &inputs); err != nil {
		return fmt.Errorf("decode render_frames inputs: %w", err)
	}
	if err := decodeOptionalJSON(job.Settings, &settings); err != nil {
		return fmt.Errorf("decode render_frames settings: %w", err)
	}
	if inputs.FrameEnd < inputs.FrameStart {
		return fmt.Errorf("render assembly invalid frame range")
	}

	workspace := filepath.Join(s.cfg.TempDir, fmt.Sprintf("%s-render-assembly", job.ID))
	framesDir := filepath.Join(workspace, "frames")
	finalDir := filepath.Join(workspace, "final")
	if err := os.RemoveAll(workspace); err != nil {
		return fmt.Errorf("clean render assembly workspace: %w", err)
	}
	defer os.RemoveAll(workspace)
	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		return fmt.Errorf("create render frames workspace: %w", err)
	}
	if err := os.MkdirAll(finalDir, 0o755); err != nil {
		return fmt.Errorf("create render final workspace: %w", err)
	}

	seenFrames := make(map[int]string)
	for _, sf := range sourceFiles {
		if sf == nil {
			continue
		}
		if err := unpackRenderArtifact(sf.Path, framesDir, seenFrames); err != nil {
			return err
		}
	}

	for frame := inputs.FrameStart; frame <= inputs.FrameEnd; frame++ {
		if _, ok := seenFrames[frame]; !ok {
			return fmt.Errorf("render assembly missing frame %d", frame)
		}
	}

	videoPath := ""
	if settings.StitchedOutput {
		ffmpegPath, err := exec.LookPath("ffmpeg")
		if err != nil {
			return fmt.Errorf("render assembly requires ffmpeg for stitched output")
		}
		ext := normalizedFrameExtension(settings.OutputFormat)
		inputPattern := filepath.Join(framesDir, fmt.Sprintf("frame_%%06d%s", ext))
		videoPath = filepath.Join(finalDir, "render.mp4")
		fps := settings.Fps
		if fps <= 0 {
			fps = 24
		}
		codec := strings.TrimSpace(settings.VideoCodec)
		if codec == "" {
			codec = "libx264"
		}
		cmd := exec.Command(ffmpegPath,
			"-y",
			"-framerate", strconv.Itoa(fps),
			"-i", inputPattern,
			"-c:v", codec,
			"-pix_fmt", "yuv420p",
			videoPath,
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("ffmpeg stitch failed: %w (%s)", err, strings.TrimSpace(string(out)))
		}
	}

	archiveFile, err := os.Create(target)
	if err != nil {
		return fmt.Errorf("create final artifact package: %w", err)
	}
	defer archiveFile.Close()

	archive := zip.NewWriter(archiveFile)
	defer archive.Close()

	writer, err := archive.Create("manifest.json")
	if err != nil {
		return fmt.Errorf("create manifest.json: %w", err)
	}
	if _, err := writer.Write(manifest); err != nil {
		return fmt.Errorf("write manifest.json: %w", err)
	}

	frames := make([]int, 0, len(seenFrames))
	for frame := range seenFrames {
		frames = append(frames, frame)
	}
	sort.Ints(frames)
	for _, frame := range frames {
		framePath := seenFrames[frame]
		ext := filepath.Ext(framePath)
		entryName := fmt.Sprintf("frames/frame_%06d%s", frame, ext)
		if err := addStoredFileToZip(archive, entryName, framePath); err != nil {
			return err
		}
	}
	if strings.TrimSpace(videoPath) != "" {
		if err := addStoredFileToZip(archive, "final/render.mp4", videoPath); err != nil {
			return err
		}
	}

	return nil
}

var frameNumberPattern = regexp.MustCompile(`(\d+)`)

func unpackRenderArtifact(sourcePath, framesDir string, seenFrames map[int]string) error {
	if strings.EqualFold(filepath.Ext(sourcePath), ".zip") {
		zr, err := zip.OpenReader(sourcePath)
		if err != nil {
			return fmt.Errorf("open task artifact zip: %w", err)
		}
		defer zr.Close()
		for _, file := range zr.File {
			if file.FileInfo().IsDir() {
				continue
			}
			if !isRenderFrameFile(file.Name) {
				continue
			}
			frame, ok := parseFrameNumberFromName(file.Name)
			if !ok {
				continue
			}
			if _, exists := seenFrames[frame]; exists {
				return fmt.Errorf("render assembly duplicate frame %d", frame)
			}
			ext := normalizedFrameExtension(filepath.Ext(file.Name))
			targetPath := filepath.Join(framesDir, fmt.Sprintf("frame_%06d%s", frame, ext))
			rc, err := file.Open()
			if err != nil {
				return fmt.Errorf("open zip entry %s: %w", file.Name, err)
			}
			out, err := os.Create(targetPath)
			if err != nil {
				rc.Close()
				return fmt.Errorf("create frame %s: %w", targetPath, err)
			}
			_, copyErr := io.Copy(out, rc)
			rc.Close()
			closeErr := out.Close()
			if copyErr != nil {
				return fmt.Errorf("extract frame %s: %w", file.Name, copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close frame %s: %w", targetPath, closeErr)
			}
			seenFrames[frame] = targetPath
		}
		return nil
	}

	if !isRenderFrameFile(sourcePath) {
		return nil
	}
	frame, ok := parseFrameNumberFromName(sourcePath)
	if !ok {
		return nil
	}
	if _, exists := seenFrames[frame]; exists {
		return fmt.Errorf("render assembly duplicate frame %d", frame)
	}
	ext := normalizedFrameExtension(filepath.Ext(sourcePath))
	targetPath := filepath.Join(framesDir, fmt.Sprintf("frame_%06d%s", frame, ext))
	if err := copyFile(sourcePath, targetPath); err != nil {
		return err
	}
	seenFrames[frame] = targetPath
	return nil
}

func isRenderFrameFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".exr":
		return true
	default:
		return false
	}
}

func parseFrameNumberFromName(name string) (int, bool) {
	matches := frameNumberPattern.FindAllString(filepath.Base(name), -1)
	if len(matches) == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(matches[len(matches)-1])
	if err != nil {
		return 0, false
	}
	return n, true
}

func normalizedFrameExtension(raw string) string {
	ext := strings.ToLower(strings.TrimSpace(raw))
	if ext == "" {
		return ".png"
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	if ext == ".jpeg" {
		return ".jpg"
	}
	return ext
}

func copyFile(sourcePath, targetPath string) error {
	src, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open %s: %w", sourcePath, err)
	}
	defer src.Close()
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return fmt.Errorf("mkdir for %s: %w", targetPath, err)
	}
	dst, err := os.Create(targetPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", targetPath, err)
	}
	defer dst.Close()
	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("copy %s -> %s: %w", sourcePath, targetPath, err)
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
