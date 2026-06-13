The product should feel less like “configure a distributed system” and more like:

> Install cmesh on each machine. Run one command. Answer a few plain questions. Done.

The core onboarding problem is that cmesh has three prerequisites it cannot magically own:

1. Tailscale identity/reachability
2. local runner availability/auth, like Claude Code or Codex
3. user intent/policy: “which peers may run what, where?”

Everything else should be automated or strongly guided.

**The Ideal Flow**

On each machine:

```bash
curl -fsSL https://get.cmesh.dev | sh
cmesh init
```

`cmesh init` should be interactive and opinionated:

```text
Welcome to cmesh.

This machine:
  Name: mac-studio
  Tailscale: connected as mac-studio.tailnet.ts.net
  Tailnet IP: 100.x.y.z
  Claude Code: found, authenticated
  Codex: found, authenticated

What should this machine do?

[1] Origin only: my local agents can delegate from here
[2] Worker only: other machines can delegate work here
[3] Both: delegate from here and accept work here
```

Then:

```text
Which directories may workers use on this machine?

[1] ~/code/**
[2] ~/work/**
[3] choose directories
```

Then:

```text
Which peers may delegate to this machine?

Found cmesh-capable peers:
  - homelab-vm
  - win-buildbox
  - macbook

Allow:
[1] only my current machine
[2] selected peers
[3] all tagged cmesh devices
```

Then:

```text
Profiles to expose:

[x] readonly  - inspect files, summarize, run no edits
[x] dev       - edit repo and run repo commands
[ ] network   - allow network-capable runner profile
[ ] git_push  - allow pushing branches
```

At the end:

```text
cmesh is ready.

Local MCP gateway:
  http://127.0.0.1:7777/mcp

Registered with:
  ✓ Claude Code
  ✓ Codex

Try this in your agent:
  "Use cmesh to ask homelab-vm to summarize ~/code/myapp."
```

That’s the bar.

**The MCP-Guided Onboarding Idea Is Strong**

Yes, expose an MCP and let the agent guide them through onboarding. But I’d split this into two layers:

**Layer 1: CLI onboarding must work without an agent**

This matters because on a headless server you may not have an agent session open. You want:

```bash
cmesh init
cmesh doctor
cmesh up
```

to be enough.

**Layer 2: agent-assisted onboarding makes it delightful**

Once the local MCP gateway exists, the agent can call tools like:

```text
doctor()
detect_runners()
list_tailnet_peers()
generate_config()
validate_config()
register_mcp()
test_delegate()
```

Then the user says:

```text
Set up cmesh between this Mac and my homelab server.
```

And Claude Code can walk them through:

```text
I found Tailscale and Claude Code on this Mac.
I can see homelab-vm on your tailnet, but cmesh is not running there yet.

Run this on homelab-vm:
curl -fsSL https://get.cmesh.dev | sh
cmesh init --worker
```

Then once homelab is up:

```text
I found homelab-vm.
It exposes readonly and dev profiles.
I'll register it as a peer and run a smoke delegation.
```

That could be magical.

**The Bootstrap Problem**

The hardest part is the first remote machine. Before cmesh is installed there, your local agent cannot use cmesh to configure it. So you need a crisp bootstrap story:

Option A: manual install command on every node:

```bash
curl -fsSL https://get.cmesh.dev | sh
cmesh init
```

Option B: SSH bootstrap from origin:

```bash
cmesh bootstrap ssh user@homelab-vm
```

This could:

- copy/install the binary
- run `cmesh init --worker --origin mac-studio`
- write config
- start the service
- run doctor checks

That would be huge for friction reduction.

Option C: Tailscale SSH bootstrap:

```bash
cmesh bootstrap tailscale homelab-vm
```

If Tailscale SSH is enabled, even better.

I’d probably ship Option A first, then Option B/ C soon after.

**Make Setup Opinionated**

Most users should not write TOML first. TOML is the escape hatch.

The default path should be:

```bash
cmesh init
```

The CLI generates:

```text
~/.config/cmesh/config.toml
```

And asks only human questions:

