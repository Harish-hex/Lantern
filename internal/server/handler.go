package server

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"

	"github.com/Harish-hex/Lantern/internal/config"
	"github.com/Harish-hex/Lantern/internal/protocol"
)

// Handler dispatches incoming Lantern packets for a single connection.
type Handler struct {
	cfg     config.Config
	store   *SessionStore
	storage *StorageManager
	upload  *Semaphore
}

// NewHandler creates a handler wired to shared state.
func NewHandler(cfg config.Config, store *SessionStore, storage *StorageManager, upload *Semaphore) *Handler {
	return &Handler{
		cfg:     cfg,
		store:   store,
		storage: storage,
		upload:  upload,
	}
}

// Handle runs the packet read-loop for a single client connection.
// It reads packets until the connection closes or an unrecoverable error occurs.
func (h *Handler) Handle(conn net.Conn) {
	defer conn.Close()
	remote := conn.RemoteAddr().String()
	log.Printf("[handler] new connection from %s", remote)

	for {
		hdr, payload, crc, err := protocol.ReadPacket(conn)
		if err != nil {
			// EOF is normal (client closed connection)
			if opErr, ok := err.(*net.OpError); ok && opErr.Err.Error() == "EOF" {
				log.Printf("[handler] %s disconnected", remote)
			} else {
				log.Printf("[handler] %s read error: %v", remote, err)
			}
			// Try to pause session if one existed
			if sess := h.store.GetByConn(conn); sess != nil {
				sess.SetState(StatePaused)
				log.Printf("[handler] session %x paused", sess.ID)
			}
			return
		}

		switch hdr.MsgType {
		case protocol.MsgHandshake:
			h.handleHandshake(conn, hdr, payload)
		case protocol.MsgFileHeader:
			h.handleFileHeader(conn, hdr, payload)
		case protocol.MsgChunk:
			h.handleChunk(conn, hdr, payload, crc)
		case protocol.MsgControl:
			h.handleControl(conn, hdr, payload)
		default:
			log.Printf("[handler] %s unknown message type: 0x%02x", remote, hdr.MsgType)
			h.sendError(conn, hdr.SessionID, hdr.Sequence, "UNKNOWN_MSG_TYPE")
		}
	}
}

// handleHandshake processes a HANDSHAKE packet.
func (h *Handler) handleHandshake(conn net.Conn, hdr protocol.Header, _ []byte) {
	sid := hdr.SessionID

	// Check if this is a resume
	if existing := h.store.Get(sid); existing != nil {
		if existing.GetState() == StatePaused {
			existing.mu.Lock()
			existing.State = StateActive
			existing.Conn = conn
			existing.mu.Unlock()
			existing.Touch()
			log.Printf("[handler] session %x resumed from %s", sid, conn.RemoteAddr())
			h.sendSessionACK(conn, sid)
			return
		}
		// Session already active with another connection → reject
		h.sendControlMsg(conn, sid, 0, protocol.ControlPayload{
			Type:    protocol.CtrlReject,
			Message: protocol.ErrSessionConflict,
		})
		return
	}

	// Check upload capacity
	if !h.upload.TryAcquire() {
		h.sendControlMsg(conn, sid, 0, protocol.ControlPayload{
			Type:       protocol.CtrlBusy,
			Message:    "server at upload capacity",
			RetryAfter: 5,
		})
		return
	}

	sess := NewSession(sid, conn)
	sess.SetState(StateActive)
	if err := h.store.Create(sess); err != nil {
		h.upload.Release()
		h.sendError(conn, sid, 0, err.Error())
		return
	}

	log.Printf("[handler] new session %x from %s", sid, conn.RemoteAddr())
	h.sendSessionACK(conn, sid)
}

// handleFileHeader processes a FILE_HEADER packet.
func (h *Handler) handleFileHeader(conn net.Conn, hdr protocol.Header, payload []byte) {
	sess := h.store.Get(hdr.SessionID)
	if sess == nil {
		h.sendError(conn, hdr.SessionID, 0, "no active session")
		return
	}
	sess.Touch()

	var meta protocol.FileMetadata
	if err := json.Unmarshal(payload, &meta); err != nil {
		h.sendError(conn, hdr.SessionID, 0, "invalid file metadata: "+err.Error())
		return
	}

	// Validate file size
	if meta.Size > h.cfg.MaxFileSize {
		h.sendError(conn, hdr.SessionID, 0, fmt.Sprintf("file too large: %d > %d", meta.Size, h.cfg.MaxFileSize))
		return
	}

	// Create temp file path
	sidHex := hex.EncodeToString(hdr.SessionID[:])
	tempPath := h.storage.CreateTemp(sidHex, meta.FileIndex)

	ft := &FileTransfer{
		Metadata: meta,
		TempPath: tempPath,
		SHA256:   protocol.NewSHA256Hasher(),
		State:    FileStateReceiving,
	}

	sess.mu.Lock()
	sess.Files[meta.FileIndex] = ft
	sess.CurrentFileIndex = meta.FileIndex
	sess.mu.Unlock()

	log.Printf("[handler] session %x: file %d/%d '%s' (%d bytes, %d chunks)",
		hdr.SessionID, meta.FileIndex+1, meta.TotalFiles, meta.Filename, meta.Size, meta.TotalChunks)

	// ACK the file header
	h.sendControlMsg(conn, hdr.SessionID, 0, protocol.ControlPayload{
		Type:    protocol.CtrlACK,
		Message: fmt.Sprintf("ready for file %d", meta.FileIndex),
	})
}

