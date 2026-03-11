package web

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
)

// Event is broadcast to all connected WebSocket clients.
type Event struct {
	Type     string `json:"type"`               // "upload" | "download" | "delete"
	FileID   string `json:"file_id"`
	Filename string `json:"filename,omitempty"`
}

// Hub manages active SSE (Server-Sent Events) connections and broadcasts events.
// We use SSE instead of WebSocket to avoid external dependencies while still
// providing live updates to browser clients.
type Hub struct {
	mu      sync.RWMutex
	clients map[chan Event]struct{}
}

func newHub() *Hub {
	return &Hub{clients: make(map[chan Event]struct{})}
}

// run is a no-op for SSE; subscription is per-request.
func (h *Hub) run() {}

// publish sends an event to all connected SSE clients.
func (h *Hub) publish(e Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.clients {
		select {
		case ch <- e:
		default: // slow client: skip rather than block
		}
	}
}

func (h *Hub) subscribe() chan Event {
	ch := make(chan Event, 32)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *Hub) unsubscribe(ch chan Event) {
	h.mu.Lock()
	delete(h.clients, ch)
	h.mu.Unlock()
}

// serveSSE streams events as Server-Sent Events until the client disconnects.
func (h *Hub) serveSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ch := h.subscribe()
	defer h.unsubscribe(ch)

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(event)
			if err != nil {
				log.Printf("web: sse marshal error: %v", err)
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}
