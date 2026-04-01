// Package toolchain manages external binary dependencies for Lantern executors.
//
// Resolution order for any tool:
//  1. Explicit custom path in CustomPaths (BYOT — Bring Your Own Tool)
//  2. Cached download in ToolsDir (~/.lantern/tools/<tool>/)
//  3. System PATH (exec.LookPath)
//  4. Auto-download from configured registry (if AutoDownload = true)
//
// Auto-download is intentionally opt-in to avoid unexpected network access.
// Each tool has a ToolSpec that declares where to download it and how to verify it.
package toolchain

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// ToolSpec describes how to resolve and optionally auto-download a tool binary.
type ToolSpec struct {
	// Name is the tool name used to look it up (e.g. "ffmpeg").
	Name string

	// BinaryName is the executable file on disk (may differ from Name on Windows: "ffmpeg.exe").
	BinaryName string

	// DownloadURLs maps GOOS/GOARCH combos to download URLs.
	// Key format: "<GOOS>/<GOARCH>"  e.g. "linux/amd64", "darwin/arm64"
	DownloadURLs map[string]string

	// SHA256Sums maps the same keys to expected hex SHA-256 checksums (optional but recommended).
	SHA256Sums map[string]string

	// InstallDir is a relative subdirectory inside ToolsDir where the binary lives after extraction.
	// For flat downloads (single binary), leave empty.
	InstallDir string

	// NeedsExtract indicates the download is an archive (e.g. .tar.gz, .zip) to unpack.
	NeedsExtract bool
}

// Manager handles verifying and fetching external dependencies (ffmpeg, blender, tesseract)
// for executors, either via pre-configured user paths, system PATH, cache, or auto-download.
type Manager struct {
	mu sync.Mutex

	// CustomPaths maps a tool name (e.g. "ffmpeg") to a local absolute path override.
	CustomPaths map[string]string

	// ToolsDir is the root path where auto-downloaded portable binaries are cached.
	// Default: ~/.lantern/tools
	ToolsDir string

	// AutoDownload enables automatic download of tools when not found locally.
	// Default: false (require explicit user opt-in).
	AutoDownload bool

	// Specs is the registry of known tools and how to obtain them.
	Specs map[string]ToolSpec

	// resolved cache: tool name → resolved binary path
	resolved map[string]string
}

// NewManager creates a toolchain manager.
func NewManager(toolsDir string, customPaths map[string]string) *Manager {
	if customPaths == nil {
		customPaths = make(map[string]string)
	}
	if toolsDir == "" {
		home, _ := os.UserHomeDir()
		toolsDir = filepath.Join(home, ".lantern", "tools")
	}
	return &Manager{
		CustomPaths: customPaths,
		ToolsDir:    toolsDir,
		AutoDownload: false,
		Specs:       builtInSpecs(),
		resolved:    make(map[string]string),
	}
}

// NewManagerWithAutoDownload creates a manager that will auto-download missing tools.
func NewManagerWithAutoDownload(toolsDir string, customPaths map[string]string) *Manager {
	m := NewManager(toolsDir, customPaths)
	m.AutoDownload = true
	return m
}

// EnsureTool resolves the binary path for a named tool following the resolution order.
// Results are cached for the lifetime of the Manager.
func (m *Manager) EnsureTool(toolName string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Return cached result.
	if path, ok := m.resolved[toolName]; ok {
		return path, nil
	}

	path, err := m.resolveTool(toolName)
	if err != nil {
		return "", err
	}
	m.resolved[toolName] = path
	return path, nil
}

