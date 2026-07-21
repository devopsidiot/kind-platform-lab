# kind-platform-lab
#
# Every target is idempotent and safe to re-run.

SHELL := /usr/bin/env bash
.SHELLFLAGS := -euo pipefail -c

CLUSTER_NAME    := kind-platform-lab
KIND_CONFIG     := manifests/kind/cluster.yaml
KUBE_CONTEXT    := kind-$(CLUSTER_NAME)
KUBECTL         := kubectl --context $(KUBE_CONTEXT)

CROSSPLANE_NS      := crossplane-system
CROSSPLANE_CHART   := crossplane-stable/crossplane
CROSSPLANE_REPO    := https://charts.crossplane.io/stable
CROSSPLANE_VERSION := 2.3.3

# Crossplane pulls function *packages* itself, from inside its own pod, and
# rejects image names that lack a registry host. `kind load docker-image` alone
# therefore cannot satisfy it, so we run a registry on the kind network.
#
# The registry is named `registry.local` deliberately: go-containerregistry,
# which Crossplane uses to pull, speaks plain HTTP to hosts whose name ends in
# `.local` (and to localhost/loopback), but HTTPS to everything else. That is
# what lets this work without generating TLS certificates or patching the
# containerd config on the node.
#
# The kubelet is a separate problem: it pulls the runtime image via containerd,
# which has no such heuristic and would fail on HTTP. We sidestep that by
# loading the image into the node with `kind load docker-image` and setting
# imagePullPolicy: IfNotPresent (see manifests/platform/function.yaml), so the
# kubelet never pulls at all.
REGISTRY_NAME := registry.local
REGISTRY_PORT := 5000
# Port published on the host, for pushing from outside the cluster.
REGISTRY_HOST_PORT := 5001

FUNCTION_NAME    := function-app-environment
FUNCTION_VERSION := v0.1.0
# Must match spec.package in manifests/platform/function.yaml.
FUNCTION_IMAGE   := $(REGISTRY_NAME):$(REGISTRY_PORT)/$(FUNCTION_NAME):$(FUNCTION_VERSION)
FUNCTION_PUSH    := localhost:$(REGISTRY_HOST_PORT)/$(FUNCTION_NAME):$(FUNCTION_VERSION)
RUNTIME_IMAGE    := $(FUNCTION_NAME)-runtime:$(FUNCTION_VERSION)

BUILD_DIR := build
XPKG      := $(BUILD_DIR)/$(FUNCTION_NAME).xpkg

WAIT_TIMEOUT := 5m

.PHONY: cluster registry crossplane build deploy test demo clean

## cluster: create the kind cluster if it does not already exist
cluster:
	@if kind get clusters 2>/dev/null | grep -qx '$(CLUSTER_NAME)'; then \
		echo "kind cluster '$(CLUSTER_NAME)' already exists"; \
	else \
		kind create cluster --config $(KIND_CONFIG); \
	fi
	@$(KUBECTL) cluster-info

## registry: run a local OCI registry on the kind network
registry: cluster
	@if [ "$$(docker inspect -f '{{.State.Running}}' $(REGISTRY_NAME) 2>/dev/null)" = "true" ]; then \
		echo "registry '$(REGISTRY_NAME)' already running"; \
	else \
		docker rm -f $(REGISTRY_NAME) >/dev/null 2>&1 || true; \
		docker run -d --name $(REGISTRY_NAME) --network kind --restart=always \
			-p $(REGISTRY_HOST_PORT):$(REGISTRY_PORT) registry:2; \
	fi

## crossplane: install Crossplane and the functions it needs
crossplane: cluster
	helm repo add crossplane-stable $(CROSSPLANE_REPO)
	helm repo update crossplane-stable
	helm upgrade --install crossplane $(CROSSPLANE_CHART) \
		--kube-context $(KUBE_CONTEXT) \
		--namespace $(CROSSPLANE_NS) \
		--create-namespace \
		--version $(CROSSPLANE_VERSION) \
		--wait \
		--timeout $(WAIT_TIMEOUT)
	$(KUBECTL) wait --for=condition=Available \
		--namespace $(CROSSPLANE_NS) \
		--timeout $(WAIT_TIMEOUT) \
		deployment --all
	$(KUBECTL) apply -f manifests/crossplane/functions.yaml
	$(KUBECTL) wait --for=condition=Healthy \
		--timeout $(WAIT_TIMEOUT) \
		function.pkg.crossplane.io --all

## build: build the function package and make it available to the cluster
build: registry
	mkdir -p $(BUILD_DIR)
	docker build -t $(RUNTIME_IMAGE) .
	rm -f $(XPKG)
	crossplane xpkg build \
		--package-root=package \
		--embed-runtime-image=$(RUNTIME_IMAGE) \
		--package-file=$(XPKG)
	# Crossplane pulls the package from the registry...
	crossplane xpkg push --package-files=$(XPKG) $(FUNCTION_PUSH)
	# ...while the kubelet uses the copy loaded directly into the node.
	id=$$(docker load -i $(XPKG) | sed -n 's/^Loaded image ID: //p'); \
	if [ -z "$$id" ]; then echo "could not determine loaded image id" >&2; exit 1; fi; \
	docker tag "$$id" $(FUNCTION_IMAGE)
	kind load docker-image $(FUNCTION_IMAGE) --name $(CLUSTER_NAME)

## deploy: apply RBAC, the function, XRD and Composition
deploy: crossplane
	$(KUBECTL) apply -f manifests/platform/rbac.yaml
	$(KUBECTL) apply -f manifests/platform/function.yaml
	$(KUBECTL) wait --for=condition=Healthy \
		--timeout $(WAIT_TIMEOUT) \
		function.pkg.crossplane.io/$(FUNCTION_NAME)
	# The image tag does not change between builds, so a rebuilt function is not
	# picked up on its own: the pod is already running something with that tag.
	# Restart it so `make build && make deploy` actually deploys new code.
	@dep=$$($(KUBECTL) get deploy -n $(CROSSPLANE_NS) -o name 2>/dev/null \
		| grep '$(FUNCTION_NAME)-' || true); \
	if [ -n "$$dep" ]; then \
		echo "restarting $$dep to pick up a possibly rebuilt image"; \
		$(KUBECTL) rollout restart -n $(CROSSPLANE_NS) $$dep; \
		$(KUBECTL) rollout status -n $(CROSSPLANE_NS) $$dep --timeout=$(WAIT_TIMEOUT); \
	fi
	$(KUBECTL) apply -f manifests/platform/xrd.yaml
	$(KUBECTL) wait --for=condition=Established \
		--timeout $(WAIT_TIMEOUT) \
		xrd/xappenvironments.platform.devopsidiot.io
	$(KUBECTL) apply -f manifests/platform/composition.yaml

## test: Go unit tests
test:
	go test ./... -count=1

## demo: everything, in order
demo: cluster crossplane build deploy test

## clean: delete the kind cluster and the local registry
clean:
	kind delete cluster --name $(CLUSTER_NAME)
	docker rm -f $(REGISTRY_NAME) >/dev/null 2>&1 || true
	rm -rf $(BUILD_DIR)
