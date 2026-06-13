# Tailscale Grants for cmesh

> Experimental: Tailscale grants are defense in depth for trusted private networks. They do not make broad worker profiles safe, and they do not replace narrow cmesh policy.

cmesh uses Tailscale for encrypted peer connectivity and authenticated node identity. Tailscale grants are defense in depth. The cmesh daemon still performs task authorization for every request.

## Required Network Shape

Each cmesh node runs one daemon with two surfaces:

- Local MCP gateway on loopback only, default port 7777.
- Mesh RPC on the node's Tailscale IP only, default port 7778.

Never expose the MCP gateway through Tailscale Serve, Funnel, cloudflared, nginx, or another proxy.

## Recommended Tags

Use a dedicated tag for devices allowed to run cmesh:

```text
tag:cmesh
```

For snapshot or risky workers, use a narrower tag:

```text
tag:cmesh-snapshot
```

Tags do not authorize tasks. They only make network admission easier to reason about.

## Minimal Grant

This grant lets tagged cmesh nodes reach the mesh RPC port on other tagged cmesh nodes:

```json
{
  "grants": [
    {
      "src": ["tag:cmesh"],
      "dst": ["tag:cmesh"],
      "ip": ["tcp:7778"]
    }
  ]
}
```

## Snapshot Worker Grant

This allows normal cmesh nodes to reach snapshot workers, while keeping the network rule explicit:

```json
{
  "grants": [
    {
      "src": ["tag:cmesh"],
      "dst": ["tag:cmesh-snapshot"],
      "ip": ["tcp:7778"]
    },
    {
      "src": ["tag:cmesh-snapshot"],
      "dst": ["tag:cmesh"],
      "ip": ["tcp:7778"]
    }
  ]
}
```

The worker daemon must still restrict risky profiles to allowed peers and allowed cwds.

## Operator Checks

On every node:

```bash
tailscale status
cmesh doctor
```

Expected cmesh doctor checks:

```text
config                   ok
tailscaled               ok
localapi                 ok
tailnet_ip               ok
whois_self               ok
bind_tailnet_ip          ok
```

The service account running cmesh must be able to query Tailscale LocalAPI. Interactive Tailscale success does not prove daemon access.

## Security Doctrine

Tailscale answers who connected. cmesh decides what that peer may do.

A request must pass all of these layers:

1. Tailscale grant permits source to destination on tcp:7778.
2. Source address is a tailnet address.
3. LocalAPI whois resolves the peer.
4. cmesh inbound policy allows the peer.
5. cmesh cwd policy allows the normalized working directory.
6. cmesh profile policy allows the requested runner/profile.
7. Runner manifest controls match the effective profile.

Any failure is denied and audited.

## Legacy ACL Form

If a tailnet still uses legacy ACLs, the equivalent network rule is:

```json
{
  "acls": [
    {
      "action": "accept",
      "src": ["tag:cmesh"],
      "dst": ["tag:cmesh:7778"]
    }
  ]
}
```

Use grants where available.
