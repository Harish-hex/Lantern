package server

import (
	"fmt"
	"net"
	"sync"
)

// SessionStore is the single source of truth for all active sessions.
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[[16]byte]*Session
}

// NewSessionStore creates an empty store.
func NewSessionStore() *SessionStore {
	return &SessionStore{
		sessions: make(map[[16]byte]*Session),
	}
}

// Create adds a new session. Returns an error if the ID already exists.
func (ss *SessionStore) Create(s *Session) error {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	if _, exists := ss.sessions[s.ID]; exists {
		return fmt.Errorf("session %x already exists", s.ID)
	}
	ss.sessions[s.ID] = s
	return nil
}

// Get retrieves a session by ID. Returns nil if not found.
func (ss *SessionStore) Get(id [16]byte) *Session {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	return ss.sessions[id]
}

// Delete removes a session from the store.
func (ss *SessionStore) Delete(id [16]byte) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	delete(ss.sessions, id)
}

// GetByConn finds a session associated with a specific connection.
// Returns nil if none match (O(n) scan — fine for LAN-scale).
func (ss *SessionStore) GetByConn(conn net.Conn) *Session {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	for _, s := range ss.sessions {
		if s.Conn == conn {
			return s
		}
	}
	return nil
}

// All returns a snapshot of all current sessions (for cleanup iteration).
func (ss *SessionStore) All() []*Session {
	ss.mu.RLock()
	defer ss.mu.RUnlock()
	result := make([]*Session, 0, len(ss.sessions))
	for _, s := range ss.sessions {
		result = append(result, s)
	}
	return result
}
