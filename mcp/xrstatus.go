package main

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
)

// callTimeout bounds every cluster round-trip so a slow API server cannot
// stall the MCP client indefinitely.
const callTimeout = 10 * time.Second

// inspector holds the cluster clients the tools query. resources is a
// function rather than a discovery client so tests can substitute a fixed
// resource list.
type inspector struct {
	client    dynamic.Interface
	core      kubernetes.Interface
	resources func() ([]*metav1.APIResourceList, error)
	log       *log.Logger
}

// condition is the subset of a Kubernetes status condition the model needs
// to reason about health.
type condition struct {
	Type               string `json:"type"`
	Status             string `json:"status"`
	Reason             string `json:"reason,omitempty"`
	Message            string `json:"message,omitempty"`
	LastTransitionTime string `json:"lastTransitionTime,omitempty"`
}

// composedResource reports the health of one resource composed by the XR.
// Error records a per-resource lookup failure without failing the whole call.
type composedResource struct {
	APIVersion string      `json:"apiVersion"`
	Kind       string      `json:"kind"`
	Name       string      `json:"name"`
	Namespace  string      `json:"namespace,omitempty"`
	Found      bool        `json:"found"`
	Error      string      `json:"error,omitempty"`
	Conditions []condition `json:"conditions"`
}

// xrStatus is the structured result of get_xr_status.
type xrStatus struct {
	APIVersion        string             `json:"apiVersion"`
	Kind              string             `json:"kind"`
	Name              string             `json:"name"`
	Synced            *condition         `json:"synced"`
	Ready             *condition         `json:"ready"`
	Conditions        []condition        `json:"conditions"`
	ComposedResources []composedResource `json:"composedResources"`
}

func xrStatusTool() mcp.Tool {
	return mcp.NewTool("get_xr_status",
		mcp.WithDescription("Get a Crossplane composite resource's Synced and "+
			"Ready conditions plus the health of every resource it composes. "+
			"Returns structured JSON."),
		mcp.WithString("kind", mcp.Required(),
			mcp.Description("Kind of the XR, e.g. XAppEnvironment")),
		mcp.WithString("name", mcp.Required(),
			mcp.Description("Name of the XR")),
		mcp.WithString("apiVersion",
			mcp.Description("Optional apiVersion (group/version) to disambiguate "+
				"when the same kind exists in more than one API group")),
		mcp.WithString("namespace",
			mcp.Description("Namespace of the XR; required only for namespaced XRs")),
	)
}

func (i *inspector) handleGetXRStatus(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	kind, err := req.RequireString("kind")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	name, err := req.RequireString("name")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	apiVersion := req.GetString("apiVersion", "")
	namespace := req.GetString("namespace", "")

	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	lists, err := i.resources()
	if err != nil {
		// Discovery can fail partially (e.g. one stale APIService) while still
		// returning usable lists. Only give up when we got nothing at all.
		if len(lists) == 0 {
			return mcp.NewToolResultErrorf("discover API resources: %v", clusterError(err)), nil
		}
		i.log.Printf("partial discovery failure, continuing: %v", err)
	}

	status, err := i.xrStatus(ctx, lists, kind, apiVersion, name, namespace)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultJSON(status)
}

// xrStatus fetches the XR and every resource it references, returning their
// conditions. Per-composed-resource lookup failures are recorded in the
// result rather than failing the call.
func (i *inspector) xrStatus(ctx context.Context, lists []*metav1.APIResourceList, kind, apiVersion, name, namespace string) (*xrStatus, error) {
	matches := resolveKind(lists, kind, apiVersion)
	if len(matches) == 0 {
		return nil, fmt.Errorf("no API resource with kind %q found in the cluster", kind)
	}
	if len(matches) > 1 {
		versions := make([]string, 0, len(matches))
		for _, m := range matches {
			versions = append(versions, m.gvr.GroupVersion().String())
		}
		sort.Strings(versions)
		return nil, fmt.Errorf("kind %q is ambiguous; pass apiVersion as one of: %s",
			kind, strings.Join(versions, ", "))
	}
	m := matches[0]

	var ri dynamic.ResourceInterface = i.client.Resource(m.gvr)
	if m.namespaced {
		if namespace == "" {
			return nil, fmt.Errorf("kind %q is namespaced; pass namespace", kind)
		}
		ri = i.client.Resource(m.gvr).Namespace(namespace)
	}
	xr, err := ri.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("%s %q not found in the cluster; check the name, "+
			"or list resources of that kind to see what exists", kind, name)
	}
	if err != nil {
		return nil, fmt.Errorf("get %s %q: %w", kind, name, clusterError(err))
	}

	conds := conditionsOf(xr)
	status := &xrStatus{
		APIVersion:        xr.GetAPIVersion(),
		Kind:              xr.GetKind(),
		Name:              xr.GetName(),
		Synced:            findCondition(conds, "Synced"),
		Ready:             findCondition(conds, "Ready"),
		Conditions:        conds,
		ComposedResources: []composedResource{},
	}

	for _, ref := range resourceRefs(xr) {
		status.ComposedResources = append(status.ComposedResources,
			i.composedStatus(ctx, lists, ref))
	}
	return status, nil
}

