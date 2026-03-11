package index

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Harish-hex/Lantern/internal/protocol"
)

type SessionSnapshot struct {
	ID               string                 `json:"id"`
	State            string                 `json:"state"`
	CreatedAt        time.Time              `json:"created_at"`
	LastActivity     time.Time              `json:"last_activity"`
	CurrentFileIndex uint32                 `json:"current_file_index"`
	Files            []FileTransferSnapshot `json:"files"`
}

type FileTransferSnapshot struct {
	FileIndex      uint32                `json:"file_index"`
	Metadata       protocol.FileMetadata `json:"metadata"`
	TempPath       string                `json:"temp_path"`
	StoredFileID   string                `json:"stored_file_id,omitempty"`
	ReceivedChunks uint32                `json:"received_chunks"`
	LastAckedSeq   uint32                `json:"last_acked_seq"`
	State          string                `json:"state"`
	StartedAt      time.Time             `json:"started_at"`
}

type StoredFileSnapshot struct {
	ID            string                `json:"id"`
	Path          string                `json:"path"`
	Metadata      protocol.FileMetadata `json:"metadata"`
	DownloadCount int32                 `json:"download_count"`
	MaxDownloads  int                   `json:"max_downloads"`
	ExpiresAt     time.Time             `json:"expires_at"`
}

type Store interface {
	SaveSession(*SessionSnapshot) error
	DeleteSession(id string) error
	LoadSessions() ([]*SessionSnapshot, error)

	SaveStoredFile(*StoredFileSnapshot) error
	DeleteStoredFile(id string) error
	LoadStoredFiles() ([]*StoredFileSnapshot, error)
}

type JSONStore struct {
	baseDir     string
	sessionsDir string
	filesDir    string
	mu          sync.Mutex
}

func NewJSONStore(baseDir string) (*JSONStore, error) {
	js := &JSONStore{
		baseDir:     baseDir,
		sessionsDir: filepath.Join(baseDir, "sessions"),
		filesDir:    filepath.Join(baseDir, "files"),
	}
	for _, dir := range []string{js.baseDir, js.sessionsDir, js.filesDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create index dir %s: %w", dir, err)
		}
	}
	return js, nil
}

func (s *JSONStore) SaveSession(snapshot *SessionSnapshot) error {
	return s.write(filepath.Join(s.sessionsDir, snapshot.ID+".json"), snapshot)
}

func (s *JSONStore) DeleteSession(id string) error {
	return s.remove(filepath.Join(s.sessionsDir, id+".json"))
}

func (s *JSONStore) LoadSessions() ([]*SessionSnapshot, error) {
	var out []*SessionSnapshot
	if err := s.loadDir(s.sessionsDir, func(data []byte) error {
		var snap SessionSnapshot
		if err := json.Unmarshal(data, &snap); err != nil {
			return err
		}
		out = append(out, &snap)
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *JSONStore) SaveStoredFile(snapshot *StoredFileSnapshot) error {
	return s.write(filepath.Join(s.filesDir, snapshot.ID+".json"), snapshot)
}

func (s *JSONStore) DeleteStoredFile(id string) error {
	return s.remove(filepath.Join(s.filesDir, id+".json"))
}

func (s *JSONStore) LoadStoredFiles() ([]*StoredFileSnapshot, error) {
	var out []*StoredFileSnapshot
	if err := s.loadDir(s.filesDir, func(data []byte) error {
		var snap StoredFileSnapshot
		if err := json.Unmarshal(data, &snap); err != nil {
			return err
		}
		out = append(out, &snap)
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *JSONStore) write(path string, v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return fmt.Errorf("write snapshot %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename snapshot %s: %w", path, err)
	}
	return nil
}

func (s *JSONStore) remove(path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove snapshot %s: %w", path, err)
	}
	return nil
}

func (s *JSONStore) loadDir(dir string, decode func([]byte) error) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read index dir %s: %w", dir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return fmt.Errorf("read snapshot %s: %w", entry.Name(), err)
		}
		if err := decode(data); err != nil {
			return fmt.Errorf("decode snapshot %s: %w", entry.Name(), err)
		}
	}
	return nil
}
