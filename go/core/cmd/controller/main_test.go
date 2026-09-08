package main_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Run the compiled test binary in Alpine to check the embedded time zone data.
func TestScheduledRunTimeZones(t *testing.T) {
	for _, zone := range []string{"UTC", "Asia/Shanghai", "America/Los_Angeles"} {
		t.Run(zone, func(t *testing.T) {
			_, err := time.LoadLocation(zone)
			require.NoError(t, err)
		})
	}
}
