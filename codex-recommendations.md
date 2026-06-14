Yes. I looked through the zip and the latest shape is good. The onboarding revamp is the right move: install binary globally → install setup MCP into Claude/Codex → let the agent walk the user through setup is much better than asking users to understand config, service installation, Tailscale identity, peer repair, and gateway registration manually.

I could not run the test suite because go.mod requires go 1.26.4 and the sandbox cannot download that toolchain, so this is a static code review. But structurally, this is the correct place to add the profile/permissions system.

Current state, as I see it

You already have:

* a clean CLI/onboarding split in cmd/cmesh;
* stdio onboarding MCP in internal/mcp/setupstdio;
* local HTTP gateway MCP in internal/mcp/gateway;
* mesh RPC in internal/mesh/rpc;
* config generation in internal/onboard;
* policy checks in internal/policy;
* runner abstraction in internal/runner;
* Claude/Codex runners;
* profile fields in config.ProfileConfig;
* runner manifests;
* semantic peer smoke testing;
* task ownership authorization and whois/provenance validation in the RPC server.

The missing part is that profiles are still mostly labels with claims, not compiled capability contracts.

Right now the generated config still thinks in terms of:

readonly, dev, network, interactive, git_push

and buildRunners only maps a limited subset of ProfileConfig into runner behavior. For Claude Code, it passes claims, bare, and output format, but not richer controls like allowed/disallowed tools, permission mode, shell policy, or custom permission bundles. For Codex, Windows currently maps to danger-full-access, which is exactly the kind of thing your future profile system needs to make explicit, loud, and opt-in.

So the next milestone should be:

Turn profiles into generated, validated, enforceable capability templates that can be created and selected through CLI and MCP, with query as the safe default.

The target model

You want four separate concepts.

1. Capability

A primitive permission unit.

Examples:

disk_info
system_info
repo_status
service_status
process_list
log_tail
network_check
shell_readonly
read_files
edit_repo
execute_in_repo
git_push
desktop_control
system_change

Capabilities should be known to cmesh, typed, documented, and assigned risk metadata.

2. Profile template

A named bundle of capabilities and constraints.

Examples:

query
query-network
query-shell
readonly-agent
repo-dev
desktop-control
system-change

A template says what the profile is supposed to allow and how it is enforced.

3. Active profile context

The local gateway’s current default for a peer/session.

Example:

win11-desktop → query
homelab → repo-dev for /home/scott/code/cmesh, expires in 30m

This gives the seamless UX you want: the user does not have to mention --profile in natural language every time.

4. Effective profile

The worker’s final decision.

The origin may request repo-dev; the worker decides whether that profile is actually allowed for this peer, cwd, lease, and capability.

The most important design decision

Do not make query an agent profile.

Make it a ProbeRunner profile.

That means:

query = deterministic probes, no Claude, no Codex, no arbitrary shell, no writes

Then add weaker profiles separately:

query-shell = allowlisted diagnostic shell, medium confidence
readonly-agent = agent can inspect/summarize bounded data, medium/low confidence
repo-dev = agent can edit/test inside allowed cwd

This lets you honestly say:

query is enforced by cmesh.

while also honestly saying:

readonly-agent depends partly on harness controls and is not equivalent to deterministic no-write enforcement.

Implementation plan

Phase 1 — Add capability/profile model

Create a new package:

internal/capability

or:

internal/profile

I’d prefer internal/profile because it can own templates, active contexts, validation, and generation.

Suggested types:

