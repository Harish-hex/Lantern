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
				ChecksumFull: "sha256:9c56cc51b374c3ba189210d5b91871a5df129f3f21257d2f3c048ceaf41b77ad",
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
