package main

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/crossplane/function-sdk-go/errors"
	"github.com/crossplane/function-sdk-go/logging"
	fnv1 "github.com/crossplane/function-sdk-go/proto/v1"
	"github.com/crossplane/function-sdk-go/request"
	"github.com/crossplane/function-sdk-go/resource"
	"github.com/crossplane/function-sdk-go/resource/composed"
	"github.com/crossplane/function-sdk-go/response"
)

// Composed resource names. These are the keys Crossplane uses to correlate
// desired with observed resources across reconciles, so they must be stable.
const (
	resourceNameConfigMap     = resource.Name("configmap")
	resourceNameNamespace     = resource.Name("namespace")
	resourceNameResourceQuota = resource.Name("resourcequota")
)

// Fields we read off the XR.
const (
	pathAppName     = "spec.appName"
	pathEnvironment = "spec.environment"
)

// Labels applied to every composed resource, so an operator can trace a
// resource back to the XR that produced it.
const (
	labelAppName     = "platform.devopsidiot.io/app"
	labelEnvironment = "platform.devopsidiot.io/environment"
)

func init() {
	// composed.From resolves an object's GVK through this scheme, so the core
	// types we compose have to be registered in it.
	_ = corev1.AddToScheme(composed.Scheme)
}

// Function composes the resources backing an XAppEnvironment.
type Function struct {
	fnv1.UnimplementedFunctionRunnerServiceServer

	log logging.Logger
}

// NewFunction returns a Function that logs to the supplied logger.
func NewFunction(log logging.Logger) *Function {
	return &Function{log: log}
}

// RunFunction composes a Namespace, a ConfigMap and a ResourceQuota for the
// XAppEnvironment in the request. The quota is tiered by spec.environment.
func (f *Function) RunFunction(_ context.Context, req *fnv1.RunFunctionRequest) (*fnv1.RunFunctionResponse, error) {
	rsp := response.To(req, response.DefaultTTL)

	xr, err := request.GetObservedCompositeResource(req)
	if err != nil {
		response.Fatal(rsp, errors.Wrap(err, "cannot get observed composite resource"))
		return rsp, nil
	}

	appName, err := xr.Resource.GetString(pathAppName)
	if err != nil {
		response.Fatal(rsp, errors.Wrapf(err, "cannot read %s", pathAppName))
		return rsp, nil
	}

	environment, err := xr.Resource.GetString(pathEnvironment)
	if err != nil {
		response.Fatal(rsp, errors.Wrapf(err, "cannot read %s", pathEnvironment))
		return rsp, nil
	}

	t, ok := tiers[environment]
	if !ok {
		response.Fatal(rsp, errors.Errorf("unsupported %s %q: must be one of %v", pathEnvironment, environment, environments()))
		return rsp, nil
	}

	// Start from the desired state accumulated by earlier functions in the
	// pipeline rather than replacing it.
	desired, err := request.GetDesiredComposedResources(req)
	if err != nil {
		response.Fatal(rsp, errors.Wrap(err, "cannot get desired composed resources"))
		return rsp, nil
	}

	namespace := namespaceName(appName, environment)
	labels := map[string]string{
		labelAppName:     appName,
		labelEnvironment: environment,
	}

	for name, obj := range map[resource.Name]runtime.Object{
		resourceNameConfigMap:     newConfigMap(namespace, labels, appName, environment, t),
		resourceNameNamespace:     newNamespace(namespace, labels),
		resourceNameResourceQuota: newResourceQuota(namespace, labels, t),
	} {
		cd, err := composed.From(obj)
		if err != nil {
			response.Fatal(rsp, errors.Wrapf(err, "cannot convert %q to composed resource", name))
			return rsp, nil
		}
		desired[name] = &resource.DesiredComposed{Resource: cd}
	}

	if err := response.SetDesiredComposedResources(rsp, desired); err != nil {
		response.Fatal(rsp, errors.Wrap(err, "cannot set desired composed resources"))
		return rsp, nil
	}

	f.log.Debug("Composed app environment", "app", appName, "environment", environment, "namespace", namespace)
	response.Normalf(rsp, "Composed %s environment for app %q in namespace %q", environment, appName, namespace)

	return rsp, nil
}

// namespaceName is the namespace every composed resource lands in.
func namespaceName(appName, environment string) string {
	return fmt.Sprintf("%s-%s", appName, environment)
}

func newNamespace(name string, labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Labels: labels,
			Name:   name,
		},
	}
}

func newConfigMap(namespace string, labels map[string]string, appName, environment string, t tier) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Labels:    labels,
			Name:      appName + "-config",
			Namespace: namespace,
		},
		Data: map[string]string{
			"app":         appName,
			"environment": environment,
			"tier.pods":   t.Pods,
		},
	}
}

func newResourceQuota(namespace string, labels map[string]string, t tier) *corev1.ResourceQuota {
	return &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{
			Labels:    labels,
			Name:      "compute-quota",
			Namespace: namespace,
		},
		Spec: corev1.ResourceQuotaSpec{
			Hard: t.hard(),
		},
	}
}
