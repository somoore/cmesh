package service

import (
	"errors"
	"strings"
	"testing"
)

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
