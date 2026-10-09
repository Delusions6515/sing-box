package group

import (
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestSmartRateMeasuresPeakWindowNotLifetimeAverage(t *testing.T) {
	var rate smartRate
	now := time.Unix(1000, 0)
	rate.add(now, 1000)
	rate.add(now.Add(100*time.Millisecond), 2000)
	rate.add(now.Add(10*time.Second), 100)
	require.Equal(t, float64(3000), rate.peak())
}