// handleChunk processes a CHUNK packet.
func (h *Handler) handleChunk(conn net.Conn, hdr protocol.Header, payload []byte, crc uint32) {
	sess := h.store.Get(hdr.SessionID)
	if sess == nil {
		h.sendError(conn, hdr.SessionID, 0, "no active session")
		return
	}
	sess.Touch()

	sess.mu.Lock()
	ft := sess.Files[sess.CurrentFileIndex]
	sess.mu.Unlock()

	if ft == nil {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, "no active file transfer")
		return
	}

	// Verify CRC-32
	if !protocol.VerifyChecksum(payload, crc) {
		log.Printf("[handler] session %x: CRC mismatch on chunk %d", hdr.SessionID, hdr.Sequence)
		h.sendControlMsg(conn, hdr.SessionID, hdr.Sequence, protocol.ControlPayload{
			Type:    protocol.CtrlNAK,
			Seq:     hdr.Sequence,
			Message: protocol.ErrCRCMismatch,
		})
		return
	}

	// Write payload to temp file
	f, err := os.OpenFile(ft.TempPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, protocol.ErrDiskFull)
		return
	}
	if _, err := f.Write(payload); err != nil {
		f.Close()
		h.sendError(conn, hdr.SessionID, hdr.Sequence, protocol.ErrDiskFull)
		return
	}
	f.Close()

	// Update running SHA-256
	ft.SHA256.Update(payload)
	ft.ReceivedChunks++
	ft.LastAckedSeq = hdr.Sequence

	// ACK the chunk
	h.sendControlMsg(conn, hdr.SessionID, hdr.Sequence, protocol.ControlPayload{
		Type: protocol.CtrlACK,
		Seq:  hdr.Sequence,
	})

	// Check if this was the last chunk
	if hdr.HasFlag(protocol.FlagLastChunk) {
		h.finalizeFile(conn, hdr.SessionID, sess, ft)
	}
}

// finalizeFile verifies the full SHA-256, moves temp → storage, and reports result.
func (h *Handler) finalizeFile(conn net.Conn, sid [16]byte, sess *Session, ft *FileTransfer) {
	ft.State = FileStateVerifying

	// Verify chunk count
	if ft.ReceivedChunks != ft.Metadata.TotalChunks {
		log.Printf("[handler] session %x: chunk count mismatch: got %d, expected %d",
			sid, ft.ReceivedChunks, ft.Metadata.TotalChunks)
		ft.State = FileStateFailed
		h.sendError(conn, sid, 0, protocol.ErrChunkCountErr)
		return
	}

	// Verify full-file SHA-256
	computedHash := ft.SHA256.Finalize()
	if computedHash != ft.Metadata.ChecksumFull {
		log.Printf("[handler] session %x: integrity failed: %s != %s",
			sid, computedHash, ft.Metadata.ChecksumFull)
		ft.State = FileStateFailed
		h.sendError(conn, sid, 0, protocol.ErrIntegrityFailed)
		h.storage.CleanupTemp(ft.TempPath)
		return
	}

	// Generate a file ID and move to storage
	fileID := fmt.Sprintf("%s_%d", hex.EncodeToString(sid[:8]), ft.Metadata.FileIndex)

	ttl := ft.Metadata.TTLSeconds
	if ttl <= 0 {
		ttl = h.cfg.TTLDefault
	}
	maxDL := ft.Metadata.MaxDownloads
	if maxDL <= 0 {
		maxDL = h.cfg.MaxDownloads
	}

	_, err := h.storage.MoveToStorage(ft.TempPath, fileID, ft.Metadata, ttl, maxDL)
	if err != nil {
		log.Printf("[handler] session %x: storage error: %v", sid, err)
		ft.State = FileStateFailed
		h.sendError(conn, sid, 0, err.Error())
		return
	}

	ft.State = FileStateComplete
	ft.StoredFileID = fileID
	log.Printf("[handler] session %x: file '%s' stored as %s", sid, ft.Metadata.Filename, fileID)

	h.sendControlMsg(conn, sid, 0, protocol.ControlPayload{
		Type:   protocol.CtrlComplete,
		FileID: fileID,
	})

	// Check if all files in session are complete
	h.checkSessionComplete(conn, sid, sess)
}

