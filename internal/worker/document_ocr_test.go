package worker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Harish-hex/Lantern/internal/worker/toolchain"
)

func TestDocumentOCRExecutorWritesTextAndJSON(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses shell script fake binary")
	}

	tempDir := t.TempDir()
	fakeOCR := filepath.Join(tempDir, "tesseract")
	script := `#!/usr/bin/env bash
set -euo pipefail
input="$1"
out="$2"
shift 2 || true
if [[ "$out" == "stdout" ]]; then
  cat <<'TSV'
level	page_num	block_num	par_num	line_num	word_num	left	top	width	height	conf	text
5	1	1	1	1	1	0	0	10	10	95	Hello
5	1	1	1	1	2	0	0	10	10	90	OCR
TSV
  exit 0
fi
echo "Hello    OCR    world" > "${out}.txt"
`
	if err := os.WriteFile(fakeOCR, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake tesseract: %v", err)
	}

	mgr := toolchain.NewManager("", map[string]string{"tesseract": fakeOCR})
	exec := NewDocumentOCRExecutor(mgr)

	inputDoc := filepath.Join(tempDir, "sample.png")
	if err := os.WriteFile(inputDoc, []byte("fake image bytes"), 0o644); err != nil {
		t.Fatalf("write input doc: %v", err)
	}

	outputDir := filepath.Join(tempDir, "out")
	payload, _ := json.Marshal(map[string]any{
		"template":       "document_ocr_batch",
		"document":       inputDoc,
		"normalize_text": true,
		"emit_json":      true,
		"language":       "eng",
	})

	if err := exec.Execute(context.Background(), payload, outputDir); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	textOut := filepath.Join(outputDir, "sample.ocr.txt")
	textBytes, err := os.ReadFile(textOut)
	if err != nil {
		t.Fatalf("read OCR text output: %v", err)
	}
	text := string(textBytes)
	if strings.Contains(text, "    ") {
		t.Fatalf("expected normalized OCR text, got: %q", text)
	}
	if !strings.Contains(text, "Hello OCR world") {
		t.Fatalf("unexpected OCR text output: %q", text)
	}

	jsonOut := filepath.Join(outputDir, "sample.ocr.json")
	jsonBytes, err := os.ReadFile(jsonOut)
	if err != nil {
		t.Fatalf("read OCR json output: %v", err)
	}
	if !strings.Contains(string(jsonBytes), "\"token_count\": 2") {
		t.Fatalf("unexpected OCR json output: %s", string(jsonBytes))
	}
}
