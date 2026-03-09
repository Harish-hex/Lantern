// Package config defines server configuration with sensible defaults.
package config

import (
	"os"
	"path/filepath"
	"time"
)

// Config holds all tuneable server parameters.
type Config struct {
	ListenAddr string
	Port       int

	StorageDir string // Final resting place for uploaded files
	TempDir    string // Temp files during in-flight uploads

	MaxFileSize            int64 // Per-file hard cap (bytes)
	MaxUploadConcurrency   int
	MaxDownloadConcurrency int
	ChunkSize              uint32 // Default chunk size sent to clients

	SessionTimeout time.Duration // Idle session reap time
	CleanupTick    time.Duration // How often the reaper runs

	TTLDefault   int // Default TTL seconds if client doesn't specify
	MaxDownloads int // Default download limit if client doesn't specify
	MaxRetries   int // NAK retransmit ceiling per chunk
}

// Default returns production-ready defaults.
func Default() Config { return DefaultConfig() }

// DefaultConfig returns production-ready defaults.
func DefaultConfig() Config {
	home, _ := os.UserHomeDir()
	base := filepath.Join(home, ".lantern")

	return Config{
		ListenAddr: "0.0.0.0",
		Port:       9090,

		StorageDir: filepath.Join(base, "storage"),
		TempDir:    filepath.Join(base, "tmp"),

		MaxFileSize:            5 * 1024 * 1024 * 1024, // 5 GB
		MaxUploadConcurrency:   10,
		MaxDownloadConcurrency: 20,
		ChunkSize:              256 * 1024, // 256 KB

		SessionTimeout: 5 * time.Minute,
		CleanupTick:    30 * time.Second,

		TTLDefault:   3600, // 1 hour
		MaxDownloads: 10,
		MaxRetries:   3,
	}
}
