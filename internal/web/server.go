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
	"archive/zip"
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
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
	mux.HandleFunc("/api/compute/enroll", ws.handleComputeEnroll)
	mux.HandleFunc("/api/compute/enroll/", ws.handleComputeEnrollByCode)
	mux.HandleFunc("/api/compute/installer/windows-amd64.exe", ws.handleWindowsWorkerBinary)
	mux.HandleFunc("/api/compute/tools/render", ws.handleComputeRenderTool)
	mux.HandleFunc("/api/compute/tools/render/install/", ws.handleComputeRenderToolInstall)
	mux.HandleFunc("/api/compute/tools/ocr", ws.handleComputeOCRTool)
	mux.HandleFunc("/api/compute/tools/ocr/install/", ws.handleComputeOCRToolInstall)
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
	computeInputUsage := ws.bridge.ComputeInputUsageMap()
	type fileDTO struct {
		ID            string    `json:"id"`
		Name          string    `json:"name"`
		Size          int64     `json:"size"`
		Downloads     int32     `json:"downloads"`
		MaxDownloads  int       `json:"max_downloads"`
		ExpiresAt     time.Time `json:"expires_at"`
		Expired       bool      `json:"expired"`
		ComputeInput  bool      `json:"compute_input,omitempty"`
		InUseByJobIDs []string  `json:"in_use_by_job_ids,omitempty"`
	}
	dtos := make([]fileDTO, 0, len(files))
	for _, f := range files {
		if _, isComputeArtifact := computeArtifactIDs[f.ID]; isComputeArtifact {
			continue
		}
		usage := computeInputUsage[f.ID]
		inUse := len(usage) > 0
		dtos = append(dtos, fileDTO{
			ID:            f.ID,
			Name:          f.Metadata.Filename,
			Size:          f.Metadata.Size,
			Downloads:     f.DownloadCount,
			MaxDownloads:  f.MaxDownloads,
			ExpiresAt:     f.ExpiresAt,
			Expired:       !inUse && f.IsExpired(),
			ComputeInput:  inUse,
			InUseByJobIDs: usage,
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
			"device_name":         worker.DeviceName,
			"os_info":             worker.OSInfo,
			"registration_ip":     worker.RegistrationIP,
			"enrolled_at":         worker.EnrolledAt,
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

func (ws *Server) handleComputeEnroll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !ws.requireComputeWriteAuth(w, r) {
		return
	}

	host := hostFromRequest(r)
	enrollment, err := ws.bridge.GenerateComputeEnrollment(host, time.Now())
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if enrollment == nil {
		jsonErr(w, http.StatusServiceUnavailable, "compute coordinator unavailable")
		return
	}

	_, windowsErr := locateWindowsWorkerBinary()
	windowsAvailable := windowsErr == nil
	installer := map[string]any{
		"windows_available":   windowsAvailable,
		"windows_binary_url":  "/api/compute/installer/windows-amd64.exe",
		"windows_package_url": fmt.Sprintf("/api/compute/enroll/%s/package/windows-amd64", enrollment.Code),
		"build_command":       "scripts/build_worker_windows.sh",
	}
	if windowsErr != nil {
		installer["windows_error"] = windowsErr.Error()
	}

	jsonStatus(w, http.StatusCreated, map[string]any{
		"enrollment": enrollment,
		"quick_start": map[string]string{
			"mac_linux": fmt.Sprintf("lantern worker --host %s --port %d --enroll-code %s --device-name my-device", enrollment.Host, enrollment.Port, enrollment.Code),
			"windows":   fmt.Sprintf("lantern-worker.exe --host %s --port %d --enroll-code %s --device-name my-device", enrollment.Host, enrollment.Port, enrollment.Code),
		},
		"installer": installer,
	})
}

func (ws *Server) handleComputeEnrollByCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !ws.requireComputeReadAuth(w, r) {
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/compute/enroll/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		jsonErr(w, http.StatusBadRequest, "missing enrollment code")
		return
	}
	code := strings.TrimSpace(parts[0])

	enrollment, ok := ws.bridge.ComputeEnrollment(code, time.Now())
	if !ok || enrollment == nil {
		jsonErr(w, http.StatusNotFound, "enrollment code not found")
		return
	}

	if len(parts) == 3 && parts[1] == "package" && parts[2] == "windows-amd64" {
		ws.serveWindowsWorkerPackage(w, enrollment)
		return
	}
	if len(parts) > 1 {
		jsonErr(w, http.StatusNotFound, "unsupported enrollment action")
		return
	}

	jsonOK(w, map[string]any{"enrollment": enrollment})
}

func (ws *Server) handleWindowsWorkerBinary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !ws.requireComputeReadAuth(w, r) {
		return
	}

	binaryPath, err := locateWindowsWorkerBinary()
	if err != nil {
		jsonErr(w, http.StatusNotFound, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/vnd.microsoft.portable-executable")
	w.Header().Set("Content-Disposition", `attachment; filename="lantern-worker-windows-amd64.exe"`)
	http.ServeFile(w, r, binaryPath)
}

func (ws *Server) handleComputeOCRTool(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !ws.requireComputeReadAuth(w, r) {
		return
	}

	path, err := exec.LookPath("tesseract")
	available := err == nil
	out := map[string]any{
		"tool":      "tesseract",
		"available": available,
		"path":      path,
		"install": map[string]string{
			"windows": "/api/compute/tools/ocr/install/windows",
			"linux":   "/api/compute/tools/ocr/install/linux",
			"macos":   "/api/compute/tools/ocr/install/macos",
		},
	}
	if err != nil {
		out["error"] = err.Error()
	}
	jsonOK(w, out)
}

func (ws *Server) handleComputeRenderTool(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !ws.requireComputeReadAuth(w, r) {
		return
	}

	path, err := exec.LookPath("blender")
	available := err == nil
	out := map[string]any{
		"tool":      "blender",
		"available": available,
		"path":      path,
		"install": map[string]string{
			"windows": "/api/compute/tools/render/install/windows",
			"linux":   "/api/compute/tools/render/install/linux",
			"macos":   "/api/compute/tools/render/install/macos",
		},
	}
	if err != nil {
		out["error"] = err.Error()
	}
	jsonOK(w, out)
}

func (ws *Server) handleComputeRenderToolInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !ws.requireComputeReadAuth(w, r) {
		return
	}

	platform := strings.TrimPrefix(r.URL.Path, "/api/compute/tools/render/install/")
	platform = strings.TrimSpace(strings.Trim(platform, "/"))
	if platform == "" {
		jsonErr(w, http.StatusBadRequest, "missing install platform")
		return
	}

	filename, content, ok := renderInstallScript(platform)
	if !ok {
		jsonErr(w, http.StatusNotFound, "unsupported platform")
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	_, _ = w.Write([]byte(content))
}

func (ws *Server) handleComputeOCRToolInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		jsonErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !ws.requireComputeReadAuth(w, r) {
		return
	}

	platform := strings.TrimPrefix(r.URL.Path, "/api/compute/tools/ocr/install/")
	platform = strings.TrimSpace(strings.Trim(platform, "/"))
	if platform == "" {
		jsonErr(w, http.StatusBadRequest, "missing install platform")
		return
	}

	filename, content, ok := ocrInstallScript(platform)
	if !ok {
		jsonErr(w, http.StatusNotFound, "unsupported platform")
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	_, _ = w.Write([]byte(content))
}

func (ws *Server) serveWindowsWorkerPackage(w http.ResponseWriter, enrollment *server.ComputeEnrollment) {
	if enrollment == nil {
		jsonErr(w, http.StatusBadRequest, "missing enrollment")
		return
	}

	binaryPath, err := locateWindowsWorkerBinary()
	if err != nil {
		jsonErr(w, http.StatusNotFound, err.Error())
		return
	}

	binBytes, err := os.ReadFile(binaryPath)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "failed to read worker binary")
		return
	}

	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)

	exeWriter, err := zw.Create("lantern-worker.exe")
	if err != nil {
		_ = zw.Close()
		jsonErr(w, http.StatusInternalServerError, "failed to build package")
		return
	}
	if _, err := exeWriter.Write(binBytes); err != nil {
		_ = zw.Close()
		jsonErr(w, http.StatusInternalServerError, "failed to write package binary")
		return
	}

	startScript := fmt.Sprintf(`@echo off
setlocal
set DEVICE_NAME=%%COMPUTERNAME%%
set /p DEVICE_NAME=Device name [%%DEVICE_NAME%%]:
if "%%DEVICE_NAME%%"=="" set DEVICE_NAME=%%COMPUTERNAME%%
echo Starting Lantern Worker for %%DEVICE_NAME%% ...
"%%~dp0lantern-worker.exe" worker --host %s --port %d --enroll-code %s --device-name "%%DEVICE_NAME%%"
pause
`, enrollment.Host, enrollment.Port, enrollment.Code)

	cmdWriter, err := zw.Create("START_WORKER.cmd")
	if err != nil {
		_ = zw.Close()
		jsonErr(w, http.StatusInternalServerError, "failed to build package")
		return
	}
	if _, err := cmdWriter.Write([]byte(startScript)); err != nil {
		_ = zw.Close()
		jsonErr(w, http.StatusInternalServerError, "failed to write package script")
		return
	}

	readme := fmt.Sprintf(`Lantern Worker Package (Windows)

1. Extract this ZIP.
2. Double-click START_WORKER.cmd.
3. Enter a friendly device name when prompted.
4. Keep that window open while this machine is acting as a worker.

Enrollment code: %s
Coordinator: %s:%d
Code expires: %s
`, enrollment.Code, enrollment.Host, enrollment.Port, enrollment.ExpiresAt.Format(time.RFC1123))
	readmeWriter, err := zw.Create("README.txt")
	if err != nil {
		_ = zw.Close()
		jsonErr(w, http.StatusInternalServerError, "failed to build package")
		return
	}
	if _, err := readmeWriter.Write([]byte(readme)); err != nil {
		_ = zw.Close()
		jsonErr(w, http.StatusInternalServerError, "failed to write package readme")
		return
	}

	if err := zw.Close(); err != nil {
		jsonErr(w, http.StatusInternalServerError, "failed to finalize package")
		return
	}

	filename := fmt.Sprintf("lantern-worker-%s-windows-amd64.zip", strings.ToLower(enrollment.Code))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	_, _ = w.Write(buf.Bytes())
}