type Intent string
const (
    IntentQuery   Intent = "query"
    IntentInspect Intent = "inspect"
    IntentNetwork Intent = "network"
    IntentBuild   Intent = "build"
    IntentModify  Intent = "modify"
    IntentDesktop Intent = "desktop"
    IntentSystem  Intent = "system"
)
type Capability string
const (
    CapDiskInfo        Capability = "disk_info"
    CapSystemInfo      Capability = "system_info"
    CapRepoStatus      Capability = "repo_status"
    CapServiceStatus   Capability = "service_status"
    CapProcessList     Capability = "process_list"
    CapLogTail         Capability = "log_tail"
    CapNetworkCheck    Capability = "network_check"
    CapShellReadonly   Capability = "shell_readonly"
    CapReadFiles       Capability = "read_files"
    CapEditRepo        Capability = "edit_repo"
    CapExecuteInRepo   Capability = "execute_in_repo"
    CapGitPush         Capability = "git_push"
    CapDesktopControl  Capability = "desktop_control"
    CapSystemChange    Capability = "system_change"
)
type ConstraintSet struct {
    Writes         string `json:"writes"`          // deny, repo_only, allow
    Network        string `json:"network"`         // deny, allow
    Shell          string `json:"shell"`           // deny, allowlisted, allow
    RequiresCWD    bool   `json:"requires_cwd"`
    RequiresLease  bool   `json:"requires_lease"`
    RequiresConfirm bool  `json:"requires_confirm"`
    MaxRuntime     string `json:"max_runtime"`
    MaxOutputBytes int64  `json:"max_output_bytes"`
}
type ShellPolicy struct {
    Mode            string              `json:"mode"` // deny, allowlist
    AllowedCommands map[string][]string `json:"allowed_commands"`
    DeniedPatterns  []string            `json:"denied_patterns"`
}
type Template struct {
    Name                  string         `json:"name"`
    Description           string         `json:"description"`
    Intents               []Intent       `json:"intents"`
    Runner                string         `json:"runner"`
    Capabilities          []Capability   `json:"capabilities"`
    Constraints           ConstraintSet  `json:"constraints"`
    ShellPolicy           *ShellPolicy   `json:"shell_policy,omitempty"`
    EnforcementConfidence string         `json:"enforcement_confidence"`
    KnownGaps             []string       `json:"known_gaps,omitempty"`
    AuditLevel            string         `json:"audit_level"` // normal, loud
    DefaultTTL            string         `json:"default_ttl,omitempty"`
}

Then seed built-in templates:

query
query-network
query-shell
readonly-agent
repo-dev
desktop-control
system-change

Do not remove custom profiles. Make built-ins templates the user can copy/modify.

Phase 2 — Extend config.ProfileConfig

Current ProfileConfig is too shallow:

Runner
Claims
AllowedPeers
RequireExplicit
Bare
Network
GitPush
MaxRuntime

Extend it instead of replacing everything at once:

type ProfileConfig struct {
    Runner          string   `toml:"runner"`
    Description     string   `toml:"description"`
    Intents         []string `toml:"intents"`
    Capabilities    []string `toml:"capabilities"`
    Claims          []string `toml:"claims"` // compatibility alias
    AllowedPeers    []string `toml:"allowed_peers"`
    RequireExplicit bool     `toml:"require_explicit"`
    RequireLease    bool     `toml:"require_lease"`
    Bare            bool     `toml:"bare"`
    Writes          string   `toml:"writes"`   // deny, repo_only, allow
    Shell           string   `toml:"shell"`    // deny, allowlisted, allow
    Network         bool     `toml:"network"`
    GitPush         bool     `toml:"git_push"`
    MaxRuntime      Duration `toml:"max_runtime"`
    MaxOutputBytes  int64    `toml:"max_output_bytes"`
    AuditLevel      string   `toml:"audit_level"`
    ShellPolicy     ShellPolicyConfig `toml:"shell_policy"`
}

Compatibility rule:

* if capabilities is empty, copy from claims;
* eventually rename Claims to Capabilities internally;
* keep claims in manifests for now if you want schema compatibility, but treat them as capabilities.

Phase 3 — Add ProbeRunner

Create:

internal/runner/probe

This runner does not call Claude/Codex. It executes typed built-in probes.

Initial probes:

disk_info
system_info
repo_status
service_status
process_list

Optional later:

log_tail
network_check
memory_info
port_check
collector_status
fleet_status
osquery_status

The query profile should use this runner:

[profiles.query]
description = "Deterministic read-only machine queries. No shell, no writes."
runner = "probe"
intents = ["query"]
capabilities = ["disk_info", "system_info", "repo_status"]
writes = "deny"
shell = "deny"
network = false
max_runtime = "30s"
max_output_bytes = 65536
audit_level = "normal"

The key acceptance test:

query cannot change wallpaper, write a file, run arbitrary shell, or invoke Claude/Codex.

Phase 4 — Add typed query task support

Right now delegate is prompt-based. Keep it, but add a typed query path.

Add a schema type:

type TaskKind string
const (
    TaskKindDelegate TaskKind = "delegate"
    TaskKindQuery    TaskKind = "query"
)
type QueryRequest struct {
    Kind   string         `json:"kind"`
    Params map[string]any `json:"params,omitempty"`
    CWD    string         `json:"cwd,omitempty"`
}

Extend schema.CreateTaskRequest:

TaskKind string        `json:"task_kind,omitempty"`
Query    *QueryRequest `json:"query,omitempty"`

