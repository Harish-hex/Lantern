package server

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Harish-hex/Lantern/internal/config"
	"github.com/Harish-hex/Lantern/internal/index"
	"github.com/Harish-hex/Lantern/internal/protocol"
)

func testConfigWithTempPaths(t *testing.T) config.Config {
	t.Helper()
	root := t.TempDir()
	cfg := config.Default()
	cfg.StorageDir = filepath.Join(root, "storage")
	cfg.TempDir = filepath.Join(root, "tmp")
	cfg.IndexDir = filepath.Join(root, "index")
	return cfg
}

func TestRestoreSessionFromSnapshotRehydratesHasherAndProgress(t *testing.T) {
	cfg := config.Default()
	cfg.ChunkSize = 4

	dir := t.TempDir()
	tempPath := filepath.Join(dir, "resume.tmp")
	if err := os.WriteFile(tempPath, []byte("abcdefgh"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var sid [16]byte
	copy(sid[:], []byte("0123456789abcdef"))

	snapshot := &index.SessionSnapshot{
		ID:           hex.EncodeToString(sid[:]),
		State:        StatePaused.String(),
		CreatedAt:    time.Now().Add(-2 * time.Minute),
		LastActivity: time.Now().Add(-30 * time.Second),
		Files: []index.FileTransferSnapshot{{
			FileIndex: 0,
			Metadata: protocol.FileMetadata{
				Filename:     "resume.bin",
				Size:         8,
				ChunkSize:    4,
				TotalChunks:  2,
				ChecksumFull: "sha256:9c56cc51b374c3ba189210d5b6d4bf57790d351c96c47c02190ecf1e430635ab",
			},
			TempPath:  tempPath,
			State:     FileStateReceiving.String(),
			StartedAt: time.Now().Add(-1 * time.Minute),
		}},
	}

	sess, err := restoreSessionFromSnapshot(snapshot, cfg)
	if err != nil {
		t.Fatalf("restoreSessionFromSnapshot: %v", err)
	}

	ft := sess.Files[0]
	if ft == nil {
		t.Fatal("restored file transfer missing")
	}
	if ft.ReceivedChunks != 2 {
		t.Fatalf("ReceivedChunks = %d, want 2", ft.ReceivedChunks)
	}
	if ft.LastAckedSeq != 2 {
		t.Fatalf("LastAckedSeq = %d, want 2", ft.LastAckedSeq)
	}
	if got := ft.SHA256.Finalize(); got != snapshot.Files[0].Metadata.ChecksumFull {
		t.Fatalf("rehydrated hash = %s, want %s", got, snapshot.Files[0].Metadata.ChecksumFull)
	}
}

func TestStoredFileSnapshotConversion(t *testing.T) {
	expires := time.Now().Add(10 * time.Minute).UTC()
	sf := &StoredFile{
		ID:           "file-123",
		Path:         "/tmp/file-123",
		MaxDownloads: 3,
		ExpiresAt:    expires,
		Metadata: protocol.FileMetadata{
			Filename: "hello.txt",
			Size:     64,
		},
	}

	snapshot := toStoredFileSnapshot(sf)
	roundTrip := storedFileFromSnapshot(snapshot)
	if roundTrip.ID != sf.ID {
		t.Fatalf("ID = %q, want %q", roundTrip.ID, sf.ID)
	}
	if roundTrip.Metadata.Filename != sf.Metadata.Filename {
		t.Fatalf("Filename = %q, want %q", roundTrip.Metadata.Filename, sf.Metadata.Filename)
	}
	if !roundTrip.ExpiresAt.Equal(expires) {
		t.Fatalf("ExpiresAt = %v, want %v", roundTrip.ExpiresAt, expires)
	}
}

func TestRestoreSessionsSkipsExpiredSnapshots(t *testing.T) {
	cfg := testConfigWithTempPaths(t)
	cfg.ResumeTTL = 30 * time.Second

	idx, err := index.NewJSONStore(cfg.IndexDir)
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}

	var sid [16]byte
	copy(sid[:], []byte("expired-session!!"))

	snapshot := &index.SessionSnapshot{
		ID:           hex.EncodeToString(sid[:]),
		State:        StatePaused.String(),
		CreatedAt:    time.Now().Add(-5 * time.Minute),
		LastActivity: time.Now().Add(-2 * time.Minute),
	}
	if err := idx.SaveSession(snapshot); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	srv, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if got := len(srv.store.All()); got != 0 {
		t.Fatalf("restored sessions = %d, want 0", got)
	}

	remaining, err := idx.LoadSessions()
	if err != nil {
		t.Fatalf("LoadSessions: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("remaining snapshots = %d, want 0", len(remaining))
	}
}

func TestRestoreSessionsDropsSnapshotWhenTempFileMissing(t *testing.T) {
	cfg := testConfigWithTempPaths(t)
	cfg.ResumeTTL = 5 * time.Minute

	idx, err := index.NewJSONStore(cfg.IndexDir)
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}

	var sid [16]byte
	copy(sid[:], []byte("missing-temp-file"))

	snapshot := &index.SessionSnapshot{
		ID:           hex.EncodeToString(sid[:]),
		State:        StatePaused.String(),
		CreatedAt:    time.Now().Add(-2 * time.Minute),
		LastActivity: time.Now().Add(-10 * time.Second),
		Files: []index.FileTransferSnapshot{{
			FileIndex: 0,
			Metadata: protocol.FileMetadata{
				Filename:    "resume.bin",
				Size:        4,
				ChunkSize:   4,
				TotalChunks: 1,
			},
			TempPath: filepath.Join(cfg.TempDir, "does-not-exist.tmp"),
			State:    FileStateReceiving.String(),
		}},
	}
	if err := idx.SaveSession(snapshot); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	srv, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if got := len(srv.store.All()); got != 0 {
		t.Fatalf("restored sessions = %d, want 0", got)
	}

	remaining, err := idx.LoadSessions()
	if err != nil {
		t.Fatalf("LoadSessions: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("remaining snapshots = %d, want 0", len(remaining))
	}
}

func TestRestoreStoredFilesFiltersExpiredAndMissing(t *testing.T) {
	cfg := testConfigWithTempPaths(t)

	idx, err := index.NewJSONStore(cfg.IndexDir)
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}

	validPath := filepath.Join(cfg.StorageDir, "file-valid")
	if err := os.MkdirAll(cfg.StorageDir, 0o755); err != nil {
		t.Fatalf("MkdirAll storage: %v", err)
	}
	if err := os.WriteFile(validPath, []byte("ok"), 0o644); err != nil {
		t.Fatalf("WriteFile valid: %v", err)
	}

	valid := &index.StoredFileSnapshot{
		ID:           "valid",
		Path:         validPath,
		ExpiresAt:    time.Now().Add(10 * time.Minute),
		MaxDownloads: 3,
		Metadata: protocol.FileMetadata{
			Filename: "valid.bin",
			Size:     2,
		},
	}
	expired := &index.StoredFileSnapshot{
		ID:        "expired",
		Path:      filepath.Join(cfg.StorageDir, "file-expired"),
		ExpiresAt: time.Now().Add(-1 * time.Minute),
		Metadata:  protocol.FileMetadata{Filename: "expired.bin", Size: 1},
	}
	missing := &index.StoredFileSnapshot{
		ID:        "missing",
		Path:      filepath.Join(cfg.StorageDir, "file-missing"),
		ExpiresAt: time.Now().Add(10 * time.Minute),
		Metadata:  protocol.FileMetadata{Filename: "missing.bin", Size: 1},
	}

	for _, snap := range []*index.StoredFileSnapshot{valid, expired, missing} {
		if err := idx.SaveStoredFile(snap); err != nil {
			t.Fatalf("SaveStoredFile(%s): %v", snap.ID, err)
		}
	}

	srv, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if got := srv.storage.GetFile("valid"); got == nil {
		t.Fatal("expected valid stored file to be restored")
	}
	if got := srv.storage.GetFile("expired"); got != nil {
		t.Fatal("expired file should not be restored")
	}
	if got := srv.storage.GetFile("missing"); got != nil {
		t.Fatal("missing file should not be restored")
	}

	remaining, err := idx.LoadStoredFiles()
	if err != nil {
		t.Fatalf("LoadStoredFiles: %v", err)
	}
	if len(remaining) != 1 || remaining[0].ID != "valid" {
		t.Fatalf("remaining stored snapshots = %+v, want only valid", remaining)
	}
}
