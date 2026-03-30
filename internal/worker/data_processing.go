package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type DataProcessingExecutor struct{}

func NewDataProcessingExecutor() *DataProcessingExecutor {
	return &DataProcessingExecutor{}
}

func (e *DataProcessingExecutor) ID() string {
	return "data_processing_batch"
}

// DataProcessingPayload represents the expected structure from the coordinator.
type DataProcessingPayload struct {
	TaskIndex int `json:"task_index"`
	Inputs    struct {
		DataPaths string `json:"data_paths"`
	} `json:"inputs"`
	Settings struct {
		Action    string `json:"action"`
		OutputExt string `json:"output_ext"`
		Delim     string `json:"delim"`
	} `json:"settings"`
}

func (e *DataProcessingExecutor) Execute(ctx context.Context, payload json.RawMessage, outputDir string) error {
	var taskPayload DataProcessingPayload
	if err := json.Unmarshal(payload, &taskPayload); err != nil {
		return fmt.Errorf("failed to parse data processing payload: %w", err)
	}

	paths := strings.Split(taskPayload.Inputs.DataPaths, "\n")
	
	// Create output dir if missing
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory %q: %w", outputDir, err)
	}

	for i, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}

		// Due to early testing without full file integration, 
		// if the user types a raw file path that doesn't exist, we fallback to just treating the string as data iteslf.
		content, err := os.ReadFile(path)
		if err != nil {
			content = []byte(fmt.Sprintf("-- Data received from Coordinator --\nPath/Data string provided: %s\n(Local file read error: %v)\n", path, err))
		}

		result := string(content)
		if taskPayload.Settings.Action == "normalize" {
			result = strings.ToUpper(result)
		} else if taskPayload.Settings.Action == "enrich" {
			result = result + "\n[LANTERN: ENRICHED ROW]"
		}

		ext := taskPayload.Settings.OutputExt
		if ext == "" {
			ext = "txt"
		}
		// Strip dot if user provided one
		ext = strings.TrimPrefix(ext, ".")

		outName := fmt.Sprintf("task_%d_item_%d_output.%s", taskPayload.TaskIndex, i, ext)
		outPath := filepath.Join(outputDir, outName)

		if err := os.WriteFile(outPath, []byte(result), 0644); err != nil {
			return fmt.Errorf("write output file %s: %w", outName, err)
		}
	}

	return nil
}
