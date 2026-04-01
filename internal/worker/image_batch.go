package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	imgdraw "image/draw"
	_ "image/gif" // register gif decoder
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/image/draw"
)

// maxImageDimension is the largest output dimension we allow to prevent memory bombs.
const maxImageDimension = 16384

// maxImageMemoryBytes is the maximum decoded frame size we'll allocate (256MB).
const maxImageMemoryBytes = 256 * 1024 * 1024

// ImageBatchExecutor processes a batch of images: resize, format conversion, and watermark.
type ImageBatchExecutor struct{}

func NewImageBatchExecutor() *ImageBatchExecutor {
	return &ImageBatchExecutor{}
}

func (e *ImageBatchExecutor) ID() string {
	return "image_batch_pipeline"
}

type imageBatchPayload struct {
	Template    string   `json:"template"`
	Images      []string `json:"images"`
	Image       string   `json:"image"` // singular — sent by per-image compiled tasks
	Format      string   `json:"format"`
	ResizeWidth int      `json:"resize_width"`
	Upscale     bool     `json:"upscale"`
	Watermark   bool     `json:"watermark"`
	Optimize    bool     `json:"optimize"`
}

type imageBatchSummary struct {
	Template  string                  `json:"template"`
	Processed int                     `json:"processed"`
	Failed    int                     `json:"failed"`
	Outputs   []imageBatchOutputEntry `json:"outputs"`
}

type imageBatchOutputEntry struct {
	Source     string `json:"source"`
	Output     string `json:"output"`
	InputSize  int64  `json:"input_bytes"`
	OutputSize int64  `json:"output_bytes"`
	Error      string `json:"error,omitempty"`
}

func (e *ImageBatchExecutor) Execute(ctx context.Context, payload json.RawMessage, outputDir string) error {
	return e.ExecuteWithLog(ctx, payload, outputDir, nil)
}

// ExecuteWithLog implements LoggableExecutor so the runner passes a log writer.
func (e *ImageBatchExecutor) ExecuteWithLog(ctx context.Context, payload json.RawMessage, outputDir string, lw *TaskLogWriter) error {
	cfg, err := parseImageBatchPayload(payload)
	if err != nil {
		return err
	}
	if len(cfg.Images) == 0 {
		return fmt.Errorf("image_batch_pipeline: no images specified")
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	logf := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		if lw != nil {
			lw.writeLine(msg)
		}
	}

	format := strings.ToLower(strings.TrimSpace(cfg.Format))
	if format == "" {
		format = "png"
	}
	// WebP encoding is not available in stdlib; fall back to PNG.
	if format == "webp" || format == "avif" {
		logf("[image] note: %s encoding not available without native libs — using png instead", format)
		format = "png"
	}

	summary := imageBatchSummary{
		Template: e.ID(),
		Outputs:  make([]imageBatchOutputEntry, 0, len(cfg.Images)),
	}

	for _, srcPath := range cfg.Images {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		entry := imageBatchOutputEntry{Source: srcPath}

		outName, outPath, processErr := e.processImage(ctx, srcPath, outputDir, cfg, format, logf)
		if processErr != nil {
			logf("[image] ❌ %s: %v", filepath.Base(srcPath), processErr)
			entry.Error = processErr.Error()
			summary.Failed++
		} else {
			entry.Output = outName
			if info, err := os.Stat(srcPath); err == nil {
				entry.InputSize = info.Size()
			}
			if info, err := os.Stat(outPath); err == nil {
				entry.OutputSize = info.Size()
			}
			logf("[image] ✅ %s → %s (%d bytes)", filepath.Base(srcPath), outName, entry.OutputSize)
			summary.Processed++
		}
		summary.Outputs = append(summary.Outputs, entry)
	}

	// Write summary.json
	summaryBytes, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal summary: %w", err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "summary.json"), summaryBytes, 0o644); err != nil {
		return fmt.Errorf("write summary: %w", err)
	}

	logf("[image] batch complete — %d processed, %d failed", summary.Processed, summary.Failed)
	return nil
}

