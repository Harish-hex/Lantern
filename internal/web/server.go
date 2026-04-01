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
	mux.HandleFunc("/api/compute/templates", ws.handleComputeTemplates)
	mux.HandleFunc("/api/compute/overview", ws.handleComputeOverview)
	mux.HandleFunc("/api/compute/actions/requeue-stalled", ws.handleComputeActions)
	mux.HandleFunc("/api/compute/jobs/preflight", ws.handleComputeJobPreflight)
	mux.HandleFunc("/api/compute/jobs", ws.handleComputeJobs)
	mux.HandleFunc("/api/compute/jobs/", ws.handleComputeJobByID)
	mux.HandleFunc("/api/compute/artifacts", ws.handleComputeArtifacts)
	mux.HandleFunc("/api/compute/workers", ws.handleComputeWorkers)
	mux.HandleFunc("/api/compute/workers/", ws.handleComputeWorkerByID)
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

func jsonStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
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
	computeArtifactIDs := ws.bridge.ComputeArtifactIDs()
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
		if _, isComputeArtifact := computeArtifactIDs[f.ID]; isComputeArtifact {
			continue
		}
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

func (ws *Server) handleComputeArtifacts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !ws.requireComputeReadAuth(w, r) {
		return
	}
	jsonOK(w, map[string]any{"artifacts": ws.bridge.ComputeArtifacts()})
}

func (ws *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	jsonOK(w, ws.bridge.Stats())
}

func (ws *Server) computeTokenFromRequest(r *http.Request) string {
	token := strings.TrimSpace(r.Header.Get("X-Lantern-Compute-Token"))
	if token == "" {
		token = strings.TrimSpace(r.URL.Query().Get("token"))
	}
	return token
}

func (ws *Server) requireComputeReadAuth(w http.ResponseWriter, r *http.Request) bool {
	if !ws.bridge.ComputeEnabled() {
		jsonErr(w, http.StatusServiceUnavailable, "compute coordinator disabled")
		return false
	}
	token := ws.computeTokenFromRequest(r)
	if !ws.bridge.ComputeReadTokenValid(token) {
		jsonErr(w, http.StatusUnauthorized, "invalid compute token")
		return false
	}
	return true
}

func (ws *Server) requireComputeWriteAuth(w http.ResponseWriter, r *http.Request) bool {
	return ws.requireComputeReadAuth(w, r)
}

func (ws *Server) decodeJSONBody(r *http.Request, dest any) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(body)) == "" {
		return nil
	}
	return json.Unmarshal(body, dest)
}

func (ws *Server) handleComputeTemplates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !ws.requireComputeReadAuth(w, r) {
		return
	}
	jsonOK(w, map[string]any{"templates": ws.bridge.ComputeTemplates()})
}

func (ws *Server) handleComputeOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !ws.requireComputeReadAuth(w, r) {
		return
	}
	overview := ws.bridge.ComputeOverview(time.Now())
	if overview == nil {
		jsonErr(w, http.StatusServiceUnavailable, "compute coordinator unavailable")
		return
	}
	jsonOK(w, overview)
}

func (ws *Server) handleComputeJobPreflight(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !ws.requireComputeWriteAuth(w, r) {
		return
	}

	var body struct {
		Type     string            `json:"type"`
		Template string            `json:"template"`
		Inputs   json.RawMessage   `json:"inputs"`
		Settings json.RawMessage   `json:"settings"`
		Tasks    []json.RawMessage `json:"tasks"`
	}
	if err := ws.decodeJSONBody(r, &body); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid preflight request")
		return
	}

	preview, err := ws.bridge.PreviewComputeJob(server.ComputeJobSubmit{
		Type:     body.Type,
		Template: body.Template,
		Inputs:   body.Inputs,
		Settings: body.Settings,
		Tasks:    body.Tasks,
	}, time.Now())
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if preview == nil {
		jsonErr(w, http.StatusServiceUnavailable, "compute coordinator unavailable")
		return
	}

	jsonOK(w, preview)
}

