package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	tsid "cmesh/internal/identity/tailscale"
	"cmesh/internal/mesh/schema"
)

type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
	Err    string `json:"error,omitempty"`
}

type Report struct {
	Checks []Check `json:"checks"`
}

func (r Report) OK() bool {
	for _, check := range r.Checks {
		if !check.OK {
			return false
		}
	}
	return true
}

func (r Report) Text() string {
	var b strings.Builder
	for _, check := range r.Checks {
		b.WriteString(check.Name)
		b.WriteString(": ")
		if check.OK {
			if check.Detail != "" {
				b.WriteString(check.Detail)
			} else {
				b.WriteString("ok")
			}
		} else if check.Err != "" {
			b.WriteString("failed: ")
			b.WriteString(check.Err)
		} else {
			b.WriteString("failed")
		}
		b.WriteByte('\n')
	}
	return b.String()
}

type TailscaleOptions struct {
	MeshPort                  int
	Timeout                   time.Duration
	AcceptExistingMeshService bool
}

func Tailscale(ctx context.Context, opts TailscaleOptions) Report {
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	localProvider := &tsid.LocalProvider{}
	var checks []Check

	status, err := localProvider.Status(ctx)
	if err != nil {
		checks = append(checks,
			fail("tailscaled", err),
			fail("localapi", err),
			fail("tailnet_ip", fmt.Errorf("status unavailable: %w", err)),
			fail("whois_self", fmt.Errorf("status unavailable: %w", err)),
			fail("bind_tailnet_ip", fmt.Errorf("status unavailable: %w", err)),
		)
		return Report{Checks: checks}
	}
	checks = append(checks, pass("tailscaled", status.BackendState))
	checks = append(checks, pass("localapi", "reachable"))

	if len(status.TailscaleIPs) == 0 {
		checks = append(checks,
			fail("tailnet_ip", fmt.Errorf("no Tailscale IP assigned")),
			fail("whois_self", fmt.Errorf("no Tailscale IP assigned")),
			fail("bind_tailnet_ip", fmt.Errorf("no Tailscale IP assigned")),
		)
		return Report{Checks: checks}
	}
	tailnetIP := status.TailscaleIPs[0]
	checks = append(checks, pass("tailnet_ip", tailnetIP))

	self, err := localProvider.WhoIs(ctx, tailnetIP)
	if err != nil {
		checks = append(checks, fail("whois_self", err))
	} else {
		detail := firstNonEmpty(self.NodeName, self.DNSName, self.NodeID)
		checks = append(checks, pass("whois_self", detail))
	}

	if err := bindTailnetIP(ctx, tailnetIP, opts.MeshPort); err != nil {
		if opts.AcceptExistingMeshService {
			if detail, ok := existingMeshServiceHealthy(ctx, tailnetIP, opts.MeshPort); ok {
				checks = append(checks, pass("bind_tailnet_ip", detail))
				return Report{Checks: checks}
			}
		}
		checks = append(checks, fail("bind_tailnet_ip", err))
	} else {
		checks = append(checks, pass("bind_tailnet_ip", "ok"))
	}
	return Report{Checks: checks}
}

func ValidateMeshBindAddress(addr string) Check {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.TrimSpace(host)
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		return fail("mesh_bind_address", fmt.Errorf("mesh RPC must bind to a Tailscale IP or tsnet listener, got %q", addr))
	}
	if !tsid.IsTailnetIP(strings.Trim(host, "[]")) {
		return fail("mesh_bind_address", fmt.Errorf("%q is not a Tailscale IP", addr))
	}
	return pass("mesh_bind_address", host)
}

func bindTailnetIP(ctx context.Context, ip string, port int) error {
	if port < 0 || port > 65535 {
		return fmt.Errorf("invalid port %d", port)
	}
	addr := net.JoinHostPort(ip, fmt.Sprintf("%d", port))
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	return ln.Close()
}

func existingMeshServiceHealthy(ctx context.Context, ip string, port int) (string, bool) {
	addr := net.JoinHostPort(ip, fmt.Sprintf("%d", port))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/v1/hello", nil)
	if err != nil {
		return "", false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", false
	}
	var hello schema.HelloResponse
	if err := json.NewDecoder(resp.Body).Decode(&hello); err != nil {
		return "", false
	}
	if hello.NodeID == "" && hello.NodeName == "" && len(hello.SchemaVersions) == 0 {
		return "", false
	}
	return "existing cmesh listener healthy", true
}

func pass(name, detail string) Check {
	return Check{Name: name, OK: true, Detail: detail}
}

func fail(name string, err error) Check {
	return Check{Name: name, OK: false, Err: err.Error()}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return "ok"
}
