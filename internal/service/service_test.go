package service

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// TestMain lets a re-exec'd copy of the test binary exit with a chosen code (set
// via CMESH_TEST_EXIT_CODE), so TestWindowsAccessDeniedError can produce a real
// *exec.ExitError with a specific exit code without shelling out.
func TestMain(m *testing.M) {
	if code, ok := os.LookupEnv("CMESH_TEST_EXIT_CODE"); ok {
		if n, err := strconv.Atoi(code); err == nil {
			os.Exit(n)
		}
	}
	os.Exit(m.Run())
}

func TestServiceCommandLineUsesServiceEntrypoint(t *testing.T) {
	got := ServiceCommandLine(`C:\Tools\cmesh.exe`, `C:\Users\scott\.config\cmesh\config.toml`)
	if !strings.Contains(got, ` _service --config `) {
		t.Fatalf("service command line = %q, want _service entrypoint", got)
	}
	if strings.Contains(got, ` up --config `) {
		t.Fatalf("service command line = %q, must not use foreground up command", got)
	}
}

func TestSystemdUnitBodyMatchesExpectedLayout(t *testing.T) {
	got := SystemdUnitBody("/usr/local/bin/cmesh", "/home/scott/.config/cmesh/config.toml")
	want := "[Unit]\nDescription=cmesh agent mesh daemon\nAfter=network-online.target\n\n[Service]\nExecStart=/usr/local/bin/cmesh up --config /home/scott/.config/cmesh/config.toml\nRestart=on-failure\nRestartSec=3\n\n[Install]\nWantedBy=default.target\n"
	if got != want {
		t.Fatalf("systemd unit body = %q, want %q", got, want)
	}
}

func TestLinuxInstallErrorAddsNoSpaceGuidance(t *testing.T) {
	err := linuxInstallError("write user systemd unit", "/home/admin/.config/systemd/user/cmesh.service", syscall.ENOSPC)
	text := err.Error()
	for _, want := range []string{"write user systemd unit", "no space left on device", "Free space", "cmesh up service install"} {
		if !strings.Contains(text, want) {
			t.Fatalf("linuxInstallError = %q, missing %q", text, want)
		}
	}
}

func TestLaunchdPlistIncludesPathEnvironment(t *testing.T) {
	got := LaunchdPlistBody(`/Applications/cmesh&tools/cmesh`, `/Users/scott/config<cmesh>.toml`, `/opt/homebrew/bin:/usr/bin`)
	for _, want := range []string{
		"<key>EnvironmentVariables</key>",
		"<key>PATH</key>",
		"<string>/opt/homebrew/bin:/usr/bin</string>",
		"/Applications/cmesh&amp;tools/cmesh",
		"/Users/scott/config&lt;cmesh&gt;.toml",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("launchd plist missing %q in %s", want, got)
		}
	}
}

func TestLaunchdPATHAddsHomebrewAheadOfLaunchdDefaults(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	got := LaunchdPATH()
	for _, want := range []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin"} {
		if !strings.Contains(got, want) {
			t.Fatalf("launchd PATH missing %q in %q", want, got)
		}
	}
	if !strings.HasPrefix(got, "/opt/homebrew/bin:") {
		t.Fatalf("launchd PATH = %q, want Homebrew first", got)
	}
}

func TestLaunchdPATHAppendsExtraEntriesOnce(t *testing.T) {
	t.Setenv("PATH", "/opt/extra:/usr/bin:/opt/extra")
	got := LaunchdPATH()
	if strings.Count(got, "/opt/extra") != 1 {
		t.Fatalf("launchd PATH = %q, want /opt/extra exactly once", got)
	}
}

func TestWindowsServiceSCArgsWithoutUser(t *testing.T) {
	got := WindowsServiceSCArgs("create", `"C:\Tools\cmesh.exe" _service --config "C:\cfg.toml"`, "", "")
	want := []string{"create", "cmesh", "binPath=", `"C:\Tools\cmesh.exe" _service --config "C:\cfg.toml"`, "start=", "auto"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("sc args = %#v, want %#v", got, want)
	}
}

