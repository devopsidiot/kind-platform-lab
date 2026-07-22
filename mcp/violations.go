package main

import (
	"context"
	"fmt"
	"sort"

	"github.com/mark3labs/mcp-go/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The composition function records its advisory verdict as a PolicyCheck
// status condition and caches the verdict, violations included, under
// status.policy; see fn/policy.go. Condition status False is the advisory
// Warning this tool looks for.
const policyConditionType = "PolicyCheck"

// policyViolation is one XR carrying the advisory Warning condition.
type policyViolation struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace,omitempty"`
	// Condition is the full PolicyCheck condition, message included.
	Condition condition `json:"condition"`
	// Violations is the per-policy breakdown from the XR's status.policy cache.
	Violations []string `json:"violations,omitempty"`
}

// policyViolations is the structured result of list_policy_violations.
// Scanned records how wide the search was, so an empty result is
// distinguishable from a search that found nothing to scan.
type policyViolations struct {
	Violations []policyViolation `json:"violations"`
	Scanned    struct {
		Kinds int `json:"kinds"`
		XRs   int `json:"xrs"`
	} `json:"scanned"`
}

func policyViolationsTool() mcp.Tool {
	return mcp.NewTool("list_policy_violations",
		mcp.WithDescription("List every Crossplane composite resource carrying the "+
			"advisory PolicyCheck Warning condition set by the policy check. Scans "+
			"all XR kinds defined in the cluster. Returns structured JSON."),
	)
}

func (i *inspector) handleListPolicyViolations(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	lists, err := i.resources()
	if err != nil && len(lists) == 0 {
		return mcp.NewToolResultErrorf("discover API resources: %v", clusterError(err)), nil
	}

	result, err := i.policyViolations(ctx, lists)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultJSON(result)
}

// policyViolations lists every XR of every XRD-defined kind and returns those
// whose PolicyCheck condition is False.
func (i *inspector) policyViolations(ctx context.Context, lists []*metav1.APIResourceList) (*policyViolations, error) {
	matches := resolveKind(lists, "CompositeResourceDefinition", "")
	if len(matches) != 1 {
		return nil, fmt.Errorf("cannot resolve kind CompositeResourceDefinition to exactly one API resource (got %d matches)", len(matches))
	}

	xrds, err := i.client.Resource(matches[0].gvr).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list XRDs: %w", clusterError(err))
	}

	result := &policyViolations{Violations: []policyViolation{}}
	for _, xrd := range xrds.Items {
		xrKind, _, _ := unstructured.NestedString(xrd.Object, "spec", "names", "kind")
		xrGroup, _, _ := unstructured.NestedString(xrd.Object, "spec", "group")
		if xrKind == "" {
			continue
		}

		// Resolve through discovery rather than the XRD's version list, so we
		// query the served, preferred version. An XRD not yet established has
		// no discoverable resource; skip it.
		xrMatches := resolveKind(lists, xrKind, "")
		var m *resolvedKind
		for idx := range xrMatches {
			if xrMatches[idx].gvr.Group == xrGroup {
				m = &xrMatches[idx]
				break
			}
		}
		if m == nil {
			i.log.Printf("skipping XRD %s: kind %s not discoverable", xrd.GetName(), xrKind)
			continue
		}
		result.Scanned.Kinds++

		// An unnamespaced List on a namespaced resource returns all namespaces.
		xrs, err := i.client.Resource(m.gvr).List(ctx, metav1.ListOptions{})
		if err != nil {
			i.log.Printf("skipping XRD %s: list %s: %v", xrd.GetName(), m.gvr.Resource, err)
			continue
		}

		for idx := range xrs.Items {
			xr := &xrs.Items[idx]
			result.Scanned.XRs++
			c := findCondition(conditionsOf(xr), policyConditionType)
			if c == nil || c.Status != "False" {
				continue
			}
			v := policyViolation{
				APIVersion: xr.GetAPIVersion(),
				Kind:       xr.GetKind(),
				Name:       xr.GetName(),
				Namespace:  xr.GetNamespace(),
				Condition:  *c,
			}
			if vs, found, _ := unstructured.NestedStringSlice(xr.Object, "status", "policy", "violations"); found {
				v.Violations = vs
			}
			result.Violations = append(result.Violations, v)
		}
	}

	sort.Slice(result.Violations, func(a, b int) bool {
		va, vb := result.Violations[a], result.Violations[b]
		if va.Kind != vb.Kind {
			return va.Kind < vb.Kind
		}
		if va.Namespace != vb.Namespace {
			return va.Namespace < vb.Namespace
		}
		return va.Name < vb.Name
	})
	return result, nil
}
