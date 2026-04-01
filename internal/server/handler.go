package server

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"syscall"
	"time"

	"github.com/Harish-hex/Lantern/internal/config"
	"github.com/Harish-hex/Lantern/internal/protocol"
)

// Handler dispatches incoming Lantern packets for a single connection.
type Handler struct {
	server   *Server
	cfg      config.Config
	store    *SessionStore
	storage  *StorageManager
	upload   *Semaphore
	download *Semaphore
	stats    *Stats
}

// NewHandler creates a handler wired to shared state.
func NewHandler(srv *Server, cfg config.Config, store *SessionStore, storage *StorageManager, upload *Semaphore, download *Semaphore, stats *Stats) *Handler {
	return &Handler{
		server:   srv,
		cfg:      cfg,
		store:    store,
		storage:  storage,
		upload:   upload,
		download: download,
		stats:    stats,
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
			if sess := h.store.GetByConn(conn); sess != nil && sess.GetState() == StateActive {
				sess.SetState(StatePaused)
				log.Printf("[handler] session %x paused", sess.ID)
				h.server.persistSession(sess)
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
			if !existing.HoldsUploadSlot {
				if !h.upload.TryAcquire() {
					existing.mu.Unlock()
					h.sendControlMsg(conn, sid, 0, protocol.ControlPayload{
						Type:       protocol.CtrlBusy,
						Message:    "server at upload capacity",
						RetryAfter: 5,
					})
					return
				}
				existing.HoldsUploadSlot = true
			}
			existing.State = StateActive
			existing.Conn = conn
			existing.mu.Unlock()
			existing.Touch()
			if h.stats != nil {
				h.stats.RecordResume()
			}
			h.server.persistSession(existing)
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
	sess.HoldsUploadSlot = true
	sess.SetState(StateActive)
	if err := h.store.Create(sess); err != nil {
		h.upload.Release()
		h.sendError(conn, sid, 0, err.Error())
		return
	}

	log.Printf("[handler] new session %x from %s", sid, conn.RemoteAddr())
	h.server.persistSession(sess)
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

	// Check available disk space for the target storage directory.
	var stat syscall.Statfs_t
	if err := syscall.Statfs(h.storage.storageDir, &stat); err == nil {
		free := int64(stat.Bavail) * int64(stat.Bsize)
		if meta.Size > free {
			h.sendError(conn, hdr.SessionID, 0, protocol.ErrDiskFull)
			return
		}
	}

	// Create temp file path
	sidHex := hex.EncodeToString(hdr.SessionID[:])
	tempPath := h.storage.CreateTemp(sidHex, meta.FileIndex)

	ft := &FileTransfer{
		Metadata:  meta,
		TempPath:  tempPath,
		SHA256:    protocol.NewSHA256Hasher(),
		State:     FileStateReceiving,
		StartedAt: time.Now(),
	}

	sess.mu.Lock()
	sess.Files[meta.FileIndex] = ft
	sess.CurrentFileIndex = meta.FileIndex
	sess.mu.Unlock()

	log.Printf("[handler] session %x: file %d/%d '%s' (%d bytes, %d chunks)",
		hdr.SessionID, meta.FileIndex+1, meta.TotalFiles, meta.Filename, meta.Size, meta.TotalChunks)
	h.server.persistSession(sess)

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
		if h.stats != nil {
			h.stats.RecordCRCNAK()
		}
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

	if hdr.HasFlag(protocol.FlagLastChunk) || hdr.Sequence%32 == 0 {
		h.server.persistSession(sess)
	}

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
	h.server.persistSession(sess)

	// Verify chunk count
	if ft.ReceivedChunks != ft.Metadata.TotalChunks {
		log.Printf("[handler] session %x: chunk count mismatch: got %d, expected %d",
			sid, ft.ReceivedChunks, ft.Metadata.TotalChunks)
		ft.State = FileStateFailed
		h.sendError(conn, sid, 0, protocol.ErrChunkCountErr)
		h.server.persistSession(sess)
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
		h.server.persistSession(sess)
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
	if h.stats != nil {
		chunkSize := ft.Metadata.ChunkSize
		if chunkSize == 0 {
			chunkSize = h.cfg.ChunkSize
		}
		h.stats.RecordUpload(fileID, ft.Metadata.Filename, ft.Metadata.Size, chunkSize, ft.StartedAt)
	}
	log.Printf("[handler] session %x: file '%s' stored as %s", sid, ft.Metadata.Filename, fileID)

	h.sendControlMsg(conn, sid, 0, protocol.ControlPayload{
		Type:   protocol.CtrlComplete,
		FileID: fileID,
	})

	// Persist after responding so slow index writes don't stall client completion.
	h.server.persistSession(sess)

	// Check if all files in session are complete
	h.checkSessionComplete(conn, sid, sess)
}

// checkSessionComplete checks if all files have been transferred.
func (h *Handler) checkSessionComplete(conn net.Conn, sid [16]byte, sess *Session) {
	sess.mu.Lock()

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
		sess.mu.Unlock()
		return
	}

	if len(failed) > 0 {
		sess.State = StateFailed
	} else {
		sess.State = StateCompleted
	}

	heldSlot := sess.HoldsUploadSlot
	if heldSlot {
		sess.HoldsUploadSlot = false
	}
	sessID := sess.ID
	sess.mu.Unlock()

	if heldSlot && h.upload != nil {
		h.upload.Release()
	}
	h.server.deleteSessionSnapshot(sessID)

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
	case protocol.CtrlJobSubmit:
		h.handleJobSubmit(conn, hdr, ctrl)
	case protocol.CtrlTaskClaim:
		h.handleTaskClaim(conn, hdr, ctrl)
	case protocol.CtrlTaskResult:
		h.handleTaskResult(conn, hdr, ctrl)
	case protocol.CtrlTaskFail:
		h.handleTaskFail(conn, hdr, ctrl)
	case protocol.CtrlTaskLog:
		h.handleTaskLog(conn, hdr, ctrl)
	case protocol.CtrlJobStatus:
		h.handleJobStatus(conn, hdr, ctrl)
	case protocol.CtrlWorkerHello:
		h.handleWorkerHello(conn, hdr, ctrl)
	case protocol.CtrlWorkerHeartbeat:
		h.handleWorkerHeartbeat(conn, hdr, ctrl)
	default:
		log.Printf("[handler] session %x: unhandled control type: %s", hdr.SessionID, ctrl.Type)
	}
}

func (h *Handler) handleJobSubmit(conn net.Conn, hdr protocol.Header, ctrl protocol.ControlPayload) {
	if !h.cfg.ComputeEnabled || h.server.compute == nil {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, "compute coordinator disabled")
		return
	}
	if !h.computeTokenValid(ctrl.Token) {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, "invalid compute token")
		return
	}
	if len(ctrl.Payload) == 0 {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, "missing job payload")
		return
	}

	var req ComputeJobSubmit
	if err := json.Unmarshal(ctrl.Payload, &req); err != nil {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, "invalid job payload")
		return
	}

	job, tasks, err := h.server.compute.SubmitJob(ctrl.JobID, req, time.Now())
	if err != nil {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, err.Error())
		return
	}

	ackPayload, err := json.Marshal(map[string]any{
		"job": job,
	})
	if err != nil {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, "failed to encode accepted job")
		return
	}

	h.sendControlMsg(conn, hdr.SessionID, hdr.Sequence, protocol.ControlPayload{
		Type:    protocol.CtrlACK,
		JobID:   job.ID,
		Status:  job.Status,
		Message: fmt.Sprintf("job accepted with %d task(s) [%s confidence]", tasks, job.Confidence),
		Payload: ackPayload,
	})
}

