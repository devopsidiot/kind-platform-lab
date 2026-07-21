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

WAIT_TIMEOUT := 5m

.PHONY: cluster crossplane clean

## cluster: create the kind cluster if it does not already exist
cluster:
	@if kind get clusters 2>/dev/null | grep -qx '$(CLUSTER_NAME)'; then \
		echo "kind cluster '$(CLUSTER_NAME)' already exists"; \
	else \
		kind create cluster --config $(KIND_CONFIG); \
	fi
	@$(KUBECTL) cluster-info

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

## clean: delete the kind cluster
clean:
	kind delete cluster --name $(CLUSTER_NAME)
