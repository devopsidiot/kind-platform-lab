# MCP server for cluster inspection

A read-only [MCP](https://modelcontextprotocol.io) server that exposes this
cluster's Crossplane state as tools an MCP client (Claude Desktop) can call.
Claude Desktop spawns the binary as a child process and speaks JSON-RPC over
stdin/stdout; all logging goes to stderr, because anything on stdout would
corrupt the protocol stream.

The server reuses the current kubeconfig context via client-go. It manages no
credentials of its own: whatever `kubectl config current-context` points at is
what it inspects.

## Read-only, no exceptions

Every tool inspects; none mutate. There is no apply, no delete, no patch, no
scale anywhere in this package — the only cluster verbs used are get, list and
log streaming. This is an observability surface, not a control surface, and
the safety of handing it to a model rests on that.

## Build

```sh
make mcp-build   # builds ./build/mcp-server
```

## Register with Claude Desktop

One command:

```sh
make mcp-register   # builds the binary and merges it into the config below
```

It merges rather than overwrites, so existing servers and preferences
survive; the previous config is kept alongside as `.bak`. Restart Claude
Desktop afterwards.

To do the same by hand instead, edit the config file at:

```
~/Library/Application Support/Claude/claude_desktop_config.json
```

and add the server, pointing at the built binary. Adjust the repo path to
your checkout. Claude Desktop spawns servers with a minimal environment, so
PATH is set explicitly to where Homebrew installs kubectl; the server itself
talks to the cluster through client-go and `~/.kube/config`, but a resolvable
kubectl keeps the environment consistent with everything else in this repo.

```json
{
  "mcpServers": {
    "kind-platform-lab": {
      "command": "/Users/dan.shepard/repos/kind-platform-lab/build/mcp-server",
      "env": {
        "PATH": "/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin"
      }
    }
  }
}
```

Restart Claude Desktop after editing; the tools appear under the
`kind-platform-lab` server.

## Tools

All four tools return structured JSON for the model to reason over, not
pre-formatted prose. Failures that actually happen — XR not found, cluster
unreachable, function pod not running — come back as clear error messages the
model can relay; they never crash the server.

### get_xr_status

Sync/ready conditions of a composite resource plus the health of every
resource it composes.

Input:

```json
{ "kind": "XAppEnvironment", "name": "checkout" }
```

`apiVersion` (to disambiguate a kind that exists in several API groups) and
`namespace` (for namespaced XRs) are optional.

Output (trimmed):

```json
{
  "apiVersion": "platform.devopsidiot.io/v1alpha1",
  "kind": "XAppEnvironment",
  "name": "checkout",
  "synced": { "type": "Synced", "status": "True", "reason": "ReconcileSuccess" },
  "ready": { "type": "Ready", "status": "True", "reason": "Available" },
  "conditions": [
    { "type": "Synced", "status": "True", "reason": "ReconcileSuccess" },
    { "type": "PolicyCheck", "status": "False", "reason": "PolicyViolation" },
    { "type": "Ready", "status": "True", "reason": "Available" }
  ],
  "composedResources": [
    { "apiVersion": "v1", "kind": "Namespace", "name": "checkout-staging", "found": true, "conditions": [] },
    { "apiVersion": "v1", "kind": "ConfigMap", "name": "checkout-config", "namespace": "checkout-staging", "found": true, "conditions": [] },
    { "apiVersion": "v1", "kind": "ResourceQuota", "name": "compute-quota", "namespace": "checkout-staging", "found": true, "conditions": [] }
  ]
}
```

A composed resource that cannot be fetched is reported with `"found": false`
and an `error` field; the call still succeeds.

### get_composition_pipeline

The ordered pipeline steps of a Composition: which function each step calls
and its input, if any.

Input:

```json
{ "name": "xappenvironments.platform.devopsidiot.io" }
```

Output:

```json
{
  "name": "xappenvironments.platform.devopsidiot.io",
  "compositeTypeRef": {
    "apiVersion": "platform.devopsidiot.io/v1alpha1",
    "kind": "XAppEnvironment"
  },
  "mode": "Pipeline",
  "steps": [
    { "step": "compose-app-environment", "functionRef": "function-app-environment" },
    { "step": "auto-ready", "functionRef": "function-auto-ready" }
  ]
}
```

### list_policy_violations

Every XR carrying the advisory `PolicyCheck: False` Warning condition set by
the LLM policy check. Scans all XR kinds defined in the cluster; the
`scanned` block distinguishes "no violations" from "nothing to scan".

Input: none.

```json
{}
```

Output:

```json
{
  "violations": [
    {
      "apiVersion": "platform.devopsidiot.io/v1alpha1",
      "kind": "XAppEnvironment",
      "name": "checkout",
      "condition": {
        "type": "PolicyCheck",
        "status": "False",
        "reason": "PolicyViolation",
        "message": "Application name is not at least three characters"
      }
    }
  ],
  "scanned": { "kinds": 1, "xrs": 1 }
}
```

### get_function_logs

Tails the composition function's pods in `crossplane-system`, found by the
`pkg.crossplane.io/function` label.

Input (both fields optional):

```json
{ "function": "function-app-environment", "lines": 50 }
```

`function` defaults to `function-app-environment`; `lines` defaults to 100
and is capped at 500 so a single call cannot flood the model's context.

Output:

```json
{
  "function": "function-app-environment",
  "namespace": "crossplane-system",
  "tailLines": 50,
  "pods": [
    {
      "pod": "function-app-environment-899c98749495-789786dfc4-jj54b",
      "phase": "Running",
      "lines": ["..."]
    }
  ]
}
```

A pod whose logs cannot be read explains itself inline — for example
`"<pod is Pending, not Running; logs unavailable: ...>"` — rather than
failing the whole call.

## Smoke test without Claude Desktop

The server is plain JSON-RPC over stdio, so it can be exercised with a pipe:

```sh
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"list_policy_violations","arguments":{}}}' \
  | ./build/mcp-server
```