func (h *Handler) handleTaskClaim(conn net.Conn, hdr protocol.Header, ctrl protocol.ControlPayload) {
	if !h.cfg.ComputeEnabled || h.server.compute == nil {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, "compute coordinator disabled")
		return
	}
	if !h.computeTokenValid(ctrl.Token) {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, "invalid compute token")
		return
	}
	if ctrl.WorkerID == "" {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, "missing worker_id")
		return
	}

	task, ok := h.server.compute.ClaimTask(ctrl.WorkerID, time.Now())
	if !ok {
		h.sendControlMsg(conn, hdr.SessionID, hdr.Sequence, protocol.ControlPayload{
			Type:       protocol.CtrlNAK,
			WorkerID:   ctrl.WorkerID,
			Message:    "no queued tasks",
			RetryAfter: int(h.cfg.ComputeHeartbeat.Seconds()),
		})
		return
	}

	h.sendControlMsg(conn, hdr.SessionID, hdr.Sequence, protocol.ControlPayload{
		Type:         protocol.CtrlTaskAssign,
		WorkerID:     ctrl.WorkerID,
		JobID:        task.JobID,
		TaskID:       task.ID,
		Attempt:      task.Attempt,
		LeaseUntil:   task.LeaseUntil,
		Capabilities: task.RequiredCapabilities,
		Payload:      task.Payload,
	})
}

