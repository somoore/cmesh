package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	"cmesh/internal/store"
	"cmesh/internal/task"
)

// seedWorkerTask puts a local worker task in the store, as the mesh server would
// before launching a runner.
func seedWorkerTask(t *testing.T, g *Gateway, taskID string) task.Record {
	t.Helper()
	ctx := context.Background()
	rec := task.Record{
		ID:               taskID,
		OriginNodeID:     "mac-studio",
		OriginSessionID:  "session-1",
		WorkerNodeID:     "origin",
		State:            task.StateQueued,
		CWD:              t.TempDir(),
		Runner:           "fake",
		ProfileRequested: "readonly",
		ProfileEffective: "readonly",
		PromptHash:       task.PromptHash("hello"),
		PayloadHash:      task.SHA256Hex([]byte(`{}`)),
	}
	if _, err := g.Store.BeginDispatch(ctx, rec, []byte(`{}`), 24*time.Hour); err != nil {
		t.Fatalf("BeginDispatch: %v", err)
	}
	if _, err := g.Store.UpdateTaskState(ctx, taskID, store.StateUpdate{State: task.StateRunning}); err != nil {
		t.Fatalf("to running: %v", err)
	}
	updated, err := g.Store.GetTask(ctx, taskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	return updated
}

// workerCtx is a context carrying a per-task token's provenance, as the
// hardening layer attaches it for a runner calling back into its own gateway.
func workerCtx(taskID string) context.Context {
	return context.WithValue(context.Background(), provenanceContextKey{}, gatewayProvenance{
		TaskID:          taskID,
		OriginNodeID:    "mac-studio",
		OriginSessionID: "session-1",
	})
}

// The two ends of the channel take different credentials, and confusing them
// breaks the design. A worker must not be able to answer its own question:
// that would manufacture apparent owner approval, which is exactly what asking
// is meant to prevent.
func TestTaskTokenCannotAnswerItsOwnQuestion(t *testing.T) {
	g := newTestGateway(t)
	rec := seedWorkerTask(t, g, "tsk_channel")

	resp, _ := g.sendCall(workerCtx(rec.ID), rec.ID, "you may proceed", "", 0, "")
	if resp.OK || resp.Error == nil || resp.Error.Code != task.ErrorPeerUnauthorized {
		t.Fatalf("a worker token must not reach task_send, got %+v", resp)
	}
	if !strings.Contains(resp.Error.Detail, "owner") {
		t.Fatalf("denial should explain who may reply, got %q", resp.Error.Detail)
	}
}

// The worker end requires a task token: a host-token caller gets a clear
// explanation rather than an obscure failure.
func TestWorkerChannelToolsRequireTaskToken(t *testing.T) {
	g := newTestGateway(t)
	rec := seedWorkerTask(t, g, "tsk_channel")
	hostCtx := context.Background()

	for _, tc := range []struct {
		name string
		call func() (ToolResponse, error)
	}{
		{"task_post", func() (ToolResponse, error) { return g.postCall(hostCtx, rec.ID, "working", "") }},
		{"task_ask", func() (ToolResponse, error) { return g.askCall(hostCtx, rec.ID, "ok?", "", nil, 1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, _ := tc.call()
			if resp.OK || resp.Error == nil || resp.Error.Code != task.ErrorPeerUnauthorized {
				t.Fatalf("%s without a task token should be refused, got %+v", tc.name, resp)
			}
			if !strings.Contains(resp.Error.Detail, "CMESH_TASK_TOKEN") {
				t.Fatalf("denial should say how to authenticate, got %q", resp.Error.Detail)
			}
		})
	}
}

// A token names exactly one task. A caller-supplied task id may be checked but
// must never widen what the token reaches -- the same rule that keeps a worker
// from choosing its own origin session on delegate.
func TestTaskTokenCannotPostToAnotherTask(t *testing.T) {
	g := newTestGateway(t)
	mine := seedWorkerTask(t, g, "tsk_mine")
	other := seedWorkerTask(t, g, "tsk_other")

	resp, _ := g.postCall(workerCtx(mine.ID), other.ID, "note", "")
	if resp.OK || resp.Error == nil || resp.Error.Code != task.ErrorPeerUnauthorized {
		t.Fatalf("posting to another task should be refused, got %+v", resp)
	}

	// Naming its own task, or naming none at all, both work.
	if resp, _ := g.postCall(workerCtx(mine.ID), mine.ID, "note", ""); !resp.OK {
		t.Fatalf("posting to its own task should succeed: %+v", resp)
	}
	if resp, _ := g.postCall(workerCtx(mine.ID), "", "note", ""); !resp.OK {
		t.Fatalf("posting without naming a task should succeed: %+v", resp)
	}
}

// A question blocks the task, an answer releases it, and the answer reaches the
// waiting worker.
func TestAskBlocksUntilAnsweredAndReleasesTask(t *testing.T) {
	ctx := context.Background()
	g := newTestGateway(t)
	rec := seedWorkerTask(t, g, "tsk_ask")

	type asked struct {
		resp ToolResponse
		err  error
	}
	done := make(chan asked, 1)
	go func() {
		resp, err := g.askCall(workerCtx(rec.ID), "", "should I upgrade the dependency?", "q1", nil, 10)
		done <- asked{resp, err}
	}()

	// The task should show awaiting_input while the question is open.
	var blocked bool
	for i := 0; i < 100; i++ {
		if current, err := g.Store.GetTask(ctx, rec.ID); err == nil && current.State == task.StateAwaitingInput {
			blocked = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("task should show awaiting_input while a question is open")
	}

	// The owner answers directly against the local store, as the mesh handler
	// does on the worker side.
	messages, err := g.Store.ListTaskMessages(ctx, rec.ID, 0, 10)
	if err != nil || len(messages) == 0 {
		t.Fatalf("expected the question to be recorded: %v %+v", err, messages)
	}
	question := messages[len(messages)-1]
	if question.Kind != store.MessageKindQuestion || question.Direction != store.MessageDirectionFromWorker {
		t.Fatalf("unexpected question message: %+v", question)
	}
	if _, err := g.Store.AppendTaskMessage(ctx, store.AppendMessageInput{
		TaskID:      rec.ID,
		Direction:   store.MessageDirectionFromOrigin,
		Kind:        store.MessageKindAnswer,
		Body:        "yes, upgrade it",
		AnsweredSeq: question.Seq,
	}); err != nil {
		t.Fatalf("append answer: %v", err)
	}

	select {
	case got := <-done:
		if !got.resp.OK {
			t.Fatalf("ask should succeed: %+v", got.resp)
		}
		result := got.resp.Result.(map[string]any)
		if result["answered"] != true {
			t.Fatalf("ask should report answered: %+v", result)
		}
		if result["answer"] != "yes, upgrade it" {
			t.Fatalf("ask should return the owner's reply: %+v", result)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("ask did not return after the question was answered")
	}

	current, err := g.Store.GetTask(ctx, rec.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if current.State != task.StateRunning {
		t.Fatalf("task state = %q, want running once answered", current.State)
	}
}

// An unanswered question returns rather than hanging, so the agent stays in
// control of whether to proceed or keep waiting.
func TestAskReturnsUnansweredRatherThanHanging(t *testing.T) {
	g := newTestGateway(t)
	rec := seedWorkerTask(t, g, "tsk_unanswered")

	start := time.Now()
	resp, err := g.askCall(workerCtx(rec.ID), "", "anyone there?", "q1", nil, 1)
	if err != nil {
		t.Fatalf("askCall: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("ask waited %s, far past its 1s budget", elapsed)
	}
	result := resp.Result.(map[string]any)
	if result["answered"] != false {
		t.Fatalf("ask should report unanswered: %+v", result)
	}
	if !strings.Contains(result["guidance"].(string), "call task_ask again") {
		t.Fatalf("unanswered result should tell the agent how to resume: %+v", result)
	}
}

// Reusing a client message id resumes the same question instead of opening a
// second one, so a runner retrying after a tool timeout does not spam the owner.
func TestAskResumesSameQuestionOnRetry(t *testing.T) {
	ctx := context.Background()
	g := newTestGateway(t)
	rec := seedWorkerTask(t, g, "tsk_resume")

	first, _ := g.askCall(workerCtx(rec.ID), "", "same question", "q-retry", nil, 1)
	second, _ := g.askCall(workerCtx(rec.ID), "", "same question", "q-retry", nil, 1)
	if first.Result.(map[string]any)["seq"] != second.Result.(map[string]any)["seq"] {
		t.Fatalf("retry opened a new question: %+v vs %+v", first.Result, second.Result)
	}
	messages, err := g.Store.ListTaskMessages(ctx, rec.ID, 0, 10)
	if err != nil {
		t.Fatalf("ListTaskMessages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("expected exactly one recorded question, got %d", len(messages))
	}
}

// A typed approval request naming a capability outside the task's granted
// profile can be refused mechanically. Anything else goes to the human, and the
// fallback is always "ask", never "allow".
func TestApprovalReviewOnlyAutoDeniesOutsideProfile(t *testing.T) {
	granted := []string{"read_files"}
	cases := []struct {
		name       string
		metadata   map[string]any
		profile    string
		caps       []string
		autoDenied bool
		needsHuman bool
	}{
		{"outside the profile is refused", map[string]any{"capability": "system_change"}, "readonly-agent", granted, true, false},
		{"within the profile still asks", map[string]any{"capability": "read_files"}, "readonly-agent", granted, false, true},
		{"free prose always asks", map[string]any{"note": "can I?"}, "readonly-agent", granted, false, true},
		{"no metadata always asks", nil, "readonly-agent", granted, false, true},
		{"unknown profile asks rather than allows", map[string]any{"capability": "system_change"}, "", nil, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			review := reviewApprovalRequest(tc.metadata, tc.profile, tc.caps)
			if review.AutoDenied != tc.autoDenied || review.NeedsHuman != tc.needsHuman {
				t.Fatalf("review = %+v, want auto_denied=%v needs_human=%v", review, tc.autoDenied, tc.needsHuman)
			}
			if review.Reason == "" {
				t.Fatal("a verdict must explain itself")
			}
		})
	}
}

// The channel is bounded: a runaway worker cannot grow the store without limit,
// and hitting the cap is a visible error rather than a silent drop.
func TestChannelEnforcesBodyAndCountLimits(t *testing.T) {
	ctx := context.Background()
	g := newTestGateway(t)
	rec := seedWorkerTask(t, g, "tsk_limits")

	oversized := strings.Repeat("x", store.MaxMessageBodyBytes+1)
	resp, _ := g.postCall(workerCtx(rec.ID), "", oversized, "")
	if resp.OK || resp.Error == nil || resp.Error.Code != task.ErrorResultTooLarge {
		t.Fatalf("oversized body should be refused with result_too_large, got %+v", resp)
	}

	for i := 0; i < store.MaxMessagesPerTask; i++ {
		if _, err := g.Store.AppendTaskMessage(ctx, store.AppendMessageInput{
			TaskID:    rec.ID,
			Direction: store.MessageDirectionFromWorker,
			Kind:      store.MessageKindNote,
			Body:      "note",
		}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	resp, _ = g.postCall(workerCtx(rec.ID), "", "one too many", "")
	if resp.OK || resp.Error == nil {
		t.Fatalf("posting past the cap should be refused, got %+v", resp)
	}
	if !strings.Contains(resp.Error.Detail, "full") {
		t.Fatalf("cap denial should say the channel is full, got %q", resp.Error.Detail)
	}
}
