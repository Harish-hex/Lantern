//go:build !windows

package server

import (
	"fmt"
	"syscall"
)

// minFreeBytesWarn is the free disk space threshold below which we emit a warning.
const minFreeBytesWarn = 500 * 1024 * 1024 // 500 MB

// minFreeBytesFail is the free disk space threshold below which we emit a failure.
const minFreeBytesFail = 100 * 1024 * 1024 // 100 MB

// buildDiskSpaceCheck returns a preflight check using syscall.Statfs to measure
// available space on the partition backing the current working directory.
// The estimatedOutputBytes hint is used to refine the warning message.
func buildDiskSpaceCheck(estimatedOutputBytes int64) ComputePreflightCheck {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(".", &stat); err != nil {
		// Graceful degradation: if we can't stat the filesystem, warn but don't block.
		return ComputePreflightCheck{
			Code:    "disk_space",
			Status:  "warn",
			Message: fmt.Sprintf("could not check disk space: %v", err),
		}
	}

	freeBytes := int64(stat.Bavail) * int64(stat.Bsize)

	switch {
	case freeBytes < minFreeBytesFail:
		return ComputePreflightCheck{
			Code:    "disk_space",
			Status:  "fail",
			Message: fmt.Sprintf("critical: only %s free disk space — job will likely fail", humanBytes(freeBytes)),
		}
	case freeBytes < minFreeBytesWarn:
		return ComputePreflightCheck{
			Code:    "disk_space",
			Status:  "warn",
			Message: fmt.Sprintf("low disk space: %s free (estimated output: %s)", humanBytes(freeBytes), humanBytes(estimatedOutputBytes)),
		}
	default:
		return ComputePreflightCheck{
			Code:    "disk_space",
			Status:  "pass",
			Message: fmt.Sprintf("%s free disk space available", humanBytes(freeBytes)),
		}
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
