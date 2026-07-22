package main

import (
	"context"
	"errors"
	"io"
	"log"
	"net/url"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	corefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// These tests exercise the failure modes that actually happen — XR not found,
// cluster unreachable, function pod not running — and assert every one comes
// back as a tool-result error with a message the model can relay, never a
// crash or a raw dial error.

// unreachableErr mimics what client-go returns when the API server is down.
var unreachableErr = &url.Error{
	Op:  "Get",
	URL: "https://127.0.0.1:49559/api",
	Err: errors.New("connect: connection refused"),
}

// callTool builds a CallToolRequest the way the JSON-RPC layer would.
func callTool(name string, args map[string]any) mcp.CallToolRequest {
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	return req
}

// resultText extracts the text content of a tool result.
func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatal("tool result has no content")
	}
	tc, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("tool result content is %T, want TextContent", res.Content[0])
	}
	return tc.Text
}

// Every discovery-backed handler must translate a dead API server into the
// same relayable message, not a raw dial error, and must not return a
// protocol-level error (which would surface as a JSON-RPC failure).
func TestHandlersReportUnreachableCluster(t *testing.T) {
	insp := &inspector{
		client:    dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
		resources: func() ([]*metav1.APIResourceList, error) { return nil, unreachableErr },
		log:       log.New(io.Discard, "", 0),
	}

	handlers := map[string]struct {
		call func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)
		req  mcp.CallToolRequest
	}{
		"get_xr_status": {
			call: insp.handleGetXRStatus,
			req:  callTool("get_xr_status", map[string]any{"kind": "XAppEnvironment", "name": "checkout"}),
		},
		"get_composition_pipeline": {
			call: insp.handleGetCompositionPipeline,
			req:  callTool("get_composition_pipeline", map[string]any{"name": "some-composition"}),
		},
		"list_policy_violations": {
			call: insp.handleListPolicyViolations,
			req:  callTool("list_policy_violations", nil),
		},
	}

	for name, tc := range handlers {
		t.Run(name, func(t *testing.T) {
			res, err := tc.call(context.Background(), tc.req)
			if err != nil {
				t.Fatalf("handler returned a protocol error: %v", err)
			}
			if !res.IsError {
				t.Fatal("expected an error tool result, got success")
			}
			if text := resultText(t, res); !strings.Contains(text, "cannot reach the cluster") {
				t.Errorf("error message does not explain unreachability: %q", text)
			}
		})
	}
}

// get_function_logs reaches the cluster through the core client, not
// discovery, so its unreachable path is injected via a list reactor.
func TestFunctionLogsReportsUnreachableCluster(t *testing.T) {
	core := corefake.NewClientset()
	core.PrependReactor("list", "pods",
		func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, unreachableErr
		})
	insp := &inspector{core: core, log: log.New(io.Discard, "", 0)}

	res, err := insp.handleGetFunctionLogs(context.Background(),
		callTool("get_function_logs", nil))
	if err != nil {
		t.Fatalf("handler returned a protocol error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected an error tool result, got success")
	}
	if text := resultText(t, res); !strings.Contains(text, "cannot reach the cluster") {
		t.Errorf("error message does not explain unreachability: %q", text)
	}
}

func TestGetXRStatusNotFoundMessage(t *testing.T) {
	// The kind resolves but no such XR exists.
	insp := &inspector{
		client:    dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
		resources: func() ([]*metav1.APIResourceList, error) { return discoveryLists, nil },
		log:       log.New(io.Discard, "", 0),
	}

	res, err := insp.handleGetXRStatus(context.Background(),
		callTool("get_xr_status", map[string]any{"kind": "XAppEnvironment", "name": "missing"}))
	if err != nil {
		t.Fatalf("handler returned a protocol error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected an error tool result, got success")
	}
	text := resultText(t, res)
	if !strings.Contains(text, `XAppEnvironment "missing" not found`) {
		t.Errorf("error message does not name the missing XR: %q", text)
	}
}

func TestGetCompositionPipelineNotFoundMessage(t *testing.T) {
	insp := &inspector{
		client:    dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()),
		resources: func() ([]*metav1.APIResourceList, error) { return crossplaneDiscoveryLists, nil },
		log:       log.New(io.Discard, "", 0),
	}

	res, err := insp.handleGetCompositionPipeline(context.Background(),
		callTool("get_composition_pipeline", map[string]any{"name": "missing"}))
	if err != nil {
		t.Fatalf("handler returned a protocol error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected an error tool result, got success")
	}
	if text := resultText(t, res); !strings.Contains(text, `composition "missing" not found`) {
		t.Errorf("error message does not name the missing composition: %q", text)
	}
}

func TestListPolicyViolationsReportsListFailure(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			{Group: "apiextensions.crossplane.io", Version: "v2", Resource: "compositeresourcedefinitions"}: "CompositeResourceDefinitionList",
		})
	client.PrependReactor("list", "compositeresourcedefinitions",
		func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, unreachableErr
		})
	insp := &inspector{
		client:    client,
		resources: func() ([]*metav1.APIResourceList, error) { return crossplaneDiscoveryLists, nil },
		log:       log.New(io.Discard, "", 0),
	}

	res, err := insp.handleListPolicyViolations(context.Background(),
		callTool("list_policy_violations", nil))
	if err != nil {
		t.Fatalf("handler returned a protocol error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected an error tool result, got success")
	}
	if text := resultText(t, res); !strings.Contains(text, "cannot reach the cluster") {
		t.Errorf("error message does not explain unreachability: %q", text)
	}
}

// A pod that is not Running must be reported with its phase, and a pod whose
// logs cannot be read must explain that inline rather than fail the call.
func TestLogLinesOrReason(t *testing.T) {
	cases := map[string]struct {
		phase corev1.PodPhase
		lines []string
		err   error
		want  string
	}{
		"Success": {
			phase: corev1.PodRunning,
			lines: []string{"a", "b"},
			want:  "a",
		},
		"PendingPod": {
			phase: corev1.PodPending,
			err:   errors.New("container not started"),
			want:  "<pod is Pending, not Running; logs unavailable: container not started>",
		},
		"RunningPodStreamError": {
			phase: corev1.PodRunning,
			err:   errors.New("boom"),
			want:  "<cannot read logs: boom>",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := logLinesOrReason(tc.phase, tc.lines, tc.err)
			if len(got) == 0 || got[0] != tc.want {
				t.Errorf("logLinesOrReason() = %v, want first line %q", got, tc.want)
			}
		})
	}
}

// A pod stuck Pending still shows up in the result with its phase visible.
func TestFunctionLogsReportsPendingPod(t *testing.T) {
	pod := functionPod("function-app-environment-abc-1", "function-app-environment")
	pod.Status.Phase = corev1.PodPending
	insp := &inspector{core: corefake.NewClientset(pod), log: log.New(io.Discard, "", 0)}

	got, err := insp.functionLogs(context.Background(), "function-app-environment", 10)
	if err != nil {
		t.Fatalf("functionLogs(): unexpected error: %v", err)
	}
	if len(got.Pods) != 1 || got.Pods[0].Phase != "Pending" {
		t.Errorf("expected one pod reported as Pending, got %+v", got.Pods)
	}
}
