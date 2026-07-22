# kind-platform-lab

A self-contained Crossplane platform demo that runs entirely on a local
kind cluster. `make demo` takes a machine with the prerequisites below
from nothing to a working, tested Crossplane control plane.

## Goal

Demonstrate the full platform-engineering loop locally:
kind cluster -> Crossplane -> custom Go composition function -> XR
-> composed resources -> Chainsaw e2e validation.

Claims are deprecated in apiextensions.crossplane.io/v2, so the XR is
applied directly. There is no claim in this repo.

## Stack

- kind (local Kubernetes)
- Crossplane v2 (control plane)
- Go + function-sdk-go (composition function)
- Chainsaw (end-to-end tests)
- Make (single-command entrypoints)

## Prerequisites

Six tools must already be installed; the Makefile does not bootstrap
them. Versions are what the demo was verified against.

- Docker 29.6.2 - must be running; kind talks to the daemon
- Go 1.26.5 - builds the function, installs Chainsaw
- kind v0.32.0 - creates the cluster
- kubectl v1.36.2
- Helm v4.2.3 - installs Crossplane
- crossplane CLI v2.2.0 - builds and pushes the function package

`brew install go kind kubectl helm crossplane`

Chainsaw is the exception: `make e2e` installs it into
`$(go env GOPATH)/bin` on first use, so it is not a prerequisite.

## Repo conventions

- Go module path: github.com/devopsidiot/kind-platform-lab
- All Go code lives under ./fn (function) and ./internal
- Kubernetes manifests under ./manifests, grouped by purpose
- Chainsaw tests under ./test/e2e/<scenario>/
- Makefile is the only supported entrypoint; every target must be
  idempotent and safe to re-run
- Alphabetize fields in XR specs; drop empty or unnecessary fields
- Prefer explicit over clever: this repo is read by humans evaluating it

## Shell and tooling

- I use zsh. Never emit fish syntax.
- Use `k` as the kubectl alias in any shell examples.

## Working style

- Write tests alongside code, not after
- Do not hand-edit generated or rendered manifests; change the source
- When a command fails, show me the actual error before proposing a fix
- Print full files rather than diffs when I ask to see code
- Do not add dependencies without telling me why

## Make targets

- `make cluster`    - create the kind cluster
- `make registry`   - run the local OCI registry on the kind network
- `make crossplane` - install Crossplane + function-auto-ready
- `make build`      - build the function image and package, push the
                      package to the registry and load the image into kind
- `make deploy`     - apply RBAC, Function, XRD, Composition
- `make test`       - Go unit tests
- `make e2e`        - Chainsaw end-to-end tests
- `make demo`       - all of the above, in order
- `make clean`      - delete the cluster, the registry and build/

Targets declare their own dependencies, so any one of them bootstraps
what it needs. `make build` needs a registry because Crossplane pulls
function packages itself and rejects image names without a registry
host; `kind load docker-image` alone cannot satisfy it. See the README
for why the registry is named `registry.local`.

## Phase 2: LLM-backed policy validation

Extend the existing composition function with an advisory policy check
backed by a local LLM running in-cluster.

### Design constraints (these are decisions, not suggestions)

- **Advisory, never blocking.** A policy violation sets a Warning status
  condition and annotates the XR. It never fails the composition and never
  prevents resources from being composed. LLM output is non-deterministic;
  non-determinism does not belong in an admission path.
- **Fail open.** If the LLM is unreachable, slow, or returns unparseable
  output, log it and compose normally. An unavailable model must never
  break provisioning.
- **Hard timeout.** The LLM call gets a short context deadline. Composition
  functions run inside a reconcile loop; a slow call stalls reconciliation
  for every XR, not just this one.
- **Do not re-check on every reconcile.** Cache the verdict under
  `status.policy`, keyed by a hash of the policy-relevant spec fields. Only
  call the LLM when that hash changes. Reconciles are frequent; LLM calls are
  not free. The cache must live in status, not annotations: Crossplane's
  composite reconciler persists only the XR's status after the function
  pipeline, so metadata written to the desired composite is silently dropped.
- **Tests never call a real model.** Unit and e2e tests use a fake LLM
  client with fixed responses. A test suite whose outcome depends on model
  sampling is not a test suite.

### Components

- Ollama Deployment, Service, ConfigMap and PVC in the cluster, model
  pulled by an init container
- A policy client in ./internal/policy with an interface the function
  depends on, so tests can substitute a fake
- Policies expressed as natural language strings in a ConfigMap, read by
  the function at reconcile time

## Phase 3: MCP server for cluster inspection

A read-only MCP server under ./mcp that exposes this cluster's Crossplane
state as tools an MCP client (Claude Desktop) can call.

### Design constraints

- **Read-only. No exceptions.** Every tool inspects; none mutate. No apply,
  no delete, no patch, no scale. This is an observability surface, not a
  control surface. The safety of the whole thing rests on this.
- **stdio transport.** Claude Desktop spawns the binary as a child process
  and talks over stdin/stdout. All logging goes to stderr — anything on
  stdout corrupts the JSON-RPC stream.
- **Reuses the cluster's kubeconfig.** The server shells out to kubectl or
  uses client-go against the current context. It does not manage its own
  credentials.
- **Structured returns.** Tools return JSON the model can reason over, not
  pre-formatted prose. Let Claude do the summarizing.
- **Bounded output.** Log and describe tools cap what they return (tail N
  lines, last N events) so a single call can't flood the context.

### Tools (mirror the cluster-diagnostician subagent)

- get_xr_status         - XR sync/ready conditions + composed resource health
- get_composition_pipeline - the pipeline steps for a given composition
- list_policy_violations - XRs carrying the advisory Warning condition
- get_function_logs     - tail logs from the composition function pod

### Stack

- Go, github.com/mark3labs/mcp-go
- client-go or shelling out to kubectl for cluster queries
