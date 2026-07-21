package main

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
)

// hard() calls MustParse, so a malformed quantity in the tier table panics at
// request time rather than at startup. This test is what catches that.
func TestTierQuantitiesParse(t *testing.T) {
	for environment, tr := range tiers {
		t.Run(environment, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("tier %q has an unparseable quantity: %v", environment, r)
				}
			}()

			hard := tr.hard()
			want := []corev1.ResourceName{
				corev1.ResourceLimitsCPU,
				corev1.ResourceLimitsMemory,
				corev1.ResourcePods,
				corev1.ResourceRequestsCPU,
				corev1.ResourceRequestsMemory,
			}
			for _, name := range want {
				if _, ok := hard[name]; !ok {
					t.Errorf("tier %q hard limits missing %q", environment, name)
				}
			}
		})
	}
}

// Requests above limits would be rejected by the API server, so the table must
// not drift into that state.
func TestTierRequestsDoNotExceedLimits(t *testing.T) {
	for environment, tr := range tiers {
		t.Run(environment, func(t *testing.T) {
			hard := tr.hard()

			reqCPU := hard[corev1.ResourceRequestsCPU]
			limCPU := hard[corev1.ResourceLimitsCPU]
			if reqCPU.Cmp(limCPU) > 0 {
				t.Errorf("requests.cpu (%s) exceeds limits.cpu (%s)", reqCPU.String(), limCPU.String())
			}

			reqMem := hard[corev1.ResourceRequestsMemory]
			limMem := hard[corev1.ResourceLimitsMemory]
			if reqMem.Cmp(limMem) > 0 {
				t.Errorf("requests.memory (%s) exceeds limits.memory (%s)", reqMem.String(), limMem.String())
			}
		})
	}
}

// The tiers are meant to be strictly increasing; a demo that shows production
// with a smaller quota than sandbox would be worse than no demo.
func TestTiersIncreaseWithEnvironment(t *testing.T) {
	ordered := []string{"sandbox", "staging", "production"}

	for i := 1; i < len(ordered); i++ {
		lower, upper := tiers[ordered[i-1]].hard(), tiers[ordered[i]].hard()
		for _, name := range []corev1.ResourceName{
			corev1.ResourceLimitsCPU,
			corev1.ResourceLimitsMemory,
			corev1.ResourcePods,
		} {
			l, u := lower[name], upper[name]
			if u.Cmp(l) <= 0 {
				t.Errorf("%s: %s (%s) is not greater than %s (%s)",
					name, ordered[i], u.String(), ordered[i-1], l.String())
			}
		}
	}
}

func TestEnvironmentsIsSortedAndComplete(t *testing.T) {
	want := []string{"production", "sandbox", "staging"}
	if diff := cmp.Diff(want, environments()); diff != "" {
		t.Errorf("environments(): -want +got:\n%s", diff)
	}
}
