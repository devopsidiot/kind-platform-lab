package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// isUnreachable reports whether err looks like the cluster cannot be reached
// at all — a stopped kind cluster, a stale kubeconfig, a network failure —
// rather than the API server answering with an error.
func isUnreachable(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	// client-go wraps dial failures in *url.Error.
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	return apierrors.IsServiceUnavailable(err) ||
		apierrors.IsServerTimeout(err) ||
		apierrors.IsTimeout(err)
}

// clusterError rewrites unreachable-cluster errors into a message the model
// can relay verbatim; every other error passes through unchanged.
func clusterError(err error) error {
	if isUnreachable(err) {
		return fmt.Errorf("cannot reach the cluster (%v); check that the kind "+
			"cluster is running and the current kubeconfig context points at it", err)
	}
	return err
}
