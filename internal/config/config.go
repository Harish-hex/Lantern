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

	ComputeEnabled bool

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

	ComputeTokenTTL                  time.Duration
	ComputeEnrollmentCodeTTL         time.Duration
	ComputeEnrollmentCodeLength      int
	ComputeLeaseTTL                  time.Duration
	ComputeHeartbeat                 time.Duration
	ComputeRetryMax                  int
	ComputeTaskSizeBytes             int64
	ComputeArtifactBudgetBytes       int64
	ComputeWorkerQuarantineThreshold int
	ComputeRequireToken              bool
	ComputeWorkerAuthToken           string
}

// Default returns production-ready defaults.
func Default() Config { return DefaultConfig() }

// DefaultConfig returns production-ready defaults.
func DefaultConfig() Config {
	home, _ := os.UserHomeDir()
	base := filepath.Join(home, ".lantern")

	cfg := Config{
		ListenAddr:     "0.0.0.0",
		Port:           9723,
		HTTPPort:       9724,
		ComputeEnabled: true,

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

		ComputeTokenTTL:                  15 * time.Minute,
		ComputeEnrollmentCodeTTL:         15 * time.Minute,
		ComputeEnrollmentCodeLength:      10,
		ComputeLeaseTTL:                  45 * time.Second,
		ComputeHeartbeat:                 15 * time.Second,
		ComputeRetryMax:                  3,
		ComputeTaskSizeBytes:             4 * 1024 * 1024,        // 4 MiB
		ComputeArtifactBudgetBytes:       1 * 1024 * 1024 * 1024, // 1 GiB
		ComputeWorkerQuarantineThreshold: 4,
	}

	if token := os.Getenv("LANTERN_COMPUTE_TOKEN"); token != "" {
		cfg.ComputeRequireToken = true
		cfg.ComputeWorkerAuthToken = token
	}

	return cfg
}
