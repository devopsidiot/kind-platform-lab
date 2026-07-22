package main

import (
	"context"
	"io"
	"log"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corefake "k8s.io/client-go/kubernetes/fake"
)

// The fake clientset serves a fixed "fake logs" body for GetLogs, so these
// tests assert pod discovery and result shape rather than log content.

func functionPod(name, function string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: functionNamespace,
			Labels:    map[string]string{functionLabel: function},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func TestFunctionLogs(t *testing.T) {
	core := corefake.NewClientset(
		functionPod("function-app-environment-abc-1", "function-app-environment"),
		functionPod("function-auto-ready-def-1", "function-auto-ready"),
	)
	insp := &inspector{core: core, log: log.New(io.Discard, "", 0)}

	got, err := insp.functionLogs(context.Background(), "function-app-environment", 50)
	if err != nil {
		t.Fatalf("functionLogs(): unexpected error: %v", err)
	}

	if got.Function != "function-app-environment" || got.TailLines != 50 {
		t.Errorf("unexpected result metadata: %+v", got)
	}
	// The label selector must exclude the auto-ready pod.
	if len(got.Pods) != 1 {
		t.Fatalf("expected exactly 1 pod, got %d: %+v", len(got.Pods), got.Pods)
	}
	p := got.Pods[0]
	if p.Pod != "function-app-environment-abc-1" || p.Phase != "Running" {
		t.Errorf("unexpected pod entry: %+v", p)
	}
	if len(p.Lines) == 0 {
		t.Errorf("expected log lines from the fake clientset, got none")
	}
}

func TestFunctionLogsNoPods(t *testing.T) {
	insp := &inspector{core: corefake.NewClientset(), log: log.New(io.Discard, "", 0)}

	_, err := insp.functionLogs(context.Background(), "function-app-environment", 50)
	if err == nil {
		t.Fatal("functionLogs(): expected an error with no pods, got nil")
	}
}
