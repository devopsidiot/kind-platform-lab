package main

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"

	"github.com/crossplane/function-sdk-go/logging"
	fnv1 "github.com/crossplane/function-sdk-go/proto/v1"
	"github.com/crossplane/function-sdk-go/resource"
	"github.com/crossplane/function-sdk-go/response"

	"github.com/devopsidiot/kind-platform-lab/internal/policy"
)

// xrJSON builds the observed XR JSON for an app and environment.
func xrJSON(appName, environment string) string {
	return `{
		"apiVersion": "platform.devopsidiot.io/v1alpha1",
		"kind": "XAppEnvironment",
		"metadata": {"name": "` + appName + `"},
		"spec": {"appName": "` + appName + `", "environment": "` + environment + `"}
	}`
}

// requestFor builds a RunFunctionRequest observing the supplied XR.
func requestFor(xr string) *fnv1.RunFunctionRequest {
	return &fnv1.RunFunctionRequest{
		Observed: &fnv1.State{
			Composite: &fnv1.Resource{Resource: resource.MustStructJSON(xr)},
		},
	}
}

// run invokes the function with a compliant fake checker, failing the test on
// transport-level errors. Tests never call a real model.
func run(t *testing.T, req *fnv1.RunFunctionRequest) *fnv1.RunFunctionResponse {
	t.Helper()
	return runWith(t, req, &policy.Fake{Verdict: policy.Verdict{Compliant: true}})
}

// runWith invokes the function with a specific checker, so policy tests can
// drive the compliant, violating, and unavailable paths.
func runWith(t *testing.T, req *fnv1.RunFunctionRequest, checker policy.Checker) *fnv1.RunFunctionResponse {
	t.Helper()
	rsp, err := NewFunction(logging.NewNopLogger(), checker).RunFunction(context.Background(), req)
	if err != nil {
		t.Fatalf("RunFunction() returned an unexpected error: %v", err)
	}
	return rsp
}

// fatalResults returns the messages of any FATAL results on the response.
func fatalResults(rsp *fnv1.RunFunctionResponse) []string {
	var msgs []string
	for _, r := range rsp.GetResults() {
		if r.GetSeverity() == fnv1.Severity_SEVERITY_FATAL {
			msgs = append(msgs, r.GetMessage())
		}
	}
	return msgs
}

func TestRunFunctionComposesAllThreeResources(t *testing.T) {
	rsp := run(t, requestFor(xrJSON("checkout", "staging")))

	if msgs := fatalResults(rsp); len(msgs) > 0 {
		t.Fatalf("RunFunction() returned fatal results: %v", msgs)
	}

	got := rsp.GetDesired().GetResources()
	want := []string{"configmap", "namespace", "resourcequota"}
	for _, name := range want {
		if _, ok := got[name]; !ok {
			t.Errorf("desired resources missing %q; got keys %v", name, keys(got))
		}
	}
	if len(got) != len(want) {
		t.Errorf("desired resources = %d, want %d (keys %v)", len(got), len(want), keys(got))
	}
}

func TestRunFunctionSetsNamespaceAndMetadata(t *testing.T) {
	rsp := run(t, requestFor(xrJSON("checkout", "staging")))
	res := rsp.GetDesired().GetResources()

	ns := &corev1.Namespace{}
	mustDecode(t, res["namespace"], ns)
	if got, want := ns.GetName(), "checkout-staging"; got != want {
		t.Errorf("namespace name = %q, want %q", got, want)
	}

	cm := &corev1.ConfigMap{}
	mustDecode(t, res["configmap"], cm)
	if got, want := cm.GetNamespace(), "checkout-staging"; got != want {
		t.Errorf("configmap namespace = %q, want %q", got, want)
	}
	if got, want := cm.Data["environment"], "staging"; got != want {
		t.Errorf("configmap data[environment] = %q, want %q", got, want)
	}

	rq := &corev1.ResourceQuota{}
	mustDecode(t, res["resourcequota"], rq)
	if got, want := rq.GetNamespace(), "checkout-staging"; got != want {
		t.Errorf("resourcequota namespace = %q, want %q", got, want)
	}

	// Every composed resource carries the traceability labels.
	wantLabels := map[string]string{
		labelAppName:     "checkout",
		labelEnvironment: "staging",
	}
	for name, obj := range map[string]interface{ GetLabels() map[string]string }{
		"namespace":     ns,
		"configmap":     cm,
		"resourcequota": rq,
	} {
		if diff := cmp.Diff(wantLabels, obj.GetLabels()); diff != "" {
			t.Errorf("%s labels: -want +got:\n%s", name, diff)
		}
	}
}

func TestRunFunctionTiersQuotaByEnvironment(t *testing.T) {
	cases := map[string]struct {
		wantCPULimit string
		wantMemLimit string
		wantPods     string
	}{
		"sandbox":    {wantCPULimit: "2", wantMemLimit: "4Gi", wantPods: "10"},
		"staging":    {wantCPULimit: "8", wantMemLimit: "16Gi", wantPods: "50"},
		"production": {wantCPULimit: "32", wantMemLimit: "64Gi", wantPods: "200"},
	}

	for environment, tc := range cases {
		t.Run(environment, func(t *testing.T) {
			rsp := run(t, requestFor(xrJSON("checkout", environment)))
			if msgs := fatalResults(rsp); len(msgs) > 0 {
				t.Fatalf("RunFunction() returned fatal results: %v", msgs)
			}

			rq := &corev1.ResourceQuota{}
			mustDecode(t, rsp.GetDesired().GetResources()["resourcequota"], rq)

			hard := rq.Spec.Hard
			if got := hard[corev1.ResourceLimitsCPU]; got.String() != tc.wantCPULimit {
				t.Errorf("hard[limits.cpu] = %q, want %q", got.String(), tc.wantCPULimit)
			}
			if got := hard[corev1.ResourceLimitsMemory]; got.String() != tc.wantMemLimit {
				t.Errorf("hard[limits.memory] = %q, want %q", got.String(), tc.wantMemLimit)
			}
			if got := hard[corev1.ResourcePods]; got.String() != tc.wantPods {
				t.Errorf("hard[pods] = %q, want %q", got.String(), tc.wantPods)
			}
		})
	}
}

