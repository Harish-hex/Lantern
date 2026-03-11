package server

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sync/atomic"
	"time"

	"github.com/Harish-hex/Lantern/internal/config"
	"github.com/Harish-hex/Lantern/internal/index"
	"github.com/Harish-hex/Lantern/internal/protocol"
)

func toStoredFileSnapshot(sf *StoredFile) *index.StoredFileSnapshot {
	return &index.StoredFileSnapshot{
		ID:            sf.ID,
		Path:          sf.Path,
		Metadata:      sf.Metadata,
		DownloadCount: atomic.LoadInt32(&sf.DownloadCount),
		MaxDownloads:  sf.MaxDownloads,
		ExpiresAt:     sf.ExpiresAt,
	}
}

func storedFileFromSnapshot(snapshot *index.StoredFileSnapshot) *StoredFile {
	return &StoredFile{
		ID:            snapshot.ID,
		Path:          snapshot.Path,
		Metadata:      snapshot.Metadata,
		DownloadCount: snapshot.DownloadCount,
		MaxDownloads:  snapshot.MaxDownloads,
		ExpiresAt:     snapshot.ExpiresAt,
	}
}

func (s *Session) Snapshot() *index.SessionSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	files := make([]index.FileTransferSnapshot, 0, len(s.Files))
	for _, ft := range s.Files {
		files = append(files, index.FileTransferSnapshot{
			FileIndex:      ft.Metadata.FileIndex,
			Metadata:       ft.Metadata,
			TempPath:       ft.TempPath,
			StoredFileID:   ft.StoredFileID,
			ReceivedChunks: ft.ReceivedChunks,
			LastAckedSeq:   ft.LastAckedSeq,
			State:          ft.State.String(),
			StartedAt:      ft.StartedAt,
		})
	}

	return &index.SessionSnapshot{
		ID:               hex.EncodeToString(s.ID[:]),
		State:            s.State.String(),
		CreatedAt:        s.CreatedAt,
		LastActivity:     s.LastActivity,
		CurrentFileIndex: s.CurrentFileIndex,
		Files:            files,
	}
}

func restoreSessionFromSnapshot(snapshot *index.SessionSnapshot, cfg config.Config) (*Session, error) {
	sidBytes, err := hex.DecodeString(snapshot.ID)
	if err != nil {
		return nil, fmt.Errorf("decode session id %q: %w", snapshot.ID, err)
	}
	if len(sidBytes) != 16 {
		return nil, fmt.Errorf("invalid session id length: %d", len(sidBytes))
	}

	var sid [16]byte
	copy(sid[:], sidBytes)

	state, ok := parseSessionState(snapshot.State)
	if !ok {
		return nil, fmt.Errorf("unknown session state %q", snapshot.State)
	}

	sess := &Session{
		ID:               sid,
		State:            state,
		CreatedAt:        snapshot.CreatedAt,
		LastActivity:     snapshot.LastActivity,
		Files:            make(map[uint32]*FileTransfer),
		CurrentFileIndex: snapshot.CurrentFileIndex,
	}

	for _, fs := range snapshot.Files {
		fileState, ok := parseFileTransferState(fs.State)
		if !ok {
			return nil, fmt.Errorf("unknown file state %q", fs.State)
		}

		ft, err := restoreFileTransfer(fs, cfg)
		if err != nil {
			return nil, err
		}
		ft.State = fileState
		sess.Files[ft.Metadata.FileIndex] = ft
	}

	return sess, nil
}

func restoreFileTransfer(snapshot index.FileTransferSnapshot, cfg config.Config) (*FileTransfer, error) {
	hasher := protocol.NewSHA256Hasher()
	ft := &FileTransfer{
		Metadata:       snapshot.Metadata,
		TempPath:       snapshot.TempPath,
		StoredFileID:   snapshot.StoredFileID,
		ReceivedChunks: snapshot.ReceivedChunks,
		LastAckedSeq:   snapshot.LastAckedSeq,
		SHA256:         hasher,
		StartedAt:      snapshot.StartedAt,
	}

	if snapshot.TempPath == "" {
		return ft, nil
	}

	file, err := os.Open(snapshot.TempPath)
	if err != nil {
		return nil, fmt.Errorf("open temp file %s: %w", snapshot.TempPath, err)
	}
	defer file.Close()

	if _, err := io.Copy(hasherHashWriter{hasher}, file); err != nil {
		return nil, fmt.Errorf("rehash temp file %s: %w", snapshot.TempPath, err)
	}

	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat temp file %s: %w", snapshot.TempPath, err)
	}
	chunkSize := snapshot.Metadata.ChunkSize
	if chunkSize == 0 {
		chunkSize = cfg.ChunkSize
	}
	if chunkSize > 0 && info.Size() > 0 {
		chunks := uint32(info.Size() / int64(chunkSize))
		if info.Size()%int64(chunkSize) != 0 {
			chunks++
		}
		ft.ReceivedChunks = chunks
		ft.LastAckedSeq = chunks
	}

	return ft, nil
}

