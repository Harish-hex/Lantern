// Package web runs the Lantern HTTP/WebSocket dashboard server.
//
// It provides:
//   - GET  /api/files           list current files (JSON)
//   - GET  /api/files/{id}      download a file via HTTP
//   - POST /api/upload          HTTP upload (multipart, up to cfg.MaxFileSizeHTTP)
//   - DELETE /api/files/{id}    delete a file
//   - GET  /ws                  WebSocket for live event streaming
//   - GET  /                    SPA front-end (embedded static assets)
//
// HTTP uploads bypass the custom binary protocol.  Transport-level TCP
// integrity is relied upon; there is no per-chunk reassembly or NAK.
// The same StorageManager used by the binary-protocol layer is shared so
// files uploaded via either path are visible to both.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/Harish-hex/Lantern/internal/protocol"
	"github.com/Harish-hex/Lantern/internal/server"
)

//go:embed static
var staticFS embed.FS

// Server wraps a *http.Server and owns the WebSocket hub.
type Server struct {
	bridge  *server.Bridge
	hub     *Hub
	httpSrv *http.Server
}

// New creates a web.Server but does not start listening.
func New(bridge *server.Bridge) *Server {
	ws := &Server{
		bridge: bridge,
		hub:    newHub(),
	}

	mux := http.NewServeMux()

	// REST API
	mux.HandleFunc("/api/files", ws.handleFiles)
	mux.HandleFunc("/api/files/", ws.handleFileByID)
	mux.HandleFunc("/api/stats", ws.handleStats)
	mux.HandleFunc("/api/upload", ws.handleUpload)
	mux.HandleFunc("/ws/upload", ws.handleUploadWS)

	// SSE live events
	mux.HandleFunc("/events", ws.hub.serveSSE)

	// SPA static assets (embedded)
	staticRoot, _ := fs.Sub(staticFS, "static")
	mux.Handle("/", noCache(http.FileServer(http.FS(staticRoot))))

	cfg := bridge.Cfg()
	ws.httpSrv = &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.ListenAddr, cfg.HTTPPort),
		Handler:      mux,
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 0, // disabled for streaming downloads
		IdleTimeout:  120 * time.Second,
	}
	return ws
}

// Start begins listening.  Blocks until the server stops.
func (ws *Server) Start() error {
	log.Printf("HTTP server listening on %s", ws.httpSrv.Addr)
	go ws.hub.run()
	if err := ws.httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Stop gracefully drains connections.
func (ws *Server) Stop(ctx context.Context) error {
	return ws.httpSrv.Shutdown(ctx)
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		next.ServeHTTP(w, r)
	})
}

// ── /api/files ────────────────────────────────────────────────────────────────

func (ws *Server) handleFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	files := ws.bridge.ListFiles()
	type fileDTO struct {
		ID           string    `json:"id"`
		Name         string    `json:"name"`
		Size         int64     `json:"size"`
		Downloads    int32     `json:"downloads"`
		MaxDownloads int       `json:"max_downloads"`
		ExpiresAt    time.Time `json:"expires_at"`
		Expired      bool      `json:"expired"`
	}
	dtos := make([]fileDTO, 0, len(files))
	for _, f := range files {
		dtos = append(dtos, fileDTO{
			ID:           f.ID,
			Name:         f.Metadata.Filename,
			Size:         f.Metadata.Size,
			Downloads:    f.DownloadCount,
			MaxDownloads: f.MaxDownloads,
			ExpiresAt:    f.ExpiresAt,
			Expired:      f.IsExpired(),
		})
	}
	jsonOK(w, dtos)
}

func (ws *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	jsonOK(w, ws.bridge.Stats())
}

// ── /api/files/{id} ──────────────────────────────────────────────────────────

func (ws *Server) handleFileByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/files/")
	if id == "" {
		jsonErr(w, http.StatusBadRequest, "missing file id")
		return
	}
	switch r.Method {
	case http.MethodGet:
		ws.serveDownload(w, r, id)
	case http.MethodDelete:
		ws.serveDelete(w, r, id)
	default:
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (ws *Server) serveDownload(w http.ResponseWriter, r *http.Request, id string) {
	sf := ws.bridge.GetFile(id)
	if sf == nil {
		jsonErr(w, http.StatusNotFound, "file not found")
		return
	}
	if sf.IsExpired() {
		jsonErr(w, http.StatusGone, "file expired")
		return
	}

	// Track active download to align with core storage semantics.
	sf.IncrementActive()
	defer sf.DecrementActive()

	// For HTTP, we approximate \"complete\" as ServeFile returning.
	defer sf.IncrementDownloads()

	ws.hub.publish(Event{Type: "download", FileID: id, Filename: sf.Metadata.Filename})

	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="%s"`, sf.Metadata.Filename))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", sf.Metadata.Size))

	http.ServeFile(w, r, sf.Path)
}

func (ws *Server) serveDelete(w http.ResponseWriter, _ *http.Request, id string) {
	sf := ws.bridge.GetFile(id)
	if sf == nil {
		jsonErr(w, http.StatusNotFound, "file not found")
		return
	}
	ws.bridge.DeleteFile(id)
	ws.hub.publish(Event{Type: "delete", FileID: id, Filename: sf.Metadata.Filename})
	w.WriteHeader(http.StatusNoContent)
}

// ── /api/upload ──────────────────────────────────────────────────────────────

const maxHTTPUploadMB = 4 << 10 // 4 GiB parse limit

func (ws *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if !ws.bridge.TryAcquireUpload() {
		jsonErr(w, http.StatusTooManyRequests, "server at max upload concurrency")
		return
	}
	defer ws.bridge.ReleaseUpload()

	if err := r.ParseMultipartForm(maxHTTPUploadMB << 20); err != nil {
		jsonErr(w, http.StatusBadRequest, "parse form: "+err.Error())
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "form file: "+err.Error())
		return
	}
	defer file.Close()

	cfg := ws.bridge.Cfg()
	ttl := cfg.TTLDefault
	maxDL := cfg.MaxDownloads

	// Generate a unique upload ID for the temp file
	uploadID := fmt.Sprintf("http_%d", time.Now().UnixNano())
	tempPath := ws.bridge.CreateTemp(uploadID)

	// Stream to temp file
	dst, err := createFile(tempPath)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "create temp: "+err.Error())
		return
	}
	n, err := io.Copy(dst, file)
	dst.Close()
	if err != nil {
		ws.bridge.CleanupTemp(tempPath)
		jsonErr(w, http.StatusInternalServerError, "write temp: "+err.Error())
		return
	}

	// Build metadata
	fileID := uploadID // reuse; unique enough for single-node server
	meta := protocol.FileMetadata{
		Filename: filepath.Base(header.Filename),
		Size:     n,
	}

	sf, err := ws.bridge.MoveToStorage(tempPath, fileID, meta, ttl, maxDL)
	if err != nil {
		ws.bridge.CleanupTemp(tempPath)
		jsonErr(w, http.StatusInternalServerError, "store: "+err.Error())
		return
	}

	ws.hub.publish(Event{Type: "upload", FileID: sf.ID, Filename: sf.Metadata.Filename})

	jsonOK(w, map[string]string{
		"id":   sf.ID,
		"name": sf.Metadata.Filename,
	})
}
