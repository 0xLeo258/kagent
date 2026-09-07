package app

import (
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

// metricsOptions uses the chart's existing metrics settings. Secure serving
// authenticates bearer tokens and authorizes access through Kubernetes RBAC.
func metricsOptions() metricsserver.Options {
	options := metricsserver.Options{
		BindAddress:   env("METRICS_BIND_ADDRESS", "0"),
		SecureServing: envBool("METRICS_SECURE"),
	}
	if options.BindAddress != "0" && options.SecureServing {
		options.FilterProvider = filters.WithAuthenticationAndAuthorization
	}
	return options
}
