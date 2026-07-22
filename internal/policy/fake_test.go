package policy

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// The function depends on the Checker interface, not on Ollama, so the suites
// can substitute a Fake. This is the substitution working: a fixed verdict
// comes back with no model in sight.
func TestFakeReturnsConfiguredVerdict(t *testing.T) {
	want := Verdict{
		Compliant:  false,
		Violations: []string{"production must not use the sandbox tier"},
		Reasoning:  "environment is production but tier is sandbox",
	}
	f := &Fake{Verdict: want}

	got, err := f.Check(context.Background(), map[string]any{"environment": "production"}, []string{"a policy"})
	if err != nil {
		t.Fatalf("Check returned unexpected error: %v", err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("verdict mismatch: -want +got:\n%s", diff)
	}
}

// A Fake configured with an error returns it, so a test can drive the caller's
// fail-open path without a real unreachable server.
func TestFakeReturnsConfiguredError(t *testing.T) {
	sentinel := errors.New("model unavailable")
	f := &Fake{Verdict: Verdict{Compliant: true}, Err: sentinel}

	got, err := f.Check(context.Background(), nil, nil)
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
	// Fail-open contract: even on error the verdict is safe to compose against.
	if !got.Compliant {
		t.Errorf("expected a Compliant verdict alongside the error, got %+v", got)
	}
}

// Verdicts are cached by a hash of the spec, so the function must not call the
// model on every reconcile. Counting calls is how a test asserts that.
func TestFakeCountsCalls(t *testing.T) {
	f := &Fake{}
	for i := 0; i < 3; i++ {
		if _, err := f.Check(context.Background(), nil, nil); err != nil {
			t.Fatalf("Check returned unexpected error: %v", err)
		}
	}
	if f.Calls != 3 {
		t.Errorf("Calls = %d, want 3", f.Calls)
	}
}