func (e *ImageBatchExecutor) processImage(
	ctx context.Context,
	srcPath, outputDir string,
	cfg imageBatchPayload,
	format string,
	logf func(string, ...any),
) (outName, outPath string, err error) {
	// 1. Open and decode
	f, err := os.Open(srcPath)
	if err != nil {
		return "", "", fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	src, _, err := image.Decode(f)
	if err != nil {
		return "", "", fmt.Errorf("decode: %w", err)
	}

	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()

	// 2. Guard against memory bombs
	if int64(w)*int64(h)*4 > maxImageMemoryBytes {
		return "", "", fmt.Errorf("image %dx%d exceeds memory limit (%dMB)", w, h, maxImageMemoryBytes/(1024*1024))
	}

	// 3. Resize if needed. By default we only downscale; setting Upscale=true
	// also allows enlarging smaller inputs to the requested target width.
	shouldResize := cfg.ResizeWidth > 0 && (w > cfg.ResizeWidth || (cfg.Upscale && w < cfg.ResizeWidth))
	if shouldResize {
		newW := cfg.ResizeWidth
		newH := int(float64(h) * float64(newW) / float64(w))

		// Guard output dimensions
		if newW > maxImageDimension || newH > maxImageDimension {
			return "", "", fmt.Errorf("output dimensions %dx%d exceed max %d", newW, newH, maxImageDimension)
		}

		dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
		draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
		src = dst
		logf("[image] resized %s: %dx%d → %dx%d", filepath.Base(srcPath), w, h, newW, newH)
	}

	// 4. Apply watermark if requested
	if cfg.Watermark {
		src = applyWatermark(src)
	}

	// 5. Build output filename
	base := filepath.Base(srcPath)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	outName = fmt.Sprintf("%s.processed.%s", name, format)
	outPath = filepath.Join(outputDir, outName)

	// 6. Encode to target format
	out, err := os.Create(outPath)
	if err != nil {
		return "", "", fmt.Errorf("create output: %w", err)
	}
	defer out.Close()

	switch format {
	case "jpg", "jpeg":
		quality := 85
		if cfg.Optimize {
			quality = 75
		}
		if err := jpeg.Encode(out, src, &jpeg.Options{Quality: quality}); err != nil {
			return "", "", fmt.Errorf("jpeg encode: %w", err)
		}
	default: // png
		enc := &png.Encoder{CompressionLevel: png.DefaultCompression}
		if cfg.Optimize {
			enc.CompressionLevel = png.BestCompression
		}
		if err := enc.Encode(out, src); err != nil {
			return "", "", fmt.Errorf("png encode: %w", err)
		}
	}

	return outName, outPath, nil
}

// applyWatermark draws a semi-transparent dark strip at the bottom of the image.
// Uses stdlib image/draw (imgdraw) compositing with a uniform color fill.
func applyWatermark(src image.Image) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(b)
	// Copy source into dst using x/image draw for high quality.
	draw.Draw(dst, b, src, b.Min, draw.Src)

	// Draw a dark semi-transparent strip at the bottom (20px tall).
	stripH := 20
	if b.Dy() < 40 {
		return dst // image too small for watermark
	}
	strip := image.Rect(b.Min.X, b.Max.Y-stripH, b.Max.X, b.Max.Y)
	// Use stdlib image/draw (aliased as imgdraw) to draw a uniform semi-transparent color.
	overlay := image.NewUniform(color.NRGBA{R: 0, G: 0, B: 0, A: 153}) // ~60% black
	imgdraw.Draw(dst, strip, overlay, image.Point{}, imgdraw.Over)

	return dst
}

// parseImageBatchPayload handles the compiled template payload format.
// Supports both {"images": [...]} (batch) and {"image": "..."} (per-task compiled).
func parseImageBatchPayload(payload json.RawMessage) (imageBatchPayload, error) {
	var cfg imageBatchPayload
	if err := json.Unmarshal(payload, &cfg); err != nil {
		return cfg, fmt.Errorf("parse image_batch_pipeline payload: %w", err)
	}

	// Merge singular "image" into "images" array (compile creates per-task payloads with singular key).
	if s := strings.TrimSpace(cfg.Image); s != "" {
		cfg.Images = append(cfg.Images, s)
	}

	// Trim whitespace from image paths
	cleaned := make([]string, 0, len(cfg.Images))
	for _, img := range cfg.Images {
		if s := strings.TrimSpace(img); s != "" {
			cleaned = append(cleaned, s)
		}
	}
	cfg.Images = cleaned

	return cfg, nil
}
