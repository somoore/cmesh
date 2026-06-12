package runner

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func ResultFromProcess(runnerName string, proc CompletedProcess, argv []string) Result {
	summary, usage, extra := parseStructuredOutput(proc.Stdout)
	if summary == "" {
		summary = strings.TrimSpace(proc.Combined)
	}
	if len(summary) > 8000 {
		summary = summary[:8000]
	}
	if usage.DurationS == 0 {
		usage.DurationS = int64(proc.Duration / time.Second)
	}
	if extra == nil {
		extra = map[string]any{}
	}
	extra["runner"] = runnerName
	extra["exit_code"] = proc.ExitCode
	extra["stdout_path"] = filepath.ToSlash(proc.StdoutPath)
	extra["stderr_path"] = filepath.ToSlash(proc.StderrPath)
	return Result{
		Status: "done",
		Tests: TestClaims{
			ClaimedPassed: false,
			Commands:      []string{RedactedCommand(argv)},
			LogsRef:       proc.TranscriptRef,
		},
		WorkerSummaryUntrusted: summary,
		Usage:                  usage,
		VerificationRequired:   true,
		RecommendedOriginChecks: []string{
			"review worker summary as untrusted text",
			"inspect transcript logs before acting on high-risk claims",
		},
		RunnerExtra:   extra,
		TranscriptRef: proc.TranscriptRef,
	}
}

func parseStructuredOutput(stdout string) (string, Usage, map[string]any) {
	stdout = strings.TrimSpace(stdout)
	if stdout == "" || !strings.HasPrefix(stdout, "{") {
		return "", Usage{}, nil
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
		return "", Usage{}, nil
	}
	extra := map[string]any{}
	for _, key := range []string{"session_id", "conversation_id", "model"} {
		if value, ok := raw[key]; ok {
			extra[key] = value
		}
	}
	summary := firstString(raw, "result", "summary", "output", "message", "content", "final_answer")
	if summary == "" {
		if encoded, err := json.Marshal(raw); err == nil {
			summary = string(encoded)
		}
	}
	usage := Usage{
		Tokens: intFromAny(raw["total_tokens"]),
		Cost:   floatFromAny(firstAny(raw, "total_cost_usd", "cost_usd", "cost")),
	}
	if u, ok := raw["usage"].(map[string]any); ok {
		if usage.Tokens == 0 {
			usage.Tokens = intFromAny(firstAny(u, "total_tokens", "tokens"))
		}
		if usage.Tokens == 0 {
			usage.Tokens = intFromAny(u["input_tokens"]) + intFromAny(u["output_tokens"])
		}
		if usage.Cost == 0 {
			usage.Cost = floatFromAny(firstAny(u, "total_cost_usd", "cost_usd", "cost"))
		}
	}
	return summary, usage, extra
}

func firstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			switch v := value.(type) {
			case string:
				return strings.TrimSpace(v)
			case []any:
				var parts []string
				for _, item := range v {
					parts = append(parts, fmt.Sprint(item))
				}
				return strings.TrimSpace(strings.Join(parts, "\n"))
			}
		}
	}
	return ""
}

func firstAny(values map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			return value
		}
	}
	return nil
}

func intFromAny(value any) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case json.Number:
		i, _ := strconv.Atoi(v.String())
		return i
	case string:
		i, _ := strconv.Atoi(v)
		return i
	default:
		return 0
	}
}

func floatFromAny(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case json.Number:
		f, _ := strconv.ParseFloat(v.String(), 64)
		return f
	case string:
		f, _ := strconv.ParseFloat(v, 64)
		return f
	default:
		return 0
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
