package index

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Harish-hex/Lantern/internal/protocol"
)

func TestJSONStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := NewJSONStore(dir)
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}

	session := &SessionSnapshot{
		ID:               "abc123",
		State:            "paused",
		CreatedAt:        time.Now().UTC(),
		LastActivity:     time.Now().UTC(),
		CurrentFileIndex: 2,
		Files: []FileTransferSnapshot{{
			FileIndex:      2,
			TempPath:       filepath.Join(dir, "upload.tmp"),
			ReceivedChunks: 8,
			LastAckedSeq:   8,
			State:          "receiving",
			StartedAt:      time.Now().UTC(),
			Metadata: protocol.FileMetadata{
				Filename:    "sample.bin",
				Size:        1024,
				ChunkSize:   128 * 1024,
				TotalChunks: 8,
			},
		}},
	}
	if err := store.SaveSession(session); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	file := &StoredFileSnapshot{
		ID:            "file-1",
		Path:          filepath.Join(dir, "file-1"),
		DownloadCount: 2,
		MaxDownloads:  5,
		ExpiresAt:     time.Now().Add(5 * time.Minute).UTC(),
		Metadata: protocol.FileMetadata{
			Filename: "hello.txt",
			Size:     12,
		},
	}
	if err := store.SaveStoredFile(file); err != nil {
		t.Fatalf("SaveStoredFile: %v", err)
	}

	sessions, err := store.LoadSessions()
	if err != nil {
		t.Fatalf("LoadSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("LoadSessions len = %d, want 1", len(sessions))
	}
	if sessions[0].Files[0].LastAckedSeq != 8 {
		t.Fatalf("LastAckedSeq = %d, want 8", sessions[0].Files[0].LastAckedSeq)
	}

	files, err := store.LoadStoredFiles()
	if err != nil {
		t.Fatalf("LoadStoredFiles: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("LoadStoredFiles len = %d, want 1", len(files))
	}
	if files[0].Metadata.Filename != "hello.txt" {
		t.Fatalf("filename = %q, want hello.txt", files[0].Metadata.Filename)
	}

	if err := store.DeleteSession(session.ID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if err := store.DeleteStoredFile(file.ID); err != nil {
		t.Fatalf("DeleteStoredFile: %v", err)
	}

	sessions, err = store.LoadSessions()
	if err != nil {
		t.Fatalf("LoadSessions after delete: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("LoadSessions after delete len = %d, want 0", len(sessions))
	}

	files, err = store.LoadStoredFiles()
	if err != nil {
		t.Fatalf("LoadStoredFiles after delete: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("LoadStoredFiles after delete len = %d, want 0", len(files))
	}
}
