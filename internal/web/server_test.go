package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
	"unsafe"

	"github.com/Harish-hex/Lantern/internal/config"
	"github.com/Harish-hex/Lantern/internal/protocol"
	lserver "github.com/Harish-hex/Lantern/internal/server"
)

func TestHandleComputeJobPreflightRequiresToken(t *testing.T) {
	ws, _, token := newComputeWebTestServer(t, true)
	if token == "" {
		t.Fatal("expected test token")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/compute/jobs/preflight", bytes.NewBufferString(`{"template":"render_frames"}`))
	req.Header.Set("Content-Type", "application/json")
	ws.httpSrv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestHandleComputeJobPreflightReturnsReadyPreview(t *testing.T) {
	ws, srv, token := newComputeWebTestServer(t, true)
	registerTestWorker(t, srv, "worker-render-1", []string{"render_frames"}, time.Now().UTC())
	registerTestWorker(t, srv, "worker-render-2", []string{"render_frames"}, time.Now().UTC())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/compute/jobs/preflight", bytes.NewBufferString(`{"template":"render_frames","inputs":{"scene":"demo.blend","frame_start":1,"frame_end":8,"chunk_size":4},"settings":{"stitched_output":true,"output_format":"png"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Lantern-Compute-Token", token)
	ws.httpSrv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		TemplateID string                   `json:"template_id"`
		Preflight  lserver.ComputePreflight `json:"preflight"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.TemplateID != "render_frames" {
		t.Fatalf("template id = %q, want render_frames", resp.TemplateID)
	}
	if !resp.Preflight.Ready {
		t.Fatal("expected ready preflight")
	}
}

func TestHandleComputeJobPreflightReturnsNotReadyPreview(t *testing.T) {
	ws, _, token := newComputeWebTestServer(t, true)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/compute/jobs/preflight", bytes.NewBufferString(`{"template":"render_frames","inputs":{"scene":"demo.blend","frame_start":1,"frame_end":8,"chunk_size":4},"settings":{"stitched_output":true,"output_format":"png"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Lantern-Compute-Token", token)
	ws.httpSrv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Preflight lserver.ComputePreflight `json:"preflight"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	// Preflight should be ready even without workers — jobs queue and wait.
	if !resp.Preflight.Ready {
		t.Fatal("expected ready preflight — jobs should queue even without healthy workers")
	}
	// But confidence should not be High when no workers exist.
	if resp.Preflight.Confidence == "High" {
		t.Fatal("expected non-High confidence without workers")
	}
}

func TestHandleComputeJobPreflightRejectsInvalidTemplate(t *testing.T) {
	ws, _, token := newComputeWebTestServer(t, true)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/compute/jobs/preflight", bytes.NewBufferString(`{"template":"missing_template"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Lantern-Compute-Token", token)
	ws.httpSrv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestHandleComputeJobsSubmitStillReturnsCreated(t *testing.T) {
	ws, srv, token := newComputeWebTestServer(t, true)
	registerTestWorker(t, srv, "worker-render-submit", []string{"render_frames"}, time.Now().UTC())
	registerTestWorker(t, srv, "worker-render-submit-b", []string{"render_frames"}, time.Now().UTC())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/compute/jobs", bytes.NewBufferString(`{"template":"render_frames","inputs":{"scene":"demo.blend","frame_start":1,"frame_end":8,"chunk_size":4},"settings":{"stitched_output":true,"output_format":"png"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Lantern-Compute-Token", token)
	ws.httpSrv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
}

func TestHandleComputeJobDeleteRemovesJob(t *testing.T) {
	ws, srv, token := newComputeWebTestServer(t, true)
	registerTestWorker(t, srv, "worker-delete-job", []string{"render_frames"}, time.Now().UTC())

	createRec := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, "/api/compute/jobs", bytes.NewBufferString(`{"template":"render_frames","inputs":{"scene":"demo.blend","frame_start":1,"frame_end":8,"chunk_size":4},"settings":{"stitched_output":true,"output_format":"png"}}`))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("X-Lantern-Compute-Token", token)
	ws.httpSrv.Handler.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d body=%s", createRec.Code, http.StatusCreated, createRec.Body.String())
	}

	var created struct {
		Job lserver.ComputeJob `json:"job"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.Job.ID == "" {
		t.Fatal("expected created job id")
	}

	deleteRec := httptest.NewRecorder()
	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/compute/jobs/"+created.Job.ID, nil)
	deleteReq.Header.Set("X-Lantern-Compute-Token", token)
	ws.httpSrv.Handler.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want %d body=%s", deleteRec.Code, http.StatusOK, deleteRec.Body.String())
	}

	stateRec := httptest.NewRecorder()
	stateReq := httptest.NewRequest(http.MethodGet, "/api/compute/jobs/"+created.Job.ID, nil)
	stateReq.Header.Set("X-Lantern-Compute-Token", token)
	ws.httpSrv.Handler.ServeHTTP(stateRec, stateReq)
	if stateRec.Code != http.StatusNotFound {
		t.Fatalf("job state status = %d, want %d", stateRec.Code, http.StatusNotFound)
	}
}

func TestHandleComputeWorkerDeleteRemovesWorker(t *testing.T) {
	ws, srv, token := newComputeWebTestServer(t, true)
	registerTestWorker(t, srv, "worker-delete-1", []string{"render_frames"}, time.Now().UTC())

	deleteRec := httptest.NewRecorder()
	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/compute/workers/worker-delete-1", nil)
	deleteReq.Header.Set("X-Lantern-Compute-Token", token)
	ws.httpSrv.Handler.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want %d body=%s", deleteRec.Code, http.StatusOK, deleteRec.Body.String())
	}

	workersRec := httptest.NewRecorder()
	workersReq := httptest.NewRequest(http.MethodGet, "/api/compute/workers", nil)
	workersReq.Header.Set("X-Lantern-Compute-Token", token)
	ws.httpSrv.Handler.ServeHTTP(workersRec, workersReq)
	if workersRec.Code != http.StatusOK {
		t.Fatalf("workers status = %d, want %d", workersRec.Code, http.StatusOK)
	}

	var workersResp struct {
		Workers []map[string]any `json:"workers"`
	}
	if err := json.Unmarshal(workersRec.Body.Bytes(), &workersResp); err != nil {
		t.Fatalf("decode workers response: %v", err)
	}
	if len(workersResp.Workers) != 0 {
		t.Fatalf("workers len = %d, want 0", len(workersResp.Workers))
	}
}

func TestHandleFilesExcludesComputeArtifacts(t *testing.T) {
	ws, srv, token := newComputeWebTestServer(t, true)
	coordinator := testCoordinator(t, srv)
	registerTestWorker(t, srv, "worker-file-filter", []string{"generic_v1"}, time.Now().UTC())

	if _, err := addStoredFileForWebTest(ws, "user_file_1", "notes.txt", []byte("hello")); err != nil {
		t.Fatalf("add user file: %v", err)
	}
	if _, err := addStoredFileForWebTest(ws, "artifact_file_1", "task-artifact.bin", []byte("artifact")); err != nil {
		t.Fatalf("add artifact file: %v", err)
	}

	now := time.Now().UTC()
	if _, _, err := coordinator.SubmitJob("job-file-filter", lserver.ComputeJobSubmit{
		Type:  "generic_v1",
		Tasks: []json.RawMessage{json.RawMessage(`{"task":1}`)},
	}, now); err != nil {
		t.Fatalf("SubmitJob: %v", err)
	}

	task, ok := coordinator.ClaimTask("worker-file-filter", now.Add(10*time.Millisecond))
	if !ok || task == nil {
		t.Fatal("expected task claim")
	}
	if _, _, err := coordinator.CompleteTask("worker-file-filter", task.ID, "artifact_file_1", "sha256:test", now.Add(20*time.Millisecond)); err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/files", nil)
	ws.httpSrv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var files []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &files); err != nil {
		t.Fatalf("decode files response: %v", err)
	}
	for _, file := range files {
		if file.ID == "artifact_file_1" || file.ID == "job-file-filter_final_package" {
			t.Fatalf("compute artifact leaked into /api/files: %s", file.ID)
		}
	}

	recArtifacts := httptest.NewRecorder()
	reqArtifacts := httptest.NewRequest(http.MethodGet, "/api/compute/artifacts", nil)
	reqArtifacts.Header.Set("X-Lantern-Compute-Token", token)
	ws.httpSrv.Handler.ServeHTTP(recArtifacts, reqArtifacts)
	if recArtifacts.Code != http.StatusOK {
		t.Fatalf("artifact status = %d, want %d body=%s", recArtifacts.Code, http.StatusOK, recArtifacts.Body.String())
	}

	var artifactsResp struct {
		Artifacts []struct {
			ID string `json:"id"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(recArtifacts.Body.Bytes(), &artifactsResp); err != nil {
		t.Fatalf("decode artifacts response: %v", err)
	}
	if len(artifactsResp.Artifacts) == 0 {
		t.Fatal("expected compute artifacts response")
	}
}

func newComputeWebTestServer(t *testing.T, requireToken bool) (*Server, *lserver.Server, string) {
	t.Helper()

	base := t.TempDir()
	cfg := config.Default()
	cfg.ComputeEnabled = true
	cfg.StorageDir = filepath.Join(base, "storage")
	cfg.TempDir = filepath.Join(base, "tmp")
	cfg.IndexDir = filepath.Join(base, "index")
	cfg.HTTPPort = 0
	token := ""
	if requireToken {
		token = "test-compute-token"
		cfg.ComputeWorkerAuthToken = token
		cfg.ComputeRequireToken = true
	}

	srv, err := lserver.New(cfg)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	return New(srv.Bridge()), srv, token
}

func registerTestWorker(t *testing.T, srv *lserver.Server, workerID string, capabilities []string, now time.Time) {
	t.Helper()

	coordinator := testCoordinator(t, srv)
	coordinator.RegisterWorker(workerID, capabilities, now)
}

func testCoordinator(t *testing.T, srv *lserver.Server) *lserver.Coordinator {
	t.Helper()

	field := reflect.ValueOf(srv).Elem().FieldByName("compute")
	if !field.IsValid() {
		t.Fatal("missing compute field")
	}
	return reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().Interface().(*lserver.Coordinator)
}

func addStoredFileForWebTest(ws *Server, fileID, filename string, content []byte) (*lserver.StoredFile, error) {
	temp := ws.bridge.CreateTemp(fileID)
	if err := os.WriteFile(temp, content, 0o644); err != nil {
		return nil, err
	}
	meta := protocol.FileMetadata{
		Filename: filename,
		Size:     int64(len(content)),
	}
	return ws.bridge.MoveToStorage(temp, fileID, meta, 3600, 0)
}
