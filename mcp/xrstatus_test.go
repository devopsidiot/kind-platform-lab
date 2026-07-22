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

// discoveryLists mirrors what ServerPreferredResources returns for the API
// surface these tests exercise.
var discoveryLists = []*metav1.APIResourceList{
	{
		GroupVersion: "v1",
		APIResources: []metav1.APIResource{
			{Name: "namespaces", Kind: "Namespace", Namespaced: false},
			{Name: "namespaces/status", Kind: "Namespace", Namespaced: false},
			{Name: "resourcequotas", Kind: "ResourceQuota", Namespaced: true},
		},
	},
	{
		GroupVersion: "platform.devopsidiot.io/v1alpha1",
		APIResources: []metav1.APIResource{
			{Name: "xappenvironments", Kind: "XAppEnvironment", Namespaced: false},
		},
	},
}

func TestResolveKind(t *testing.T) {
	cases := map[string]struct {
		kind       string
		apiVersion string
		want       []resolvedKind
	}{
		"ExactMatch": {
			kind: "XAppEnvironment",
			want: []resolvedKind{{
				gvr: schema.GroupVersionResource{
					Group: "platform.devopsidiot.io", Version: "v1alpha1",
					Resource: "xappenvironments",
				},
			}},
		},
		"CaseInsensitive": {
			kind: "namespace",
			want: []resolvedKind{{
				gvr: schema.GroupVersionResource{Version: "v1", Resource: "namespaces"},
			}},
		},
		"NamespacedResource": {
			kind: "ResourceQuota",
			want: []resolvedKind{{
				gvr:        schema.GroupVersionResource{Version: "v1", Resource: "resourcequotas"},
				namespaced: true,
			}},
		},
		"WrongAPIVersionFiltersOut": {
			kind:       "XAppEnvironment",
			apiVersion: "example.org/v1",
			want:       nil,
		},
		"UnknownKind": {
			kind: "Doesnotexist",
			want: nil,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := resolveKind(discoveryLists, tc.kind, tc.apiVersion)
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(resolvedKind{})); diff != "" {
				t.Errorf("resolveKind(): -want, +got:\n%s", diff)
			}
		})
	}
}

func TestXRStatus(t *testing.T) {
	xr := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "platform.devopsidiot.io/v1alpha1",
		"kind":       "XAppEnvironment",
		"metadata":   map[string]any{"name": "checkout"},
		"spec": map[string]any{
			"appName":     "checkout",
			"environment": "staging",
			"crossplane": map[string]any{
				"resourceRefs": []any{
					map[string]any{
						"apiVersion": "v1",
						"kind":       "Namespace",
						"name":       "checkout-staging",
					},
					map[string]any{
						"apiVersion": "v1",
						"kind":       "ResourceQuota",
						"name":       "quota",
						"namespace":  "checkout-staging",
					},
				},
			},
		},
		"status": map[string]any{
			"conditions": []any{
				map[string]any{
					"type": "Synced", "status": "True", "reason": "ReconcileSuccess",
				},
				map[string]any{
					"type": "Ready", "status": "True", "reason": "Available",
				},
			},
		},
	}}
	ns := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata":   map[string]any{"name": "checkout-staging"},
	}}
	// The ResourceQuota is deliberately absent: its lookup failure must be
	// recorded in the result, not fail the call.

	scheme := runtime.NewScheme()
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{
			{Group: "platform.devopsidiot.io", Version: "v1alpha1", Resource: "xappenvironments"}: "XAppEnvironmentList",
			{Version: "v1", Resource: "namespaces"}:                                               "NamespaceList",
			{Version: "v1", Resource: "resourcequotas"}:                                           "ResourceQuotaList",
		},
		xr, ns,
	)
	insp := &inspector{
		client: client,
		log:    log.New(io.Discard, "", 0),
	}

	got, err := insp.xrStatus(context.Background(), discoveryLists,
		"XAppEnvironment", "", "checkout", "")
	if err != nil {
		t.Fatalf("xrStatus(): unexpected error: %v", err)
	}

	want := &xrStatus{
		APIVersion: "platform.devopsidiot.io/v1alpha1",
		Kind:       "XAppEnvironment",
		Name:       "checkout",
		Synced:     &condition{Type: "Synced", Status: "True", Reason: "ReconcileSuccess"},
		Ready:      &condition{Type: "Ready", Status: "True", Reason: "Available"},
		Conditions: []condition{
			{Type: "Synced", Status: "True", Reason: "ReconcileSuccess"},
			{Type: "Ready", Status: "True", Reason: "Available"},
		},
		ComposedResources: []composedResource{
			{
				APIVersion: "v1", Kind: "Namespace", Name: "checkout-staging",
				Found: true, Conditions: []condition{},
			},
			{
				APIVersion: "v1", Kind: "ResourceQuota", Name: "quota",
				Namespace: "checkout-staging", Found: false,
				Error:      `resourcequotas "quota" not found`,
				Conditions: []condition{},
			},
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("xrStatus(): -want, +got:\n%s", diff)
	}
}

func TestXRStatusUnknownKind(t *testing.T) {
	insp := &inspector{
		client: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
		log:    log.New(io.Discard, "", 0),
	}
	_, err := insp.xrStatus(context.Background(), discoveryLists,
		"Doesnotexist", "", "x", "")
	if err == nil {
		t.Fatal("xrStatus(): expected an error for an unknown kind, got nil")
	}
}