// resolveTool does the actual resolution (caller holds m.mu).
func (m *Manager) resolveTool(toolName string) (string, error) {
	// 1. Explicit custom path (BYOT).
	if path, ok := m.CustomPaths[toolName]; ok {
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("custom path for %q (%s) is not accessible: %w", toolName, path, err)
		}
		log.Printf("[toolchain] %s: using custom path %s", toolName, path)
		return path, nil
	}

	spec, hasSpec := m.Specs[toolName]
	binName := toolName
	if hasSpec && spec.BinaryName != "" {
		binName = spec.BinaryName
	}

	// 2. Cached download in ToolsDir.
	cachedPath := filepath.Join(m.ToolsDir, toolName, binName)
	if runtime.GOOS == "windows" && !strings.HasSuffix(cachedPath, ".exe") {
		cachedPath += ".exe"
	}
	if _, err := os.Stat(cachedPath); err == nil {
		log.Printf("[toolchain] %s: found cached binary at %s", toolName, cachedPath)
		return cachedPath, nil
	}

	// 3. System PATH.
	if path, err := exec.LookPath(binName); err == nil {
		log.Printf("[toolchain] %s: found on PATH at %s", toolName, path)
		return path, nil
	}

	// 4. Auto-download (if enabled and spec exists).
	if !m.AutoDownload {
		return "", fmt.Errorf("tool %q not found (checked custom paths, %s, and PATH); "+
			"install it manually or enable auto-download", toolName, m.ToolsDir)
	}
	if !hasSpec {
		return "", fmt.Errorf("tool %q not found and no download spec registered; install manually", toolName)
	}

	goos := runtime.GOOS
	goarch := runtime.GOARCH
	key := goos + "/" + goarch
	url, ok := spec.DownloadURLs[key]
	if !ok {
		return "", fmt.Errorf("tool %q: no download URL for platform %s", toolName, key)
	}

	log.Printf("[toolchain] %s: not found — auto-downloading from %s", toolName, url)
	destDir := filepath.Join(m.ToolsDir, toolName)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("create tool dir %s: %w", destDir, err)
	}

	downloadedPath, err := m.downloadFile(url, destDir)
	if err != nil {
		return "", fmt.Errorf("download %s: %w", toolName, err)
	}

	// Verify checksum if provided.
	if spec.SHA256Sums != nil {
		if expected, ok := spec.SHA256Sums[key]; ok {
			if err := verifySHA256(downloadedPath, expected); err != nil {
				_ = os.Remove(downloadedPath)
				return "", fmt.Errorf("checksum verification failed for %s: %w", toolName, err)
			}
			log.Printf("[toolchain] %s: checksum verified ✓", toolName)
		}
	}

	// Handle archive extraction (tar.gz / zip) — basic flat extraction for single-binary tools.
	finalPath := downloadedPath
	if spec.NeedsExtract {
		var extractErr error
		finalPath, extractErr = extractBinary(downloadedPath, destDir, binName)
		if extractErr != nil {
			return "", fmt.Errorf("extract %s: %w", toolName, extractErr)
		}
		_ = os.Remove(downloadedPath)
	}

	// Mark executable on unix.
	if runtime.GOOS != "windows" {
		if err := os.Chmod(finalPath, 0o755); err != nil {
			log.Printf("[toolchain] warning: chmod %s: %v", finalPath, err)
		}
	}

	log.Printf("[toolchain] %s: installed to %s", toolName, finalPath)
	return finalPath, nil
}

// Probe checks whether a tool is available without downloading it.
// Returns the resolved path and nil, or an empty string and a descriptive error.
func (m *Manager) Probe(toolName string) (string, error) {
	m.mu.Lock()
	autoDownload := m.AutoDownload
	m.AutoDownload = false
	m.mu.Unlock()

	path, err := m.EnsureTool(toolName)

	m.mu.Lock()
	m.AutoDownload = autoDownload
	m.mu.Unlock()

	return path, err
}

// downloadFile downloads url into dir, returning the path of the saved file.
func (m *Manager) downloadFile(url, dir string) (string, error) {
	resp, err := http.Get(url) //nolint:gosec // URL comes from curated ToolSpec, not user input
	if err != nil {
		return "", fmt.Errorf("HTTP GET: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected HTTP status %d for %s", resp.StatusCode, url)
	}

	// Derive a filename from the URL.
	parts := strings.Split(url, "/")
	filename := parts[len(parts)-1]
	if filename == "" {
		filename = "download"
	}
	destPath := filepath.Join(dir, filename)

	f, err := os.Create(destPath)
	if err != nil {
		return "", fmt.Errorf("create %s: %w", destPath, err)
	}
	defer f.Close()

	written, err := io.Copy(f, resp.Body)
	if err != nil {
		return "", fmt.Errorf("write download: %w", err)
	}
	log.Printf("[toolchain] downloaded %d bytes to %s", written, destPath)
	return destPath, nil
}

// verifySHA256 checks the SHA-256 checksum of a file.
func verifySHA256(path, expectedHex string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, expectedHex) {
		return fmt.Errorf("expected %s, got %s", expectedHex, got)
	}
	return nil
}

// extractBinary extracts the named binary from a downloaded archive file.
// Supports .tar.gz and .zip archives. Returns path to the extracted binary.
func extractBinary(archivePath, destDir, binaryName string) (string, error) {
	lower := strings.ToLower(archivePath)
	switch {
	case strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz"):
		return extractTarGz(archivePath, destDir, binaryName)
	case strings.HasSuffix(lower, ".zip"):
		return extractZip(archivePath, destDir, binaryName)
	default:
		// Not an archive — assume it is already the binary.
		dest := filepath.Join(destDir, binaryName)
		if err := os.Rename(archivePath, dest); err != nil {
			return "", fmt.Errorf("rename to %s: %w", dest, err)
		}
		return dest, nil
	}
}

