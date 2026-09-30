package smart

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCumulativeLossUsesPacketCounts(t *testing.T) {
	now := time.Now()
	key := MetricKey{Group: "smart", Target: "example.com", Network: "tcp", Node: "node"}
	store := NewStore(Config{})
	store.Record(now, key, Observation{Closed: true, Success: true, LossAvailable: true, LossRate: 0.1, SentPackets: 100, RetransmittedPackets: 10})
	store.Record(now, key, Observation{Closed: true, Success: true, LossAvailable: true, LossRate: 0, SentPackets: 900})
	input, ok := store.ModelInput(key)
	require.True(t, ok)
	require.InDelta(t, 0.01, input.CumulativeLossRate, 1e-9)
	snapshot := store.Snapshot(now, time.Hour, 100)
	restored := NewStore(Config{})
	require.True(t, restored.Restore(snapshot))
	input, ok = restored.ModelInput(key)
	require.True(t, ok)
	require.InDelta(t, 0.01, input.CumulativeLossRate, 1e-9)
	restored.Merge(snapshot)
	input, _ = restored.ModelInput(key)
	require.InDelta(t, 0.01, input.CumulativeLossRate, 1e-9)
}

func TestStoreRejectsOtherSnapshotVersions(t *testing.T) {
	store := NewStore(Config{})
	key := MetricKey{Group: "smart", Target: "example.com", Network: "tcp", Node: "node"}
	for _, version := range []int{0, 1, 2, SnapshotVersion + 1} {
		snapshot := Snapshot{Version: version, Metrics: []MetricSnapshot{{Key: key, Success: 1}}}
		require.False(t, store.Restore(snapshot))
		require.False(t, store.Merge(snapshot))
	}
	_, loaded := store.ModelInput(key)
	require.False(t, loaded)
}
