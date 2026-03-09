package server

// Semaphore is a counting semaphore backed by a buffered channel.
// It limits concurrent access to a shared resource.
type Semaphore struct {
	ch chan struct{}
}

// NewSemaphore creates a semaphore with the given capacity.
func NewSemaphore(n int) *Semaphore {
	return &Semaphore{ch: make(chan struct{}, n)}
}

// Acquire blocks until a slot is available.
func (s *Semaphore) Acquire() {
	s.ch <- struct{}{}
}

// TryAcquire attempts to acquire immediately. Returns false if at capacity.
func (s *Semaphore) TryAcquire() bool {
	select {
	case s.ch <- struct{}{}:
		return true
	default:
		return false
	}
}

// Release frees one slot. Must be called exactly once per Acquire/TryAcquire.
func (s *Semaphore) Release() {
	<-s.ch
}
