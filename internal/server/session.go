package server

import (
	"net"
	"sync"
	"time"

	"github.com/Harish-hex/Lantern/internal/protocol"
)

// SessionState represents the lifecycle of a transfer session.
type SessionState int

const (
	StateNew       SessionState = iota
	StateActive                        // Actively transferring
	StatePaused                        // Client disconnected gracefully; resumable
	StateCompleted                     // All files transferred & verified
	StateFailed                        // Unrecoverable error
)

func (s SessionState) String() string {
	return [...]string{"new", "active", "paused", "completed", "failed"}[s]
}

// FileTransferState tracks one file within a session.
type FileTransferState int

const (
	FileStatePending    FileTransferState = iota
	FileStateReceiving
	FileStateVerifying
	FileStateComplete
	FileStateFailed
)

// FileTransfer tracks an individual file being uploaded.
type FileTransfer struct {
	Metadata       protocol.FileMetadata
	TempPath       string
	StoredFileID   string // Assigned after successful verification
	ReceivedChunks uint32
	LastAckedSeq   uint32
	SHA256         *protocol.SHA256Hasher
	State          FileTransferState
	StartedAt      time.Time
}

// Session represents one client connection and the set of files it is
// transferring. Sessions can survive a disconnect (Paused) and be
// resumed with the same SessionID.
type Session struct {
	mu           sync.Mutex
	ID           [16]byte
	State        SessionState
	CreatedAt    time.Time
	LastActivity time.Time
	Conn         net.Conn

	Files            map[uint32]*FileTransfer // keyed by file_index
	CurrentFileIndex uint32
	HoldsUploadSlot  bool
}

// NewSession creates a fresh session bound to the given connection.
func NewSession(id [16]byte, conn net.Conn) *Session {
	now := time.Now()
	return &Session{
		ID:           id,
		State:        StateNew,
		CreatedAt:    now,
		LastActivity: now,
		Conn:         conn,
		Files:        make(map[uint32]*FileTransfer),
	}
}

// Touch updates LastActivity to prevent idle-reaping.
func (s *Session) Touch() {
	s.mu.Lock()
	s.LastActivity = time.Now()
	s.mu.Unlock()
}

// SetState transitions the session state.
func (s *Session) SetState(state SessionState) {
	s.mu.Lock()
	s.State = state
	s.mu.Unlock()
}

// GetState returns the current state (thread-safe).
func (s *Session) GetState() SessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.State
}

// IdleFor returns how long the session has been idle.
func (s *Session) IdleFor() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Since(s.LastActivity)
}