func locateWindowsWorkerBinary() (string, error) {
	if override := strings.TrimSpace(os.Getenv("LANTERN_WORKER_WINDOWS_EXE")); override != "" {
		if info, err := os.Stat(override); err == nil && !info.IsDir() {
			return override, nil
		}
	}

	candidates := []string{
		filepath.Join("bin", "lantern-worker-windows-amd64.exe"),
	}
	if exePath, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exePath), "lantern-worker-windows-amd64.exe"))
	}

	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("windows worker binary not found; run scripts/build_worker_windows.sh")
}

func ocrInstallScript(platform string) (string, string, bool) {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "windows", "win", "win64":
		return "install_ocr_windows.ps1", `# Lantern OCR Tool Installer (Windows)
Set-ExecutionPolicy -Scope Process -ExecutionPolicy Bypass -Force

if (Get-Command winget -ErrorAction SilentlyContinue) {
	winget install --id UB-Mannheim.TesseractOCR -e --accept-source-agreements --accept-package-agreements
} elseif (Get-Command choco -ErrorAction SilentlyContinue) {
	choco install tesseract -y
} else {
	Write-Error "Install winget or chocolatey, then re-run this script."
	exit 1
}

tesseract --version
Write-Output "OCR tool installation completed."
`, true
	case "linux":
		return "install_ocr_linux.sh", `#!/usr/bin/env bash
set -euo pipefail

if command -v apt-get >/dev/null 2>&1; then
	sudo apt-get update
	sudo apt-get install -y tesseract-ocr
elif command -v dnf >/dev/null 2>&1; then
	sudo dnf install -y tesseract
elif command -v yum >/dev/null 2>&1; then
	sudo yum install -y tesseract
elif command -v pacman >/dev/null 2>&1; then
	sudo pacman -Sy --noconfirm tesseract
else
	echo "No supported package manager found. Install tesseract manually."
	exit 1
fi

tesseract --version
echo "OCR tool installation completed."
`, true
	case "macos", "darwin", "mac":
		return "install_ocr_macos.sh", `#!/usr/bin/env bash
set -euo pipefail

if ! command -v brew >/dev/null 2>&1; then
	echo "Homebrew is required. Install Homebrew first: https://brew.sh"
	exit 1
fi

brew install tesseract
tesseract --version
echo "OCR tool installation completed."
`, true
	default:
		return "", "", false
	}
}

	func renderInstallScript(platform string) (string, string, bool) {
		switch strings.ToLower(strings.TrimSpace(platform)) {
		case "windows", "win", "win64":
			return "install_render_windows.ps1", `# Lantern Render Tool Installer (Windows)
	Set-ExecutionPolicy -Scope Process -ExecutionPolicy Bypass -Force

	if (Get-Command winget -ErrorAction SilentlyContinue) {
		winget install --id BlenderFoundation.Blender -e --accept-source-agreements --accept-package-agreements
	} elseif (Get-Command choco -ErrorAction SilentlyContinue) {
		choco install blender -y
	} else {
		Write-Error "Install winget or chocolatey, then re-run this script."
		exit 1
	}

	if (Get-Command blender -ErrorAction SilentlyContinue) {
		blender --version
	} else {
		Write-Output "Blender installed but not on PATH yet; restart your terminal and verify manually."
	}
	Write-Output "Render tool installation completed."
	`, true
		case "linux":
			return "install_render_linux.sh", `#!/usr/bin/env bash
	set -euo pipefail

	if command -v apt-get >/dev/null 2>&1; then
		sudo apt-get update
		sudo apt-get install -y blender
	elif command -v dnf >/dev/null 2>&1; then
		sudo dnf install -y blender
	elif command -v yum >/dev/null 2>&1; then
		sudo yum install -y blender
	elif command -v pacman >/dev/null 2>&1; then
		sudo pacman -Sy --noconfirm blender
	else
		echo "No supported package manager found. Install blender manually."
		exit 1
	fi

	blender --version || true
	echo "Render tool installation completed."
	`, true
		case "macos", "darwin", "mac":
			return "install_render_macos.sh", `#!/usr/bin/env bash
	set -euo pipefail

	if ! command -v brew >/dev/null 2>&1; then
		echo "Homebrew is required. Install Homebrew first: https://brew.sh"
		exit 1
	fi

	brew install --cask blender
	if command -v blender >/dev/null 2>&1; then
		blender --version
	else
		echo "Blender installed but not on PATH yet; launch once from Applications or add its binary to PATH."
	fi
	echo "Render tool installation completed."
	`, true
		default:
			return "", "", false
		}
	}

func hostFromRequest(r *http.Request) string {
	if r == nil {
		return "127.0.0.1"
	}
	host := strings.TrimSpace(r.URL.Query().Get("worker_host"))
	if host != "" {
		return host
	}
	host = strings.TrimSpace(r.Host)
	if host == "" {
		return "127.0.0.1"
	}
	parsed, _, err := net.SplitHostPort(host)
	if err == nil && parsed != "" {
		return parsed
	}
	return host
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
	inUseByJobs := ws.bridge.ComputeInputUsage(id)
	if sf.IsExpired() && len(inUseByJobs) == 0 {
		jsonErr(w, http.StatusGone, "file expired")
		return
	}

	// Track active download to align with core storage semantics.
	sf.IncrementActive()
	defer sf.DecrementActive()

	// For HTTP, we approximate \"complete\" as ServeFile returning.
	if len(inUseByJobs) == 0 {
		defer sf.IncrementDownloads()
	}

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
	if inUse := ws.bridge.ComputeInputUsage(id); len(inUse) > 0 {
		jsonStatus(w, http.StatusConflict, map[string]any{
			"error":             "file is currently referenced by compute jobs",
			"in_use_by_job_ids": inUse,
		})
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
