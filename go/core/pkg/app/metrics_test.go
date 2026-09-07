package app

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMetricsOptions(t *testing.T) {
	tests := []struct {
		name        string
		address     string
		secure      string
		wantAddress string
		wantSecure  bool
		wantFilter  bool
	}{
		{name: "disabled by default", wantAddress: "0"},
		{name: "explicitly disabled", address: "0", secure: "true", wantAddress: "0", wantSecure: true},
		{name: "http", address: ":8080", secure: "false", wantAddress: ":8080"},
		{name: "https with Kubernetes authentication", address: ":8443", secure: "true", wantAddress: ":8443", wantSecure: true, wantFilter: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("METRICS_BIND_ADDRESS", tt.address)
			t.Setenv("METRICS_SECURE", tt.secure)
			options := metricsOptions()
			assert.Equal(t, tt.wantAddress, options.BindAddress)
			assert.Equal(t, tt.wantSecure, options.SecureServing)
			assert.Equal(t, tt.wantFilter, options.FilterProvider != nil)
		})
	}
}