func extractTarGz(archivePath, destDir, binaryName string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	// Use tar command for simplicity and correctness.
	if _, err := exec.LookPath("tar"); err != nil {
		return "", fmt.Errorf("tar not available for extraction: %w", err)
	}
	cmd := exec.Command("tar", "-xzf", archivePath, "--strip-components=1", "-C", destDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("tar extract: %s: %w", string(out), err)
	}

	// Find the binary after extraction.
	return findExtractedBinary(destDir, binaryName)
}

func extractZip(archivePath, destDir, binaryName string) (string, error) {
	if _, err := exec.LookPath("unzip"); err == nil {
		cmd := exec.Command("unzip", "-o", archivePath, "-d", destDir)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("unzip: %s: %w", string(out), err)
		}
		return findExtractedBinary(destDir, binaryName)
	}

	// Fallback: use Go's archive/zip.
	return extractZipGo(archivePath, destDir, binaryName)
}

func extractZipGo(archivePath, destDir, binaryName string) (string, error) {
	import_zip, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", err
	}
	defer import_zip.Close()

	var foundPath string
	for _, f := range import_zip.File {
		base := filepath.Base(f.Name)
		if base != binaryName && base != binaryName+".exe" {
			continue
		}
		destPath := filepath.Join(destDir, base)
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		out, err := os.Create(destPath)
		if err != nil {
			rc.Close()
			return "", err
		}
		_, err = io.Copy(out, rc)
		rc.Close()
		out.Close()
		if err != nil {
			return "", err
		}
		foundPath = destPath
		break
	}
	if foundPath == "" {
		return "", fmt.Errorf("binary %q not found in zip archive", binaryName)
	}
	return foundPath, nil
}

func findExtractedBinary(dir, binaryName string) (string, error) {
	// Try exact path first.
	candidates := []string{
		filepath.Join(dir, binaryName),
		filepath.Join(dir, binaryName+".exe"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}

	// Walk the directory tree looking for the binary.
	var found string
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if base == binaryName || base == binaryName+".exe" {
			found = path
			return io.EOF // stop walk
		}
		return nil
	})
	if found != "" {
		return found, nil
	}
	return "", fmt.Errorf("binary %q not found after extraction in %s", binaryName, dir)
}

// builtInSpecs returns the registry of known tools with their download specs.
// These are the tools required by the Lantern executor templates.
func builtInSpecs() map[string]ToolSpec {
	return map[string]ToolSpec{
		"ffmpeg": {
			Name:         "ffmpeg",
			BinaryName:   "ffmpeg",
			NeedsExtract: true,
			DownloadURLs: map[string]string{
				"linux/amd64":  "https://github.com/BtbN/FFmpeg-Builds/releases/latest/download/ffmpeg-master-latest-linux64-gpl.tar.gz",
				"linux/arm64":  "https://github.com/BtbN/FFmpeg-Builds/releases/latest/download/ffmpeg-master-latest-linuxarm64-gpl.tar.gz",
				"darwin/amd64": "https://evermeet.cx/ffmpeg/getrelease/zip",
				"darwin/arm64": "https://evermeet.cx/ffmpeg/getrelease/zip",
				"windows/amd64": "https://github.com/BtbN/FFmpeg-Builds/releases/latest/download/ffmpeg-master-latest-win64-gpl.zip",
			},
		},
		"blender": {
			Name:         "blender",
			BinaryName:   "blender",
			NeedsExtract: true,
			DownloadURLs: map[string]string{
				// Blender requires user to provide their own installation due to license/size.
				// These entries document the official download locations for reference.
				"linux/amd64":  "https://download.blender.org/release/Blender4.3/blender-4.3.2-linux-x64.tar.xz",
				"darwin/arm64": "https://download.blender.org/release/Blender4.3/blender-4.3.2-macos-arm64.dmg",
				"windows/amd64": "https://download.blender.org/release/Blender4.3/blender-4.3.2-windows-x64.zip",
			},
		},
		"tesseract": {
			Name:       "tesseract",
			BinaryName: "tesseract",
			// Tesseract requires package manager installation (apt, brew, choco).
			// Documenting for guidance only; auto-download not viable cross-platform.
			DownloadURLs: map[string]string{},
		},
	}
}

// ToolStatus describes the current availability of a tool.
type ToolStatus struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Path      string `json:"path,omitempty"`
	Error     string `json:"error,omitempty"`
}

// ProbeAll checks all registered tools and returns their status.
// This is useful for preflight validation without triggering downloads.
func (m *Manager) ProbeAll() []ToolStatus {
	m.mu.Lock()
	names := make([]string, 0, len(m.Specs))
	for name := range m.Specs {
		names = append(names, name)
	}
	m.mu.Unlock()

	statuses := make([]ToolStatus, 0, len(names))
	for _, name := range names {
		path, err := m.Probe(name)
		s := ToolStatus{Name: name, Available: err == nil, Path: path}
		if err != nil {
			s.Error = err.Error()
		}
		statuses = append(statuses, s)
	}
	return statuses
}