func TestWindowsServiceSCArgsWithUser(t *testing.T) {
	got := strings.Join(WindowsServiceSCArgs("config", `C:\Tools\cmesh.exe _service`, `.\scott`, "secret"), "\x00")
	for _, want := range []string{"config", "cmesh", "binPath=", `C:\Tools\cmesh.exe _service`, "start=", "auto", "obj=", `.\scott`, "password=", "secret"} {
		if !strings.Contains(got, want) {
			t.Fatalf("WindowsServiceSCArgs missing %q in %#v", want, got)
		}
	}
}

func TestWindowsUserTaskCommandLineUsesForegroundDaemon(t *testing.T) {
	got := WindowsUserTaskCommandLine(`C:\Users\scott\.local\bin\cmesh.exe`, `C:\Users\scott\.config\cmesh\config.toml`)
	want := `"C:\Users\scott\.local\bin\cmesh.exe" up --config "C:\Users\scott\.config\cmesh\config.toml"`
	if got != want {
		t.Fatalf("WindowsUserTaskCommandLine = %q, want %q", got, want)
	}
	if strings.Contains(got, `_service`) {
		t.Fatalf("user scheduled task must run foreground up, not SCM entrypoint: %q", got)
	}
}

func TestWindowsUserTaskCreateArgs(t *testing.T) {
	got := strings.Join(WindowsUserTaskCreateArgs(`C:\bin\cmesh.exe`, `C:\cfg.toml`), "\x00")
	for _, want := range []string{"/Create", "/TN", WindowsUserTaskName, "/TR", `"C:\bin\cmesh.exe" up --config "C:\cfg.toml"`, "/SC", "ONLOGON", "/RL", "LIMITED", "/F"} {
		if !strings.Contains(got, want) {
			t.Fatalf("WindowsUserTaskCreateArgs missing %q in %#v", want, got)
		}
	}
}

func TestWindowsFirewallRuleArgsAreMeshOnly(t *testing.T) {
	got := strings.Join(WindowsFirewallRuleArgs(`C:\bin\cmesh.exe`, 7778), "\x00")
	for _, want := range []string{"advfirewall", "firewall", "add", "rule", "name=cmesh mesh 7778", "dir=in", "action=allow", "protocol=TCP", "localport=7778", `program=C:\bin\cmesh.exe`} {
		if !strings.Contains(got, want) {
			t.Fatalf("WindowsFirewallRuleArgs missing %q in %#v", want, got)
		}
	}
	if strings.Contains(got, "7777") {
		t.Fatalf("firewall rule must not expose local MCP gateway: %#v", got)
	}
}

func TestWindowsServiceNotInstalledError(t *testing.T) {
	err := errors.New("query Windows service cmesh: [SC] OpenService FAILED 1060:\n\nThe specified service does not exist as an installed service.")
	if !IsWindowsServiceNotInstalledError(err) {
		t.Fatal("expected service-not-installed error")
	}
	if IsWindowsServiceNotInstalledError(errors.New("query Windows service cmesh: access denied")) {
		t.Fatal("access denied should remain a service query failure")
	}
	if IsWindowsServiceNotInstalledError(nil) {
		t.Fatal("nil error must not be treated as not-installed")
	}
}

