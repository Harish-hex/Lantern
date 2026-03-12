package server

// bridge.go — exposes a narrow read/write interface from the HTTP layer into
// the TCP server's shared subsystems (StorageManager, Semaphore, Config).
//
// Divergence from the binary protocol:
//   HTTP uploads rely on TCP transport-level integrity and therefore have no
//   chunk reassembly or per-chunk retry logic.  Full re-upload is the failure
//   path.  The custom retry / NAK mechanism lives exclusively in
//   internal/protocol and is not replicated here.

import (
	"time"

	"github.com/Harish-hex/Lantern/internal/config"
	"github.com/Harish-hex/Lantern/internal/protocol"
)

// Bridge is the single coupling point between the HTTP/WebSocket layer and
// the server's internal subsystems.  Keep it thin: only add methods that the
// HTTP handlers genuinely need.
type Bridge struct {
	storage *StorageManager
	upload  *Semaphore
	cfg     config.Config
	stats   *Stats
}

// ── Config ───────────────────────────────────────────────────────────────────

// Cfg returns the server config so HTTP handlers can read defaults such as
// TTLDefault, MaxDownloads, ChunkSize, etc.
func (b *Bridge) Cfg() config.Config { return b.cfg }

func (b *Bridge) Stats() StatsSnapshot {
	if b.stats == nil {
		return StatsSnapshot{}
	}
	return b.stats.Snapshot()
}

func (b *Bridge) RecordUpload(fileID, filename string, bytes int64, chunkSize uint32, startedAt time.Time) {
	if b.stats == nil {
		return
	}
	b.stats.RecordUpload(fileID, filename, bytes, chunkSize, startedAt)
}

// ── Storage (read) ───────────────────────────────────────────────────────────

// GetFile looks up a stored file by its ID.  Returns nil when not found.
func (b *Bridge) GetFile(id string) *StoredFile {
	return b.storage.GetFile(id)
}

// ListFiles returns a point-in-time snapshot of all stored files.
func (b *Bridge) ListFiles() []*StoredFile {
	b.storage.mu.RLock()
	defer b.storage.mu.RUnlock()
	out := make([]*StoredFile, 0, len(b.storage.files))
	for _, sf := range b.storage.files {
		out = append(out, sf)
	}
	return out
}

// ── Storage (write) ──────────────────────────────────────────────────────────

// CreateTemp returns a unique temp-file path for an in-progress HTTP upload.
func (b *Bridge) CreateTemp(uploadID string) string {
	return b.storage.CreateTemp(uploadID, 0)
}

// MoveToStorage finalises an HTTP upload: moves the temp file into permanent
// storage and registers it in the in-memory index.
func (b *Bridge) MoveToStorage(
	tempPath, fileID string,
	meta protocol.FileMetadata,
	ttlSeconds, maxDownloads int,
) (*StoredFile, error) {
	return b.storage.MoveToStorage(tempPath, fileID, meta, ttlSeconds, maxDownloads)
}

// CleanupTemp removes a temp file (best-effort, ignores errors).
func (b *Bridge) CleanupTemp(path string) {
	b.storage.CleanupTemp(path)
}

// DeleteFile removes a file from disk and the registry.
func (b *Bridge) DeleteFile(id string) {
	b.storage.DeleteFile(id)
}

// ── Upload slot management ───────────────────────────────────────────────────

// TryAcquireUpload attempts to reserve one upload concurrency slot without
// blocking.  Returns false when the server is at MaxUploadConcurrency.
func (b *Bridge) TryAcquireUpload() bool {
	return b.upload.TryAcquire()
}

// ReleaseUpload frees one upload concurrency slot.  Must be called exactly
// once for every successful TryAcquireUpload.
func (b *Bridge) ReleaseUpload() {
	b.upload.Release()
}
