package tailscale

import "testing"

// IsTailnetIP is the first authentication gate of the mesh RPC server (and now
// of the unauthenticated /v1/hello). A prefix typo would admit the whole local
// network or deny the whole tailnet, so its boundaries are pinned here.
func TestIsTailnetIPBoundaries(t *testing.T) {
	accept := []string{
		"100.64.0.1",
		"100.127.255.254",
		"100.64.0.1:443",
		"fd7a:115c:a1e0::1",
		"[fd7a:115c:a1e0::1]:443",
	}
	reject := []string{
		"",
		"garbage",
		"127.0.0.1",
		"10.0.0.1",
		"192.168.1.10",
		"100.63.255.255",
		"100.128.0.1",
		"::1",
		"fd7a:115c:a1e1::1",
		"100.64.0.1:not-a-port",
	}
	for _, addr := range accept {
		if !IsTailnetIP(addr) {
			t.Errorf("IsTailnetIP(%q) = false, want true", addr)
		}
	}
	for _, addr := range reject {
		if IsTailnetIP(addr) {
			t.Errorf("IsTailnetIP(%q) = true, want false", addr)
		}
	}
}
