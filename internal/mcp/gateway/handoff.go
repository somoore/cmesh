package gateway

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"cmesh/internal/mesh/schema"
	"cmesh/internal/task"
)

// Handoff continues one peer's work on another: Claude writes the fix on the
// Linux box, Codex verifies it natively on Windows.
//
// It carries the source task's branch as a git bundle and nothing else. The
// receiving agent gets structured refs -- branch, base, head, changed files --
// and reads the diff itself. A worker's prose never seeds another worker's
// prompt: that would turn one agent's claims into another agent's instructions
// and make "who decided this" unanswerable.
//
// This is a wrapper over the ordinary dispatch path, not a second dispatcher.
// It resolves the seed and then calls delegateCall, so outbound policy, denied
// profiles, profile ceilings, and the receiving peer's inbound policy all apply
// exactly as they would to any other task. Handoff grants no new authority.

// handoffCall seeds a task on one peer from another peer's completed work.
func (g *Gateway) handoffCall(ctx context.Context, req DelegateRequest, fromTaskID string) (ToolResponse, error) {
	if err := requireOwnerCaller(ctx); err != nil {
		return toolFailure(task.ErrorPeerUnauthorized, err.Error())
	}
	fromTaskID = strings.TrimSpace(fromTaskID)
	if fromTaskID == "" {
		return toolFailure(task.ErrorWorkerFailed, "from_task_id is required")
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return toolFailure(task.ErrorWorkerFailed, "prompt is required: say what the receiving agent should do with the branch, in your own words")
	}
	if strings.TrimSpace(req.Peer) == "" && len(req.Peers) == 0 {
		return toolFailure(task.ErrorPeerUnreachable, "peer is required")
	}

	seed, failure, err := g.handoffSeed(ctx, fromTaskID)
	if err != nil {
		return failure, err
	}
	if strings.TrimSpace(req.Peer) != "" && g.peerMatchesNode(req.Peer, seed.sourceNode) {
		return toolFailure(task.ErrorWorkerFailed, "handoff targets the peer that already holds this work; use followup_task_id to continue on the same peer")
	}

	req.Handoff = seed.seed
	resp, err := g.delegateCall(ctx, req)
	if err != nil {
		return resp, err
	}
	// Surface the lineage so the caller can report what was handed over without
	// re-deriving it.
	if created, ok := resp.Result.(schema.CreateTaskResponse); ok {
		return ToolResponse{OK: resp.OK, Error: resp.Error, Result: map[string]any{
			"task":          created,
			"handoff_from":  fromTaskID,
			"branch":        seed.seed.Branch,
			"base_commit":   seed.seed.BaseCommit,
			"head_commit":   seed.seed.HeadCommit,
			"files_changed": seed.seed.FilesChanged,
		}}, nil
	}
	return resp, nil
}

type resolvedHandoff struct {
	seed       *schema.HandoffSeed
	sourceNode string
}

// handoffSeed resolves a completed task into a seed the receiving peer can
// apply. Every refusal here is explicit: a caller must never be handed a task
// that silently starts from nothing.
func (g *Gateway) handoffSeed(ctx context.Context, fromTaskID string) (resolvedHandoff, ToolResponse, error) {
	peerTask, err := g.Store.GetPeerTask(ctx, fromTaskID)
	if err != nil {
		resp, respErr := toolFailure(task.ErrorWorkerFailed, "source task is not known to this node: "+err.Error())
		return resolvedHandoff{}, resp, respErr
	}
	client, err := g.clientByNode(peerTask.PeerNodeID)
	if err != nil {
		resp, respErr := toolFailure(task.ErrorPeerUnreachable, err.Error())
		return resolvedHandoff{}, resp, respErr
	}
	result, err := client.TaskResult(ctx, peerTask.RemoteTaskID)
	if err != nil {
		resp, respErr := rpcFailure(task.ErrorPeerUnreachable, err)
		return resolvedHandoff{}, resp, respErr
	}
	if result.Result == nil {
		resp, respErr := toolFailure(task.ErrorWorkerFailed, "source task has no result envelope yet; wait for it to finish before handing it off")
		return resolvedHandoff{}, resp, respErr
	}
	branch := strings.TrimSpace(result.Result.Branch)
	if branch == "" {
		// A read-only or no-op task produced no branch. That is not a missing
		// base -- there is simply nothing to hand over -- so say so plainly
		// rather than seeding an empty task.
		resp, respErr := toolFailure(task.ErrorWorkerFailed, "source task produced no branch, so there is nothing to hand off; delegate a fresh task instead")
		return resolvedHandoff{}, resp, respErr
	}

	bundle, err := client.TaskArtifact(ctx, peerTask.RemoteTaskID, "git-bundle")
	if err != nil {
		resp, respErr := rpcFailure(task.ErrorWorkerFailed, err)
		return resolvedHandoff{}, resp, respErr
	}
	if len(bundle) == 0 {
		resp, respErr := toolFailure(task.ErrorWorkerFailed, "source task has no git bundle artifact; it may have changed no files")
		return resolvedHandoff{}, resp, respErr
	}
	if len(bundle) > schema.MaxHandoffBundleBytes {
		resp, respErr := toolFailure(task.ErrorResultTooLarge, fmt.Sprintf(
			"source bundle is %d bytes, over the %d byte handoff limit; push the branch to a shared remote instead",
			len(bundle), schema.MaxHandoffBundleBytes))
		return resolvedHandoff{}, resp, respErr
	}

	return resolvedHandoff{
		sourceNode: peerTask.PeerNodeID,
		seed: &schema.HandoffSeed{
			SourceTaskID: fromTaskID,
			SourcePeer:   peerTask.PeerNodeID,
			Branch:       branch,
			BaseCommit:   result.Result.BaseCommit,
			HeadCommit:   result.Result.HeadCommit,
			FilesChanged: result.Result.FilesChanged,
			BundleBase64: base64.StdEncoding.EncodeToString(bundle),
		},
	}, ToolResponse{}, nil
}
