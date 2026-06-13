package runner

import (
	"context"
	"os"
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
			}
		}
	}
	os.Exit(2)
}