// checkSessionComplete checks if all files have been transferred.
func (h *Handler) checkSessionComplete(conn net.Conn, sid [16]byte, sess *Session) {
	sess.mu.Lock()
	defer sess.mu.Unlock()

	allDone := true
	var succeeded, failed []string

	for _, ft := range sess.Files {
		switch ft.State {
		case FileStateComplete:
			succeeded = append(succeeded, ft.StoredFileID)
		case FileStateFailed:
			failed = append(failed, ft.Metadata.Filename)
		default:
			allDone = false
		}
	}

	if !allDone {
		return
	}

	if len(failed) > 0 {
		sess.State = StateFailed
	} else {
		sess.State = StateCompleted
	}

	h.upload.Release()

	log.Printf("[handler] session %x complete: %d succeeded, %d failed",
		sid, len(succeeded), len(failed))
}

// handleControl processes a CONTROL packet from the client.
func (h *Handler) handleControl(conn net.Conn, hdr protocol.Header, payload []byte) {
	sess := h.store.Get(hdr.SessionID)
	if sess != nil {
		sess.Touch()
	}

	var ctrl protocol.ControlPayload
	if err := json.Unmarshal(payload, &ctrl); err != nil {
		h.sendError(conn, hdr.SessionID, 0, "invalid control payload")
		return
	}

	switch ctrl.Type {
	case protocol.CtrlDownload:
		h.handleDownload(conn, hdr, ctrl)
	default:
		log.Printf("[handler] session %x: unhandled control type: %s", hdr.SessionID, ctrl.Type)
	}
}

// handleDownload serves a stored file to the client.
func (h *Handler) handleDownload(conn net.Conn, hdr protocol.Header, ctrl protocol.ControlPayload) {
	sf := h.storage.GetFile(ctrl.FileID)
	if sf == nil {
		h.sendError(conn, hdr.SessionID, 0, "file not found: "+ctrl.FileID)
		return
	}

	if sf.IsExpired() {
		h.sendError(conn, hdr.SessionID, 0, protocol.ErrFileExpired)
		return
	}

	// Open the file
	f, err := os.Open(sf.Path)
	if err != nil {
		h.sendError(conn, hdr.SessionID, 0, "cannot open file: "+err.Error())
		return
	}
	defer f.Close()

	// Increment download count
	sf.IncrementDownloads()

	// Send FILE_HEADER
	metaBytes, _ := json.Marshal(sf.Metadata)
	fhdr := protocol.NewHeader(protocol.MsgFileHeader, 0, 0, 0, hdr.SessionID)
	if err := protocol.WritePacket(conn, fhdr, metaBytes); err != nil {
		log.Printf("[handler] download FILE_HEADER write error: %v", err)
		return
	}

	// Stream chunks
	chunkBuf := make([]byte, h.cfg.ChunkSize)
	var seq uint32

	for {
		n, readErr := f.Read(chunkBuf)
		if n > 0 {
			seq++
			flags := byte(0)
			if readErr != nil { // EOF on this read → last chunk
				flags |= protocol.FlagLastChunk
			}
			chdr := protocol.NewHeader(protocol.MsgChunk, flags, 0, seq, hdr.SessionID)
			if err := protocol.WritePacket(conn, chdr, chunkBuf[:n]); err != nil {
				log.Printf("[handler] download chunk %d write error: %v", seq, err)
				return
			}
		}
		if readErr != nil {
			break
		}
	}

	log.Printf("[handler] download complete: %s (%d chunks)", ctrl.FileID, seq)
}

// ----- Helper senders -----

func (h *Handler) sendSessionACK(conn net.Conn, sid [16]byte) {
	h.sendControlMsg(conn, sid, 0, protocol.ControlPayload{
		Type:      protocol.CtrlACK,
		SessionID: hex.EncodeToString(sid[:]),
	})
}

func (h *Handler) sendError(conn net.Conn, sid [16]byte, seq uint32, msg string) {
	h.sendControlMsg(conn, sid, seq, protocol.ControlPayload{
		Type:    protocol.CtrlError,
		Seq:     seq,
		Message: msg,
	})
}

func (h *Handler) sendControlMsg(conn net.Conn, sid [16]byte, seq uint32, ctrl protocol.ControlPayload) {
	payload, _ := json.Marshal(ctrl)
	hdr := protocol.NewHeader(protocol.MsgControl, 0, 0, seq, sid)
	if err := protocol.WritePacket(conn, hdr, payload); err != nil {
		log.Printf("[handler] failed to send control to %s: %v", conn.RemoteAddr(), err)
	}
}
