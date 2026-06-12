package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const DefaultOutputLimit int64 = 1 << 20

type Program struct {
	Argv        []string
	Dir         string
	Env         map[string]string
	Stdin       string
	Timeout     time.Duration
	OutputLimit int64
	LogDir      string
	LogName     string
}

type CompletedProcess struct {
	Argv          []string
	ExitCode      int
	Stdout        string
	Stderr        string
	Combined      string
	Duration      time.Duration
	TranscriptRef string
	StdoutPath    string
	StderrPath    string
}

func RunProgram(ctx context.Context, p Program) (CompletedProcess, error) {
	if len(p.Argv) == 0 || strings.TrimSpace(p.Argv[0]) == "" {
		return CompletedProcess{}, E(ErrRunnerUnavailable, "resolve command", errors.New("empty command"))
	}
	prog, err := exec.LookPath(p.Argv[0])
	if err != nil {
		return CompletedProcess{}, E(ErrRunnerUnavailable, "resolve command", fmt.Errorf("%s: %w", p.Argv[0], err))
	}
	dir := p.Dir
	if dir != "" {
		info, err := os.Stat(dir)
		if err != nil {
			return CompletedProcess{}, E(ErrCWDDenied, "stat cwd", err)
		}
		if !info.IsDir() {
			return CompletedProcess{}, E(ErrCWDDenied, "stat cwd", fmt.Errorf("%s is not a directory", dir))
		}
	}
	runCtx := ctx
	cancel := func() {}
	if p.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, p.Timeout)
	}
	defer cancel()

	limit := p.OutputLimit
	if limit <= 0 {
		limit = DefaultOutputLimit
	}

	started := time.Now()
	cmd := exec.Command(prog, p.Argv[1:]...)
	cmd.Dir = dir
	cmd.Env = mergeEnv(os.Environ(), p.Env)
	setProcessGroup(cmd)
	if p.Stdin != "" {
		cmd.Stdin = strings.NewReader(p.Stdin)
	}

	var stdout, stderr, combined limitedBuffer
	stdout.limit = limit
	stderr.limit = limit
	combined.limit = limit

	var stdoutFile, stderrFile *os.File
	var stdoutPath, stderrPath string
	if p.LogDir != "" {
		if err := os.MkdirAll(p.LogDir, 0o755); err != nil {
			return CompletedProcess{}, E(ErrWorkerFailed, "create log dir", err)
		}
		base := sanitizeLogName(p.LogName)
		if base == "" {
			base = "worker"
		}
		stdoutPath = filepath.Join(p.LogDir, base+".stdout.log")
		stderrPath = filepath.Join(p.LogDir, base+".stderr.log")
		stdoutFile, err = os.Create(stdoutPath)
		if err != nil {
			return CompletedProcess{}, E(ErrWorkerFailed, "create stdout log", err)
		}
		defer stdoutFile.Close()
		stderrFile, err = os.Create(stderrPath)
		if err != nil {
			return CompletedProcess{}, E(ErrWorkerFailed, "create stderr log", err)
		}
		defer stderrFile.Close()
	}

	stdoutWriters := []io.Writer{&stdout, &combined}
	stderrWriters := []io.Writer{&stderr, &combined}
	if stdoutFile != nil {
		stdoutWriters = append(stdoutWriters, stdoutFile)
	}
	if stderrFile != nil {
		stderrWriters = append(stderrWriters, stderrFile)
	}
	cmd.Stdout = io.MultiWriter(stdoutWriters...)
	cmd.Stderr = io.MultiWriter(stderrWriters...)

	if err := cmd.Start(); err != nil {
		return CompletedProcess{}, E(ErrRunnerUnavailable, "start command", err)
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	var waitErr error
	select {
	case waitErr = <-waitCh:
	case <-runCtx.Done():
		terminateProcessGroup(cmd)
		waitErr = <-waitCh
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return processResult(p, started, stdout, stderr, combined, stdoutPath, stderrPath, -1), E(ErrTimeout, "run command", runCtx.Err())
		}
		return processResult(p, started, stdout, stderr, combined, stdoutPath, stderrPath, -1), E(ErrCancelled, "run command", runCtx.Err())
	}

	result := processResult(p, started, stdout, stderr, combined, stdoutPath, stderrPath, exitCode(waitErr))
	if stdout.truncated || stderr.truncated || combined.truncated {
		return result, E(ErrResultTooLarge, "capture output", fmt.Errorf("output exceeded %d bytes", limit))
	}
	if waitErr != nil {
		return result, E(ErrWorkerFailed, "run command", waitErr)
	}
	return result, nil
}

func processResult(p Program, started time.Time, stdout, stderr, combined limitedBuffer, stdoutPath, stderrPath string, code int) CompletedProcess {
	ref := ""
	if stdoutPath != "" || stderrPath != "" {
		ref = "transcript:" + strings.TrimSuffix(filepath.Base(stdoutPath), ".stdout.log")
	}
	return CompletedProcess{
		Argv:          append([]string(nil), p.Argv...),
		ExitCode:      code,
		Stdout:        stdout.String(),
		Stderr:        stderr.String(),
		Combined:      combined.String(),
		Duration:      time.Since(started),
		TranscriptRef: ref,
		StdoutPath:    stdoutPath,
		StderrPath:    stderrPath,
	}
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func mergeEnv(base []string, extra map[string]string) []string {
	if len(extra) == 0 {
		return base
	}
	values := map[string]string{}
	order := make([]string, 0, len(base)+len(extra))
	for _, entry := range base {
		key, val, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, exists := values[key]; !exists {
			order = append(order, key)
		}
		values[key] = val
	}
	for key, val := range extra {
		if _, exists := values[key]; !exists {
			order = append(order, key)
		}
		values[key] = val
	}
	env := make([]string, 0, len(values))
	for _, key := range order {
		env = append(env, key+"="+values[key])
	}
	return env
}

func sanitizeLogName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		}
	}
	return b.String()
}

type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int64
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		return len(p), nil
	}
	remaining := b.limit - int64(b.buf.Len())
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if int64(len(p)) > remaining {
		b.buf.Write(p[:remaining])
		b.truncated = true
		return len(p), nil
	}
	b.buf.Write(p)
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	return b.buf.String()
}
