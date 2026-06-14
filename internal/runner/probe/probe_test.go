package probe

import (
	"context"
	"encoding/json"
	"net"
	"runtime"
	"testing"

	"cmesh/internal/profile"
	"cmesh/internal/runner"
)

func TestProbeRunnerSystemInfoUsesNoShell(t *testing.T) {
	run := New(map[string]ProfileConfig{
		profile.Query: {Capabilities: []string{profile.CapSystemInfo}, EnforcementConfidence: "high"},
	})
	result, err := run.Run(runner.RunRequest{
		TaskID:    "task-probe-system",
		Profile:   profile.Query,
		QueryKind: profile.CapSystemInfo,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.CommandOutput.Command != "probe:system_info" {
		t.Fatalf("command = %q", result.CommandOutput.Command)
	}
	if result.CommandOutput.ExitCode != 0 {
		t.Fatalf("exit = %d stderr=%q", result.CommandOutput.ExitCode, result.CommandOutput.Stderr)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.CommandOutput.Stdout), &payload); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, result.CommandOutput.Stdout)
	}
	if payload["kind"] != profile.CapSystemInfo {
		t.Fatalf("kind = %v", payload["kind"])
	}
	if result.RunnerExtra["no_shell"] != true || result.RunnerExtra["no_writes"] != true {
		t.Fatalf("probe did not declare no_shell/no_writes: %#v", result.RunnerExtra)
	}
}

func TestProbeRunnerNetworkCheckUsesTypedTCPProbe(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			_ = conn.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	run := New(map[string]ProfileConfig{
		profile.QueryNetwork: {Capabilities: []string{profile.CapNetworkCheck}, EnforcementConfidence: "high"},
	})
	result, err := run.Run(runner.RunRequest{
		Context:   context.Background(),
		TaskID:    "task-probe-network",
		Profile:   profile.QueryNetwork,
		QueryKind: profile.CapNetworkCheck,
		QueryParams: map[string]any{
			"host": "127.0.0.1",
			"port": port,
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.CommandOutput.Stdout), &payload); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, result.CommandOutput.Stdout)
	}
	if payload["kind"] != profile.CapNetworkCheck || payload["reachable"] != true {
		t.Fatalf("payload = %#v", payload)
	}
	if result.RunnerExtra["no_shell"] != true || result.RunnerExtra["no_writes"] != true {
		t.Fatalf("probe did not declare no_shell/no_writes: %#v", result.RunnerExtra)
	}
}

func TestProbeRunnerRejectsUngrantCapability(t *testing.T) {
	run := New(map[string]ProfileConfig{
		profile.Query: {Capabilities: []string{profile.CapSystemInfo}, EnforcementConfidence: "high"},
	})
	_, err := run.Run(runner.RunRequest{
		TaskID:    "task-probe-denied",
		Profile:   profile.Query,
		QueryKind: profile.CapDiskInfo,
	})
	if runner.CodeOf(err) != runner.ErrProfileDenied {
		t.Fatalf("error code = %q err=%v", runner.CodeOf(err), err)
	}
}

func TestProbeRunnerRejectsAmbiguousDiskInfoParams(t *testing.T) {
	run := New(map[string]ProfileConfig{
		profile.Query: {Capabilities: []string{profile.CapDiskInfo}, EnforcementConfidence: "high"},
	})
	_, err := run.Run(runner.RunRequest{
		TaskID:    "task-probe-disk",
		Profile:   profile.Query,
		QueryKind: profile.CapDiskInfo,
		QueryParams: map[string]any{
			"path":   ".",
			"volume": ".",
		},
	})
	if runner.CodeOf(err) != runner.ErrWorkerFailed {
		t.Fatalf("error code = %q err=%v", runner.CodeOf(err), err)
	}
}

func TestDiskInfoParamValidationRejectsRemoteOrAmbiguousVolumes(t *testing.T) {
	if err := validateDiskPath(`\\server\share`); runtime.GOOS == "windows" && err == nil {
		t.Fatal("Windows UNC path should be rejected")
	}
	if runtime.GOOS != "windows" {
		if _, err := normalizeDiskVolume("relative"); err == nil {
			t.Fatal("non-Windows relative volume should be rejected")
		}
	}
}
