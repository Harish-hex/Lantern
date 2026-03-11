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
	HTTPPort   int // HTTP/WebSocket server port (0 = disabled)

	StorageDir string // Final resting place for uploaded files
	TempDir    string // Temp files during in-flight uploads
	IndexDir   string // Persisted file/session snapshots for restart recovery

	MaxFileSize            int64 // Per-file hard cap (bytes)
	MaxUploadConcurrency   int
	MaxDownloadConcurrency int
	ChunkSize              uint32 // Default chunk size sent to clients

	SessionTimeout time.Duration // Idle session reap time
	ResumeTTL      time.Duration // How long paused sessions remain resumable
	CleanupTick    time.Duration // How often the reaper runs

	EnableMDNS      bool
	MDNSServiceName string

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
		HTTPPort:   9724,

		StorageDir: filepath.Join(base, "storage"),
		TempDir:    filepath.Join(base, "tmp"),
		IndexDir:   filepath.Join(base, "index"),

		MaxFileSize:            5 * 1024 * 1024 * 1024, // 5 GB
		MaxUploadConcurrency:   10,
		MaxDownloadConcurrency: 20,
		ChunkSize:              256 * 1024, // 256 KB

		SessionTimeout: 5 * time.Minute,
		ResumeTTL:      15 * time.Minute,
		CleanupTick:    30 * time.Second,

		EnableMDNS:      false,
		MDNSServiceName: "Lantern",

		TTLDefault:   3600, // 1 hour
		MaxDownloads: 10,
		MaxRetries:   3,
	}
}
