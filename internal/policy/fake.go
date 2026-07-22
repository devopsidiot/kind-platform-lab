package policy

import "context"

// Fake is a Checker with a fixed response, for tests. CLAUDE.md requires that
// the unit and e2e suites never call a real model, so they substitute a Fake
// wherever the function depends on a Checker. It lives outside _test.go so the
// function package and the e2e harness can both use it.
type Fake struct {
	// Verdict is returned by every Check call.
	Verdict Verdict
	// Err, when non-nil, is returned by every Check call alongside Verdict.
	Err error
	// Calls counts how many times Check has been invoked, so a test can assert
	// the function caches verdicts instead of re-checking every reconcile.
	Calls int
}

// Fake implements Checker.
var _ Checker = (*Fake)(nil)

// Check records the invocation and returns the configured Verdict and Err.
func (f *Fake) Check(_ context.Context, _ map[string]any, _ []string) (Verdict, error) {
	f.Calls++
	return f.Verdict, f.Err
}
