package server

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	ComputeJobStatusQueued         = "queued"
	ComputeJobStatusRunning        = "running"
	ComputeJobStatusRetrying       = "retrying"
	ComputeJobStatusAssembling     = "assembling"
	ComputeJobStatusDone           = "done"
	ComputeJobStatusNeedsAttention = "needs_attention"

	ComputeTaskStatusQueued         = "queued"
	ComputeTaskStatusLeased         = "leased"
	ComputeTaskStatusRetrying       = "retrying"
	ComputeTaskStatusCompleted      = "completed"
	ComputeTaskStatusNeedsAttention = "needs_attention"

	ComputeFailureTransientInfrastructure = "transient_infrastructure"
	ComputeFailureCapabilityMismatch      = "capability_mismatch"
	ComputeFailureInvalidInput            = "invalid_input"
	ComputeFailureResourceExhaustion      = "resource_exhaustion"
	ComputeFailureUnknown                 = "unknown"
)

type ComputeArtifact struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Kind      string          `json:"kind"`
	SizeBytes int64           `json:"size_bytes"`
	CreatedAt time.Time       `json:"created_at"`
	Summary   json.RawMessage `json:"summary,omitempty"`
}

type ComputePreflightCheck struct {
	Code    string `json:"code"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type ComputePreflight struct {
	Ready                bool                    `json:"ready"`
	Confidence           string                  `json:"confidence,omitempty"`
	EstimatedTasks       int                     `json:"estimated_tasks"`
	EstimatedOutputBytes int64                   `json:"estimated_output_bytes"`
	RequiredCapabilities []string                `json:"required_capabilities,omitempty"`
	Checks               []ComputePreflightCheck `json:"checks,omitempty"`
}

type ComputeTemplate struct {
	ID                   string          `json:"id"`
	Name                 string          `json:"name"`
	BestFor              string          `json:"best_for"`
	OutputKind           string          `json:"output_kind"`
	RequiredCapabilities []string        `json:"required_capabilities,omitempty"`
	DefaultInputs        json.RawMessage `json:"default_inputs,omitempty"`
	DefaultSettings      json.RawMessage `json:"default_settings,omitempty"`
}

type ComputeTemplateMetric struct {
	TemplateID         string  `json:"template_id"`
	TemplateName       string  `json:"template_name"`
	StartedJobs        int     `json:"started_jobs"`
	CompletedJobs      int     `json:"completed_jobs"`
	NeedsAttentionJobs int     `json:"needs_attention_jobs"`
	CompletionRate     float64 `json:"completion_rate"`
}

type ComputeOverview struct {
	GeneratedAt          time.Time               `json:"generated_at"`
	StartedJobs          int                     `json:"started_jobs"`
	CompletedJobs        int                     `json:"completed_jobs"`
	NeedsAttentionJobs   int                     `json:"needs_attention_jobs"`
	ActiveJobs           int                     `json:"active_jobs"`
	QueueDepth           int                     `json:"queue_depth"`
	GlobalCompletionRate float64                 `json:"global_completion_rate"`
	FailureBuckets       map[string]int          `json:"failure_buckets"`
	TemplateMetrics      []ComputeTemplateMetric `json:"template_metrics"`
}

type compiledComputeTask struct {
	Payload              json.RawMessage
	RequiredCapabilities []string
}

type compiledComputeSubmission struct {
	Template             ComputeTemplate
	Tasks                []compiledComputeTask
	EstimatedOutputBytes int64
}

type computeTemplateDescriptor struct {
	template ComputeTemplate
	compile  func(inputs, settings json.RawMessage) (*compiledComputeSubmission, error)
}

type renderFramesInputs struct {
	Scene      string `json:"scene"`
	FrameStart int    `json:"frame_start"`
	FrameEnd   int    `json:"frame_end"`
	ChunkSize  int    `json:"chunk_size"`
}

type renderFramesSettings struct {
	StitchedOutput bool   `json:"stitched_output"`
	OutputFormat   string `json:"output_format"`
}

type videoTranscodeInputs struct {
	Sources []string `json:"sources"`
}

type videoTranscodeSettings struct {
	Preset       string `json:"preset"`
	AudioCodec   string `json:"audio_codec"`
	Container    string `json:"container"`
	EmitManifest bool   `json:"emit_manifest"`
}

type imageBatchInputs struct {
	Images []string `json:"images"`
}

type imageBatchSettings struct {
	Format      string `json:"format"`
	ResizeWidth int    `json:"resize_width"`
	Watermark   bool   `json:"watermark"`
	Optimize    bool   `json:"optimize"`
}

type dataProcessingInputs struct {
	Datasets []string `json:"datasets"`
}

type dataProcessingSettings struct {
	Transforms    []string `json:"transforms"`
	SummaryReport bool     `json:"summary_report"`
}

type documentOCRInputs struct {
	Documents []string `json:"documents"`
}

type documentOCRSettings struct {
	NormalizeText bool   `json:"normalize_text"`
	EmitJSON      bool   `json:"emit_json"`
	Language      string `json:"language"`
}

func BuiltInComputeTemplates() []ComputeTemplate {
	templates := make([]ComputeTemplate, 0, len(computeTemplateDescriptors()))
	for _, descriptor := range computeTemplateDescriptors() {
		template := descriptor.template
		template.RequiredCapabilities = cloneStrings(template.RequiredCapabilities)
		template.DefaultInputs = cloneRawMessage(template.DefaultInputs)
		template.DefaultSettings = cloneRawMessage(template.DefaultSettings)
		templates = append(templates, template)
	}
	return templates
}

func computeTemplateDescriptors() []computeTemplateDescriptor {
	return []computeTemplateDescriptor{
		{
			template: ComputeTemplate{
				ID:                   "render_frames",
				Name:                 "Render Frames",
				BestFor:              "frame-range render workloads",
				OutputKind:           "frame archive with optional stitched output",
				RequiredCapabilities: []string{"render_frames"},
				DefaultInputs: mustMarshalRaw(renderFramesInputs{
					Scene:      "scene.blend",
					FrameStart: 1,
					FrameEnd:   24,
					ChunkSize:  6,
				}),
				DefaultSettings: mustMarshalRaw(renderFramesSettings{
					StitchedOutput: true,
					OutputFormat:   "png",
				}),
			},
			compile: compileRenderFrames,
		},
		{
			template: ComputeTemplate{
				ID:                   "video_transcode_batch",
				Name:                 "Video Transcode Batch",
				BestFor:              "converting many source videos to standard presets",
				OutputKind:           "transformed video batch package",
				RequiredCapabilities: []string{"video_transcode_batch"},
				DefaultInputs: mustMarshalRaw(videoTranscodeInputs{
					Sources: []string{"source-001.mov", "source-002.mov", "source-003.mov"},
				}),
				DefaultSettings: mustMarshalRaw(videoTranscodeSettings{
					Preset:       "1080p-h264",
					AudioCodec:   "aac",
					Container:    "mp4",
					EmitManifest: true,
				}),
			},
			compile: compileVideoTranscodeBatch,
		},
		{
			template: ComputeTemplate{
				ID:                   "image_batch_pipeline",
				Name:                 "Image Batch Pipeline",
				BestFor:              "resize, format conversion, watermark, and optimization",
				OutputKind:           "processed image bundle",
				RequiredCapabilities: []string{"image_batch_pipeline"},
				DefaultInputs: mustMarshalRaw(imageBatchInputs{
					Images: []string{"image-001.png", "image-002.png", "image-003.png", "image-004.png"},
				}),
				DefaultSettings: mustMarshalRaw(imageBatchSettings{
					Format:      "webp",
					ResizeWidth: 2048,
					Watermark:   false,
					Optimize:    true,
				}),
			},
			compile: compileImageBatchPipeline,
		},
		{
			template: ComputeTemplate{
				ID:                   "data_processing_batch",
				Name:                 "Data Processing Batch",
				BestFor:              "CSV and JSON transformation plus enrichment",
				OutputKind:           "transformed datasets and summary report",
				RequiredCapabilities: []string{"data_processing_batch"},
				DefaultInputs: mustMarshalRaw(dataProcessingInputs{
					Datasets: []string{"customers.csv", "events.json"},
				}),
				DefaultSettings: mustMarshalRaw(dataProcessingSettings{
					Transforms:    []string{"normalize", "enrich"},
					SummaryReport: true,
				}),
			},
			compile: compileDataProcessingBatch,
		},
		{
			template: ComputeTemplate{
				ID:                   "document_ocr_batch",
				Name:                 "Document OCR Batch",
				BestFor:              "PDF and image text extraction with normalization",
				OutputKind:           "text and JSON extraction package with logs",
				RequiredCapabilities: []string{"document_ocr_batch"},
				DefaultInputs: mustMarshalRaw(documentOCRInputs{
					Documents: []string{"document-001.pdf", "document-002.png", "document-003.pdf"},
				}),
				DefaultSettings: mustMarshalRaw(documentOCRSettings{
					NormalizeText: true,
					EmitJSON:      true,
					Language:      "eng",
				}),
			},
			compile: compileDocumentOCRBatch,
		},
	}
}

func normalizeComputeTemplateID(raw string) string {
	normalized := strings.TrimSpace(strings.ToLower(raw))
	normalized = strings.ReplaceAll(normalized, "-", "_")
	normalized = strings.ReplaceAll(normalized, " ", "_")

	switch normalized {
	case "renderframes":
		return "render_frames"
	case "videotranscodebatch":
		return "video_transcode_batch"
	case "imagebatchpipeline":
		return "image_batch_pipeline"
	case "dataprocessingbatch":
		return "data_processing_batch"
	case "documentocrbatch":
		return "document_ocr_batch"
	}

	return normalized
}

func lookupComputeTemplate(raw string) (computeTemplateDescriptor, bool) {
	normalized := normalizeComputeTemplateID(raw)
	for _, descriptor := range computeTemplateDescriptors() {
		if descriptor.template.ID == normalized || normalizeComputeTemplateID(descriptor.template.Name) == normalized {
			return descriptor, true
		}
	}
	return computeTemplateDescriptor{}, false
}

func compileTemplateSubmission(templateID string, inputs, settings json.RawMessage) (*compiledComputeSubmission, error) {
	descriptor, ok := lookupComputeTemplate(templateID)
	if !ok {
		return nil, fmt.Errorf("unknown built-in template %q", templateID)
	}

	submission, err := descriptor.compile(inputs, settings)
	if err != nil {
		return nil, err
	}
	submission.Template = descriptor.template
	return submission, nil
}

func compileRenderFrames(inputsRaw, settingsRaw json.RawMessage) (*compiledComputeSubmission, error) {
	inputs := renderFramesInputs{
		Scene:      "scene.blend",
		FrameStart: 1,
		FrameEnd:   24,
		ChunkSize:  6,
	}
	settings := renderFramesSettings{
		StitchedOutput: true,
		OutputFormat:   "png",
	}
	if err := decodeOptionalJSON(inputsRaw, &inputs); err != nil {
		return nil, fmt.Errorf("render_frames inputs: %w", err)
	}
	if err := decodeOptionalJSON(settingsRaw, &settings); err != nil {
		return nil, fmt.Errorf("render_frames settings: %w", err)
	}
	if inputs.FrameStart <= 0 || inputs.FrameEnd <= 0 {
		return nil, fmt.Errorf("render_frames requires positive frame_start and frame_end")
	}
	if inputs.FrameEnd < inputs.FrameStart {
		return nil, fmt.Errorf("render_frames frame_end must be greater than or equal to frame_start")
	}
	if inputs.ChunkSize <= 0 {
		return nil, fmt.Errorf("render_frames chunk_size must be greater than zero")
	}
	if strings.TrimSpace(settings.OutputFormat) == "" {
		settings.OutputFormat = "png"
	}

	frameCount := inputs.FrameEnd - inputs.FrameStart + 1
	if inputs.ChunkSize > frameCount {
		inputs.ChunkSize = frameCount
	}

	tasks := make([]compiledComputeTask, 0, (frameCount+inputs.ChunkSize-1)/inputs.ChunkSize)
	for start := inputs.FrameStart; start <= inputs.FrameEnd; start += inputs.ChunkSize {
		end := start + inputs.ChunkSize - 1
		if end > inputs.FrameEnd {
			end = inputs.FrameEnd
		}
		tasks = append(tasks, compiledComputeTask{
			Payload: mustMarshalRaw(map[string]any{
				"template":        "render_frames",
				"scene":           inputs.Scene,
				"frame_start":     start,
				"frame_end":       end,
				"output_format":   settings.OutputFormat,
				"stitched_output": settings.StitchedOutput,
			}),
			RequiredCapabilities: []string{"render_frames"},
		})
	}

	estimatedOutputBytes := int64(frameCount) * 8 * 1024 * 1024
	if settings.StitchedOutput {
		estimatedOutputBytes += 180 * 1024 * 1024
	}

	return &compiledComputeSubmission{
		Tasks:                tasks,
		EstimatedOutputBytes: estimatedOutputBytes,
	}, nil
}

func compileVideoTranscodeBatch(inputsRaw, settingsRaw json.RawMessage) (*compiledComputeSubmission, error) {
	inputs := videoTranscodeInputs{
		Sources: []string{"source-001.mov", "source-002.mov", "source-003.mov"},
	}
	settings := videoTranscodeSettings{
		Preset:       "1080p-h264",
		AudioCodec:   "aac",
		Container:    "mp4",
		EmitManifest: true,
	}
	if err := decodeOptionalJSON(inputsRaw, &inputs); err != nil {
		return nil, fmt.Errorf("video_transcode_batch inputs: %w", err)
	}
	if err := decodeOptionalJSON(settingsRaw, &settings); err != nil {
		return nil, fmt.Errorf("video_transcode_batch settings: %w", err)
	}
	if len(inputs.Sources) == 0 {
		return nil, fmt.Errorf("video_transcode_batch requires at least one source")
	}
	if strings.TrimSpace(settings.Preset) == "" {
		return nil, fmt.Errorf("video_transcode_batch preset is required")
	}

	tasks := make([]compiledComputeTask, 0, len(inputs.Sources))
	for _, source := range inputs.Sources {
		if strings.TrimSpace(source) == "" {
			return nil, fmt.Errorf("video_transcode_batch source entries must be non-empty")
		}
		tasks = append(tasks, compiledComputeTask{
			Payload: mustMarshalRaw(map[string]any{
				"template":      "video_transcode_batch",
				"source":        source,
				"preset":        settings.Preset,
				"audio_codec":   settings.AudioCodec,
				"container":     settings.Container,
				"emit_manifest": settings.EmitManifest,
			}),
			RequiredCapabilities: []string{"video_transcode_batch"},
		})
	}

	return &compiledComputeSubmission{
		Tasks:                tasks,
		EstimatedOutputBytes: int64(len(inputs.Sources)) * 180 * 1024 * 1024,
	}, nil
}

func compileImageBatchPipeline(inputsRaw, settingsRaw json.RawMessage) (*compiledComputeSubmission, error) {
	inputs := imageBatchInputs{
		Images: []string{"image-001.png", "image-002.png", "image-003.png", "image-004.png"},
	}
	settings := imageBatchSettings{
		Format:      "webp",
		ResizeWidth: 2048,
		Watermark:   false,
		Optimize:    true,
	}
	if err := decodeOptionalJSON(inputsRaw, &inputs); err != nil {
		return nil, fmt.Errorf("image_batch_pipeline inputs: %w", err)
	}
	if err := decodeOptionalJSON(settingsRaw, &settings); err != nil {
		return nil, fmt.Errorf("image_batch_pipeline settings: %w", err)
	}
	if len(inputs.Images) == 0 {
		return nil, fmt.Errorf("image_batch_pipeline requires at least one image")
	}

	format := strings.ToLower(strings.TrimSpace(settings.Format))
	switch format {
	case "jpg", "jpeg", "png", "webp":
	default:
		return nil, fmt.Errorf("image_batch_pipeline unsupported format %q", settings.Format)
	}
	if settings.ResizeWidth <= 0 {
		return nil, fmt.Errorf("image_batch_pipeline resize_width must be greater than zero")
	}

	tasks := make([]compiledComputeTask, 0, len(inputs.Images))
	for _, image := range inputs.Images {
		if strings.TrimSpace(image) == "" {
			return nil, fmt.Errorf("image_batch_pipeline image entries must be non-empty")
		}
		tasks = append(tasks, compiledComputeTask{
			Payload: mustMarshalRaw(map[string]any{
				"template":     "image_batch_pipeline",
				"image":        image,
				"format":       format,
				"resize_width": settings.ResizeWidth,
				"watermark":    settings.Watermark,
				"optimize":     settings.Optimize,
			}),
			RequiredCapabilities: []string{"image_batch_pipeline"},
		})
	}

	return &compiledComputeSubmission{
		Tasks:                tasks,
		EstimatedOutputBytes: int64(len(inputs.Images)) * 8 * 1024 * 1024,
	}, nil
}

func compileDataProcessingBatch(inputsRaw, settingsRaw json.RawMessage) (*compiledComputeSubmission, error) {
	inputs := dataProcessingInputs{
		Datasets: []string{"customers.csv", "events.json"},
	}
	settings := dataProcessingSettings{
		Transforms:    []string{"normalize", "enrich"},
		SummaryReport: true,
	}
	if err := decodeOptionalJSON(inputsRaw, &inputs); err != nil {
		return nil, fmt.Errorf("data_processing_batch inputs: %w", err)
	}
	if err := decodeOptionalJSON(settingsRaw, &settings); err != nil {
		return nil, fmt.Errorf("data_processing_batch settings: %w", err)
	}
	if len(inputs.Datasets) == 0 {
		return nil, fmt.Errorf("data_processing_batch requires at least one dataset")
	}

	tasks := make([]compiledComputeTask, 0, len(inputs.Datasets))
	for _, dataset := range inputs.Datasets {
		if strings.TrimSpace(dataset) == "" {
			return nil, fmt.Errorf("data_processing_batch dataset entries must be non-empty")
		}
		tasks = append(tasks, compiledComputeTask{
			Payload: mustMarshalRaw(map[string]any{
				"template":       "data_processing_batch",
				"dataset":        dataset,
				"transforms":     cloneStrings(settings.Transforms),
				"summary_report": settings.SummaryReport,
			}),
			RequiredCapabilities: []string{"data_processing_batch"},
		})
	}

	estimatedOutputBytes := int64(len(inputs.Datasets)) * 32 * 1024 * 1024
	if settings.SummaryReport {
		estimatedOutputBytes += 4 * 1024 * 1024
	}

	return &compiledComputeSubmission{
		Tasks:                tasks,
		EstimatedOutputBytes: estimatedOutputBytes,
	}, nil
}

func compileDocumentOCRBatch(inputsRaw, settingsRaw json.RawMessage) (*compiledComputeSubmission, error) {
	inputs := documentOCRInputs{
		Documents: []string{"document-001.pdf", "document-002.png", "document-003.pdf"},
	}
	settings := documentOCRSettings{
		NormalizeText: true,
		EmitJSON:      true,
		Language:      "eng",
	}
	if err := decodeOptionalJSON(inputsRaw, &inputs); err != nil {
		return nil, fmt.Errorf("document_ocr_batch inputs: %w", err)
	}
	if err := decodeOptionalJSON(settingsRaw, &settings); err != nil {
		return nil, fmt.Errorf("document_ocr_batch settings: %w", err)
	}
	if len(inputs.Documents) == 0 {
		return nil, fmt.Errorf("document_ocr_batch requires at least one document")
	}
	if strings.TrimSpace(settings.Language) == "" {
		settings.Language = "eng"
	}

	tasks := make([]compiledComputeTask, 0, len(inputs.Documents))
	for _, document := range inputs.Documents {
		if strings.TrimSpace(document) == "" {
			return nil, fmt.Errorf("document_ocr_batch document entries must be non-empty")
		}
		tasks = append(tasks, compiledComputeTask{
			Payload: mustMarshalRaw(map[string]any{
				"template":       "document_ocr_batch",
				"document":       document,
				"normalize_text": settings.NormalizeText,
				"emit_json":      settings.EmitJSON,
				"language":       settings.Language,
			}),
			RequiredCapabilities: []string{"document_ocr_batch"},
		})
	}

	estimatedOutputBytes := int64(len(inputs.Documents)) * 4 * 1024 * 1024
	if settings.EmitJSON {
		estimatedOutputBytes += int64(len(inputs.Documents)) * 512 * 1024
	}

	return &compiledComputeSubmission{
		Tasks:                tasks,
		EstimatedOutputBytes: estimatedOutputBytes,
	}, nil
}

func classifyComputeFailure(message string) string {
	normalized := strings.ToLower(strings.TrimSpace(message))
	switch {
	case normalized == "":
		return ComputeFailureUnknown
	case strings.Contains(normalized, "capability"),
		strings.Contains(normalized, "unsupported"),
		strings.Contains(normalized, "no healthy workers"),
		strings.Contains(normalized, "worker not registered"):
		return ComputeFailureCapabilityMismatch
	case strings.Contains(normalized, "invalid"),
		strings.Contains(normalized, "missing"),
		strings.Contains(normalized, "malformed"),
		strings.Contains(normalized, "json"):
		return ComputeFailureInvalidInput
	case strings.Contains(normalized, "disk"),
		strings.Contains(normalized, "storage"),
		strings.Contains(normalized, "quota"),
		strings.Contains(normalized, "memory"),
		strings.Contains(normalized, "resource"):
		return ComputeFailureResourceExhaustion
	case strings.Contains(normalized, "timeout"),
		strings.Contains(normalized, "expired"),
		strings.Contains(normalized, "connection"),
		strings.Contains(normalized, "retry"),
		strings.Contains(normalized, "transient"):
		return ComputeFailureTransientInfrastructure
	default:
		return ComputeFailureTransientInfrastructure
	}
}

func computeFailureTerminal(category string) bool {
	switch category {
	case ComputeFailureCapabilityMismatch, ComputeFailureInvalidInput, ComputeFailureResourceExhaustion:
		return true
	default:
		return false
	}
}

func decodeOptionalJSON[T any](raw json.RawMessage, target *T) error {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return nil
	}
	return json.Unmarshal(raw, target)
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return append([]string(nil), values...)
}

func cloneRawMessage(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func mustMarshalRaw(value any) json.RawMessage {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

func uniqueSortedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	sort.Strings(out)
	return out
}
