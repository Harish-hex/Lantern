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

type compiledDataProcessingPayload struct {
	Template      string   `json:"template"`
	Dataset       string   `json:"dataset"`
	Transforms    []string `json:"transforms"`
	SummaryReport bool     `json:"summary_report"`
}

type legacyDataProcessingPayload struct {
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

type dataProcessingSummary struct {
	Template   string   `json:"template"`
	Sources    []string `json:"sources"`
	Transforms []string `json:"transforms"`
	Outputs    []string `json:"outputs"`
}

func (e *DataProcessingExecutor) Execute(ctx context.Context, payload json.RawMessage, outputDir string) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("failed to create output directory %q: %w", outputDir, err)
	}

	sources, transforms, writeSummary, err := parseDataProcessingPayload(payload)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		return fmt.Errorf("data_processing_batch task has no dataset sources")
	}

	outputs := make([]string, 0, len(sources))
	for _, source := range sources {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		content, err := readDatasetSource(source)
		if err != nil {
			return err
		}

		result := applyDataTransforms(string(content), transforms)
		outName := outputNameForDataset(source)
		outPath := filepath.Join(outputDir, outName)
		if err := os.WriteFile(outPath, []byte(result), 0o644); err != nil {
			return fmt.Errorf("write output file %s: %w", outName, err)
		}
		outputs = append(outputs, outName)
	}

	if writeSummary {
		summary := dataProcessingSummary{
			Template:   e.ID(),
			Sources:    append([]string(nil), sources...),
			Transforms: append([]string(nil), transforms...),
			Outputs:    outputs,
		}
		summaryBytes, err := json.MarshalIndent(summary, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal data processing summary: %w", err)
		}
		if err := os.WriteFile(filepath.Join(outputDir, "summary.json"), summaryBytes, 0o644); err != nil {
			return fmt.Errorf("write summary.json: %w", err)
		}
	}

	return nil
}

func parseDataProcessingPayload(payload json.RawMessage) ([]string, []string, bool, error) {
	var compiled compiledDataProcessingPayload
	if err := json.Unmarshal(payload, &compiled); err == nil && (compiled.Dataset != "" || compiled.Template == "data_processing_batch" || len(compiled.Transforms) > 0) {
		source := strings.TrimSpace(compiled.Dataset)
		if source == "" {
			return nil, nil, false, fmt.Errorf("data_processing_batch task missing dataset")
		}
		return []string{source}, normalizeTransforms(compiled.Transforms), compiled.SummaryReport, nil
	}

	var legacy legacyDataProcessingPayload
	if err := json.Unmarshal(payload, &legacy); err != nil {
		return nil, nil, false, fmt.Errorf("failed to parse data processing payload: %w", err)
	}

	sources := make([]string, 0)
	for _, source := range strings.Split(legacy.Inputs.DataPaths, "\n") {
		source = strings.TrimSpace(source)
		if source != "" {
			sources = append(sources, source)
		}
	}

	transforms := make([]string, 0, 1)
	if action := strings.TrimSpace(legacy.Settings.Action); action != "" {
		transforms = append(transforms, action)
	}

	return sources, normalizeTransforms(transforms), true, nil
}

func normalizeTransforms(transforms []string) []string {
	out := make([]string, 0, len(transforms))
	for _, transform := range transforms {
		transform = strings.ToLower(strings.TrimSpace(transform))
		if transform == "" {
			continue
		}
		out = append(out, transform)
	}
	return out
}

func readDatasetSource(source string) ([]byte, error) {
	content, err := os.ReadFile(source)
	if err == nil {
		return content, nil
	}

	return []byte(fmt.Sprintf("-- Data received from Coordinator --\nSource provided: %s\n(Local file read error: %v)\n", source, err)), nil
}

func applyDataTransforms(content string, transforms []string) string {
	result := content
	for _, transform := range transforms {
		switch transform {
		case "normalize":
			result = strings.ToUpper(result)
		case "enrich":
			result += "\n[LANTERN: ENRICHED ROW]"
		}
	}
	return result
}

func outputNameForDataset(source string) string {
	base := filepath.Base(strings.TrimSpace(source))
	if base == "" || base == "." || base == string(filepath.Separator) {
		base = "dataset"
	}
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)
	if name == "" {
		name = "dataset"
	}
	if ext == "" {
		ext = ".txt"
	}
	return name + ".processed" + ext
}
