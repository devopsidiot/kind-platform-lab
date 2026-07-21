package main

import (
	"sort"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"

	fnv1 "github.com/crossplane/function-sdk-go/proto/v1"
	"github.com/crossplane/function-sdk-go/resource"
)

// mustDecode decodes a composed resource from the response into out.
func mustDecode(t *testing.T, r *fnv1.Resource, out runtime.Object) {
	t.Helper()
	if r == nil {
		t.Fatalf("expected a composed resource to decode into %T, got none", out)
	}
	if err := resource.AsObject(r.GetResource(), out); err != nil {
		t.Fatalf("cannot decode composed resource into %T: %v", out, err)
	}
}

// keys returns the sorted keys of a desired resource map, for error messages.
func keys(m map[string]*fnv1.Resource) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
