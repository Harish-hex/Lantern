// Package server implements the Lantern file transfer server.
package server

import (
	"fmt"
	"log"
	"net"
	"time"

	"github.com/Harish-hex/Lantern/internal/config"
)

// Server is the main entry point for the Lantern daemon.
type Server struct {
	cfg      config.Config
	listener net.Listener
	store    *SessionStore
	storage  *StorageManager
	upload   *Semaphore
	download *Semaphore
	quit     chan struct{}
}

// New creates and wires a Server but does not start listening.
func New(cfg config.Config) (*Server, error) {
	storage, err := NewStorageManager(cfg.StorageDir, cfg.TempDir)
	if err != nil {
		return nil, fmt.Errorf("init storage: %w", err)
	}

	s := &Server{
		cfg:     cfg,
		store:   NewSessionStore(),
		storage: storage,
		upload:  NewSemaphore(cfg.MaxUploadConcurrency),
		quit:    make(chan struct{}),
	}

	if cfg.MaxDownloadConcurrency > 0 {
		s.download = NewSemaphore(cfg.MaxDownloadConcurrency)
	}

	return s, nil
}

// Bridge returns a web.Bridge wired to this server's internal subsystems.
// Call this after New() and before Start().
func (s *Server) Bridge() *Bridge {
	return &Bridge{
		storage: s.storage,
		upload:  s.upload,
		cfg:     s.cfg,
	}
}

// Start binds to the configured address and enters the accept loop.
func (s *Server) Start() error {
	addr := fmt.Sprintf("%s:%d", s.cfg.ListenAddr, s.cfg.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	s.listener = ln

	log.Printf("[server] Lantern listening on %s", addr)
	log.Printf("[server] storage: %s  |  temp: %s", s.cfg.StorageDir, s.cfg.TempDir)
	log.Printf("[server] max upload concurrency: %d  |  chunk size: %d KB",
		s.cfg.MaxUploadConcurrency, s.cfg.ChunkSize/1024)

	// Start background cleanup goroutine
	go s.cleanupLoop()

	// Accept loop
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-s.quit:
				return nil // graceful shutdown
			default:
				log.Printf("[server] accept error: %v", err)
				continue
			}
		}
		handler := NewHandler(s.cfg, s.store, s.storage, s.upload, s.download)
		go handler.Handle(conn)
	}
}

// Stop gracefully shuts down the server.
func (s *Server) Stop() {
	close(s.quit)
	if s.listener != nil {
		s.listener.Close()
	}
	log.Println("[server] shutdown complete")
}

// cleanupLoop periodically reaps expired files and idle sessions.
func (s *Server) cleanupLoop() {
	ticker := time.NewTicker(s.cfg.CleanupTick)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.reapExpiredFiles()
			s.reapIdleSessions()
		case <-s.quit:
			return
		}
	}
}

// reapExpiredFiles deletes files past their TTL or download limit.
func (s *Server) reapExpiredFiles() {
	for _, sf := range s.storage.ExpiredFiles() {
		log.Printf("[cleanup] deleting expired file %s (%s)", sf.ID, sf.Metadata.Filename)
		s.storage.DeleteFile(sf.ID)
	}
}

// reapIdleSessions removes sessions that have been idle too long.
func (s *Server) reapIdleSessions() {
	for _, sess := range s.store.All() {
		if sess.IdleFor() > s.cfg.SessionTimeout {
			state := sess.GetState()
			if state == StateActive || state == StatePaused {
				log.Printf("[cleanup] reaping idle session %x (idle %v)", sess.ID, sess.IdleFor())
				sess.SetState(StateFailed)
				s.store.Delete(sess.ID)
				s.upload.Release()
			}
		}
	}
}