// Distinct environments must not collide on namespace, or two XRs would fight
// over the same composed resources.
func TestRunFunctionNamespaceIsPerEnvironment(t *testing.T) {
	seen := map[string]string{}
	for environment := range tiers {
		rsp := run(t, requestFor(xrJSON("checkout", environment)))
		ns := &corev1.Namespace{}
		mustDecode(t, rsp.GetDesired().GetResources()["namespace"], ns)

		if other, collides := seen[ns.GetName()]; collides {
			t.Errorf("environments %q and %q both compose namespace %q", other, environment, ns.GetName())
		}
		seen[ns.GetName()] = environment
	}
}

func TestRunFunctionRejectsUnknownEnvironment(t *testing.T) {
	rsp := run(t, requestFor(xrJSON("checkout", "qa")))

	msgs := fatalResults(rsp)
	if len(msgs) == 0 {
		t.Fatal("RunFunction() accepted an unsupported environment; want a fatal result")
	}
	// The message should name the offending value and the valid options, since
	// this is what surfaces on the claim.
	for _, want := range []string{`"qa"`, "sandbox", "staging", "production"} {
		if !strings.Contains(msgs[0], want) {
			t.Errorf("fatal message %q does not mention %q", msgs[0], want)
		}
	}
	if n := len(rsp.GetDesired().GetResources()); n != 0 {
		t.Errorf("desired resources = %d, want 0 when the environment is invalid", n)
	}
}

func TestRunFunctionRejectsMissingFields(t *testing.T) {
	cases := map[string]string{
		"NoAppName": `{
			"apiVersion": "platform.devopsidiot.io/v1alpha1",
			"kind": "XAppEnvironment",
			"spec": {"environment": "sandbox"}
		}`,
		"NoEnvironment": `{
			"apiVersion": "platform.devopsidiot.io/v1alpha1",
			"kind": "XAppEnvironment",
			"spec": {"appName": "checkout"}
		}`,
		"NoSpec": `{
			"apiVersion": "platform.devopsidiot.io/v1alpha1",
			"kind": "XAppEnvironment"
		}`,
	}

	for name, xr := range cases {
		t.Run(name, func(t *testing.T) {
			rsp := run(t, requestFor(xr))
			if msgs := fatalResults(rsp); len(msgs) == 0 {
				t.Error("RunFunction() accepted an incomplete XR; want a fatal result")
			}
		})
	}
}

// The function is one step in a pipeline, so it must add to the desired state
// it was handed rather than replacing it.
func TestRunFunctionPreservesExistingDesiredResources(t *testing.T) {
	req := requestFor(xrJSON("checkout", "sandbox"))
	req.Desired = &fnv1.State{
		Resources: map[string]*fnv1.Resource{
			"from-earlier-function": {Resource: resource.MustStructJSON(`{
				"apiVersion": "v1",
				"kind": "Secret",
				"metadata": {"name": "preexisting", "namespace": "default"}
			}`)},
		},
	}

	rsp := run(t, req)
	if msgs := fatalResults(rsp); len(msgs) > 0 {
		t.Fatalf("RunFunction() returned fatal results: %v", msgs)
	}

	got := rsp.GetDesired().GetResources()
	if _, ok := got["from-earlier-function"]; !ok {
		t.Errorf("function dropped a resource set by an earlier function; got keys %v", keys(got))
	}
	if len(got) != 4 {
		t.Errorf("desired resources = %d, want 4 (3 composed + 1 preexisting); keys %v", len(got), keys(got))
	}
}

// Running twice over the same input must produce the same desired state, or
// Crossplane would churn the composed resources every reconcile.
func TestRunFunctionIsDeterministic(t *testing.T) {
	first := run(t, requestFor(xrJSON("checkout", "production")))
	second := run(t, requestFor(xrJSON("checkout", "production")))

	for name, a := range first.GetDesired().GetResources() {
		b := second.GetDesired().GetResources()[name]
		if diff := cmp.Diff(a.GetResource().AsMap(), b.GetResource().AsMap()); diff != "" {
			t.Errorf("%s differs between runs: -first +second:\n%s", name, diff)
		}
	}
}

// The composed types are all core Kubernetes objects, which never publish a
// Ready status condition. If the function does not assert readiness itself the
// XR stays Ready=False forever, so every composed resource must say READY_TRUE.
func TestRunFunctionMarksComposedResourcesReady(t *testing.T) {
	rsp := run(t, requestFor(xrJSON("checkout", "staging")))

	res := rsp.GetDesired().GetResources()
	if len(res) == 0 {
		t.Fatal("no composed resources to check readiness on")
	}
	for name, r := range res {
		if got := r.GetReady(); got != fnv1.Ready_READY_TRUE {
			t.Errorf("composed resource %q readiness = %v, want %v", name, got, fnv1.Ready_READY_TRUE)
		}
	}
}

func TestRunFunctionSetsResponseTTL(t *testing.T) {
	rsp := run(t, requestFor(xrJSON("checkout", "sandbox")))
	if got, want := rsp.GetMeta().GetTtl().AsDuration(), response.DefaultTTL; got != want {
		t.Errorf("response TTL = %v, want %v", got, want)
	}
}