For query tasks:

* Prompt can be empty or generated from Query;
* worker chooses probe runner from the effective profile;
* policy checks required capability before runner execution.

Add MCP tool:

query_peer(peer, kind, params, cwd?, wait?)

Examples:

{
  "peer": "win11-desktop",
  "kind": "disk_info",
  "params": {
    "volume": "C:"
  }
}
{
  "peer": "homelab",
  "kind": "repo_status",
  "cwd": "/home/scott/code/cmesh"
}

Do not make the model write shell. The model chooses a typed query.

Phase 5 — Add ShellRunner only after ProbeRunner

For query-shell, create:

internal/runner/shell

This is not arbitrary shell. It should run allowlisted commands only.

Config:

[profiles.query-shell]
description = "Allowlisted read-oriented diagnostic shell. No writes."
runner = "shell"
intents = ["query"]
capabilities = ["shell_readonly", "service_status", "log_tail"]
writes = "deny"
shell = "allowlisted"
network = false
max_runtime = "30s"
max_output_bytes = 65536
audit_level = "loud"
enforcement_confidence = "medium"
[profiles.query-shell.shell_policy]
mode = "allowlist"
[profiles.query-shell.shell_policy.windows]
allowed_commands = ["Get-Service", "Get-Process", "Get-EventLog", "Get-PSDrive"]
[profiles.query-shell.shell_policy.darwin]
allowed_commands = ["df", "uname", "sysctl", "ps", "log"]
[profiles.query-shell.shell_policy.linux]
allowed_commands = ["df", "uname", "systemctl status", "ps", "journalctl"]

But I would not start here. Build ProbeRunner first.

Phase 6 — Add profile generator CLI

Add command group:

cmesh profile templates
cmesh profile generate
cmesh profile validate
cmesh profile write
cmesh profile current
cmesh profile use
cmesh profile reset

Suggested UX:

cmesh profile templates
query              high      probes only, no writes, no shell
query-network      high      probes + bounded network checks
query-shell        medium    allowlisted diagnostic shell
readonly-agent     medium    agent summarizes bounded data
repo-dev           medium    edit/test inside allowed cwd
desktop-control    high-risk desktop/session changes
system-change      high-risk services/settings/packages

Generate:

cmesh profile generate query
cmesh profile generate query-shell --os windows --cap service_status --cap disk_info

Write:

cmesh profile write query --confirmed

Use active context:

cmesh profile use win11-desktop query

Elevated profile with TTL:

cmesh profile use win11-desktop repo-dev \
  --cwd 'C:\Users\scott\code\cmesh' \
  --ttl 30m

Phase 7 — Expose the same flow through MCP

This is where your new onboarding flow pays off.

Add these tools to both setup stdio MCP and gateway MCP, with different capabilities.

Setup MCP tools

Used before daemon/gateway is fully configured:

profile_templates
profile_generate
profile_validate
profile_preview_config
profile_write_config

Gateway MCP tools

Used after daemon is running:

profile_templates
profile_current
profile_use
profile_reset
profile_describe
query_peer

Potentially later:

profile_grant
profile_revoke

But be careful: an agent should not grant itself elevated authority. For elevated profiles, MCP should generally say:

This requires local operator confirmation. Ask the user to approve or run the CLI command.

For query/default profiles, MCP can be frictionless.

Phase 8 — Active profile context store

Create local gateway state:

~/.local/share/cmesh/profile-context.json

or put it in SQLite.

Suggested struct:

{
  "default_profile": "query",
  "peers": {
    "win11-desktop": {
      "profile": "query",
      "scope": "global",
      "updated_at": "..."
    },
    "homelab": {
      "profile": "repo-dev",
      "cwd": "/home/scott/code/cmesh",
      "scope": "session",
      "expires_at": "...",
      "lease_id": "lease_..."
    }
  }
}

Gateway behavior:

* if delegate.profile is empty, resolve active profile for peer;
* if still empty, use global default query;
* attach profile_source = active_context;
* send requested_profile to worker.

The worker still decides the effective profile.

Phase 9 — Update onboarding wizard

This is the user experience I would aim for.

After configure_node or before final ready state, the setup MCP should ask:

Choose the default authority this node exposes to peers.

Options:

1. query — safe deterministic probes, no writes, no shell
2. query + repo-dev — query by default, allow temporary repo edits when explicitly activated
3. custom — generate a profile from selected capabilities

Default should be query.

