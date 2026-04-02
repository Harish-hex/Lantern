package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Harish-hex/Lantern/internal/worker/toolchain"
)

type DocumentOCRExecutor struct {
	toolchainManager *toolchain.Manager
}

func NewDocumentOCRExecutor(manager *toolchain.Manager) *DocumentOCRExecutor {
	return &DocumentOCRExecutor{toolchainManager: manager}
}

func (e *DocumentOCRExecutor) ID() string {
	return "document_ocr_batch"
}

type documentOCRPayload struct {
	Template      string `json:"template"`
	Document      string `json:"document"`
	NormalizeText bool   `json:"normalize_text"`
	EmitJSON      bool   `json:"emit_json"`
	Language      string `json:"language"`
}

type documentOCRJSON struct {
	Template string               `json:"template"`
	Source   string               `json:"source"`
	Language string               `json:"language"`
	Text     string               `json:"text"`
	Tokens   []documentOCRToken   `json:"tokens,omitempty"`
	Summary  documentOCRJSONStats `json:"summary"`
}

type documentOCRToken struct {
	Text       string  `json:"text"`
	Confidence float64 `json:"confidence"`
	Line       int     `json:"line"`
}

type documentOCRJSONStats struct {
	TokenCount int `json:"token_count"`
}

func (e *DocumentOCRExecutor) Execute(ctx context.Context, payload json.RawMessage, outputDir string) error {
	cfg, err := parseDocumentOCRPayload(payload)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	tesseractPath, err := e.resolveTesseractBinary()
	if err != nil {
		return fmt.Errorf("ocr tool unavailable: %w", err)
	}

	baseName := sanitizeOCRBaseName(cfg.Document)
	outputBase := filepath.Join(outputDir, baseName+".ocr")

	cmd := exec.CommandContext(ctx, tesseractPath, cfg.Document, outputBase, "-l", cfg.Language)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tesseract extract failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	textPath := outputBase + ".txt"
	rawText, err := os.ReadFile(textPath)
	if err != nil {
		return fmt.Errorf("read OCR text output: %w", err)
	}

	finalText := string(rawText)
	if cfg.NormalizeText {
		finalText = normalizeOCRText(finalText)
		if err := os.WriteFile(textPath, []byte(finalText), 0o644); err != nil {
			return fmt.Errorf("rewrite normalized OCR text: %w", err)
		}
	}

	if cfg.EmitJSON {
		tsvCmd := exec.CommandContext(ctx, tesseractPath, cfg.Document, "stdout", "-l", cfg.Language, "tsv")
		tsvOut, err := tsvCmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("tesseract TSV failed: %w (%s)", err, strings.TrimSpace(string(tsvOut)))
		}

		tokens := parseOCRTSV(string(tsvOut))
		payload := documentOCRJSON{
			Template: e.ID(),
			Source:   cfg.Document,
			Language: cfg.Language,
			Text:     finalText,
			Tokens:   tokens,
			Summary: documentOCRJSONStats{
				TokenCount: len(tokens),
			},
		}
		jsonBytes, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal OCR JSON: %w", err)
		}
		if err := os.WriteFile(filepath.Join(outputDir, baseName+".ocr.json"), jsonBytes, 0o644); err != nil {
			return fmt.Errorf("write OCR JSON: %w", err)
		}
	}

	return nil
}

func parseDocumentOCRPayload(raw json.RawMessage) (documentOCRPayload, error) {
	cfg := documentOCRPayload{
		NormalizeText: true,
		EmitJSON:      true,
		Language:      "eng",
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("parse document_ocr_batch payload: %w", err)
	}
	cfg.Document = strings.TrimSpace(cfg.Document)
	if cfg.Document == "" {
		return cfg, fmt.Errorf("document_ocr_batch payload missing document")
	}
	cfg.Language = strings.TrimSpace(cfg.Language)
	if cfg.Language == "" {
		cfg.Language = "eng"
	}
	return cfg, nil
}

func (e *DocumentOCRExecutor) resolveTesseractBinary() (string, error) {
	if e.toolchainManager != nil {
		if path, err := e.toolchainManager.EnsureTool("tesseract"); err == nil {
			return path, nil
		}
	}
	path, err := exec.LookPath("tesseract")
	if err != nil {
		return "", fmt.Errorf("tesseract not found; install from dashboard OCR tool setup or package manager")
	}
	return path, nil
}

func sanitizeOCRBaseName(input string) string {
	base := filepath.Base(strings.TrimSpace(input))
	ext := filepath.Ext(base)
	name := strings.TrimSpace(strings.TrimSuffix(base, ext))
	if name == "" {
		return "document"
	}
	name = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, name)
	return name
}

func normalizeOCRText(raw string) string {
	scanner := bufio.NewScanner(strings.NewReader(raw))
	lines := make([]string, 0)
	for scanner.Scan() {
		line := strings.Join(strings.Fields(scanner.Text()), " ")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func parseOCRTSV(tsv string) []documentOCRToken {
	lines := strings.Split(strings.ReplaceAll(tsv, "\r\n", "\n"), "\n")
	if len(lines) <= 1 {
		return nil
	}
	tokens := make([]documentOCRToken, 0, len(lines))
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) < 12 {
			continue
		}
		text := strings.TrimSpace(cols[11])
		if text == "" {
			continue
		}
		lineNum, _ := strconv.Atoi(strings.TrimSpace(cols[4]))
		conf, _ := strconv.ParseFloat(strings.TrimSpace(cols[10]), 64)
		tokens = append(tokens, documentOCRToken{
			Text:       text,
			Confidence: conf,
			Line:       lineNum,
		})
	}
	return tokens
}
