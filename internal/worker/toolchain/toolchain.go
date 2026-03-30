package toolchain

import (
	"fmt"
	"os"
	"os/exec"
)

// Manager handles verifying and fetching external dependencies (ffmpeg, blender)
// for executors, either via pre-configured user paths or falling back to auto-download later.
type Manager struct {
	// CustomPaths maps a tool name (e.g. "ffmpeg") to a local absoluate path
	CustomPaths map[string]string

	// ToolsDir is the root path where auto-downloaded portable binaries would be placed
	ToolsDir string
}

// NewManager creates a manager for specific dependencies requested by executors
func NewManager(toolsDir string, customPaths map[string]string) *Manager {
	if customPaths == nil {
		customPaths = make(map[string]string)
	}
	return &Manager{
		ToolsDir:    toolsDir,
		CustomPaths: customPaths,
	}
}

// EnsureTool finds the executable tool required.
// In this basic version, it checks BYOT overrides or system PATH.
// In future iterations, if missing, it will auto-pull from official portable releases.
func (m *Manager) EnsureTool(toolName string) (string, error) {
	// Check user-forced custom paths (BYOT)
	if path, ok := m.CustomPaths[toolName]; ok {
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("custom path validation failed for %q at %s: %w", toolName, path, err)
		}
		return path, nil
	}

	// Check standard system PATH
	if path, err := exec.LookPath(toolName); err == nil {
		return path, nil
	}

	// Future: Start auto-downloader process into m.ToolsDir (e.g. ~/.lantern/tools/ffmpeg)

	return "", fmt.Errorf("tool %q is required but not found in PATH or custom config", toolName)
}
