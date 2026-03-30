package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"
	"unsafe"

	"github.com/Harish-hex/Lantern/internal/config"
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
	if resp.Preflight.Ready {
		t.Fatal("expected not-ready preflight without workers")
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

	field := reflect.ValueOf(srv).Elem().FieldByName("compute")
	if !field.IsValid() {
		t.Fatal("missing compute field")
	}
	coordinator := reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().Interface().(*lserver.Coordinator)
	coordinator.RegisterWorker(workerID, capabilities, now)
}
