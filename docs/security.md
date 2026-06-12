# Security Model

cmesh treats network reachability as defense in depth, not authorization.

## Layers

1. **Network admission**
   Tailscale grants or ACLs decide which tailnet nodes can reach the mesh port.

2. **Connection identity**
   On inbound mesh RPC, cmesh queries Tailscale LocalAPI with `whois(remote_addr)`.

3. **Task authorization**
   cmesh policy decides whether that peer may run that task, in that cwd, under that profile.

4. **Runner containment**
   The selected runner uses its native controls. cmesh never passes skip-all-permissions flags.

## Binding rules

- Local MCP gateway binds only to loopback.
- Mesh RPC binds only to the node's Tailscale IP.
- Mesh RPC refuses wildcard bind addresses.
- Local gateway rejects forwarded identity headers.
- Local gateway validates loopback `Host` and `Origin`.

## Policy checks

Worker-side inbound policy checks:

- allowed peer;
- realpath-normalized cwd allowlist;
- requested profile availability;
- runner availability;
- hop limit;
- cycle detection.

Origin-side outbound policy can deny peers, profiles, and subdelegation before any mesh RPC call is made.

## Results are claims

Worker output is prompt-injection-bearing content. The result schema calls the main summary field `worker_summary_untrusted` on purpose.

The default result envelope is bounded and structured. Full transcripts are referenced for explicit human or CLI review rather than automatically inlined into the agent context.

## Restart semantics

Worker daemon restart is fail-closed in v1:

- queued/running tasks are marked `failed`;
- error code is `worker_daemon_restarted`;
- audit records the transition;
- silent task loss is forbidden.

