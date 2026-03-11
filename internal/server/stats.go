package server

import (
	"sync"
	"time"
)

type TransferStat struct {
	Direction  string    `json:"direction"`
	FileID     string    `json:"file_id,omitempty"`
	Filename   string    `json:"filename"`
	Bytes      int64     `json:"bytes"`
	DurationMS int64     `json:"duration_ms"`
	Throughput float64   `json:"throughput_bytes_per_sec"`
	RecordedAt time.Time `json:"recorded_at"`
}

type StatsSnapshot struct {
	UploadBytes     int64          `json:"upload_bytes"`
	DownloadBytes   int64          `json:"download_bytes"`
	UploadCount     int64          `json:"upload_count"`
	DownloadCount   int64          `json:"download_count"`
	CRCNAKs         int64          `json:"crc_naks"`
	ResumeCount     int64          `json:"resume_count"`
	RecentTransfers []TransferStat `json:"recent_transfers"`
}

type Stats struct {
	mu       sync.Mutex
	snapshot StatsSnapshot
}

func NewStats() *Stats {
	return &Stats{}
}

func (s *Stats) RecordCRCNAK() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshot.CRCNAKs++
}

func (s *Stats) RecordResume() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshot.ResumeCount++
}

func (s *Stats) RecordUpload(fileID, filename string, bytes int64, startedAt time.Time) {
	s.recordTransfer("upload", fileID, filename, bytes, startedAt)
}

func (s *Stats) RecordDownload(fileID, filename string, bytes int64, startedAt time.Time) {
	s.recordTransfer("download", fileID, filename, bytes, startedAt)
}

func (s *Stats) Snapshot() StatsSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()

	recent := make([]TransferStat, len(s.snapshot.RecentTransfers))
	copy(recent, s.snapshot.RecentTransfers)

	out := s.snapshot
	out.RecentTransfers = recent
	return out
}

func (s *Stats) recordTransfer(direction, fileID, filename string, bytes int64, startedAt time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	duration := time.Since(startedAt)
	if duration <= 0 {
		duration = time.Millisecond
	}
	stat := TransferStat{
		Direction:  direction,
		FileID:     fileID,
		Filename:   filename,
		Bytes:      bytes,
		DurationMS: duration.Milliseconds(),
		Throughput: float64(bytes) / duration.Seconds(),
		RecordedAt: time.Now(),
	}

	switch direction {
	case "upload":
		s.snapshot.UploadBytes += bytes
		s.snapshot.UploadCount++
	case "download":
		s.snapshot.DownloadBytes += bytes
		s.snapshot.DownloadCount++
	}

	s.snapshot.RecentTransfers = append([]TransferStat{stat}, s.snapshot.RecentTransfers...)
	if len(s.snapshot.RecentTransfers) > 10 {
		s.snapshot.RecentTransfers = s.snapshot.RecentTransfers[:10]
	}
}
