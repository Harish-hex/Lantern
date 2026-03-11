package server

import (
	"testing"
	"time"
)

func TestStatsTracksChunkSizeAveragesAndRecentSamples(t *testing.T) {
	s := NewStats()
	started := time.Now().Add(-2 * time.Second)

	s.RecordUpload("u1", "up.bin", 1024, 128*1024, started)
	s.RecordUpload("u2", "up2.bin", 2048, 256*1024, started)
	s.RecordDownload("d1", "down.bin", 4096, 64*1024, started)

	snap := s.Snapshot()
	wantUploadAvg := float64((128*1024)+(256*1024)) / 2
	if snap.AvgUploadChunkSizeBytes != wantUploadAvg {
		t.Fatalf("AvgUploadChunkSizeBytes = %v, want %v", snap.AvgUploadChunkSizeBytes, wantUploadAvg)
	}
	if snap.AvgDownloadChunkSizeBytes != float64(64*1024) {
		t.Fatalf("AvgDownloadChunkSizeBytes = %v, want %v", snap.AvgDownloadChunkSizeBytes, float64(64*1024))
	}
	if len(snap.RecentChunkSizes) != 3 {
		t.Fatalf("RecentChunkSizes len = %d, want 3", len(snap.RecentChunkSizes))
	}
	if snap.RecentChunkSizes[0].Direction != "download" {
		t.Fatalf("latest chunk sample direction = %q, want download", snap.RecentChunkSizes[0].Direction)
	}
}
