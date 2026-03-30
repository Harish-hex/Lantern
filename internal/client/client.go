// Package client implements the Lantern file transfer client.
package client

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/Harish-hex/Lantern/internal/protocol"
)

type ComputeTask struct {
	ID                   string          `json:"id"`
	JobID                string          `json:"job_id"`
	WorkerID             string          `json:"worker_id"`
	LastWorkerID         string          `json:"last_worker_id"`
	Status               string          `json:"status"`
	Attempt              int             `json:"attempt"`
	LeaseUntil           time.Time       `json:"lease_until"`
	UpdatedAt            time.Time       `json:"updated_at"`
	Checksum             string          `json:"checksum"`
	Error                string          `json:"error"`
	FailureCategory      string          `json:"failure_category"`
	RequiredCapabilities []string        `json:"required_capabilities"`
	Payload              json.RawMessage `json:"payload"`
}

type ComputeArtifact struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Kind      string          `json:"kind"`
	SizeBytes int64           `json:"size_bytes"`
	CreatedAt time.Time       `json:"created_at"`
	Summary   json.RawMessage `json:"summary"`
}

type ComputePreflightCheck struct {
	Code    string `json:"code"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type ComputePreflight struct {
	Ready                bool                    `json:"ready"`
	Confidence           string                  `json:"confidence"`
	EstimatedTasks       int                     `json:"estimated_tasks"`
	EstimatedOutputBytes int64                   `json:"estimated_output_bytes"`
	RequiredCapabilities []string                `json:"required_capabilities"`
	Checks               []ComputePreflightCheck `json:"checks"`
}

type ComputeJob struct {
	ID                   string            `json:"id"`
	Type                 string            `json:"type"`
	TemplateID           string            `json:"template_id"`
	TemplateName         string            `json:"template_name"`
	OutputKind           string            `json:"output_kind"`
	Status               string            `json:"status"`
	Confidence           string            `json:"confidence"`
	NeedsAttentionReason string            `json:"needs_attention_reason"`
	FailureCategory      string            `json:"failure_category"`
	CreatedAt            time.Time         `json:"created_at"`
	UpdatedAt            time.Time         `json:"updated_at"`
	StartedAt            time.Time         `json:"started_at"`
	FinishedAt           time.Time         `json:"finished_at"`
	TotalTasks           int               `json:"total_tasks"`
	CompletedTasks       int               `json:"completed_tasks"`
	FailedTasks          int               `json:"failed_tasks"`
	RetryingTasks        int               `json:"retrying_tasks"`
	Inputs               json.RawMessage   `json:"inputs"`
	Settings             json.RawMessage   `json:"settings"`
	Preflight            ComputePreflight  `json:"preflight"`
	Artifacts            []ComputeArtifact `json:"artifacts"`
}

type ComputeJobStatus struct {
	Job   ComputeJob    `json:"job"`
	Tasks []ComputeTask `json:"tasks"`
}

// Client connects to a Lantern server and transfers files.
type Client struct {
	conn      net.Conn
	sessionID [16]byte
	chunkSize uint32
}

// New dials the server and performs the handshake.
func New(host string, port int) (*Client, error) {
	addr := fmt.Sprintf("%s:%d", host, port)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", addr, err)
	}

	// Generate random session ID
	var sid [16]byte
	if _, err := rand.Read(sid[:]); err != nil {
		conn.Close()
		return nil, fmt.Errorf("generate session ID: %w", err)
	}

	c := &Client{
		conn:      conn,
		sessionID: sid,
		chunkSize: 256 * 1024, // 256 KB default
	}

	// Send handshake
	hdr := protocol.NewHeader(protocol.MsgHandshake, 0, 0, 0, sid)
	if err := protocol.WritePacket(conn, hdr, nil); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send handshake: %w", err)
	}

	// Read server response
	_, payload, _, err := protocol.ReadPacket(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read handshake response: %w", err)
	}

	var ctrl protocol.ControlPayload
	if err := json.Unmarshal(payload, &ctrl); err != nil {
		conn.Close()
		return nil, fmt.Errorf("parse handshake response: %w", err)
	}

	if ctrl.Type == protocol.CtrlBusy {
		conn.Close()
		return nil, fmt.Errorf("server busy: %s (retry in %ds)", ctrl.Message, ctrl.RetryAfter)
	}

	if ctrl.Type == protocol.CtrlError || ctrl.Type == protocol.CtrlReject {
		conn.Close()
		return nil, fmt.Errorf("server rejected: %s", ctrl.Message)
	}

	log.Printf("[client] connected to %s, session %s", addr, hex.EncodeToString(sid[:]))

	return c, nil
}

// SendFiles uploads one or more files to the server.
func (c *Client) SendFiles(paths []string, ttl int, maxDownloads int, progressFn func(filename string, pct float64)) ([]string, error) {
	totalFiles := len(paths)
	var fileIDs []string

	for i, path := range paths {
		fileID, err := c.sendFile(path, uint32(i), uint32(totalFiles), ttl, maxDownloads, progressFn)
		if err != nil {
			return fileIDs, fmt.Errorf("file %s: %w", filepath.Base(path), err)
		}
		fileIDs = append(fileIDs, fileID)
	}

	return fileIDs, nil
}

// sendFile uploads a single file.
func (c *Client) sendFile(path string, index, total uint32, ttl, maxDownloads int, progressFn func(string, float64)) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat: %w", err)
	}

	fileSize := stat.Size()
	filename := filepath.Base(path)

	// Compute full-file SHA-256 first
	hasher := sha256.New()
	if _, err := io.Copy(hasher, f); err != nil {
		return "", fmt.Errorf("hash: %w", err)
	}
	checksum := hex.EncodeToString(hasher.Sum(nil))

	// Seek back to beginning for transfer
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("seek: %w", err)
	}

	// Calculate total chunks
	totalChunks := uint32(fileSize / int64(c.chunkSize))
	if fileSize%int64(c.chunkSize) != 0 {
		totalChunks++
	}

	// Send FILE_HEADER
	meta := protocol.FileMetadata{
		Filename:     filename,
		Size:         fileSize,
		FileIndex:    index,
		TotalFiles:   total,
		TotalChunks:  totalChunks,
		ChecksumFull: checksum,
		TTLSeconds:   ttl,
		MaxDownloads: maxDownloads,
	}
	metaBytes, _ := json.Marshal(meta)
	fhdr := protocol.NewHeader(protocol.MsgFileHeader, 0, 0, 0, c.sessionID)
	if err := protocol.WritePacket(c.conn, fhdr, metaBytes); err != nil {
		return "", fmt.Errorf("send file header: %w", err)
	}

	// Wait for header ACK
	if _, err := c.readACK(); err != nil {
		return "", fmt.Errorf("file header: %w", err)
	}

	const maxRetries = 3

	// Stream chunks
	buf := make([]byte, c.chunkSize)
	var seq uint32

	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			seq++
			flags := byte(0)
			if readErr != nil { // last chunk (EOF)
				flags |= protocol.FlagLastChunk
			}

			chunkData := make([]byte, n)
			copy(chunkData, buf[:n])

			var ctrl *protocol.ControlPayload
			var err error

			for attempt := 0; attempt <= maxRetries; attempt++ {
				chdr := protocol.NewHeader(protocol.MsgChunk, flags, 0, seq, c.sessionID)
				if err = protocol.WritePacket(c.conn, chdr, chunkData); err != nil {
					return "", fmt.Errorf("send chunk %d (attempt %d): %w", seq, attempt+1, err)
				}

				// Wait for chunk ACK/NAK
				ctrl, err = c.readACK()
				if err != nil {
					return "", fmt.Errorf("chunk %d: %w", seq, err)
				}

				if ctrl.Type != protocol.CtrlNAK {
					break
				}

				// NAK: retry if we have attempts left
				if attempt < maxRetries {
					log.Printf("[client] chunk %d NAK (%s), retrying (%d/%d)...", seq, ctrl.Message, attempt+1, maxRetries)
					time.Sleep(time.Duration(attempt+1) * 200 * time.Millisecond)
					continue
				}

				return "", fmt.Errorf("chunk %d NAK after %d retries: %s", seq, maxRetries, ctrl.Message)
			}

			// Report progress
			if progressFn != nil {
				pct := float64(seq) / float64(totalChunks) * 100
				progressFn(filename, pct)
			}

			// If this was the complete response, return the file ID
			if ctrl.Type == protocol.CtrlComplete {
				return ctrl.FileID, nil
			}
		}
		if readErr != nil {
			break
		}
	}

	// Wait for final completion message
	ctrl, err := c.readACK()
	if err != nil {
		return "", fmt.Errorf("wait completion: %w", err)
	}
	if ctrl.Type == protocol.CtrlComplete {
		return ctrl.FileID, nil
	}

	return "", fmt.Errorf("unexpected final response: %s", ctrl.Type)
}

// DownloadFile requests a file by ID and saves it to destDir.
func (c *Client) DownloadFile(fileID, destDir string, progressFn func(filename string, pct float64)) error {
	// Send download request
	ctrl := protocol.ControlPayload{
		Type:   protocol.CtrlDownload,
		FileID: fileID,
	}
	payload, _ := json.Marshal(ctrl)
	hdr := protocol.NewHeader(protocol.MsgControl, 0, 0, 0, c.sessionID)
	if err := protocol.WritePacket(c.conn, hdr, payload); err != nil {
		return fmt.Errorf("send download request: %w", err)
	}

	// Read FILE_HEADER response
	respHdr, respPayload, _, err := protocol.ReadPacket(c.conn)
	if err != nil {
		return fmt.Errorf("read download response: %w", err)
	}

	if respHdr.MsgType == protocol.MsgControl {
		var errCtrl protocol.ControlPayload
		json.Unmarshal(respPayload, &errCtrl)
		return fmt.Errorf("server error: %s", errCtrl.Message)
	}

	if respHdr.MsgType != protocol.MsgFileHeader {
		return fmt.Errorf("expected FILE_HEADER, got 0x%02x", respHdr.MsgType)
	}

	var meta protocol.FileMetadata
	if err := json.Unmarshal(respPayload, &meta); err != nil {
		return fmt.Errorf("parse file metadata: %w", err)
	}

	// Create output file
	outPath := filepath.Join(destDir, meta.Filename)
	outFile, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", outPath, err)
	}
	defer outFile.Close()

	// Read chunks until FlagLastChunk
	var chunkCount uint32
	for {
		chdr, chunkPayload, _, err := protocol.ReadPacket(c.conn)
		if err != nil {
			return fmt.Errorf("read chunk: %w", err)
		}

		if chdr.MsgType != protocol.MsgChunk {
			return fmt.Errorf("expected CHUNK, got 0x%02x", chdr.MsgType)
		}

		if _, err := outFile.Write(chunkPayload); err != nil {
			return fmt.Errorf("write chunk: %w", err)
		}

		chunkCount++

		if progressFn != nil && meta.TotalChunks > 0 {
			pct := float64(chunkCount) / float64(meta.TotalChunks) * 100
			progressFn(meta.Filename, pct)
		}

		if chdr.HasFlag(protocol.FlagLastChunk) {
			break
		}
	}

	log.Printf("[client] downloaded %s → %s (%d chunks)", fileID, outPath, chunkCount)
	return nil
}

func (c *Client) SubmitComputeJob(jobID, jobType, token string, tasks []json.RawMessage) (*protocol.ControlPayload, error) {
	if len(tasks) == 0 {
		return nil, fmt.Errorf("at least one task payload is required")
	}
	payload, err := json.Marshal(map[string]any{
		"type":  jobType,
		"tasks": tasks,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal submit payload: %w", err)
	}
	return c.sendControl(protocol.ControlPayload{
		Type:    protocol.CtrlJobSubmit,
		JobID:   jobID,
		Token:   token,
		Payload: payload,
	})
}

func (c *Client) SubmitComputeTemplateJob(jobID, templateID, token string, inputs, settings json.RawMessage) (*protocol.ControlPayload, error) {
	payload, err := json.Marshal(map[string]any{
		"template": templateID,
		"inputs":   inputs,
		"settings": settings,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal template submit payload: %w", err)
	}
	return c.sendControl(protocol.ControlPayload{
		Type:    protocol.CtrlJobSubmit,
		JobID:   jobID,
		Token:   token,
		Payload: payload,
	})
}

func (c *Client) RegisterWorker(workerID, token string, capabilities []string) error {
	_, err := c.sendControl(protocol.ControlPayload{
		Type:         protocol.CtrlWorkerHello,
		WorkerID:     workerID,
		Token:        token,
		Capabilities: capabilities,
	})
	return err
}

func (c *Client) HeartbeatWorker(workerID, token string) error {
	_, err := c.sendControl(protocol.ControlPayload{
		Type:     protocol.CtrlWorkerHeartbeat,
		WorkerID: workerID,
		Token:    token,
	})
	return err
}

func (c *Client) ClaimComputeTask(workerID, token string) (*protocol.ControlPayload, error) {
	ctrl, err := c.sendControl(protocol.ControlPayload{
		Type:     protocol.CtrlTaskClaim,
		WorkerID: workerID,
		Token:    token,
	})
	if err != nil {
		return nil, err
	}
	if ctrl.Type == protocol.CtrlNAK {
		return ctrl, nil
	}
	if ctrl.Type != protocol.CtrlTaskAssign {
		return nil, fmt.Errorf("unexpected claim response: %s", ctrl.Type)
	}
	return ctrl, nil
}

func (c *Client) CompleteComputeTask(workerID, taskID, artifactID, checksum, token string) error {
	_, err := c.sendControl(protocol.ControlPayload{
		Type:       protocol.CtrlTaskResult,
		WorkerID:   workerID,
		TaskID:     taskID,
		FileID:     artifactID,
		Checksum:   checksum,
		Token:      token,
	})
	return err
}

func (c *Client) FailComputeTask(workerID, taskID, failureMessage, checksum, token string) error {
	_, err := c.sendControl(protocol.ControlPayload{
		Type:     protocol.CtrlTaskFail,
		WorkerID: workerID,
		TaskID:   taskID,
		Message:  failureMessage,
		Checksum: checksum,
		Token:    token,
	})
	return err
}

func (c *Client) ComputeJobStatus(jobID, token string) (*ComputeJobStatus, error) {
	ctrl, err := c.sendControl(protocol.ControlPayload{
		Type:  protocol.CtrlJobStatus,
		JobID: jobID,
		Token: token,
	})
	if err != nil {
		return nil, err
	}

	var status ComputeJobStatus
	if len(ctrl.Payload) > 0 {
		if err := json.Unmarshal(ctrl.Payload, &status); err != nil {
			return nil, fmt.Errorf("parse job status payload: %w", err)
		}
	}
	if status.Job.ID == "" {
		status.Job.ID = ctrl.JobID
		status.Job.Status = ctrl.Status
	}

	return &status, nil
}

func (c *Client) sendControl(ctrl protocol.ControlPayload) (*protocol.ControlPayload, error) {
	payload, err := json.Marshal(ctrl)
	if err != nil {
		return nil, fmt.Errorf("marshal control payload: %w", err)
	}
	hdr := protocol.NewHeader(protocol.MsgControl, 0, 0, 0, c.sessionID)
	if err := protocol.WritePacket(c.conn, hdr, payload); err != nil {
		return nil, fmt.Errorf("send control: %w", err)
	}
	resp, err := c.readACK()
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// Close terminates the connection.
func (c *Client) Close() {
	c.conn.Close()
}

// readACK reads the next control message from the server.
func (c *Client) readACK() (*protocol.ControlPayload, error) {
	_, payload, _, err := protocol.ReadPacket(c.conn)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}

	var ctrl protocol.ControlPayload
	if err := json.Unmarshal(payload, &ctrl); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	if ctrl.Type == protocol.CtrlError {
		return &ctrl, fmt.Errorf("server error: %s", ctrl.Message)
	}

	return &ctrl, nil
}