func (ws *Server) handleComputeJobs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !ws.requireComputeReadAuth(w, r) {
			return
		}

		jobs := ws.bridge.ComputeJobs()
		out := make([]map[string]any, 0, len(jobs))
		for _, job := range jobs {
			if job == nil {
				continue
			}
			progress := 0.0
			if job.TotalTasks > 0 {
				progress = float64(job.CompletedTasks+job.FailedTasks) * 100 / float64(job.TotalTasks)
			}
			out = append(out, map[string]any{
				"id":                     job.ID,
				"type":                   job.Type,
				"template_id":            job.TemplateID,
				"template_name":          job.TemplateName,
				"output_kind":            job.OutputKind,
				"status":                 job.Status,
				"confidence":             job.Confidence,
				"needs_attention_reason": job.NeedsAttentionReason,
				"failure_category":       job.FailureCategory,
				"created_at":             job.CreatedAt,
				"updated_at":             job.UpdatedAt,
				"started_at":             job.StartedAt,
				"finished_at":            job.FinishedAt,
				"total_tasks":            job.TotalTasks,
				"completed_tasks":        job.CompletedTasks,
				"failed_tasks":           job.FailedTasks,
				"retrying_tasks":         job.RetryingTasks,
				"artifact_count":         len(job.Artifacts),
				"progress_pct":           progress,
			})
		}

		jsonOK(w, map[string]any{"jobs": out})
	case http.MethodPost:
		if !ws.requireComputeWriteAuth(w, r) {
			return
		}

		var body struct {
			JobID    string            `json:"job_id"`
			Type     string            `json:"type"`
			Template string            `json:"template"`
			Inputs   json.RawMessage   `json:"inputs"`
			Settings json.RawMessage   `json:"settings"`
			Tasks    []json.RawMessage `json:"tasks"`
		}
		if err := ws.decodeJSONBody(r, &body); err != nil {
			jsonErr(w, http.StatusBadRequest, "invalid job request")
			return
		}

		job, acceptedTasks, err := ws.bridge.SubmitComputeJob(body.JobID, server.ComputeJobSubmit{
			Type:     body.Type,
			Template: body.Template,
			Inputs:   body.Inputs,
			Settings: body.Settings,
			Tasks:    body.Tasks,
		}, time.Now())
		if err != nil {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}

		jsonStatus(w, http.StatusCreated, map[string]any{
			"job":            job,
			"accepted_tasks": acceptedTasks,
		})
	default:
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (ws *Server) handleComputeJobByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/compute/jobs/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		jsonErr(w, http.StatusBadRequest, "missing job id")
		return
	}
	jobID := parts[0]

	if len(parts) == 1 && r.Method == http.MethodGet {
		if !ws.requireComputeReadAuth(w, r) {
			return
		}

		job, tasks, ok := ws.bridge.ComputeJobState(jobID)
		if !ok || job == nil {
			jsonErr(w, http.StatusNotFound, "job not found")
			return
		}

		jsonOK(w, map[string]any{
			"job":   job,
			"tasks": tasks,
		})
		return
	}

	if len(parts) == 1 && r.Method == http.MethodDelete {
		if !ws.requireComputeWriteAuth(w, r) {
			return
		}

		job, removedTasks, err := ws.bridge.DeleteComputeJob(jobID, time.Now())
		if err != nil {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}

		jsonOK(w, map[string]any{
			"job":               job,
			"removed_tasks":     removedTasks,
			"removed_artifacts": len(job.Artifacts),
			"message":           "job deleted",
		})
		return
	}

	if len(parts) == 2 && parts[1] == "retry" && r.Method == http.MethodPost {
		if !ws.requireComputeWriteAuth(w, r) {
			return
		}

		job, err := ws.bridge.RetryComputeJob(jobID, time.Now())
		if err != nil {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}

		jsonOK(w, map[string]any{
			"job":     job,
			"message": "job requeued for execution",
		})
		return
	}

	jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (ws *Server) handleComputeWorkers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !ws.requireComputeReadAuth(w, r) {
		return
	}

	now := time.Now()
	workers := ws.bridge.ComputeWorkers()
	out := make([]map[string]any, 0, len(workers))
	for _, worker := range workers {
		if worker == nil {
			continue
		}
		freshness := "healthy"
		age := now.Sub(worker.LastSeen)
		if age > 2*ws.bridge.Cfg().ComputeHeartbeat {
			freshness = "warning"
		}
		if age > ws.bridge.Cfg().ComputeLeaseTTL {
			freshness = "stale"
		}

		flakiness := "stable"
		if worker.Disabled {
			flakiness = "disabled"
		} else if worker.ReliabilityScore < 90 || worker.StaleReassignments > 0 {
			flakiness = "warning"
		}

		out = append(out, map[string]any{
			"id":                  worker.ID,
			"status":              worker.Status,
			"last_seen":           worker.LastSeen,
			"lease_until":         worker.LeaseUntil,
			"capabilities":        worker.Capabilities,
			"completed_tasks":     worker.CompletedTasks,
			"failed_tasks":        worker.FailedTasks,
			"stale_reassignments": worker.StaleReassignments,
			"reliability_score":   worker.ReliabilityScore,
			"disabled":            worker.Disabled,
			"disabled_reason":     worker.DisabledReason,
			"freshness":           freshness,
			"flakiness":           flakiness,
		})
	}

	jsonOK(w, map[string]any{"workers": out})
}

func (ws *Server) handleComputeWorkerByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/compute/workers/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		jsonErr(w, http.StatusBadRequest, "missing worker id")
		return
	}
	workerID := parts[0]

	if len(parts) == 1 && r.Method == http.MethodDelete {
		if !ws.requireComputeWriteAuth(w, r) {
			return
		}

		worker, requeuedTasks, err := ws.bridge.DeleteComputeWorker(workerID, time.Now())
		if err != nil {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}

		jsonOK(w, map[string]any{
			"worker":         worker,
			"requeued_tasks": requeuedTasks,
			"message":        "worker deleted",
		})
		return
	}

	if len(parts) != 2 || r.Method != http.MethodPost {
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !ws.requireComputeWriteAuth(w, r) {
		return
	}

	action := parts[1]
	if action != "disable" && action != "enable" {
		jsonErr(w, http.StatusNotFound, "unknown worker action")
		return
	}

	var body struct {
		Reason string `json:"reason"`
	}
	if err := ws.decodeJSONBody(r, &body); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid worker action request")
		return
	}

	worker, err := ws.bridge.SetComputeWorkerDisabled(workerID, action == "disable", body.Reason, time.Now())
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}

	jsonOK(w, map[string]any{
		"worker":  worker,
		"message": fmt.Sprintf("worker %s %sd", workerID, action),
	})
}

func (ws *Server) handleComputeActions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !ws.requireComputeWriteAuth(w, r) {
		return
	}
	taskIDs, err := ws.bridge.RequeueStalledComputeTasks(time.Now())
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonOK(w, map[string]any{
		"task_ids": taskIDs,
		"message":  fmt.Sprintf("processed %d stalled task(s)", len(taskIDs)),
	})
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
