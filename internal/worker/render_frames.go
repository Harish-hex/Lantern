package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Harish-hex/Lantern/internal/worker/toolchain"
)

type RenderFramesExecutor struct {
	toolchainManager *toolchain.Manager
}

func NewRenderFramesExecutor(manager *toolchain.Manager) *RenderFramesExecutor {
	return &RenderFramesExecutor{toolchainManager: manager}
}

func (e *RenderFramesExecutor) ID() string {
	return "render_frames"
}

type renderFramesPayload struct {
	Template     string `json:"template"`
	SceneFile    string `json:"scene_file"`
	FrameStart   int    `json:"frame_start"`
	FrameEnd     int    `json:"frame_end"`
	OutputFormat string `json:"output_format"`
	RenderDevice string `json:"render_device"`
	VideoCodec   string `json:"video_codec"`
}

func (e *RenderFramesExecutor) Execute(ctx context.Context, payload json.RawMessage, outputDir string) error {
	cfg, err := parseRenderFramesPayload(payload)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	blenderPath, err := e.resolveBlenderBinary()
	if err != nil {
		return fmt.Errorf("render_frames requires Blender on worker: %w", err)
	}

	formatArg := strings.ToUpper(cfg.OutputFormat)
	if formatArg == "JPG" {
		formatArg = "JPEG"
	}
	outputPattern := filepath.Join(outputDir, "frame_######")

	cmd := exec.CommandContext(
		ctx,
		blenderPath,
		"-b", cfg.SceneFile,
		"-s", fmt.Sprintf("%d", cfg.FrameStart),
		"-e", fmt.Sprintf("%d", cfg.FrameEnd),
		"-o", outputPattern,
		"-F", formatArg,
		"-a",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("blender render failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	summary := map[string]any{
		"template":      e.ID(),
		"scene_file":    cfg.SceneFile,
		"frame_start":   cfg.FrameStart,
		"frame_end":     cfg.FrameEnd,
		"output_format": cfg.OutputFormat,
		"render_device": cfg.RenderDevice,
		"video_codec":   cfg.VideoCodec,
	}
	data, _ := json.MarshalIndent(summary, "", "  ")
	_ = os.WriteFile(filepath.Join(outputDir, "summary.json"), data, 0o644)
	return nil
}

func parseRenderFramesPayload(raw json.RawMessage) (renderFramesPayload, error) {
	cfg := renderFramesPayload{
		OutputFormat: "png",
		RenderDevice: "cpu",
		VideoCodec:   "libx264",
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("parse render_frames payload: %w", err)
	}
	cfg.SceneFile = strings.TrimSpace(cfg.SceneFile)
	if cfg.SceneFile == "" {
		return cfg, fmt.Errorf("render_frames payload missing scene_file")
	}
	if cfg.FrameStart <= 0 || cfg.FrameEnd <= 0 || cfg.FrameEnd < cfg.FrameStart {
		return cfg, fmt.Errorf("render_frames payload has invalid frame range")
	}
	cfg.OutputFormat = strings.ToLower(strings.TrimSpace(cfg.OutputFormat))
	switch cfg.OutputFormat {
	case "png", "jpg", "jpeg", "exr":
	default:
		return cfg, fmt.Errorf("render_frames output_format %q not supported", cfg.OutputFormat)
	}
	cfg.RenderDevice = strings.ToLower(strings.TrimSpace(cfg.RenderDevice))
	if cfg.RenderDevice == "" {
		cfg.RenderDevice = "cpu"
	}
	if cfg.RenderDevice != "cpu" && cfg.RenderDevice != "gpu" {
		cfg.RenderDevice = "cpu"
	}
	if strings.TrimSpace(cfg.VideoCodec) == "" {
		cfg.VideoCodec = "libx264"
	}
	return cfg, nil
}

func (e *RenderFramesExecutor) resolveBlenderBinary() (string, error) {
	if e.toolchainManager != nil {
		if path, err := e.toolchainManager.Probe("blender"); err == nil {
			return path, nil
		}
	}
	path, err := exec.LookPath("blender")
	if err != nil {
		return "", fmt.Errorf("blender not found on PATH")
	}
	return path, nil
}
