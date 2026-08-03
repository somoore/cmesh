package gateway

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"cmesh/internal/store"
	"cmesh/internal/task"
)

// A workstream is a named delegation thread: several tasks, possibly on
// different peers, that belong to one piece of work.
//
// It is a name for something that already existed. Every task carries an
// origin_session_id grouping it with its siblings and its subdelegated
// children, and that session is the ownership key for task-token reads. Naming
// it turns invisible plumbing into something a conductor can say out loud:
// "how is the auth refactor going across all three machines?"
//
// The security consequence is deliberate and must stay documented: tasks in one
// workstream share a session, so a worker in that workstream can see its
// siblings' tasks. That is the team-visibility boundary. It is also why only a
// host-token caller may choose a workstream — a task token's session comes from
// its stored context, never from a request, or a worker could name its way into
// another team's tasks.

// workstreamSlug bounds a workstream name. It becomes an origin session id,
// which appears in task records, idempotency keys, and audit events, so it is
// restricted to characters that stay readable everywhere they surface.
var workstreamSlug = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// workstreamSessionPrefix namespaces a named workstream's session so it can
// never collide with a harness-supplied session id.
const workstreamSessionPrefix = "ws:"

// workstreamSession maps a workstream name to the origin session id that
// groups its tasks.
func workstreamSession(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil
	}
	if !workstreamSlug.MatchString(name) {
		return "", fmt.Errorf("workstream %q must be 1-64 characters of letters, digits, dot, dash, or underscore, starting with a letter or digit", name)
	}
	return workstreamSessionPrefix + name, nil
}

// workstreamName recovers the workstream a session belongs to, or "" when the
// session is not a named workstream.
func workstreamName(sessionID string) string {
	if strings.HasPrefix(sessionID, workstreamSessionPrefix) {
		return strings.TrimPrefix(sessionID, workstreamSessionPrefix)
	}
	return ""
}

// workstreamStatusCall reports every task in a workstream, across peers, in one
// call, so a conductor can answer "where does this work stand" without polling
// each task separately.
//
// Status comes from the local mirror rather than a live fetch per peer: this is
// a summary of what this node dispatched, and a peer being briefly unreachable
// should not fail the overview. Use task_status for an authoritative single
// task.
func (g *Gateway) workstreamStatusCall(ctx context.Context, name string, limit int) (ToolResponse, error) {
	if err := requireOwnerCaller(ctx); err != nil {
		return toolFailure(task.ErrorPeerUnauthorized, err.Error())
	}
	session, err := workstreamSession(name)
	if err != nil {
		return toolFailure(task.ErrorWorkerFailed, err.Error())
	}
	if session == "" {
		return toolFailure(task.ErrorWorkerFailed, "workstream is required")
	}
	if limit <= 0 {
		limit = 100
	}
	peerTasks, err := g.Store.ListPeerTasks(ctx, limit)
	if err != nil {
		return toolFailure(task.ErrorWorkerFailed, err.Error())
	}

	byPeer := map[string][]map[string]any{}
	counts := map[string]int{}
	total := 0
	for _, peerTask := range peerTasks {
		if strings.TrimSpace(peerTask.OriginSessionID) != session {
			continue
		}
		total++
		counts[string(peerTask.State)]++
		entry := map[string]any{
			"task_id":    peerTask.TaskID,
			"state":      peerTask.State,
			"updated_at": peerTask.UpdatedAt,
		}
		if peerTask.ProfileEffective != "" {
			entry["profile"] = peerTask.ProfileEffective
		}
		byPeer[peerTask.PeerNodeID] = append(byPeer[peerTask.PeerNodeID], entry)
	}

	peers := make([]string, 0, len(byPeer))
	for peer := range byPeer {
		peers = append(peers, peer)
	}
	sort.Strings(peers)

	return ToolResponse{OK: true, Result: map[string]any{
		"workstream": name,
		"session_id": session,
		"tasks":      total,
		"by_state":   counts,
		"peers":      peers,
		"by_peer":    byPeer,
		"note":       "States come from this node's mirror of what it dispatched. Use task_status for an authoritative single task, and task_events to follow one while it runs.",
	}}, nil
}

// workstreamFilter narrows a task list to one workstream. An empty name leaves
// the list unchanged.
func workstreamFilter(peerTasks []store.PeerTask, name string) ([]store.PeerTask, error) {
	session, err := workstreamSession(name)
	if err != nil {
		return nil, err
	}
	if session == "" {
		return peerTasks, nil
	}
	out := peerTasks[:0]
	for _, peerTask := range peerTasks {
		if strings.TrimSpace(peerTask.OriginSessionID) == session {
			out = append(out, peerTask)
		}
	}
	return out, nil
}
