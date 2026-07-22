package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	fnv1 "github.com/crossplane/function-sdk-go/proto/v1"
	"github.com/crossplane/function-sdk-go/resource"

	"github.com/devopsidiot/kind-platform-lab/internal/policy"
)

// policyConfigMapJSON renders the policy ConfigMap Crossplane would fetch as an
// extra resource, with one policy per data value.
func policyConfigMapJSON(policies map[string]string) string {
	data, _ := json.Marshal(policies)
	return `{
		"apiVersion": "v1",
		"kind": "ConfigMap",
		"metadata": {"name": "app-policies", "namespace": "crossplane-system"},
		"data": ` + string(data) + `
	}`
}

// withPolicies attaches the policy ConfigMap to a request, as Crossplane does
// once it has honoured the function's extra-resource requirement.
func withPolicies(req *fnv1.RunFunctionRequest, policies map[string]string) *fnv1.RunFunctionRequest {
	req.ExtraResources = map[string]*fnv1.Resources{
		policyConfigMapKey: {Items: []*fnv1.Resource{
			{Resource: resource.MustStructJSON(policyConfigMapJSON(policies))},
		}},
	}
	return req
}

func findCondition(rsp *fnv1.RunFunctionResponse, typ string) *fnv1.Condition {
	for _, c := range rsp.GetConditions() {
		if c.GetType() == typ {
			return c
		}
	}
	return nil
}

func warningResults(rsp *fnv1.RunFunctionResponse) []string {
	var msgs []string
	for _, r := range rsp.GetResults() {
		if r.GetSeverity() == fnv1.Severity_SEVERITY_WARNING {
			msgs = append(msgs, r.GetMessage())
		}
	}
	return msgs
}

