package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// functionNamespace is where Crossplane runs function pods.
	functionNamespace = "crossplane-system"
	// functionLabel is the label Crossplane puts on a function's runtime pods.
	functionLabel = "pkg.crossplane.io/function"
	// defaultFunctionName is this repo's composition function.
	defaultFunctionName = "function-app-environment"

	// defaultLogLines and maxLogLines bound the tail so a single call cannot
	// flood the model's context.
	defaultLogLines = 100
	maxLogLines     = 500
)

// podLogs is the tail of one pod's log.
type podLogs struct {
	Pod   string   `json:"pod"`
	Phase string   `json:"phase"`
	Lines []string `json:"lines"`
}

// functionLogs is the structured result of get_function_logs.
type functionLogs struct {
	Function  string    `json:"function"`
	Namespace string    `json:"namespace"`
	TailLines int       `json:"tailLines"`
	Pods      []podLogs `json:"pods"`
}

func functionLogsTool() mcp.Tool {
	return mcp.NewTool("get_function_logs",
		mcp.WithDescription("Tail the logs of a Crossplane composition function's "+
			"pods. Returns structured JSON with the last N lines per pod."),
		mcp.WithString("function",
			mcp.Description("Function name; defaults to "+defaultFunctionName)),
		mcp.WithNumber("lines",
			mcp.Description(fmt.Sprintf("Lines to tail per pod; defaults to %d, capped at %d",
				defaultLogLines, maxLogLines))),
	)
}

func (i *inspector) handleGetFunctionLogs(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	function := req.GetString("function", defaultFunctionName)
	lines := req.GetInt("lines", defaultLogLines)
	if lines <= 0 {
		lines = defaultLogLines
	}
	if lines > maxLogLines {
		lines = maxLogLines
	}

	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()

	result, err := i.functionLogs(ctx, function, lines)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultJSON(result)
}

// functionLogs finds the function's runtime pods by label and tails each one.
func (i *inspector) functionLogs(ctx context.Context, function string, lines int) (*functionLogs, error) {
	pods, err := i.core.CoreV1().Pods(functionNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: functionLabel + "=" + function,
	})
	if err != nil {
		return nil, fmt.Errorf("list pods for function %q: %w", function, clusterError(err))
	}
	if len(pods.Items) == 0 {
		return nil, fmt.Errorf("no pods found for function %q in namespace %s; "+
			"check that the Function is installed and its deployment is running",
			function, functionNamespace)
	}

	tail := int64(lines)
	result := &functionLogs{
		Function:  function,
		Namespace: functionNamespace,
		TailLines: lines,
		Pods:      []podLogs{},
	}
	for _, pod := range pods.Items {
		// Tail regardless of phase — a crashed pod still has logs worth
		// reading.
		lines, err := i.tailPod(ctx, pod.Name, tail)
		result.Pods = append(result.Pods, podLogs{
			Pod:   pod.Name,
			Phase: string(pod.Status.Phase),
			Lines: logLinesOrReason(pod.Status.Phase, lines, err),
		})
	}
	return result, nil
}

// logLinesOrReason returns the tailed lines, or — when tailing failed — a
// single line explaining why in terms of the pod's state, so one broken pod
// explains itself without hiding the others.
func logLinesOrReason(phase corev1.PodPhase, lines []string, err error) []string {
	if err == nil {
		return lines
	}
	if phase == corev1.PodRunning {
		return []string{fmt.Sprintf("<cannot read logs: %v>", err)}
	}
	return []string{fmt.Sprintf("<pod is %s, not Running; logs unavailable: %v>", phase, err)}
}

// tailPod returns the last tail lines of the pod's log.
func (i *inspector) tailPod(ctx context.Context, pod string, tail int64) ([]string, error) {
	stream, err := i.core.CoreV1().Pods(functionNamespace).
		GetLogs(pod, &corev1.PodLogOptions{TailLines: &tail}).Stream(ctx)
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	raw, err := io.ReadAll(stream)
	if err != nil {
		return nil, err
	}
	out := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(out) == 1 && out[0] == "" {
		return []string{}, nil
	}
	return out, nil
}