// resourceRef identifies one composed resource referenced by an XR.
type resourceRef struct {
	apiVersion string
	kind       string
	name       string
	namespace  string
}

// resourceRefs reads the XR's composed-resource references. Crossplane v2
// keeps them under spec.crossplane.resourceRefs; v1-style XRs used
// spec.resourceRefs, kept as a fallback.
func resourceRefs(xr *unstructured.Unstructured) []resourceRef {
	raw, found, _ := unstructured.NestedSlice(xr.Object, "spec", "crossplane", "resourceRefs")
	if !found {
		raw, _, _ = unstructured.NestedSlice(xr.Object, "spec", "resourceRefs")
	}
	refs := make([]resourceRef, 0, len(raw))
	for _, r := range raw {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		str := func(k string) string { s, _ := m[k].(string); return s }
		refs = append(refs, resourceRef{
			apiVersion: str("apiVersion"),
			kind:       str("kind"),
			name:       str("name"),
			namespace:  str("namespace"),
		})
	}
	return refs
}

// composedStatus fetches one composed resource and reports its conditions.
func (i *inspector) composedStatus(ctx context.Context, lists []*metav1.APIResourceList, ref resourceRef) composedResource {
	out := composedResource{
		APIVersion: ref.apiVersion,
		Kind:       ref.kind,
		Name:       ref.name,
		Namespace:  ref.namespace,
		Conditions: []condition{},
	}

	matches := resolveKind(lists, ref.kind, ref.apiVersion)
	if len(matches) != 1 {
		out.Error = fmt.Sprintf("cannot resolve %s/%s to an API resource", ref.apiVersion, ref.kind)
		return out
	}
	m := matches[0]

	var ri dynamic.ResourceInterface = i.client.Resource(m.gvr)
	if m.namespaced {
		if ref.namespace == "" {
			out.Error = "resource is namespaced but the reference has no namespace"
			return out
		}
		ri = i.client.Resource(m.gvr).Namespace(ref.namespace)
	}

	u, err := ri.Get(ctx, ref.name, metav1.GetOptions{})
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.Found = true
	out.Conditions = conditionsOf(u)
	return out
}

// resolvedKind is one discovery match for a kind.
type resolvedKind struct {
	gvr        schema.GroupVersionResource
	namespaced bool
}

// resolveKind finds the API resources whose kind matches, optionally filtered
// by apiVersion (group/version). Kind matching is case-insensitive;
// subresources (names containing "/") are skipped.
func resolveKind(lists []*metav1.APIResourceList, kind, apiVersion string) []resolvedKind {
	var out []resolvedKind
	for _, list := range lists {
		if list == nil {
			continue
		}
		if apiVersion != "" && list.GroupVersion != apiVersion {
			continue
		}
		gv, err := schema.ParseGroupVersion(list.GroupVersion)
		if err != nil {
			continue
		}
		for _, r := range list.APIResources {
			if strings.Contains(r.Name, "/") || !strings.EqualFold(r.Kind, kind) {
				continue
			}
			out = append(out, resolvedKind{
				gvr:        gv.WithResource(r.Name),
				namespaced: r.Namespaced,
			})
		}
	}
	return out
}

// conditionsOf extracts status.conditions from an unstructured object.
// Resources without conditions (e.g. Namespace) return an empty slice.
func conditionsOf(u *unstructured.Unstructured) []condition {
	raw, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	conds := make([]condition, 0, len(raw))
	for _, c := range raw {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		str := func(k string) string { s, _ := m[k].(string); return s }
		conds = append(conds, condition{
			Type:               str("type"),
			Status:             str("status"),
			Reason:             str("reason"),
			Message:            str("message"),
			LastTransitionTime: str("lastTransitionTime"),
		})
	}
	return conds
}

// findCondition returns the condition with the given type, or nil.
func findCondition(conds []condition, typ string) *condition {
	for i := range conds {
		if conds[i].Type == typ {
			return &conds[i]
		}
	}
	return nil
}