// desiredXRAnnotations pulls the annotations off the desired composite the
// function set, so a test can assert the verdict was recorded on it.
func desiredXRAnnotations(rsp *fnv1.RunFunctionResponse) map[string]string {
	r := rsp.GetDesired().GetComposite().GetResource()
	if r == nil {
		return nil
	}
	meta, _ := r.AsMap()["metadata"].(map[string]any)
	raw, _ := meta["annotations"].(map[string]any)
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// The function must always request the policy ConfigMap, so Crossplane attaches
// it on the next reconcile even when it is absent now.
func TestRunFunctionRequestsPolicyConfigMap(t *testing.T) {
	fake := &policy.Fake{}
	rsp := runWith(t, requestFor(xrJSON("checkout", "staging")), fake)

	sel := rsp.GetRequirements().GetExtraResources()[policyConfigMapKey]
	if sel == nil {
		t.Fatal("function did not request the policy configmap")
	}
	if got, want := sel.GetMatchName(), defaultPolicyConfigMapName; got != want {
		t.Errorf("matchName = %q, want %q", got, want)
	}
	if got, want := sel.GetNamespace(), defaultPolicyConfigMapNamespace; got != want {
		t.Errorf("namespace = %q, want %q", got, want)
	}
	// With no ConfigMap attached there are no policies, so the model is not hit.
	if fake.Calls != 0 {
		t.Errorf("checker called %d times with no policies; want 0", fake.Calls)
	}
	if c := findCondition(rsp, conditionTypePolicy); c != nil {
		t.Errorf("published a %s condition with no policies configured: %+v", conditionTypePolicy, c)
	}
}

// A compliant verdict publishes a True condition, caches itself on the XR, and
// raises no warning.
func TestRunFunctionPolicyCompliant(t *testing.T) {
	policies := map[string]string{"tier": "production must not run the sandbox tier"}
	fake := &policy.Fake{Verdict: policy.Verdict{Compliant: true, Reasoning: "looks fine"}}

	rsp := runWith(t, withPolicies(requestFor(xrJSON("checkout", "staging")), policies), fake)

	if fake.Calls != 1 {
		t.Fatalf("checker called %d times, want 1", fake.Calls)
	}
	c := findCondition(rsp, conditionTypePolicy)
	if c == nil || c.GetStatus() != fnv1.Status_STATUS_CONDITION_TRUE {
		t.Fatalf("expected a True %s condition, got %+v", conditionTypePolicy, c)
	}
	if c.GetReason() != reasonCompliant {
		t.Errorf("condition reason = %q, want %q", c.GetReason(), reasonCompliant)
	}
	if w := warningResults(rsp); len(w) != 0 {
		t.Errorf("compliant verdict raised warnings: %v", w)
	}

	// The verdict is cached on the desired XR, keyed by the spec hash.
	ann := desiredXRAnnotations(rsp)
	wantHash := policyHash(map[string]any{"appName": "checkout", "environment": "staging"})
	if ann[annotationPolicyHash] != wantHash {
		t.Errorf("cached hash = %q, want %q", ann[annotationPolicyHash], wantHash)
	}
	if ann[annotationPolicyCompliant] != "true" {
		t.Errorf("policy-compliant annotation = %q, want %q", ann[annotationPolicyCompliant], "true")
	}
	if _, ok := ann[annotationPolicyViolations]; ok {
		t.Errorf("compliant verdict left a violations annotation: %q", ann[annotationPolicyViolations])
	}

	// Advisory: the resources are still composed regardless of the verdict.
	if n := len(rsp.GetDesired().GetResources()); n != 3 {
		t.Errorf("composed %d resources, want 3", n)
	}
}

// A violation is advisory: a Warning condition, a warning event, and an
// annotation on the XR, but composition still happens.
func TestRunFunctionPolicyViolation(t *testing.T) {
	policies := map[string]string{"tier": "production must not run the sandbox tier"}
	fake := &policy.Fake{Verdict: policy.Verdict{
		Compliant:  false,
		Violations: []string{"production must not run the sandbox tier"},
		Reasoning:  "environment is production but tier is sandbox",
	}}

	rsp := runWith(t, withPolicies(requestFor(xrJSON("checkout", "production")), policies), fake)

	c := findCondition(rsp, conditionTypePolicy)
	if c == nil || c.GetStatus() != fnv1.Status_STATUS_CONDITION_FALSE {
		t.Fatalf("expected a False %s condition, got %+v", conditionTypePolicy, c)
	}
	if c.GetReason() != reasonViolation {
		t.Errorf("condition reason = %q, want %q", c.GetReason(), reasonViolation)
	}
	if !strings.Contains(c.GetMessage(), "sandbox tier") {
		t.Errorf("condition message %q does not name the violation", c.GetMessage())
	}

	if w := warningResults(rsp); len(w) == 0 {
		t.Error("violation did not raise a warning result")
	}

	// The violation is recorded on the XR's annotations.
	ann := desiredXRAnnotations(rsp)
	if ann[annotationPolicyCompliant] != "false" {
		t.Errorf("policy-compliant annotation = %q, want %q", ann[annotationPolicyCompliant], "false")
	}
	if !strings.Contains(ann[annotationPolicyViolations], "sandbox tier") {
		t.Errorf("policy-violations annotation %q does not name the violation", ann[annotationPolicyViolations])
	}

	// Never blocking: the resources are composed anyway, and nothing is fatal.
	if msgs := fatalResults(rsp); len(msgs) > 0 {
		t.Errorf("violation produced fatal results: %v", msgs)
	}
	if n := len(rsp.GetDesired().GetResources()); n != 3 {
		t.Errorf("composed %d resources, want 3", n)
	}
}

// A verdict already cached under the current spec hash is reused: the model is
// not called again, but the condition is still republished.
func TestRunFunctionPolicyUsesCachedVerdict(t *testing.T) {
	policies := map[string]string{"tier": "production must not run the sandbox tier"}
	hash := policyHash(map[string]any{"appName": "checkout", "environment": "staging"})

	xr := `{
		"apiVersion": "platform.devopsidiot.io/v1alpha1",
		"kind": "XAppEnvironment",
		"metadata": {
			"name": "checkout",
			"annotations": {
				"` + annotationPolicyHash + `": "` + hash + `",
				"` + annotationPolicyCompliant + `": "true"
			}
		},
		"spec": {"appName": "checkout", "environment": "staging"}
	}`

	// Err is set so that if the function did call the model, a cache miss would
	// surface as an Unknown condition; a cache hit must not touch it.
	fake := &policy.Fake{Err: errors.New("should not be called")}
	rsp := runWith(t, withPolicies(requestFor(xr), policies), fake)

	if fake.Calls != 0 {
		t.Errorf("checker called %d times on a cache hit; want 0", fake.Calls)
	}
	c := findCondition(rsp, conditionTypePolicy)
	if c == nil || c.GetStatus() != fnv1.Status_STATUS_CONDITION_TRUE {
		t.Fatalf("expected the cached verdict republished as a True condition, got %+v", c)
	}
}

// When the checker fails, the function fails open: an inconclusive condition,
// no cached hash (so it retries), and resources composed normally.
func TestRunFunctionPolicyFailsOpen(t *testing.T) {
	policies := map[string]string{"tier": "production must not run the sandbox tier"}
	fake := &policy.Fake{
		Verdict: policy.Verdict{Compliant: true, Reasoning: "policy check unavailable"},
		Err:     errors.New("ollama unreachable"),
	}

	rsp := runWith(t, withPolicies(requestFor(xrJSON("checkout", "staging")), policies), fake)

	c := findCondition(rsp, conditionTypePolicy)
	if c == nil || c.GetStatus() != fnv1.Status_STATUS_CONDITION_UNKNOWN {
		t.Fatalf("expected an Unknown %s condition, got %+v", conditionTypePolicy, c)
	}
	if c.GetReason() != reasonUnavailable {
		t.Errorf("condition reason = %q, want %q", c.GetReason(), reasonUnavailable)
	}
	// No verdict is cached, so the next reconcile retries the model.
	if ann := desiredXRAnnotations(rsp); ann[annotationPolicyHash] != "" {
		t.Errorf("failed check cached a hash %q; want none so it retries", ann[annotationPolicyHash])
	}
	if msgs := fatalResults(rsp); len(msgs) > 0 {
		t.Errorf("unavailable checker produced fatal results: %v", msgs)
	}
	if n := len(rsp.GetDesired().GetResources()); n != 3 {
		t.Errorf("composed %d resources, want 3", n)
	}
}
