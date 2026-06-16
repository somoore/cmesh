# Roadmap

cmesh is experimental software for trusted private networks. This roadmap sets expectations for the hardening path without implying that the current release is enterprise remote management.

## Now

- Keep the default setup narrow: `query` first, explicit peers, explicit cwd allowlists, and no public listener.
- Preserve the least-authority profile ladder: deterministic probes before read-only agents, repo edit profiles, desktop control, or system change profiles.
- Keep built-in high-risk profiles explicit and lease-gated; require custom high-risk profiles to opt into the same `require_lease` guard.
- Keep CI focused on reproducible Go tests, workflow linting, vulnerability scanning, supply-chain checks, and CodeQL.

## Next

- Add OS sandbox wrappers where practical:
  - macOS Seatbelt profile support for runner subprocesses.
  - Linux systemd sandboxing examples for service deployments.
  - Windows restricted user or job object guidance for worker runners.
- Add tamper-evident audit logs with hash chaining or signatures.
- Expand local smoke coverage for daemon-to-daemon flows that can run without a live tailnet.
- Add signed runner manifest verification so local config is not the only trust root for runner claims.
- Improve helper tooling for origin-side result verification, including optional diff review and command receipt checks.

## Later

- Add a shared-team authorization layer for per-human approval and role separation.
- Add remote interactive approval forwarding for desktop/system-change workflows.
- Add richer policy checks for prompt-content risk before a high-risk profile is used.
- Add more typed probes so common tasks do not need shell or agent-backed profiles.

## Intentionally out of scope

- Public internet exposure of the mesh RPC endpoint.
- Treating worker output as trusted truth.
- Silent privilege escalation from a low-risk profile to a high-risk profile.
- Replacing Tailscale account security, OS account isolation, filesystem permissions, or human review.