func (h *Handler) handleWorkerHello(conn net.Conn, hdr protocol.Header, ctrl protocol.ControlPayload) {
	if !h.cfg.ComputeEnabled || h.server.compute == nil {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, "compute coordinator disabled")
		return
	}
	if !h.computeTokenValid(ctrl.Token) {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, "invalid compute token")
		return
	}
	if ctrl.WorkerID == "" {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, "missing worker_id")
		return
	}

	h.server.compute.RegisterWorker(ctrl.WorkerID, ctrl.Capabilities, time.Now())
	h.sendControlMsg(conn, hdr.SessionID, hdr.Sequence, protocol.ControlPayload{
		Type:     protocol.CtrlACK,
		WorkerID: ctrl.WorkerID,
		Message:  "worker registered",
	})
}

func (h *Handler) handleTaskResult(conn net.Conn, hdr protocol.Header, ctrl protocol.ControlPayload) {
	if !h.cfg.ComputeEnabled || h.server.compute == nil {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, "compute coordinator disabled")
		return
	}
	if !h.computeTokenValid(ctrl.Token) {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, "invalid compute token")
		return
	}
	if ctrl.WorkerID == "" {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, "missing worker_id")
		return
	}
	if ctrl.TaskID == "" {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, "missing task_id")
		return
	}

	task, job, err := h.server.compute.CompleteTask(ctrl.WorkerID, ctrl.TaskID, ctrl.FileID, ctrl.Checksum, time.Now())
	if err != nil {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, err.Error())
		return
	}

	h.sendControlMsg(conn, hdr.SessionID, hdr.Sequence, protocol.ControlPayload{
		Type:     protocol.CtrlACK,
		WorkerID: ctrl.WorkerID,
		JobID:    job.ID,
		TaskID:   task.ID,
		Status:   task.Status,
		Message:  "task completed",
	})
}

func (h *Handler) handleTaskFail(conn net.Conn, hdr protocol.Header, ctrl protocol.ControlPayload) {
	if !h.cfg.ComputeEnabled || h.server.compute == nil {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, "compute coordinator disabled")
		return
	}
	if !h.computeTokenValid(ctrl.Token) {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, "invalid compute token")
		return
	}
	if ctrl.WorkerID == "" {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, "missing worker_id")
		return
	}
	if ctrl.TaskID == "" {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, "missing task_id")
		return
	}
	if ctrl.Message == "" {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, "missing failure message")
		return
	}

	task, job, err := h.server.compute.FailTask(ctrl.WorkerID, ctrl.TaskID, ctrl.Message, ctrl.Checksum, time.Now())
	if err != nil {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, err.Error())
		return
	}

	h.sendControlMsg(conn, hdr.SessionID, hdr.Sequence, protocol.ControlPayload{
		Type:     protocol.CtrlACK,
		WorkerID: ctrl.WorkerID,
		JobID:    job.ID,
		TaskID:   task.ID,
		Status:   task.Status,
		Message:  taskFailMessage(task),
	})
}

