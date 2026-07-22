package policy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

// These tests exercise the Ollama Checker's HTTP and JSON handling against a
// stub server. They never reach a real model: the stub returns canned
// /api/generate replies, which is exactly what CLAUDE.md means by "tests never
// call a real model".

// stubOllama starts an httptest server that runs handler for /api/generate and
// returns a wired-up Ollama pointing at it.
func stubOllama(t *testing.T, handler http.HandlerFunc) *Ollama {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return &Ollama{BaseURL: srv.URL, Model: "test-model", HTTPClient: srv.Client()}
}

// generateReply writes a well-formed /api/generate envelope whose response
// field carries verdictJSON, mimicking Ollama's JSON mode.
func generateReply(w http.ResponseWriter, verdictJSON string) {
	_ = json.NewEncoder(w).Encode(generateResponse{Response: verdictJSON})
}

func TestOllamaParsesVerdict(t *testing.T) {
	o := stubOllama(t, func(w http.ResponseWriter, r *http.Request) {
		// The request must ask for JSON mode and carry the policies.
		body, _ := io.ReadAll(r.Body)
		var req generateRequest
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("stub could not decode request: %v", err)
		}
		if req.Format != "json" {
			t.Errorf("request Format = %q, want %q", req.Format, "json")
		}
		if req.Stream {
			t.Error("request Stream = true, want false")
		}
		if !strings.Contains(req.Prompt, "no sandbox tier in production") {
			t.Errorf("prompt missing the policy text:\n%s", req.Prompt)
		}
		generateReply(w, `{"compliant": false, "violations": ["no sandbox tier in production"], "reasoning": "prod uses sandbox"}`)
	})

	got, err := o.Check(context.Background(),
		map[string]any{"appName": "web", "environment": "production", "tier": "sandbox"},
		[]string{"no sandbox tier in production"})
	if err != nil {
		t.Fatalf("Check returned unexpected error: %v", err)
	}

	want := Verdict{
		Compliant:  false,
		Violations: []string{"no sandbox tier in production"},
		Reasoning:  "prod uses sandbox",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("verdict mismatch: -want +got:\n%s", diff)
	}
}

func TestOllamaCompliantVerdict(t *testing.T) {
	o := stubOllama(t, func(w http.ResponseWriter, r *http.Request) {
		generateReply(w, `{"compliant": true, "violations": [], "reasoning": "all good"}`)
	})

	got, err := o.Check(context.Background(), map[string]any{"environment": "sandbox"}, []string{"some policy"})
	if err != nil {
		t.Fatalf("Check returned unexpected error: %v", err)
	}
	if !got.Compliant || len(got.Violations) != 0 {
		t.Errorf("expected a clean compliant verdict, got %+v", got)
	}
}

// No policies configured is a clean pass that never touches the network.
func TestOllamaNoPoliciesSkipsCall(t *testing.T) {
	o := stubOllama(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("Check called the server with no policies configured")
	})

	got, err := o.Check(context.Background(), map[string]any{"environment": "sandbox"}, nil)
	if err != nil {
		t.Fatalf("Check returned unexpected error: %v", err)
	}
	if !got.Compliant {
		t.Errorf("expected a Compliant verdict with no policies, got %+v", got)
	}
}

// Fail open on a non-200: an error is returned, but the verdict is Compliant so
// a caller that logs and carries on composes normally.
func TestOllamaFailsOpenOnServerError(t *testing.T) {
	o := stubOllama(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})

	got, err := o.Check(context.Background(), map[string]any{"environment": "sandbox"}, []string{"policy"})
	if err == nil {
		t.Fatal("expected an error on HTTP 500, got nil")
	}
	if !got.Compliant {
		t.Errorf("expected fail-open Compliant verdict, got %+v", got)
	}
}

// Fail open on unparseable model output.
func TestOllamaFailsOpenOnUnparseableOutput(t *testing.T) {
	o := stubOllama(t, func(w http.ResponseWriter, r *http.Request) {
		generateReply(w, "this is not json")
	})

	got, err := o.Check(context.Background(), map[string]any{"environment": "sandbox"}, []string{"policy"})
	if err == nil {
		t.Fatal("expected an error on unparseable output, got nil")
	}
	if !got.Compliant {
		t.Errorf("expected fail-open Compliant verdict, got %+v", got)
	}
}

// Fail open when the server is unreachable: point at a closed address.
func TestOllamaFailsOpenWhenUnreachable(t *testing.T) {
	o := &Ollama{BaseURL: "http://127.0.0.1:1", Model: "test-model", Timeout: time.Second}

	got, err := o.Check(context.Background(), map[string]any{"environment": "sandbox"}, []string{"policy"})
	if err == nil {
		t.Fatal("expected an error when unreachable, got nil")
	}
	if !got.Compliant {
		t.Errorf("expected fail-open Compliant verdict, got %+v", got)
	}
}

// The hard timeout is enforced: a slow model returns an error before it
// answers, and the verdict fails open.
func TestOllamaEnforcesTimeout(t *testing.T) {
	release := make(chan struct{})

	o := stubOllama(t, func(w http.ResponseWriter, r *http.Request) {
		// Block until the test releases us, so the call can only return via its
		// own deadline.
		<-release
	})
	// Registered after stubOllama's srv.Close cleanup, so LIFO runs this first:
	// the handler unblocks before Close waits on the connection.
	t.Cleanup(func() { close(release) })
	o.Timeout = 50 * time.Millisecond

	start := time.Now()
	got, err := o.Check(context.Background(), map[string]any{"environment": "sandbox"}, []string{"policy"})
	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Check took %s; timeout was not enforced", elapsed)
	}
	if !got.Compliant {
		t.Errorf("expected fail-open Compliant verdict, got %+v", got)
	}
}
