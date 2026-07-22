package main

import (
	"context"
	"io"
	"log"
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestPolicyViolations(t *testing.T) {
	xrd := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.crossplane.io/v2",
		"kind":       "CompositeResourceDefinition",
		"metadata":   map[string]any{"name": "xappenvironments.platform.devopsidiot.io"},
		"spec": map[string]any{
			"group": "platform.devopsidiot.io",
			"names": map[string]any{"kind": "XAppEnvironment", "plural": "xappenvironments"},
		},
	}}
	violating := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "platform.devopsidiot.io/v1alpha1",
		"kind":       "XAppEnvironment",
		"metadata":   map[string]any{"name": "bad-app"},
		"status": map[string]any{
			"conditions": []any{
				map[string]any{
					"type": "PolicyCheck", "status": "False",
					"reason": "PolicyViolation", "message": "policy 1 broken; policy 2 broken",
				},
			},
			"policy": map[string]any{
				"compliant":  false,
				"hash":       "abc123",
				"violations": []any{"policy 1 broken", "policy 2 broken"},
			},
		},
	}}
	compliant := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "platform.devopsidiot.io/v1alpha1",
		"kind":       "XAppEnvironment",
		"metadata":   map[string]any{"name": "good-app"},
		"status": map[string]any{
			"conditions": []any{
				map[string]any{"type": "PolicyCheck", "status": "True", "reason": "PolicyCompliant"},
			},
		},
	}}
	// CheckUnavailable is Unknown, not False: the advisory Warning was never
	// raised, so this XR must not be reported.
	unavailable := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "platform.devopsidiot.io/v1alpha1",
		"kind":       "XAppEnvironment",
		"metadata":   map[string]any{"name": "unknown-app"},
		"status": map[string]any{
			"conditions": []any{
				map[string]any{"type": "PolicyCheck", "status": "Unknown", "reason": "CheckUnavailable"},
			},
		},
	}}

	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			{Group: "apiextensions.crossplane.io", Version: "v2", Resource: "compositeresourcedefinitions"}: "CompositeResourceDefinitionList",
			{Group: "platform.devopsidiot.io", Version: "v1alpha1", Resource: "xappenvironments"}:           "XAppEnvironmentList",
		},
		xrd, violating, compliant, unavailable,
	)
	insp := &inspector{client: client, log: log.New(io.Discard, "", 0)}

	got, err := insp.policyViolations(context.Background(), crossplaneDiscoveryLists)
	if err != nil {
		t.Fatalf("policyViolations(): unexpected error: %v", err)
	}

	want := &policyViolations{
		Violations: []policyViolation{{
			APIVersion: "platform.devopsidiot.io/v1alpha1",
			Kind:       "XAppEnvironment",
			Name:       "bad-app",
			Condition: condition{
				Type: "PolicyCheck", Status: "False",
				Reason: "PolicyViolation", Message: "policy 1 broken; policy 2 broken",
			},
			Violations: []string{"policy 1 broken", "policy 2 broken"},
		}},
	}
	want.Scanned.Kinds = 1
	want.Scanned.XRs = 3
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("policyViolations(): -want, +got:\n%s", diff)
	}
}

func TestPolicyViolationsNoXRDs(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			{Group: "apiextensions.crossplane.io", Version: "v2", Resource: "compositeresourcedefinitions"}: "CompositeResourceDefinitionList",
		},
	)
	insp := &inspector{client: client, log: log.New(io.Discard, "", 0)}

	got, err := insp.policyViolations(context.Background(), crossplaneDiscoveryLists)
	if err != nil {
		t.Fatalf("policyViolations(): unexpected error: %v", err)
	}
	if len(got.Violations) != 0 || got.Scanned.Kinds != 0 {
		t.Errorf("expected an empty result with no XRDs, got %+v", got)
	}
}