For a worker node, setup should generate:

[profiles.query]
runner = "probe"
...
[runners.probe]
command = ["builtin"]
default_profile = "query"

If a coding agent is detected, setup can also offer:

[profiles.repo-dev]
runner = "claude-code"
...
enabled = false / require_explicit = true / require_lease = true

Do not make dev the default anymore.

This is a big change. Today pickDefaultProfile prefers dev then readonly. I’d change default preference to:

query → readonly → dev

Once ProbeRunner exists.

What to change in the current codebase

1. internal/config

Add profile fields described above.

Change pickDefaultProfile from:

dev, readonly, first

to:

query, readonly, dev, first

after query exists.

Add validation:

* unknown capabilities rejected;
* writes=deny cannot combine with edit_repo;
* shell=deny cannot combine with shell_readonly;
* runner=probe cannot have edit_repo;
* runner=claude-code profiles with “high confidence” should be rejected or downgraded;
* desktop_control and system_change require require_explicit=true;
* elevated profile without TTL/lease should warn, not necessarily reject for personal mode.

2. internal/onboard

Replace hardcoded profiles := []string{"readonly", "dev"} with:

query default
repo-dev optional
query-shell optional

GenerateConfig should call a profile generator rather than switch on literal profile names.

Instead of:

writeProfile(...)

use:

profile.RenderTOML(template, opts)

or return structured config objects and serialize.

3. internal/runner

Add:

probe
shell

Update registry build:

case probe.Name:
   probe.New(...)
case shell.Name:
   shell.New(...)

You currently error on unknown runners. That is good; keep it.

4. internal/mcp/gateway

Add tools:

profile_templates
profile_current
profile_use
profile_reset
query_peer

Change delegateCall so empty profile resolves from active context:

if req.Profile == "" {
    req.Profile = g.ProfileContext.Resolve(req.Peer)
}

But do not hide the selected profile. Return it in the response.

Example:

{
  "task_id": "...",
  "requested_profile": "query",
  "profile_source": "active_context"
}

5. internal/mcp/setupstdio

Add setup-time tools:

profile_templates
profile_generate
profile_validate

Update configure_node input schema. Today it accepts profiles []string with “readonly/dev/network/interactive/git_push”. Replace or extend with:

{
  "profile_templates": ["query", "repo-dev"],
  "custom_profiles": [...]
}

Keep old profiles for compatibility.

6. internal/policy

Decision should include:

NormalizedCWD string
Capabilities []string
Intents []string
Writes string
Shell string
Network bool
AuditLevel string

Policy should validate capability semantics, not only profile existence.

Inbound checks should be:

1. peer allowed;
2. cwd normalized if required;
3. profile exists;
4. peer allowed for profile;
5. lease/explicit required if elevated;
6. task kind/capability allowed by profile;
7. runner exists;
8. return effective profile and effective capabilities.

7. internal/mesh/schema

Add query support and active profile metadata:

type CreateTaskRequest struct {
    ...
    TaskKind       string        `json:"task_kind,omitempty"`
    Query          *QueryRequest `json:"query,omitempty"`
    ProfileSource  string        `json:"profile_source,omitempty"`
    ProfileContextID string      `json:"profile_context_id,omitempty"`
}

Add response fields:

RequestedProfile string
EffectiveProfile string

8. docs

Update README to lead with query:

Default profile: query
Deterministic probes
No shell
No writes
No arbitrary agent execution

Then show:

cmesh query win11 disk --volume C:

For agent onboarding:

help me setup cmesh

Then:

Ask win11 how much disk is left.

The profile generator design

The generator should accept human intent and produce a safe template.

Input:

{
  "name": "windows-diagnostics",
  "intent": ["query"],
  "capabilities": ["disk_info", "service_status", "shell_readonly"],
  "network": false,
  "writes": "deny",
  "shell": "allowlisted",
  "os": ["windows"],
  "ttl": "30m"
}

Output:

[profiles.windows-diagnostics]
description = "Windows diagnostic profile: disk/service checks with allowlisted shell."
runner = "shell"
intents = ["query"]
capabilities = ["disk_info", "service_status", "shell_readonly"]
writes = "deny"
shell = "allowlisted"
network = false
require_explicit = true
max_runtime = "30s"
max_output_bytes = 65536
audit_level = "loud"
enforcement_confidence = "medium"
[profiles.windows-diagnostics.shell_policy]
mode = "allowlist"
[profiles.windows-diagnostics.shell_policy.windows]
allowed_commands = ["Get-PSDrive", "Get-Service"]

