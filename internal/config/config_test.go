package config

import "testing"

func TestDefaultConfigMDNSDefaults(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.EnableMDNS {
		t.Fatal("EnableMDNS = true, want false")
	}
	if cfg.MDNSServiceName != "Lantern" {
		t.Fatalf("MDNSServiceName = %q, want Lantern", cfg.MDNSServiceName)
	}
	if !cfg.ComputeEnabled {
		t.Fatal("ComputeEnabled = false, want true")
	}
	if cfg.ComputeRetryMax != 3 {
		t.Fatalf("ComputeRetryMax = %d, want 3", cfg.ComputeRetryMax)
	}
	if cfg.ComputeTaskSizeBytes != 4*1024*1024 {
		t.Fatalf("ComputeTaskSizeBytes = %d, want %d", cfg.ComputeTaskSizeBytes, 4*1024*1024)
	}
	if cfg.ComputeArtifactBudgetBytes != 1*1024*1024*1024 {
		t.Fatalf("ComputeArtifactBudgetBytes = %d, want %d", cfg.ComputeArtifactBudgetBytes, 1*1024*1024*1024)
	}
	if cfg.ComputeWorkerQuarantineThreshold != 4 {
		t.Fatalf("ComputeWorkerQuarantineThreshold = %d, want 4", cfg.ComputeWorkerQuarantineThreshold)
	}
}
