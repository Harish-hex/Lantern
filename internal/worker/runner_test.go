package worker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLanternFileIDPatternMatchesKnownFormats(t *testing.T) {
	tests := []struct {
		name  string
		id    string
		match bool
	}{
		{name: "content addressed", id: "7aff719efccac0c2_0", match: true},
		{name: "web upload", id: "ws_1774975169059567000_0", match: true},
		{name: "plain filename", id: "photo.png", match: false},
		{name: "relative path", id: "./input/photo.png", match: false},
		{name: "missing suffix", id: "ws_1774975169059567000", match: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := lanternFileIDPattern.MatchString(tc.id)
			if got != tc.match {
				t.Fatalf("MatchString(%q) = %v, want %v", tc.id, got, tc.match)
			}
		})
	}
}

func TestSelectTaskArtifactUploadPathUsesDirectFileForSmallSingleOutput(t *testing.T) {
	workspace := t.TempDir()
	outputDir := filepath.Join(workspace, "out")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(outputDir): %v", err)
	}

	imgPath := filepath.Join(outputDir, "result.processed.png")
	if err := os.WriteFile(imgPath, []byte("small-image"), 0o644); err != nil {
		t.Fatalf("WriteFile(image): %v", err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "summary.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("WriteFile(summary): %v", err)
	}

	r := NewRunner(RunnerConfig{
		WorkspaceDir:          workspace,
		TaskZipThresholdBytes: 1024,
	})

	artifactPath, usedZip, err := r.selectTaskArtifactUploadPath(outputDir, "task-1")
	if err != nil {
		t.Fatalf("selectTaskArtifactUploadPath: %v", err)
	}
	if usedZip {
		t.Fatal("expected direct artifact upload, got zip")
	}
	if artifactPath != imgPath {
		t.Fatalf("artifactPath = %q, want %q", artifactPath, imgPath)
	}
}

func TestSelectTaskArtifactUploadPathUsesZipForMultiFileOutput(t *testing.T) {
	workspace := t.TempDir()
	outputDir := filepath.Join(workspace, "out")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(outputDir): %v", err)
	}

	if err := os.WriteFile(filepath.Join(outputDir, "a.processed.png"), []byte("a"), 0o644); err != nil {
		t.Fatalf("WriteFile(a): %v", err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "b.processed.png"), []byte("b"), 0o644); err != nil {
		t.Fatalf("WriteFile(b): %v", err)
	}

	r := NewRunner(RunnerConfig{
		WorkspaceDir:          workspace,
		TaskZipThresholdBytes: 1024,
	})

	artifactPath, usedZip, err := r.selectTaskArtifactUploadPath(outputDir, "task-2")
	if err != nil {
		t.Fatalf("selectTaskArtifactUploadPath: %v", err)
	}
	if !usedZip {
		t.Fatal("expected zip artifact for multi-file output")
	}
	if filepath.Ext(artifactPath) != ".zip" {
		t.Fatalf("artifactPath = %q, want .zip", artifactPath)
	}
}
