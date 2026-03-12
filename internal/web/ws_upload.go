package web

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Harish-hex/Lantern/internal/protocol"
	"github.com/gorilla/websocket"
)

const (
	wsFlagLastChunk  = 1 << 0
	wsFlagRetransmit = 1 << 2
)

var uploadUpgrader = websocket.Upgrader{
	ReadBufferSize:  128 * 1024,
	WriteBufferSize: 128 * 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type wsUploadStart struct {
	Type string                `json:"type"`
	Meta protocol.FileMetadata `json:"meta"`
}

type wsUploadControl struct {
	Type       string `json:"type"`
	Seq        uint32 `json:"seq"`
	FileID     string `json:"file_id,omitempty"`
	Message    string `json:"message,omitempty"`
	RetryAfter int    `json:"retry_after,omitempty"`
}

type wsUploadSession struct {
	meta           protocol.FileMetadata
	tempPath       string
	hasher         *protocol.SHA256Hasher
	receivedChunks uint32
	lastAcked      uint32
	startedAt      time.Time
	uploadID       string
}

func (ws *Server) handleUploadWS(w http.ResponseWriter, r *http.Request) {
	conn, err := uploadUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	if !ws.bridge.TryAcquireUpload() {
		_ = conn.WriteJSON(wsUploadControl{Type: "busy", Message: "server at max upload concurrency", RetryAfter: 1})
		return
	}
	defer ws.bridge.ReleaseUpload()

	conn.SetReadLimit(64 << 20)

	sess, err := ws.readUploadStart(conn)
	if err != nil {
		log.Printf("[web][ws-upload] start rejected: %v", err)
		_ = conn.WriteJSON(wsUploadControl{Type: "error", Message: err.Error()})
		return
	}
	defer ws.bridge.CleanupTemp(sess.tempPath)

	if err := conn.WriteJSON(wsUploadControl{Type: "ack", Seq: 0, Message: "ready"}); err != nil {
		return
	}

	for {
		msgType, payload, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if msgType != websocket.BinaryMessage {
			_ = conn.WriteJSON(wsUploadControl{Type: "error", Message: "expected binary chunk frame"})
			return
		}

		seq, flags, chunkPayload, err := parseChunkFrame(payload)
		if err != nil {
			log.Printf("[web][ws-upload] parse chunk error: %v", err)
			_ = conn.WriteJSON(wsUploadControl{Type: "error", Message: err.Error()})
			return
		}

		// Duplicate retransmit: acknowledge idempotently.
		if seq <= sess.lastAcked {
			_ = conn.WriteJSON(wsUploadControl{Type: "ack", Seq: seq})
			continue
		}
		if seq != sess.lastAcked+1 {
			_ = conn.WriteJSON(wsUploadControl{Type: "nak", Seq: sess.lastAcked + 1, Message: "out of order chunk"})
			continue
		}

		if err := appendChunk(sess.tempPath, chunkPayload); err != nil {
			log.Printf("[web][ws-upload] append chunk failed: %v", err)
			_ = conn.WriteJSON(wsUploadControl{Type: "error", Message: "disk write failed"})
			return
		}
		sess.hasher.Update(chunkPayload)
		sess.receivedChunks++
		sess.lastAcked = seq

		if err := conn.WriteJSON(wsUploadControl{Type: "ack", Seq: seq}); err != nil {
			return
		}

		if flags&wsFlagLastChunk == 0 {
			continue
		}

		fileID, err := ws.finalizeUploadSession(sess)
		if err != nil {
			log.Printf("[web][ws-upload] finalize failed: %v", err)
			_ = conn.WriteJSON(wsUploadControl{Type: "error", Message: err.Error()})
			return
		}
		log.Printf("[web][ws-upload] complete: file=%s chunks=%d", fileID, sess.receivedChunks)
		_ = conn.WriteJSON(wsUploadControl{Type: "complete", FileID: fileID})
		return
	}
}

func (ws *Server) readUploadStart(conn *websocket.Conn) (*wsUploadSession, error) {
	msgType, payload, err := conn.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("read start frame: %w", err)
	}
	if msgType != websocket.TextMessage {
		return nil, fmt.Errorf("expected start control frame")
	}

	var start wsUploadStart
	if err := json.Unmarshal(payload, &start); err != nil {
		return nil, fmt.Errorf("invalid start payload")
	}
	if start.Type != "start" {
		return nil, fmt.Errorf("missing start message")
	}

	meta := start.Meta
	meta.Filename = filepath.Base(strings.TrimSpace(meta.Filename))
	if meta.Filename == "" {
		return nil, fmt.Errorf("filename is required")
	}
	if meta.Size <= 0 {
		return nil, fmt.Errorf("file size must be positive")
	}
	cfg := ws.bridge.Cfg()
	if meta.Size > cfg.MaxFileSize {
		return nil, fmt.Errorf("file too large: %d > %d", meta.Size, cfg.MaxFileSize)
	}
	if meta.ChunkSize == 0 {
		meta.ChunkSize = cfg.ChunkSize
	}
	if meta.TotalChunks == 0 {
		meta.TotalChunks = uint32((meta.Size + int64(meta.ChunkSize) - 1) / int64(meta.ChunkSize))
	}

	uploadID := fmt.Sprintf("ws_%d", time.Now().UnixNano())
	tempPath := ws.bridge.CreateTemp(uploadID)

	return &wsUploadSession{
		meta:      meta,
		tempPath:  tempPath,
		hasher:    protocol.NewSHA256Hasher(),
		startedAt: time.Now(),
		uploadID:  uploadID,
	}, nil
}

func parseChunkFrame(frame []byte) (seq uint32, flags byte, payload []byte, err error) {
	if len(frame) < 5 {
		return 0, 0, nil, fmt.Errorf("chunk frame too small")
	}
	seq = binary.BigEndian.Uint32(frame[:4])
	flags = frame[4]
	payload = frame[5:]
	if len(payload) == 0 {
		return 0, 0, nil, fmt.Errorf("empty chunk payload")
	}
	_ = flags & wsFlagRetransmit
	return seq, flags, payload, nil
}

func appendChunk(path string, payload []byte) error {
	f, err := createFileAppend(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(payload)
	return err
}

func createFileAppend(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}

func (ws *Server) finalizeUploadSession(sess *wsUploadSession) (string, error) {
	if sess.receivedChunks != sess.meta.TotalChunks {
		return "", fmt.Errorf("chunk count mismatch: got %d expected %d", sess.receivedChunks, sess.meta.TotalChunks)
	}
	if expected := strings.TrimSpace(sess.meta.ChecksumFull); expected != "" {
		got := sess.hasher.Finalize()
		if !strings.EqualFold(expected, got) {
			return "", fmt.Errorf("integrity failed")
		}
	}

	ttl := sess.meta.TTLSeconds
	if ttl <= 0 {
		ttl = ws.bridge.Cfg().TTLDefault
	}
	maxDL := sess.meta.MaxDownloads
	if maxDL <= 0 {
		maxDL = ws.bridge.Cfg().MaxDownloads
	}
	fileID := fmt.Sprintf("%s_0", sess.uploadID)

	sf, err := ws.bridge.MoveToStorage(sess.tempPath, fileID, sess.meta, ttl, maxDL)
	if err != nil {
		return "", err
	}

	ws.bridge.RecordUpload(sf.ID, sf.Metadata.Filename, sf.Metadata.Size, sess.meta.ChunkSize, sess.startedAt)
	ws.hub.publish(Event{Type: "upload", FileID: sf.ID, Filename: sf.Metadata.Filename})
	return sf.ID, nil
}
