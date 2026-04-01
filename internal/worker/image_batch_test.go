package worker

import (
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestImageBatchExecutorUpscaleOption(t *testing.T) {
	sourceDir := t.TempDir()
	sourcePath := filepath.Join(sourceDir, "small.png")
	writeSolidPNG(t, sourcePath, 100, 50)

	// Upscale disabled: width should remain unchanged.
	outputDirNoUpscale := filepath.Join(t.TempDir(), "no-upscale")
	executor := NewImageBatchExecutor()
	payloadNoUpscale := map[string]any{
		"template":     "image_batch_pipeline",
		"image":        sourcePath,
		"format":       "png",
		"resize_width": 200,
		"upscale":      false,
		"watermark":    false,
		"optimize":     true,
	}
	payloadNoUpscaleRaw, _ := json.Marshal(payloadNoUpscale)
	if err := executor.Execute(context.Background(), payloadNoUpscaleRaw, outputDirNoUpscale); err != nil {
		t.Fatalf("Execute(no upscale): %v", err)
	}
	noUpscaleOut := filepath.Join(outputDirNoUpscale, "small.processed.png")
	w, h := readImageSize(t, noUpscaleOut)
	if w != 100 || h != 50 {
		t.Fatalf("no-upscale output = %dx%d, want 100x50", w, h)
	}

	// Upscale enabled: width should resize to target while preserving aspect ratio.
	outputDirUpscale := filepath.Join(t.TempDir(), "upscale")
	payloadUpscale := map[string]any{
		"template":     "image_batch_pipeline",
		"image":        sourcePath,
		"format":       "png",
		"resize_width": 200,
		"upscale":      true,
		"watermark":    false,
		"optimize":     true,
	}
	payloadUpscaleRaw, _ := json.Marshal(payloadUpscale)
	if err := executor.Execute(context.Background(), payloadUpscaleRaw, outputDirUpscale); err != nil {
		t.Fatalf("Execute(upscale): %v", err)
	}
	upscaleOut := filepath.Join(outputDirUpscale, "small.processed.png")
	w, h = readImageSize(t, upscaleOut)
	if w != 200 || h != 100 {
		t.Fatalf("upscale output = %dx%d, want 200x100", w, h)
	}
}

func writeSolidPNG(t *testing.T, path string, width, height int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: 50, G: 120, B: 200, A: 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create(%s): %v", path, err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("png.Encode(%s): %v", path, err)
	}
}

func readImageSize(t *testing.T, path string) (int, int) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		t.Fatalf("Decode(%s): %v", path, err)
	}
	b := img.Bounds()
	return b.Dx(), b.Dy()
}