func (h *Handler) handleTaskLog(conn net.Conn, hdr protocol.Header, ctrl protocol.ControlPayload) {
	if !h.cfg.ComputeEnabled || h.server.compute == nil {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, "compute coordinator disabled")
		return
	}
	if !h.computeTokenValid(ctrl.Token) {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, "invalid compute token")
		return
	}
	if ctrl.WorkerID == "" {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, "missing worker_id")
		return
	}
	if ctrl.TaskID == "" {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, "missing task_id")
		return
	}

	if err := h.server.compute.AppendTaskLog(ctrl.WorkerID, ctrl.TaskID, ctrl.LogLines, time.Now()); err != nil {
		// Log but don't error the worker — log ingestion failures are non-fatal.
		log.Printf("[handler] task log append failed: worker=%s task=%s err=%v", ctrl.WorkerID, ctrl.TaskID, err)
	}

	h.sendControlMsg(conn, hdr.SessionID, hdr.Sequence, protocol.ControlPayload{
		Type:     protocol.CtrlACK,
		WorkerID: ctrl.WorkerID,
		TaskID:   ctrl.TaskID,
	})
}

func (h *Handler) handleJobStatus(conn net.Conn, hdr protocol.Header, ctrl protocol.ControlPayload) {
	if !h.cfg.ComputeEnabled || h.server.compute == nil {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, "compute coordinator disabled")
		return
	}
	if !h.computeTokenValid(ctrl.Token) {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, "invalid compute token")
		return
	}
	if ctrl.JobID == "" {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, "missing job_id")
		return
	}

	job, tasks, ok := h.server.compute.JobState(ctrl.JobID)
	if !ok {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, "job not found")
		return
	}

	statusPayload, err := json.Marshal(map[string]any{
		"job":   job,
		"tasks": tasks,
	})
	if err != nil {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, "failed to encode job status")
		return
	}

	h.sendControlMsg(conn, hdr.SessionID, hdr.Sequence, protocol.ControlPayload{
		Type:    protocol.CtrlACK,
		JobID:   job.ID,
		Status:  job.Status,
		Payload: statusPayload,
	})
}

func taskFailMessage(task *ComputeTask) string {
	if task == nil {
		return "task failed"
	}
	if task.Status == ComputeTaskStatusRetrying {
		return "task scheduled for retry"
	}
	if task.Status == ComputeTaskStatusNeedsAttention {
		return "task needs attention"
	}
	return "task failed"
}

func (h *Handler) handleWorkerHeartbeat(conn net.Conn, hdr protocol.Header, ctrl protocol.ControlPayload) {
	if !h.cfg.ComputeEnabled || h.server.compute == nil {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, "compute coordinator disabled")
		return
	}
	if !h.computeTokenValid(ctrl.Token) {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, "invalid compute token")
		return
	}
	if ctrl.WorkerID == "" {
		h.sendError(conn, hdr.SessionID, hdr.Sequence, "missing worker_id")
		return
	}

	if ok := h.server.compute.HeartbeatWorker(ctrl.WorkerID, time.Now()); !ok {
		h.sendControlMsg(conn, hdr.SessionID, hdr.Sequence, protocol.ControlPayload{
			Type:     protocol.CtrlNAK,
			WorkerID: ctrl.WorkerID,
			Message:  "worker not registered",
		})
		return
	}

	h.sendControlMsg(conn, hdr.SessionID, hdr.Sequence, protocol.ControlPayload{
		Type:       protocol.CtrlACK,
		WorkerID:   ctrl.WorkerID,
		RetryAfter: int(h.cfg.ComputeHeartbeat.Seconds()),
		Message:    "heartbeat accepted",
	})
}

func (h *Handler) computeTokenValid(token string) bool {
	if !h.cfg.ComputeRequireToken {
		return true
	}
	if h.cfg.ComputeWorkerAuthToken == "" {
		return false
	}
	return token == h.cfg.ComputeWorkerAuthToken
}

// handleDownload serves a stored file to the client.
func (h *Handler) handleDownload(conn net.Conn, hdr protocol.Header, ctrl protocol.ControlPayload) {
	startedAt := time.Now()
	if h.download != nil {
		h.download.Acquire()
		defer h.download.Release()
	}

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

	// Track active download to prevent deletion while streaming.
	sf.IncrementActive()
	defer sf.DecrementActive()

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

	// Mark download as complete for download-count semantics.
	sf.IncrementDownloads()
	if h.stats != nil {
		h.stats.RecordDownload(ctrl.FileID, sf.Metadata.Filename, sf.Metadata.Size, h.cfg.ChunkSize, startedAt)
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
