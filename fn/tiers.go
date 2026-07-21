package main

import (
	"sort"

	corev1 "k8s.io/api/core/v1"
	k8sresource "k8s.io/apimachinery/pkg/api/resource"
)

// A tier is the set of ResourceQuota hard limits applied to an environment.
//
// Quantities are held as strings so the table below reads like the YAML a
// platform engineer would otherwise hand-write.
type tier struct {
	LimitsCPU      string
	LimitsMemory   string
	Pods           string
	RequestsCPU    string
	RequestsMemory string
}

// tiers maps spec.environment to its quota. Adding an environment here is the
// only change needed to support it; RunFunction rejects anything absent.
var tiers = map[string]tier{
	"sandbox": {
		LimitsCPU:      "2",
		LimitsMemory:   "4Gi",
		Pods:           "10",
		RequestsCPU:    "1",
		RequestsMemory: "2Gi",
	},
	"staging": {
		LimitsCPU:      "8",
		LimitsMemory:   "16Gi",
		Pods:           "50",
		RequestsCPU:    "4",
		RequestsMemory: "8Gi",
	},
	"production": {
		LimitsCPU:      "32",
		LimitsMemory:   "64Gi",
		Pods:           "200",
		RequestsCPU:    "16",
		RequestsMemory: "32Gi",
	},
}

// environments returns the supported environment names, sorted, for use in
// error messages.
func environments() []string {
	names := make([]string, 0, len(tiers))
	for name := range tiers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// hard renders the tier as the ResourceQuota hard limits.
//
// The quantities are compile-time constants in the table above, so a parse
// failure is a programming error rather than bad user input.
func (t tier) hard() corev1.ResourceList {
	return corev1.ResourceList{
		corev1.ResourceLimitsCPU:      k8sresource.MustParse(t.LimitsCPU),
		corev1.ResourceLimitsMemory:   k8sresource.MustParse(t.LimitsMemory),
		corev1.ResourcePods:           k8sresource.MustParse(t.Pods),
		corev1.ResourceRequestsCPU:    k8sresource.MustParse(t.RequestsCPU),
		corev1.ResourceRequestsMemory: k8sresource.MustParse(t.RequestsMemory),
	}
}
