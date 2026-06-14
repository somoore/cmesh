package setupstdio

// setupCmeshPromptName is the deterministic entry point: "help me setup cmesh"
// maps to this MCP prompt so the model is instructed to drive the wizard rather
// than guess.
const setupCmeshPromptName = "setup-cmesh"

// prompts returns the prompt descriptors advertised by prompts/list.
func prompts() []map[string]any {
	return []map[string]any{
		{
			"name":        setupCmeshPromptName,
			"description": "Set up cmesh on this machine and link it with another node. Drives the stateless onboarding wizard.",
			"arguments":   []map[string]any{},
		},
	}
}

// setupCmeshPromptMessages returns the prompts/get message payload for
// setup-cmesh. The instruction tells the model to call onboarding_status first
// and follow next_step, and makes the no-cross-node-session reality explicit so
// it never tells the user one node "resumes" the other's session.
func setupCmeshPromptMessages() []map[string]any {
	text := "You are helping the user set up cmesh, a peer-to-peer mesh for delegating " +
		"coding tasks between their machines.\n\n" +
		"Drive the stateless onboarding wizard. ALWAYS call the onboarding_status tool FIRST, " +
		"then follow its next_step exactly. onboarding_status re-derives progress from the " +
		"environment on every call, so it is safe to call repeatedly and after any change.\n\n" +
		"State-changing tools (configure_node, start_service, link_peer, register_gateway) require " +
		"the user's consent: call them once to preview, then again with confirmed=true after the " +
		"user agrees. Never set confirmed=true without explicit user approval.\n\n" +
		"The happy path on the origin node is a chain, each tool's next_step pointing to the next: " +
		"configure_node -> start_service -> link_peer -> (onboarding_status shows 'ready' once the " +
		"bidirectional smoke passes — or, for an origin-only node, once the peer is linked and reachable) " +
		"-> register_gateway. Always follow next_step. When " +
		"onboarding_status reports 'ready', its result already carries the payoff — the example prompts " +
		"(templated with the real peer name) and the per-peer default profile/profile menu — so surface " +
		"those to the user from the 'ready' result. register_gateway is the step that switches this agent " +
		"onto the cmesh HTTP delegation gateway, which requires ONE session restart — after that the stdio " +
		"setup MCP and onboarding_status are gone and delegation runs over the gateway. Do not skip " +
		"register_gateway: it is the only place that completes the swap.\n\n" +
		"IMPORTANT: there is NO cross-node session. cmesh cannot \"resume\" one machine's session on " +
		"another. Each node configures ITSELF to trust and reach the other; cmesh discovers the rest " +
		"over the tailnet. To bring up a second machine, the user installs cmesh there and runs " +
		"\"continue helping me configure cmesh for this new node\" on that machine, where this same " +
		"wizard runs independently and derives its own next step."
	return []map[string]any{
		{
			"role": "user",
			"content": map[string]any{
				"type": "text",
				"text": text,
			},
		},
	}
}
