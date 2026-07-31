package runner

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRunProgramReturnsWorkerFailedForNonZeroExit(t *testing.T) {
	proc, err := RunProgram(context.Background(), Program{
		Argv: []string{os.Args[0], "-test.run=TestHelperProcess", "--", "exit-7"},
		Env:  map[string]string{"CMESH_HELPER_PROCESS": "1"},
	})
	if CodeOf(err) != ErrWorkerFailed {
		t.Fatalf("error code = %q, want %q (err=%v)", CodeOf(err), ErrWorkerFailed, err)
	}
	if proc.ExitCode != 7 {
		t.Fatalf("exit code = %d, want 7", proc.ExitCode)
	}
}

func TestRunProgramTimeoutTerminatesProcess(t *testing.T) {
	started := time.Now()
	proc, err := RunProgram(context.Background(), Program{
		Argv:    []string{os.Args[0], "-test.run=TestHelperProcess", "--", "sleep"},
		Env:     map[string]string{"CMESH_HELPER_PROCESS": "1"},
		Timeout: 100 * time.Millisecond,
	})
	if CodeOf(err) != ErrTimeout {
		t.Fatalf("error code = %q, want %q (err=%v)", CodeOf(err), ErrTimeout, err)
	}
	if proc.ExitCode != -1 {
		t.Fatalf("exit code = %d, want -1", proc.ExitCode)
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("timeout did not terminate child process promptly")
	}
}

// A run that overflows the output limit AND exits non-zero is a failure first.
// Reporting result_too_large would mask worker_failed and point the caller at
// an oversized transcript instead of a failing command. Truncation must stay
// visible on the result in both cases.
func TestRunProgramFailureOutranksTruncation(t *testing.T) {
	cases := []struct {
		name     string
		mode     string
		wantCode ErrorCode
		wantExit int
	}{
		{"truncated and failed", "flood-fail", ErrWorkerFailed, 9},
		{"truncated but succeeded", "flood-ok", ErrResultTooLarge, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proc, err := RunProgram(context.Background(), Program{
				Argv:        []string{os.Args[0], "-test.run=TestHelperProcess", "--", tc.mode},
				Env:         map[string]string{"CMESH_HELPER_PROCESS": "1"},
				OutputLimit: 128,
			})
			if CodeOf(err) != tc.wantCode {
				t.Fatalf("error code = %q, want %q (err=%v)", CodeOf(err), tc.wantCode, err)
			}
			if proc.ExitCode != tc.wantExit {
				t.Fatalf("exit code = %d, want %d", proc.ExitCode, tc.wantExit)
			}
			if !proc.Truncated {
				t.Fatal("truncation must stay visible on the result")
			}
		})
	}
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("CMESH_HELPER_PROCESS") != "1" {
		return
	}
	args := os.Args
	for i, arg := range args {
		if arg == "--" && i+1 < len(args) {
			switch args[i+1] {
			case "exit-7":
				os.Exit(7)
			case "sleep":
				time.Sleep(30 * time.Second)
				os.Exit(0)
			case "flood-ok":
				fmt.Print(strings.Repeat("x", 4096))
				os.Exit(0)
			case "flood-fail":
				fmt.Print(strings.Repeat("x", 4096))
				os.Exit(9)
			}
		}
	}
	os.Exit(2)
}
