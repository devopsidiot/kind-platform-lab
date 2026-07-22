package main

import (
	"context"
	"io"
	"log"
	"testing"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// crossplaneDiscoveryLists extends the base test lists with the Crossplane
// API types the pipeline and violations tools resolve.
var crossplaneDiscoveryLists = append(discoveryLists, []*metav1.APIResourceList{
	{
		GroupVersion: "apiextensions.crossplane.io/v1",
		APIResources: []metav1.APIResource{
			{Name: "compositions", Kind: "Composition", Namespaced: false},
		},
	},
	{
		GroupVersion: "apiextensions.crossplane.io/v2",
		APIResources: []metav1.APIResource{
			{Name: "compositeresourcedefinitions", Kind: "CompositeResourceDefinition", Namespaced: false},
		},
	},
}...)

func TestCompositionPipeline(t *testing.T) {
	comp := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.crossplane.io/v1",
		"kind":       "Composition",
		"metadata":   map[string]any{"name": "xappenvironments.platform.devopsidiot.io"},
		"spec": map[string]any{
			"compositeTypeRef": map[string]any{
				"apiVersion": "platform.devopsidiot.io/v1alpha1",
				"kind":       "XAppEnvironment",
			},
			"mode": "Pipeline",
			"pipeline": []any{
				map[string]any{
					"step":        "compose-app-environment",
					"functionRef": map[string]any{"name": "function-app-environment"},
				},
				map[string]any{
					"step":        "auto-ready",
					"functionRef": map[string]any{"name": "function-auto-ready"},
					"input":       map[string]any{"apiVersion": "x/v1", "kind": "Input"},
				},
			},
		},
	}}

	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			{Group: "apiextensions.crossplane.io", Version: "v1", Resource: "compositions"}: "CompositionList",
		},
		comp,
	)
	insp := &inspector{client: client, log: log.New(io.Discard, "", 0)}

	got, err := insp.compositionPipeline(context.Background(), crossplaneDiscoveryLists,
		"xappenvironments.platform.devopsidiot.io")
	if err != nil {
		t.Fatalf("compositionPipeline(): unexpected error: %v", err)
	}

	want := &compositionPipeline{
		Name: "xappenvironments.platform.devopsidiot.io",
		CompositeTypeRef: map[string]any{
			"apiVersion": "platform.devopsidiot.io/v1alpha1",
			"kind":       "XAppEnvironment",
		},
		Mode: "Pipeline",
		Steps: []pipelineStep{
			{Step: "compose-app-environment", FunctionRef: "function-app-environment"},
			{Step: "auto-ready", FunctionRef: "function-auto-ready",
				Input: map[string]any{"apiVersion": "x/v1", "kind": "Input"}},
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("compositionPipeline(): -want, +got:\n%s", diff)
	}
}

func TestCompositionPipelineNotFound(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			{Group: "apiextensions.crossplane.io", Version: "v1", Resource: "compositions"}: "CompositionList",
		},
	)
	insp := &inspector{client: client, log: log.New(io.Discard, "", 0)}

	_, err := insp.compositionPipeline(context.Background(), crossplaneDiscoveryLists, "missing")
	if err == nil {
		t.Fatal("compositionPipeline(): expected an error for a missing composition, got nil")
	}
}
