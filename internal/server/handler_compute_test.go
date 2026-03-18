package server

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/Harish-hex/Lantern/internal/config"
	"github.com/Harish-hex/Lantern/internal/index"
	"github.com/Harish-hex/Lantern/internal/protocol"
)

func TestHandleTaskResultACKsAndCompletesTask(t *testing.T) {
	h, sid, now := newComputeHandlerTest(t)

	h.server.compute.RegisterWorker("worker-1", nil, now)
	_, _, err := h.server.compute.SubmitJob("job-handler-ok", ComputeJobSubmit{
		Type:  "edge_tiles_v1",
		Tasks: []json.RawMessage{json.RawMessage(`{"tile_x":0}`)},
	}, now)
	if err != nil {
		t.Fatalf("SubmitJob: %v", err)
	}

	task, ok := h.server.compute.ClaimTask("worker-1", now.Add(10*time.Millisecond))
	if !ok || task == nil {
		t.Fatal("expected task claim")
	}

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	go func() {
		defer serverConn.Close()
		h.handleTaskResult(serverConn, protocol.NewHeader(protocol.MsgControl, 0, 0, 7, sid), protocol.ControlPayload{
			Type:     protocol.CtrlTaskResult,
			WorkerID: "worker-1",
			TaskID:   task.ID,
			Checksum: "sha256:done",
		})
	}()

	resp := readControlPayload(t, clientConn)
	if resp.Type != protocol.CtrlACK {
		t.Fatalf("response type = %q, want ACK", resp.Type)
	}
	if resp.TaskID != task.ID {
		t.Fatalf("response task_id = %q, want %q", resp.TaskID, task.ID)
	}
	if resp.Status != "completed" {
		t.Fatalf("response status = %q, want completed", resp.Status)
	}
}

func TestHandleTaskFailValidatesFailureMessage(t *testing.T) {
	h, sid, now := newComputeHandlerTest(t)

	h.server.compute.RegisterWorker("worker-1", nil, now)
	_, _, err := h.server.compute.SubmitJob("job-handler-fail", ComputeJobSubmit{
		Type:  "edge_tiles_v1",
		Tasks: []json.RawMessage{json.RawMessage(`{"tile_x":0}`)},
	}, now)
	if err != nil {
		t.Fatalf("SubmitJob: %v", err)
	}

	task, ok := h.server.compute.ClaimTask("worker-1", now.Add(10*time.Millisecond))
	if !ok || task == nil {
		t.Fatal("expected task claim")
	}

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	go func() {
		defer serverConn.Close()
		h.handleTaskFail(serverConn, protocol.NewHeader(protocol.MsgControl, 0, 0, 9, sid), protocol.ControlPayload{
			Type:     protocol.CtrlTaskFail,
			WorkerID: "worker-1",
			TaskID:   task.ID,
		})
	}()

	resp := readControlPayload(t, clientConn)
	if resp.Type != protocol.CtrlError {
		t.Fatalf("response type = %q, want ERROR", resp.Type)
	}
	if resp.Message != "missing failure message" {
		t.Fatalf("response message = %q, want missing failure message", resp.Message)
	}
}

func newComputeHandlerTest(t *testing.T) (*Handler, [16]byte, time.Time) {
	t.Helper()

	cfg := config.Default()
	cfg.ComputeEnabled = true

	idx, err := index.NewJSONStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}

	srv := &Server{
		cfg:     cfg,
		store:   NewSessionStore(),
		compute: NewCoordinator(cfg, idx),
	}

	var sid [16]byte
	sid[0] = 1
	sid[15] = 7

	return NewHandler(srv, cfg, srv.store, nil, nil, nil, nil), sid, time.Now().UTC()
}

func readControlPayload(t *testing.T, conn net.Conn) protocol.ControlPayload {
	t.Helper()

	_, payload, _, err := protocol.ReadPacket(conn)
	if err != nil {
		t.Fatalf("ReadPacket: %v", err)
	}

	var ctrl protocol.ControlPayload
	if err := json.Unmarshal(payload, &ctrl); err != nil {
		t.Fatalf("Unmarshal response: %v", err)
	}
	return ctrl
}
