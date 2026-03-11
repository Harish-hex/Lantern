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
}
