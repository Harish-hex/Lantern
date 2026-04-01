package worker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDataProcessingExecutorExecuteCompiledPayload(t *testing.T) {
	sourceDir := t.TempDir()
	sourcePath := filepath.Join(sourceDir, "customers.csv")
	if err := os.WriteFile(sourcePath, []byte("alice,bob"), 0o644); err != nil {
		t.Fatalf("WriteFile(source): %v", err)
	}

	outputDir := filepath.Join(t.TempDir(), "output")
	executor := NewDataProcessingExecutor()

	payload := fmt.Sprintf(`{
		"template":"data_processing_batch",
		"dataset":%q,
		"transforms":["normalize","enrich"],
		"summary_report":true
	}`, sourcePath)

	err := executor.Execute(context.Background(), []byte(payload), outputDir)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	resultBytes, err := os.ReadFile(filepath.Join(outputDir, "customers.processed.csv"))
	if err != nil {
		t.Fatalf("ReadFile(result): %v", err)
	}
	result := string(resultBytes)
	if !strings.Contains(result, "ALICE,BOB") {
		t.Fatalf("processed result = %q, want normalized content", result)
	}
	if !strings.Contains(result, "[LANTERN: ENRICHED ROW]") {
		t.Fatalf("processed result = %q, want enrichment marker", result)
	}

	if _, err := os.Stat(filepath.Join(outputDir, "summary.json")); err != nil {
		t.Fatalf("summary.json missing: %v", err)
	}
}

func TestDataProcessingExecutorExecuteLegacyPayload(t *testing.T) {
	outputDir := filepath.Join(t.TempDir(), "output")
	executor := NewDataProcessingExecutor()

	err := executor.Execute(context.Background(), []byte(`{
		"task_index":1,
		"inputs":{"data_paths":"inline-source"},
		"settings":{"action":"normalize"}
	}`), outputDir)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	files, err := os.ReadDir(outputDir)
	if err != nil {
		t.Fatalf("ReadDir(output): %v", err)
	}
	if len(files) == 0 {
		t.Fatal("expected generated output files")
	}
}
