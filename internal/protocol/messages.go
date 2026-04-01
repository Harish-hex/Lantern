package protocol

import (
	"encoding/json"
	"time"
)

// ----- Message Types (Header.MsgType) -----

const (
	MsgHandshake  byte = 0x01 // Client → Server: initiate or resume session
	MsgFileHeader byte = 0x02 // Announce file metadata (bidirectional for download)
	MsgChunk      byte = 0x03 // One piece of a file (bidirectional)
	MsgControl    byte = 0x04 // ACK, NAK, ERROR, STATUS, etc. (bidirectional)
)

// ----- Flags (Header.Flags bit positions) -----

const (
	FlagLastChunk  byte = 1 << 0 // Final chunk of a file
	FlagCompressed byte = 1 << 1 // Payload is compressed
	FlagRetransmit byte = 1 << 2 // Resent chunk
	FlagEncrypted  byte = 1 << 3 // Reserved for Phase 3
)

// ----- Control Message Sub-Types -----

const (
	CtrlACK             = "ACK"
	CtrlNAK             = "NAK"
	CtrlError           = "ERROR"
	CtrlBusy            = "BUSY"
	CtrlResume          = "RESUME"
	CtrlDownload        = "DOWNLOAD"
	CtrlComplete        = "COMPLETE"
	CtrlReject          = "REJECT"
	CtrlPartialComplete = "PARTIAL_COMPLETE"
	CtrlWorkerHello     = "WORKER_HELLO"
	CtrlWorkerHeartbeat = "WORKER_HEARTBEAT"
	CtrlJobSubmit       = "JOB_SUBMIT"
	CtrlTaskAssign      = "TASK_ASSIGN"
	CtrlTaskResult      = "TASK_RESULT"
	CtrlTaskFail        = "TASK_FAIL"
	CtrlTaskClaim       = "TASK_CLAIM"
	CtrlTaskLease       = "TASK_LEASE"
	CtrlTaskLog         = "TASK_LOG"
	CtrlJobStatus       = "JOB_STATUS"
)

// ----- Error Codes -----

const (
	ErrCRCMismatch     = "CRC_MISMATCH"
	ErrDiskFull        = "DISK_FULL"
	ErrChunkCountErr   = "CHUNK_COUNT_MISMATCH"
	ErrIntegrityFailed = "INTEGRITY_FAILED"
	ErrFileExpired     = "FILE_EXPIRED"
	ErrSessionConflict = "SESSION_CONFLICT"
)

// FileMetadata is the JSON payload carried by FILE_HEADER packets.
type FileMetadata struct {
	Filename     string `json:"filename"`
	Size         int64  `json:"size"`
	MimeType     string `json:"mime_type"`
	ChecksumFull string `json:"checksum_full"` // "sha256:<hex>"
	ChunkSize    uint32 `json:"chunk_size"`
	TotalChunks  uint32 `json:"total_chunks"`
	FileIndex    uint32 `json:"file_index"`
	TotalFiles   uint32 `json:"total_files"`
	MaxDownloads int    `json:"max_downloads"`
	TTLSeconds   int    `json:"ttl_seconds"`
}

// ControlPayload is the JSON payload carried by CONTROL packets.
type ControlPayload struct {
	Type       string   `json:"type"`                  // CtrlACK, CtrlNAK, etc.
	Seq        uint32   `json:"seq,omitempty"`         // Relevant chunk sequence
	Message    string   `json:"message,omitempty"`     // Human-readable or error code
	FileID     string   `json:"file_id,omitempty"`     // Assigned on COMPLETE
	SessionID  string   `json:"session_id,omitempty"`  // Echoed on HANDSHAKE ACK
	RetryAfter int      `json:"retry_after,omitempty"` // Seconds, used with BUSY
	Succeeded  []string `json:"succeeded,omitempty"`   // For PARTIAL_COMPLETE
	Failed     []string `json:"failed,omitempty"`      // For PARTIAL_COMPLETE

	WorkerID     string          `json:"worker_id,omitempty"`
	JobID        string          `json:"job_id,omitempty"`
	TaskID       string          `json:"task_id,omitempty"`
	Token        string          `json:"token,omitempty"`
	Capabilities []string        `json:"capabilities,omitempty"`
	Status       string          `json:"status,omitempty"`
	Attempt      int             `json:"attempt,omitempty"`
	LeaseUntil   time.Time       `json:"lease_until,omitempty"`
	Checksum     string          `json:"checksum,omitempty"`
	Payload      json.RawMessage `json:"payload,omitempty"`
	LogLines     []string        `json:"log_lines,omitempty"` // TASK_LOG: streamed log lines from worker
}
