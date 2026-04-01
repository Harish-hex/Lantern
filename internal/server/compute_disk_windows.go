//go:build windows

package server

import "fmt"

// buildDiskSpaceCheck returns a graceful no-op on Windows (syscall.Statfs unavailable).
// On Windows workers should use the OS disk management tools to ensure sufficient space.
func buildDiskSpaceCheck(estimatedOutputBytes int64) ComputePreflightCheck {
	return ComputePreflightCheck{
		Code:    "disk_space",
		Status:  "warn",
		Message: fmt.Sprintf("disk space check unavailable on Windows (estimated output: %s)", humanBytes(estimatedOutputBytes)),
	}
}

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
