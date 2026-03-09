package server

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Harish-hex/Lantern/internal/protocol"
)

// StoredFile represents a fully received, verified file on disk.
type StoredFile struct {
	ID              string
	Path            string
	Metadata        protocol.FileMetadata
	DownloadCount   int32 // atomic
	MaxDownloads    int
	ExpiresAt       time.Time
	ActiveDownloads int32 // atomic — currently streaming
}

// IncrementDownloads atomically increments the counter. Returns the new count.
func (f *StoredFile) IncrementDownloads() int32 {
	return atomic.AddInt32(&f.DownloadCount, 1)
}

// IsExpired returns true if the file has exceeded its TTL or download limit.
func (f *StoredFile) IsExpired() bool {
	if time.Now().After(f.ExpiresAt) {
		return true
	}
	if f.MaxDownloads > 0 && atomic.LoadInt32(&f.DownloadCount) >= int32(f.MaxDownloads) {
		return true
	}
	return false
}

// StorageManager handles temp and final file storage.
type StorageManager struct {
	mu         sync.RWMutex
	storageDir string
	tempDir    string
	files      map[string]*StoredFile // keyed by file ID
}

// NewStorageManager creates directories and returns a ready manager.
func NewStorageManager(storageDir, tempDir string) (*StorageManager, error) {
	for _, dir := range []string{storageDir, tempDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create directory %s: %w", dir, err)
		}
	}
	return &StorageManager{
		storageDir: storageDir,
		tempDir:    tempDir,
		files:      make(map[string]*StoredFile),
	}, nil
}

// CreateTemp returns a path for a temporary upload file.
func (sm *StorageManager) CreateTemp(sessionIDHex string, fileIndex uint32) string {
	return filepath.Join(sm.tempDir, fmt.Sprintf("%s_%d.tmp", sessionIDHex, fileIndex))
}

// MoveToStorage finalises an upload: moves temp → storage and registers it.
func (sm *StorageManager) MoveToStorage(tempPath, fileID string, meta protocol.FileMetadata, ttlSeconds int, maxDownloads int) (*StoredFile, error) {
	destPath := filepath.Join(sm.storageDir, fileID)
	if err := os.Rename(tempPath, destPath); err != nil {
		return nil, fmt.Errorf("move %s → %s: %w", tempPath, destPath, err)
	}

	sf := &StoredFile{
		ID:           fileID,
		Path:         destPath,
		Metadata:     meta,
		MaxDownloads: maxDownloads,
		ExpiresAt:    time.Now().Add(time.Duration(ttlSeconds) * time.Second),
	}

	sm.mu.Lock()
	sm.files[fileID] = sf
	sm.mu.Unlock()

	return sf, nil
}

// GetFile returns a stored file by ID, or nil.
func (sm *StorageManager) GetFile(fileID string) *StoredFile {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.files[fileID]
}

// DeleteFile removes a file from disk and the registry.
func (sm *StorageManager) DeleteFile(fileID string) {
	sm.mu.Lock()
	sf, ok := sm.files[fileID]
	if ok {
		delete(sm.files, fileID)
	}
	sm.mu.Unlock()

	if ok {
		os.Remove(sf.Path) // best-effort
	}
}

// CleanupTemp removes a temp file (best-effort).
func (sm *StorageManager) CleanupTemp(tempPath string) {
	os.Remove(tempPath)
}

// ExpiredFiles returns all files that have exceeded TTL or download limit.
func (sm *StorageManager) ExpiredFiles() []*StoredFile {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	var expired []*StoredFile
	for _, sf := range sm.files {
		if sf.IsExpired() && atomic.LoadInt32(&sf.ActiveDownloads) == 0 {
			expired = append(expired, sf)
		}
	}
	return expired
}
