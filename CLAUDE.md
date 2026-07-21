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