The generator should refuse unsafe contradictions:

query + edit_repo              reject
query + desktop_control        reject unless intent includes desktop
writes=deny + git_push         reject
shell=deny + shell_readonly    reject
runner=probe + shell_readonly  reject
desktop_control no explicit    reject
system_change no explicit      reject

MCP onboarding flow for profiles

Here is the ideal agent-driven flow:

1. User says: “help me setup cmesh.”
2. Setup MCP calls onboarding_status.
3. Setup MCP detects Tailscale, agent runners, peers.
4. Setup MCP calls profile_templates.
5. Agent says:

I recommend starting with query-only:
- disk/system/repo status
- no writes
- no shell
- no network
You can add repo-dev later.

6. Agent calls profile_generate for query.
7. Agent previews config.
8. User confirms.
9. Agent calls configure_node / write_config.
10. Service starts.
11. Peer link runs.
12. Gateway registers.
13. Agent says:

You are ready. Default active profile for peers is query.
Try: Ask win11 how much disk is left on C.

This is the frictionless path, and it makes the safe thing the default thing.

Acceptance tests I would add

Profile generation tests

generate query template
generate query-network template
generate query-shell template
generate repo-dev template
reject query + edit_repo
reject writes=deny + git_push
reject shell=deny + shell_readonly
desktop-control requires explicit
system-change requires explicit

Policy tests

query allows disk_info
query denies shell_readonly
query denies desktop_control
query denies edit_repo
query-network allows network_check
query denies network_check
query-shell allows allowlisted command
query-shell denies non-allowlisted command
repo-dev requires cwd
repo-dev cwd is normalized before allowlist
elevated profile requires explicit/lease

Runner tests

ProbeRunner disk_info returns structured result
ProbeRunner does not call shell
ProbeRunner rejects unknown query kind
ShellRunner rejects denied pattern
ShellRunner rejects command not in allowlist
Claude profile reports medium confidence
Codex Windows danger-full-access cannot be hidden as readonly

MCP tests

profile_templates tool returns built-ins
profile_generate previews TOML
profile_use sets active context
delegate with no profile attaches active profile
query_peer creates query task
query_peer result is bounded structured JSON

End-to-end tests

Mac asks Windows disk_info under query → success
Mac asks Windows wallpaper change under query → denied
Mac asks Windows speedtest under query → denied
Mac asks Windows speedtest under query-network → success
Mac asks Windows repo edit under query → denied
Mac asks Windows repo edit under repo-dev lease → success

My recommended implementation sequence

Do it in this order:

Commit 1: profile/capability data model

Add internal/profile with built-in templates, validation, and TOML rendering. No runtime behavior yet.

Commit 2: config extension

Extend ProfileConfig, preserve old configs, add validation warnings/errors.

Commit 3: ProbeRunner

Add internal/runner/probe with disk_info, system_info, repo_status.

Commit 4: query task schema + RPC path

Add TaskKind=query, QueryRequest, worker execution via ProbeRunner.

Commit 5: query_peer MCP tool

Expose query through gateway. Keep prompt-based delegate unchanged.

Commit 6: profile generator CLI

Add cmesh profile templates/generate/validate/write.

Commit 7: profile generator MCP tools

Expose the same generator through setup stdio MCP and gateway MCP.

Commit 8: active profile context

Add profile_use, profile_current, profile_reset; have delegate inherit active profile.

Commit 9: onboarding update

Make generated configs default to query, with optional repo-dev.

Commit 10: shell/diagnostic profile

Only after deterministic query works, add ShellRunner and query-shell.

One important correction to make now

When you introduce query, stop telling users “start with readonly profiles.”

Change README/docs language to:

Start with query profiles.
Use readonly-agent only when deterministic probes are not enough.
Use dev only for repo-local changes.

readonly has become semantically overloaded. Your safe default is not readonly; it is query.

Final recommendation

You are in a good spot. The onboarding revamp is strong enough that I would not redesign it again. I would now build the permission system underneath it.

The north star for this next chunk:

The setup MCP should be able to generate a default query profile that is actually enforced by cmesh, not by model obedience.

Once that exists, your first-run story becomes much safer and much more compelling:

Install cmesh.
Open Claude/Codex.
Say “help me setup cmesh.”
Accept query-only default.
Ask a peer for disk/system/repo status.
No writes. No shell. No remote MCP. Audited.

That is the OSS-credible foundation.