func TestWindowsAccessDeniedError(t *testing.T) {
	// Produce a real *exec.ExitError carrying a chosen exit code without a shell:
	// re-exec the test binary itself and have it exit(code) via the env switch
	// handled in TestMain. This mirrors sc.exe failing with code 5 (access denied),
	// whose "Access is denied." text goes to (uncaptured) stdout/stderr.
	exitWith := func(code int) error {
		cmd := exec.Command(os.Args[0], "-test.run=TestWindowsAccessDeniedError")
		cmd.Env = append(os.Environ(), fmt.Sprintf("CMESH_TEST_EXIT_CODE=%d", code))
		return cmd.Run()
	}

	if err := exitWith(5); err == nil || !isWindowsAccessDeniedError(err) {
		t.Fatalf("exit code 5 must be detected as access-denied, got %v", err)
	}
	if err := exitWith(1); isWindowsAccessDeniedError(err) {
		t.Fatal("exit code 1 must not be treated as access-denied")
	}
	// Text fallbacks for callers that do capture sc.exe output.
	if !isWindowsAccessDeniedError(errors.New("sc.exe create: Access is denied.")) {
		t.Fatal("expected access-denied detection from message text")
	}
	if !isWindowsAccessDeniedError(errors.New("exit status 5")) {
		t.Fatal("expected access-denied detection from \"exit status 5\" text")
	}
	if isWindowsAccessDeniedError(nil) {
		t.Fatal("nil error must not be treated as access-denied")
	}
}

func TestWindowsServiceNotRunningError(t *testing.T) {
	err := errors.New("sc.exe stop cmesh: [SC] ControlService FAILED 1062:\n\nThe service has not been started.: exit status 1062")
	if !isWindowsServiceNotRunningError(err) {
		t.Fatal("expected service-not-running detection")
	}
	if isWindowsServiceNotRunningError(errors.New("exit status 5")) {
		t.Fatal("access denied must not be treated as service-not-running")
	}
	if isWindowsServiceNotRunningError(nil) {
		t.Fatal("nil error must not be treated as service-not-running")
	}
}

func TestWindowsServiceAdminRequiredErrorIsFriendly(t *testing.T) {
	err := windowsServiceAdminRequiredError("removing")
	if !IsWindowsElevationRequired(err) {
		t.Fatalf("admin-required error must wrap ErrWindowsElevationRequired: %v", err)
	}
	text := err.Error()
	for _, want := range []string{"removing the Windows service requires Administrator approval", "elevated PowerShell"} {
		if !strings.Contains(text, want) {
			t.Fatalf("admin-required error %q missing %q", text, want)
		}
	}
	for _, raw := range []string{"OpenService FAILED", "Access is denied", "exit status"} {
		if strings.Contains(text, raw) {
			t.Fatalf("admin-required error leaked raw SCM text %q: %q", raw, text)
		}
	}
}

func TestParseWindowsServiceStartName(t *testing.T) {
	out := `
SERVICE_NAME: cmesh
        TYPE               : 10  WIN32_OWN_PROCESS
        START_TYPE         : 2   AUTO_START
        SERVICE_START_NAME : .\scott
`
	got, err := parseWindowsServiceStartName(out)
	if err != nil {
		t.Fatalf("parseWindowsServiceStartName: %v", err)
	}
	if got != `.\scott` {
		t.Fatalf("start name = %q", got)
	}
}

func TestSameWindowsUserNormalizesDomainAndDotPrefix(t *testing.T) {
	if !SameWindowsUser(`.\scott`, `DESKTOP\scott`) {
		t.Fatal("expected dot-prefix and machine-domain names to match")
	}
	if SameWindowsUser(`.\scott`, `DESKTOP\other`) {
		t.Fatal("different users should not match")
	}
}

func TestNormalizeWindowsUserName(t *testing.T) {
	cases := map[string]string{
		`.\Scott`:        "scott",
		`DESKTOP\Scott`:  "scott",
		`  DOMAIN\Bob  `: "bob",
		`alice`:          "alice",
		``:               "",
	}
	for in, want := range cases {
		if got := NormalizeWindowsUserName(in); got != want {
			t.Fatalf("NormalizeWindowsUserName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestInstallRejectsEmptyExePath guards the construction contract: Install must
// fail fast without an executable path rather than shelling out.
func TestInstallRejectsEmptyExePath(t *testing.T) {
	if err := Install(Options{}); err == nil {
		t.Fatal("Install with empty ExePath must return an error")
	}
}
