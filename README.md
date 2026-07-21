# kind-platform-lab

A self-contained [Crossplane](https://crossplane.io) platform demo that runs
entirely on a local [kind](https://kind.sigs.k8s.io) cluster. `make demo` takes
a machine from nothing to a working, tested control plane.

Users ask for an *app environment*; the platform gives them a namespace with a
config map and a resource quota sized for that environment. The interesting
part is the middle: a custom Go composition function decides what to compose.

```
  XAppEnvironment (XR)
  ────────────────────
  spec:
    appName: checkout
    environment: staging
            │
            ▼
  ┌──────────────────────┐
  │ XRD                  │  rejects unknown environments and
  │ (schema validation)  │  missing fields at admission
  └──────────┬───────────┘
             ▼
  ┌──────────────────────┐
  │ Composition          │  mode: Pipeline
  └──────────┬───────────┘
             │
     ┌───────┴────────────────────┐
     ▼                            ▼
  ┌───────────────────┐   ┌────────────────────┐
  │ function-app-     │   │ function-auto-     │
  │ environment       │   │ ready              │
  │ (Go, this repo)   │   │ (crossplane-contrib)
  └─────────┬─────────┘   └────────────────────┘
            │
            │ looks up the tier for spec.environment
            ▼
  ┌────────────────────────────────────────────────┐
  │ Namespace      checkout-staging                │
  │ ConfigMap      checkout-config                 │
  │ ResourceQuota  compute-quota                   │
  │                                                │
  │   sandbox       2 cpu /  4Gi /  10 pods        │
  │   staging       8 cpu / 16Gi /  50 pods        │
  │   production   32 cpu / 64Gi / 200 pods        │
  └────────────────────────────────────────────────┘
```

## Prerequisites

Install these before running anything. Versions are what this was developed
and verified against; nearby versions are likely fine.

| Tool | Verified with | Notes |
| --- | --- | --- |
| Docker | 29.6.2 | must be **running** — kind talks to the daemon |
| Go | 1.26.5 | builds the function and installs Chainsaw |
| kind | v0.32.0 | creates the cluster |
| kubectl | v1.36.2 | |
| Helm | v4.2.3 | installs Crossplane |
| crossplane CLI | v2.2.0 | builds and pushes the function package |

On macOS:

```zsh
brew install go kind kubectl helm crossplane
```

Chainsaw (the e2e test runner) is **not** a prerequisite — the Makefile
installs it into `$(go env GOPATH)/bin` on first use.

## Quick start

```zsh
make demo
```

That creates the cluster, installs Crossplane, builds and loads the function,
applies the platform API, and runs both the unit and end-to-end tests. Expect
it to take a few minutes on a cold machine, mostly pulling images.

Then try it yourself (`k` is the usual `kubectl` alias; `kind` sets your current
context, so no `--context` flag is needed):

```zsh
k apply -f examples/appenvironment.yaml

k get xappenvironment
# NAME       SYNCED   READY   COMPOSITION                                AGE
# checkout   True     True    xappenvironments.platform.devopsidiot.io   30s

k get ns -l platform.devopsidiot.io/app=checkout
k get configmap,resourcequota -A -l platform.devopsidiot.io/app=checkout
```

Set `environment` to something outside the enum and the API server rejects it
before the function ever runs.

`appName` and `environment` are **immutable**. They determine the composed
namespace's name, and a namespace cannot be renamed, so editing either is
rejected — see [Things that surprised us](#things-that-surprised-us). To move
an app to another tier, create a second XR and delete the first.

Tear everything down with `make clean`.

## Make targets

| Target | Does |
| --- | --- |
| `make cluster` | create the kind cluster |
| `make registry` | run the local OCI registry on the kind network |
| `make crossplane` | install Crossplane and `function-auto-ready` |
| `make build` | build the function image and package, publish both to the cluster |
| `make deploy` | apply RBAC, the Function, the XRD and the Composition |
| `make test` | Go unit tests |
| `make e2e` | Chainsaw end-to-end tests |
| `make demo` | all of the above, in order |
| `make clean` | delete the cluster, the registry and `build/` |

Every target is idempotent and safe to re-run. Targets depend on what they
need, so `make e2e` on a clean machine will build the cluster first.

## How the function reaches the cluster

This is the least obvious part of the repo, and the reason there is a registry
in a demo that claims to be self-contained.

Crossplane pulls function **packages** itself, from inside its own pod. It
never consults the node's image store, and it rejects image names that have no
registry host. So `kind load docker-image` on its own cannot deliver a
function — the package reference has to point at a real registry.

The kubelet is a *separate* consumer of the same image: it pulls the **runtime**
image via containerd when it starts the function pod.

Those two consumers are satisfied differently:

```
  make build
      │
      ├── docker build ─────────────► runtime image
      │
      ├── crossplane xpkg build ────► .xpkg  (runtime embedded)
      │
      ├── crossplane xpkg push ─────► registry.local:5000
      │                                        │
      │                                        └──► Crossplane pulls
      │                                             the package (HTTP)
      │
      └── kind load docker-image ───► node containerd
                                               │
                                               └──► kubelet uses the local
                                                    copy, imagePullPolicy:
                                                    IfNotPresent, no pull
```

The registry container is named `registry.local` deliberately.
`go-containerregistry`, which Crossplane pulls with, speaks plain HTTP to hosts
whose name ends in `.local` (and to localhost and loopback) but HTTPS to
everything else. Crossplane has no insecure-registry option — only
`--ca-bundle-path` — so that naming convention is what avoids having to
generate TLS certificates or patch containerd on the node.

## Repo layout

```
fn/                             the composition function (Go)
  fn.go                         RunFunction: reads the XR, composes three resources
  tiers.go                      environment -> ResourceQuota table
  main.go                       gRPC entrypoint
manifests/
  kind/cluster.yaml             kind cluster definition
  crossplane/functions.yaml     function-auto-ready
  platform/xrd.yaml             the XAppEnvironment API
  platform/composition.yaml     the pipeline
  platform/function.yaml        this repo's function + DeploymentRuntimeConfig
  platform/rbac.yaml            lets Crossplane manage core resources
package/crossplane.yaml         function package metadata
test/e2e/                       Chainsaw tests
examples/                       a sample XR
```

## Things that surprised us

Recorded because they cost real time, and because they are the parts most
likely to bite you if you change something.

**Claims are deprecated.** In `apiextensions.crossplane.io/v2` there is no
claim; the XR is applied directly.

**The XRD is cluster scoped.** A namespaced XR may not own cluster-scoped
resources, and a `Namespace` is one. Applying a namespaced XR that composes a
namespace fails with `cannot apply cluster scoped composed resource`.

**Changing the XRD scope needs a Crossplane restart.** The composite controller
caches the old scope and fails with `an empty namespace may not be set when a
resource name is provided`. No target handles this; `make clean` does.

**Crossplane has no access to core Kubernetes types by default.** Composing a
`Namespace` or `ResourceQuota` without `manifests/platform/rbac.yaml` fails
with the misleading `failed waiting for Informer to sync`.

**The function marks its own resources ready.** `function-auto-ready` keys off a
`Ready` status condition, and `Namespace`, `ConfigMap` and `ResourceQuota` never
publish one, so the XR would otherwise sit at `Ready=False` forever.

**The image tag does not change between builds.** A rebuilt function is not
picked up on its own, because the running pod already has something with that
tag. `make deploy` restarts the function deployment to compensate.

**`appName` and `environment` are immutable, and have to be.** Both feed the
composed namespace's name. Crossplane updates composed resources in place and
`metadata.name` cannot change, so editing either field used to leave the old
namespace untouched while its quota and labels were rewritten to the new tier —
a namespace called `checkout-staging` holding a production-sized quota, with the
XR still reporting `Synced=True` and `Ready=True`. The XRD now rejects the edit
with a CEL rule. If you make the namespace name independent of these fields, you
can relax that.
