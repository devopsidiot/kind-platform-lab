package policy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// DefaultTimeout bounds a single LLM call when Ollama.Timeout is unset.
//
// Composition functions run inside a reconcile loop, so a slow model call
// stalls reconciliation for every XR, not just this one. The deadline is
// deliberately short; a model that cannot answer in time is treated as
// unavailable and the check fails open.
const DefaultTimeout = 10 * time.Second

// Ollama is a Checker backed by an Ollama server, using the /api/generate
// endpoint in JSON mode so the model returns a parseable object rather than
// prose.
type Ollama struct {
	// BaseURL is the Ollama server root, e.g. http://ollama.llm.svc:11434.
	BaseURL string
	// Model is the served model name, e.g. llama3.2:3b.
	Model string
	// Timeout bounds a single call. Zero or negative means DefaultTimeout.
	Timeout time.Duration
	// HTTPClient issues the request. Nil means http.DefaultClient.
	HTTPClient *http.Client
}

// Ollama implements Checker.
var _ Checker = (*Ollama)(nil)

// generateRequest is the subset of Ollama's /api/generate body we send.
// Format "json" is Ollama's JSON mode: the model is constrained to emit a
// single valid JSON value.
type generateRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Format string `json:"format"`
	Stream bool   `json:"stream"`
}

// generateResponse is the subset of Ollama's /api/generate reply we read. In
// JSON mode Response is itself a JSON document, encoded as a string.
type generateResponse struct {
	Response string `json:"response"`
}

// llmVerdict is the schema we ask the model to fill in. It mirrors Verdict but
// stays separate so the model's field names are decoupled from our exported
// type.
type llmVerdict struct {
	Compliant  bool     `json:"compliant"`
	Violations []string `json:"violations"`
	Reasoning  string   `json:"reasoning"`
}

// Check evaluates spec against policies by asking the model for a JSON verdict.
//
// It enforces a hard timeout and fails open: on any failure — unreachable,
// slow, non-200, or unparseable output — it returns a Compliant verdict and a
// non-nil error describing what went wrong, so provisioning is never blocked
// by an unavailable model.
func (o *Ollama) Check(ctx context.Context, spec map[string]any, policies []string) (Verdict, error) {
	// unavailable is the fail-open result: a Compliant verdict carrying the
	// error, so a caller that ignores err still composes normally.
	unavailable := func(err error) (Verdict, error) {
		return Verdict{Compliant: true, Reasoning: "policy check unavailable"}, err
	}

	// No policies means nothing to check; this is a clean pass, not a failure.
	if len(policies) == 0 {
		return Verdict{Compliant: true, Reasoning: "no policies configured"}, nil
	}

	prompt, err := buildPrompt(spec, policies)
	if err != nil {
		return unavailable(fmt.Errorf("build prompt: %w", err))
	}

	body, err := json.Marshal(generateRequest{
		Model:  o.Model,
		Prompt: prompt,
		Format: "json",
		Stream: false,
	})
	if err != nil {
		return unavailable(fmt.Errorf("marshal request: %w", err))
	}

	timeout := o.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	url := strings.TrimRight(o.BaseURL, "/") + "/api/generate"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return unavailable(fmt.Errorf("build http request: %w", err))
	}
	req.Header.Set("Content-Type", "application/json")

	client := o.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return unavailable(fmt.Errorf("call ollama: %w", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return unavailable(fmt.Errorf("ollama returned status %s", resp.Status))
	}

	var envelope generateResponse
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return unavailable(fmt.Errorf("decode ollama response: %w", err))
	}

	var v llmVerdict
	if err := json.Unmarshal([]byte(envelope.Response), &v); err != nil {
		return unavailable(fmt.Errorf("parse model verdict %q: %w", envelope.Response, err))
	}

	return Verdict{
		Compliant:  v.Compliant,
		Violations: v.Violations,
		Reasoning:  v.Reasoning,
	}, nil
}

// buildPrompt renders the spec and policies into a prompt that asks for a JSON
// verdict. Ollama's JSON mode requires the word "JSON" to appear in the
// prompt, and the schema is spelled out so the model fills the right fields.
func buildPrompt(spec map[string]any, policies []string) (string, error) {
	// MarshalIndent sorts map keys, so the same spec always yields the same
	// prompt — worth having when a verdict is cached by a hash of its input.
	specJSON, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal spec: %w", err)
	}

	var b strings.Builder
	b.WriteString("You are a platform policy auditor. Decide whether the resource ")
	b.WriteString("spec below complies with every policy.\n\n")
	b.WriteString("Policies:\n")
	for i, p := range policies {
		fmt.Fprintf(&b, "%d. %s\n", i+1, p)
	}
	b.WriteString("\nResource spec (JSON):\n")
	b.Write(specJSON)
	b.WriteString("\n\nRespond with a single JSON object with exactly these fields:\n")
	b.WriteString(`{"compliant": <bool>, "violations": [<string>], "reasoning": <string>}` + "\n")
	b.WriteString("Set compliant to false if the spec breaks any policy, and name each ")
	b.WriteString("broken policy in violations. Keep reasoning to one or two sentences.\n")
	return b.String(), nil
}
