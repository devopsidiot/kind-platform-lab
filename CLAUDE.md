# kind-platform-lab

A self-contained Crossplane platform demo that runs entirely on a local
kind cluster. `make demo` should take a machine with Docker and Go from
nothing to a working, tested Crossplane control plane.

## Goal

Demonstrate the full platform-engineering loop locally:
kind cluster -> Crossplane -> custom Go composition function -> XR claim
-> composed resources -> Chainsaw e2e validation.

## Stack

- kind (local Kubernetes)
- Crossplane v2 (control plane)
- Go + function-sdk-go (composition function)
- Chainsaw (end-to-end tests)
- Make (single-command entrypoints)

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

## Make targets (target state)

- `make cluster`    - create the kind cluster
- `make crossplane` - install Crossplane + required functions
- `make build`      - build and load the function image into kind
- `make deploy`     - apply XRD, Composition, Function
- `make test`       - Go unit tests
- `make e2e`        - Chainsaw end-to-end tests
- `make demo`       - all of the above, in order
- `make clean`      - delete the kind cluster