- What is this machine for?
- Which directories are safe?
- Which peers are allowed?
- Which runner should be used?
- Should I register MCP with Claude Code/Codex?

Not:

- mesh port?
- mcp port?
- bind mode?
- idempotency retention?
- runner manifest path?

Those should be advanced flags.

**Recommended Onboarding Commands**

I’d aim for these:

```bash
cmesh init
```

Interactive setup.

```bash
cmesh doctor
```

Checks everything: Tailscale, bind, LocalAPI, runner auth, config, MCP, peer reachability.

```bash
cmesh connect
```

Registers MCP with local harnesses.

```bash
cmesh peers
```

Shows detected/configured peers.

```bash
cmesh test homelab-vm
```

Runs a safe smoke delegation.

```bash
cmesh service install
cmesh service start
```

Installs daemon as launchd/systemd/Windows service.

```bash
cmesh bootstrap ssh user@host
```

Installs/configures cmesh remotely.

**What “Frictionless” Means for Different Users**

For a single power user:

```bash
brew install cmesh
cmesh init
cmesh connect
```

For a Linux server:

```bash
curl -fsSL https://get.cmesh.dev | sh
cmesh init --worker
cmesh service install
```

For a full two-node setup:

```bash
# On Mac
cmesh init --origin

# On server
cmesh init --worker --allow-peer mac-studio --allow-cwd /srv/myapp

# Back on Mac
cmesh test server
```

For agent-assisted setup:

```text
Claude, set up cmesh so this Mac can delegate readonly and dev tasks to homelab-vm.
```

Claude replies with one remote command and then handles the rest locally.

**Service Management Is Essential**

If users have to remember to keep `cmesh up` running in terminals, it will feel toy-like.

You want:

```bash
cmesh service install
cmesh service start
cmesh service status
cmesh service logs
```

Platform-specific:

- macOS: launchd
- Linux: systemd user service or system service
- Windows: Windows service

This is where a Tauri app later becomes useful: “cmesh is running / not running” in the menu bar.

**The First Delegation Should Be Built In**

After setup:

```bash
cmesh test homelab-vm
```

Should delegate something harmless:

```text
Summarize hostname, OS, cmesh version, runner version, and current cwd permissions.
```

It should return:

```text
✓ peer reachable
✓ whois identity verified
✓ inbound policy allowed
✓ runner available
✓ task completed
```

Then the agent guidance can say:

```text
Try in Claude Code:
"Use cmesh to ask homelab-vm to summarize ~/code/myrepo."
```

**Should MCP Guide Setup? Yes, but Carefully**

The MCP should not be remote setup magic by itself. But it should expose local setup tools so the agent can guide and verify:

Potential MCP tools:

```text
doctor()
init_status()
detect_runners()
detect_tailnet()
list_tailnet_peers()
list_configured_peers()
suggest_config()
validate_config()
register_harness()
test_peer(peer)
```

Some tools should be read-only. Mutating setup tools should require explicit user confirmation, especially anything changing config, services, or allowed directories.

**The Copy Should Be Very Plain**

The product language should become:

> cmesh lets the coding agent on this machine ask the coding agent on another machine to do work.

Then:

> You install cmesh on each machine. cmesh uses Tailscale to reach peers, verifies who is calling, checks your policy, runs the local agent there, and gives your current agent the result.

That’s the whole thing.

**The Dream Demo**

The demo should be this simple:

```bash
# Mac
brew install cmesh
cmesh init

# Homelab
curl -fsSL https://get.cmesh.dev | sh
cmesh init --worker --allow-peer mac-studio --allow-cwd ~/code/**

# Mac
cmesh test homelab
```

Then in Claude Code:

```text
Use cmesh to delegate tests for ~/code/myapp to homelab and keep working here.
```

Claude:

```text
Delegated to homelab: task_abc123.
I'll continue locally and check back for the result.
```

Later:

```text
homelab finished. Tests failed in package internal/store.
It created branch cmesh/task_abc123/fix-store-test.
Here is the diffstat. I recommend running go test ./internal/store locally before merging.
```

That is crisp. That sells itself.