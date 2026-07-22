package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	metav1unstructured "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/crossplane/function-sdk-go/errors"
	fnv1 "github.com/crossplane/function-sdk-go/proto/v1"
	"github.com/crossplane/function-sdk-go/request"
	"github.com/crossplane/function-sdk-go/resource"
	"github.com/crossplane/function-sdk-go/response"

	"github.com/devopsidiot/kind-platform-lab/internal/policy"
)

// Extra-resource key under which we ask Crossplane to fetch the policy
// ConfigMap and under which it hands it back.
const policyConfigMapKey = "policies"

// Annotations recording the last policy verdict and the hash of the
// policy-relevant spec it was computed for, so the model is only called when
// that spec changes. The compliant/violations pair also makes the verdict
// visible on the XR without decoding anything.
const (
	annotationPolicyHash       = "platform.devopsidiot.io/policy-hash"
	annotationPolicyCompliant  = "platform.devopsidiot.io/policy-compliant"
	annotationPolicyViolations = "platform.devopsidiot.io/policy-violations"
)

// The status condition the policy check publishes on the XR. It is advisory:
// it is a distinct condition type, so it never gates the XR's Ready condition.
const conditionTypePolicy = "PolicyCheck"

// Condition reasons.
const (
	reasonCompliant   = "Compliant"
	reasonViolation   = "PolicyViolation"
	reasonUnavailable = "CheckUnavailable"
)

// advisePolicy runs the advisory policy check for spec and records the outcome
// on rsp. It never fails composition: every error path logs and returns, and
// the check itself fails open. The policy-relevant spec is passed in so the
// caller decides which fields are policy-relevant.
func (f *Function) advisePolicy(ctx context.Context, req *fnv1.RunFunctionRequest, rsp *fnv1.RunFunctionResponse, observed *resource.Composite, spec map[string]any) {
	// Always ask Crossplane for the policy ConfigMap. On the first reconcile it
	// is not yet attached; requesting it here makes it present next time.
	f.requirePolicyConfigMap(rsp)

	policies, err := policiesFromRequest(req)
	if err != nil {
		f.log.Info("cannot read policy configmap; skipping policy check", "error", err)
		return
	}
	if len(policies) == 0 {
		// None configured, or not fetched yet: nothing to advise on.
		return
	}

	hash := policyHash(spec)

	// Do not re-check on every reconcile: if the policy-relevant spec is
	// unchanged, republish the cached verdict instead of calling the model.
	if v, ok := cachedVerdict(observed, hash); ok {
		f.recordVerdict(req, rsp, observed, hash, v)
		return
	}

	verdict, err := f.checker.Check(ctx, spec, policies)
	if err != nil {
		// Fail open: log, publish an inconclusive condition, and leave the hash
		// annotation unset so the next reconcile retries.
		f.log.Info("policy check unavailable; composing normally", "error", err)
		response.ConditionUnknown(rsp, conditionTypePolicy, reasonUnavailable).
			WithMessage(err.Error())
		return
	}

	f.recordVerdict(req, rsp, observed, hash, verdict)
}

// requirePolicyConfigMap adds an extra-resource requirement for the policy
// ConfigMap so Crossplane fetches it and attaches it to the next request.
func (f *Function) requirePolicyConfigMap(rsp *fnv1.RunFunctionResponse) {
	selector := &fnv1.ResourceSelector{
		ApiVersion: "v1",
		Kind:       "ConfigMap",
		Match:      &fnv1.ResourceSelector_MatchName{MatchName: f.policyConfigMapName},
	}
	if ns := f.policyConfigMapNamespace; ns != "" {
		selector.Namespace = &ns
	}

	if rsp.Requirements == nil {
		rsp.Requirements = &fnv1.Requirements{}
	}
	if rsp.Requirements.ExtraResources == nil {
		rsp.Requirements.ExtraResources = map[string]*fnv1.ResourceSelector{}
	}
	rsp.Requirements.ExtraResources[policyConfigMapKey] = selector
}