type hasherHashWriter struct {
	hasher *protocol.SHA256Hasher
}

func (w hasherHashWriter) Write(p []byte) (int, error) {
	w.hasher.Update(p)
	return len(p), nil
}

func (s *Server) persistSession(sess *Session) {
	if s.idx == nil || sess == nil {
		return
	}
	if err := s.idx.SaveSession(sess.Snapshot()); err != nil {
		s.logf("[index] save session %x: %v", sess.ID, err)
	}
}

func (s *Server) deleteSessionSnapshot(id [16]byte) {
	if s.idx == nil {
		return
	}
	if err := s.idx.DeleteSession(hex.EncodeToString(id[:])); err != nil {
		s.logf("[index] delete session %x: %v", id, err)
	}
}

func (s *Server) restorePersistedState() error {
	if s.idx == nil {
		return nil
	}
	if err := s.restoreStoredFiles(); err != nil {
		return err
	}
	if err := s.restoreSessions(); err != nil {
		return err
	}
	return nil
}

func (s *Server) restoreStoredFiles() error {
	snapshots, err := s.idx.LoadStoredFiles()
	if err != nil {
		return fmt.Errorf("load stored files: %w", err)
	}

	now := time.Now()
	for _, snapshot := range snapshots {
		if now.After(snapshot.ExpiresAt) {
			_ = s.idx.DeleteStoredFile(snapshot.ID)
			continue
		}
		if _, err := os.Stat(snapshot.Path); err != nil {
			_ = s.idx.DeleteStoredFile(snapshot.ID)
			continue
		}
		if err := s.storage.RegisterStoredFile(storedFileFromSnapshot(snapshot)); err != nil {
			return fmt.Errorf("register stored file %s: %w", snapshot.ID, err)
		}
	}
	return nil
}

func (s *Server) restoreSessions() error {
	snapshots, err := s.idx.LoadSessions()
	if err != nil {
		return fmt.Errorf("load sessions: %w", err)
	}

	now := time.Now()
	for _, snapshot := range snapshots {
		if now.Sub(snapshot.LastActivity) > s.cfg.ResumeTTL {
			_ = s.idx.DeleteSession(snapshot.ID)
			continue
		}

		sess, err := restoreSessionFromSnapshot(snapshot, s.cfg)
		if err != nil {
			_ = s.idx.DeleteSession(snapshot.ID)
			continue
		}
		sess.State = StatePaused
		sess.HoldsUploadSlot = false
		if err := s.store.Create(sess); err != nil {
			return fmt.Errorf("restore session %s: %w", snapshot.ID, err)
		}
	}
	return nil
}

func parseSessionState(raw string) (SessionState, bool) {
	switch raw {
	case StateNew.String():
		return StateNew, true
	case StateActive.String():
		return StateActive, true
	case StatePaused.String():
		return StatePaused, true
	case StateCompleted.String():
		return StateCompleted, true
	case StateFailed.String():
		return StateFailed, true
	default:
		return 0, false
	}
}

func parseFileTransferState(raw string) (FileTransferState, bool) {
	switch raw {
	case FileStatePending.String():
		return FileStatePending, true
	case FileStateReceiving.String():
		return FileStateReceiving, true
	case FileStateVerifying.String():
		return FileStateVerifying, true
	case FileStateComplete.String():
		return FileStateComplete, true
	case FileStateFailed.String():
		return FileStateFailed, true
	default:
		return 0, false
	}
}

func releaseUploadSlot(sess *Session, upload *Semaphore) {
	if sess == nil || upload == nil {
		return
	}

	sess.mu.Lock()
	held := sess.HoldsUploadSlot
	if held {
		sess.HoldsUploadSlot = false
	}
	sess.mu.Unlock()

	if held {
		upload.Release()
	}
}

func (s SessionState) isTerminal() bool {
	return s == StateCompleted || s == StateFailed
}

func (s FileTransferState) String() string {
	return [...]string{"pending", "receiving", "verifying", "complete", "failed"}[s]
}
