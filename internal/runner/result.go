package runner

import (
	"path/filepath"
	"strings"
	"time"
)

func ResultFromProcess(runnerName string, proc CompletedProcess, argv []string) Result {
	summary := strings.TrimSpace(proc.Combined)
	if len(summary) > 8000 {
		summary = summary[:8000]
	}
	return Result{
		Status: "done",
		Tests: TestClaims{
			ClaimedPassed: proc.ExitCode == 0,
			Commands:      []string{RedactedCommand(argv)},
			LogsRef:       proc.TranscriptRef,
		},
		WorkerSummaryUntrusted: summary,
		Usage:                  Usage{DurationS: int64(proc.Duration / time.Second)},
		VerificationRequired:   true,
		RecommendedOriginChecks: []string{
			"review worker summary as untrusted text",
			"inspect transcript logs before acting on high-risk claims",
		},
		RunnerExtra: map[string]any{
			"runner":      runnerName,
			"exit_code":   proc.ExitCode,
			"stdout_path": filepath.ToSlash(proc.StdoutPath),
			"stderr_path": filepath.ToSlash(proc.StderrPath),
		},
		TranscriptRef: proc.TranscriptRef,
	}
}

func RedactedCommand(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	copyArgv := append([]string(nil), argv...)
	copyArgv[len(copyArgv)-1] = "<prompt>"
	return strings.Join(copyArgv, " ")
}