// policiesFromRequest reads the policy strings out of the fetched ConfigMap.
// Each value under .data is one policy. It returns nil (not an error) when the
// ConfigMap has not been attached yet.
func policiesFromRequest(req *fnv1.RunFunctionRequest) ([]string, error) {
	extra, err := request.GetExtraResources(req)
	if err != nil {
		return nil, errors.Wrap(err, "cannot get extra resources")
	}

	items := extra[policyConfigMapKey]
	if len(items) == 0 {
		return nil, nil
	}

	data, found, err := metav1unstructured.NestedStringMap(items[0].Resource.Object, "data")
	if err != nil {
		return nil, errors.Wrap(err, "cannot read configmap data")
	}
	if !found {
		return nil, nil
	}

	policies := make([]string, 0, len(data))
	for _, v := range data {
		if v = strings.TrimSpace(v); v != "" {
			policies = append(policies, v)
		}
	}
	// Sort so the prompt and the hash are stable regardless of map ordering.
	sort.Strings(policies)
	return policies, nil
}

// policyHash is the cache key: a hash of the policy-relevant spec fields, so a
// change to any of them triggers a fresh check.
func policyHash(spec map[string]any) string {
	// encoding/json sorts map keys, so the same spec always hashes the same.
	b, err := json.Marshal(spec)
	if err != nil {
		// spec holds only strings, so this cannot fail; fall back to a value
		// that never matches a stored hash rather than panicking.
		return "unhashable"
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// cachedVerdict returns the verdict stored on the observed XR, but only if it
// was computed for the same inputs (its hash annotation matches).
func cachedVerdict(observed *resource.Composite, hash string) (policy.Verdict, bool) {
	if observed == nil || observed.Resource == nil {
		return policy.Verdict{}, false
	}
	ann := observed.Resource.GetAnnotations()
	if ann[annotationPolicyHash] != hash {
		return policy.Verdict{}, false
	}
	compliant, ok := ann[annotationPolicyCompliant]
	if !ok {
		return policy.Verdict{}, false
	}
	v := policy.Verdict{Compliant: compliant == "true"}
	if msg := ann[annotationPolicyViolations]; msg != "" {
		v.Violations = []string{msg}
	}
	return v, true
}

// recordVerdict publishes the verdict as a status condition and caches it on
// the desired XR's annotations. Any failure here is logged, not fatal.
func (f *Function) recordVerdict(req *fnv1.RunFunctionRequest, rsp *fnv1.RunFunctionResponse, observed *resource.Composite, hash string, v policy.Verdict) {
	applyPolicyCondition(rsp, v)

	desired, err := request.GetDesiredCompositeResource(req)
	if err != nil {
		f.log.Info("cannot get desired composite to record policy verdict", "error", err)
		return
	}

	// The desired XR may be empty if no earlier function populated it; seed its
	// identity from the observed XR so Crossplane accepts the annotation write.
	if desired.Resource.GetKind() == "" {
		desired.Resource.SetAPIVersion(observed.Resource.GetAPIVersion())
		desired.Resource.SetKind(observed.Resource.GetKind())
	}
	if desired.Resource.GetName() == "" {
		desired.Resource.SetName(observed.Resource.GetName())
	}

	ann := desired.Resource.GetAnnotations()
	if ann == nil {
		ann = map[string]string{}
	}
	ann[annotationPolicyHash] = hash
	if v.Compliant {
		ann[annotationPolicyCompliant] = "true"
		delete(ann, annotationPolicyViolations)
	} else {
		ann[annotationPolicyCompliant] = "false"
		ann[annotationPolicyViolations] = strings.Join(v.Violations, "; ")
	}
	desired.Resource.SetAnnotations(ann)

	if err := response.SetDesiredCompositeResource(rsp, desired); err != nil {
		f.log.Info("cannot set desired composite with policy verdict", "error", err)
	}
}

// applyPolicyCondition maps a verdict onto a status condition, and raises a
// warning event for a violation so it surfaces on the XR.
func applyPolicyCondition(rsp *fnv1.RunFunctionResponse, v policy.Verdict) {
	if v.Compliant {
		response.ConditionTrue(rsp, conditionTypePolicy, reasonCompliant).
			WithMessage("spec complies with all configured policies")
		return
	}

	msg := strings.Join(v.Violations, "; ")
	if msg == "" {
		msg = v.Reasoning
	}
	response.ConditionFalse(rsp, conditionTypePolicy, reasonViolation).WithMessage(msg)
	response.Warning(rsp, errors.Errorf("advisory policy violation: %s", msg))
}
