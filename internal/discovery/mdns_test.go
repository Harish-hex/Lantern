package discovery

import "testing"

func TestBuildTXTRecords(t *testing.T) {
	txt := buildTXTRecords(ServiceInfo{
		Host:     "lantern.local",
		TCPPort:  9723,
		HTTPPort: 9724,
	})

	expected := map[string]bool{
		"tcp_port=9723":      false,
		"http_port=9724":     false,
		"host=lantern.local": false,
	}

	for _, entry := range txt {
		if _, ok := expected[entry]; ok {
			expected[entry] = true
		}
	}

	for key, seen := range expected {
		if !seen {
			t.Fatalf("missing TXT record %q in %v", key, txt)
		}
	}
}
