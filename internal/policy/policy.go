// Package policy provides an advisory, LLM-backed check that an XR's spec
// complies with a set of natural-language policies.
//
// The check is advisory by design (see CLAUDE.md's Phase 2 constraints): a
// Checker never blocks composition. Callers must fail open — on a non-nil
// error, treat the returned Verdict as "check unavailable" and compose
// normally. Every Checker in this package upholds that contract by returning a
// Compliant verdict alongside any error, so a caller that logs the error and
// carries on still composes.
package policy

import "context"

// Verdict is the structured result of checking a spec against policies.
//
// The JSON tags let a caller cache a verdict verbatim, e.g. in an annotation.
type Verdict struct {
	// Compliant is true when the spec breaks none of the supplied policies.
	Compliant bool `json:"compliant"`
	// Violations names the policies the spec breaks, one entry each. It is
	// empty when Compliant is true.
	Violations []string `json:"violations,omitempty"`
	// Reasoning is a short human-readable explanation of the verdict.
	Reasoning string `json:"reasoning,omitempty"`
}

// Checker evaluates an XR spec against a set of natural-language policies.
//
// spec holds the policy-relevant fields of the XR; policies are the rules to
// evaluate it against. Implementations must honour the deadline on ctx and
// must not block composition: on any failure they return a non-nil error
// together with a Compliant Verdict, so a caller that fails open composes
// normally.
type Checker interface {
	Check(ctx context.Context, spec map[string]any, policies []string) (Verdict, error)
}
