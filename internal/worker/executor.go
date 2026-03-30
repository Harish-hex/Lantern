package worker

import (
	"context"
	"encoding/json"
)

// Executor defines the interface for running a specific compute template task.
type Executor interface {
	// ID returns the template ID this executor handles (e.g. "data_processing_batch").
	ID() string

	// Execute runs the core logic of the task.
	// The payload is the JSON definition for this specific task.
	// The outputDir is an absolute path where the executor should place its results.
	Execute(ctx context.Context, payload json.RawMessage, outputDir string) error
}

// Registry holds the available executors for a worker.
type Registry struct {
	executors map[string]Executor
}

func NewRegistry() *Registry {
	return &Registry{
		executors: make(map[string]Executor),
	}
}

// Register adds an executor to the registry.
func (r *Registry) Register(e Executor) {
	r.executors[e.ID()] = e
}

// Get finds an executor by template ID.
func (r *Registry) Get(id string) (Executor, bool) {
	e, ok := r.executors[id]
	return e, ok
}

// Capabilities returns a list of template IDs this registry can handle.
func (r *Registry) Capabilities() []string {
	var caps []string
	for id := range r.executors {
		caps = append(caps, id)
	}
	return caps
}
