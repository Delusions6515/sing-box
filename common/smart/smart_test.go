package smart

import (
	"bytes"
	"encoding/json"
	"math"
	"sync"
	"testing"
	"time"
)

func TestTimeDecayMatchesMihomoHourlyBuckets(t *testing.T) {
	lastUsed := int64(100*3600 + 1800)
	tests := []struct {
		after time.Duration
		want  float64
	}{
		{24 * time.Hour, 1},
		{36 * time.Hour, 0.95},
		{120 * time.Hour, 0.65},
		{444 * time.Hour, 0.4},
		{900 * time.Hour, 0.3},
	}
	for _, test := range tests {
		now := lastUsed - lastUsed%3600 + int64(test.after.Seconds())
		if actual := TimeDecay(lastUsed, now, 0.3); math.Abs(actual-test.want) > 0.000001 {
			t.Fatalf("after %v: got %f, want %f", test.after, actual, test.want)
		}
	}
}

func TestCalculateWeightMatchesMihomoWebVector(t *testing.T) {
	weight, insufficient := CalculateWeight(ModelInput{
		Success:     8,
		Failure:     2,
		ConnectTime: 50 * time.Millisecond,
		Latency:     100 * time.Millisecond,
	}, time.Unix(0, 0))
	if insufficient {
		t.Fatal("sufficient samples were rejected")
	}
	if math.Abs(weight-0.848) > 0.000001 {
		t.Fatalf("got %f, want 0.848", weight)
	}
}

func TestIdentifyConnectionScenes(t *testing.T) {
	tests := []struct {
		name  string
		input ModelInput
		want  Scene
	}{
		{"web", ModelInput{}, SceneWeb},
		{"interactive", ModelInput{Latency: 80 * time.Millisecond, UploadMB: 1, DownloadMB: 1, MaxUploadRateKB: 300, MaxDownloadRateKB: 300, ConnectionDuration: 5 * time.Minute}, SceneInteractive},
		{"streaming", ModelInput{UploadMB: 1, DownloadMB: 70, MaxUploadRateKB: 100, MaxDownloadRateKB: 2500, ConnectionDuration: 2 * time.Minute}, SceneStreaming},
		{"transfer", ModelInput{UploadMB: 200, MaxUploadRateKB: 100, ConnectionDuration: 2 * time.Minute}, SceneTransfer},
	}
	for _, test := range tests {
		if actual := IdentifyConnectionScene(test.input); actual != test.want {
			t.Fatalf("%s: got %v, want %v", test.name, actual, test.want)
		}
	}
}

func TestStoreUsesLatestClosedObservationForScene(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	key := MetricKey{Group: "smart", Target: "video.example", Network: "tcp", Node: "node"}
	store := NewStore(Config{})
	store.Record(now, key, Observation{Closed: true, Success: true, ConnectTime: 20 * time.Millisecond, FirstByte: 40 * time.Millisecond, UploadBytes: 200 * 1024 * 1024, PeakUploadBPS: 5 * 1024 * 1024, Duration: 2 * time.Minute})
	store.Record(now.Add(time.Second), key, Observation{Closed: true, Success: true, ConnectTime: 20 * time.Millisecond, FirstByte: 40 * time.Millisecond, UploadBytes: 1024, PeakUploadBPS: 10 * 1024, Duration: time.Second})

	metric := store.metrics[key]
	if metric == nil || metric.Latest.UploadMB >= 0.005 {
		t.Fatalf("latest closed observation was not retained: %+v", metric)
	}
	if scene := IdentifyConnectionScene(metric.modelInput(key.Network)); scene != SceneWeb {
		t.Fatalf("latest web observation inherited old transfer scene: %v", scene)
	}
}

func TestStoreUsesUDPModelForUDPCandidates(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	store := NewStore(Config{})
	tcp := MetricKey{Group: "smart", Target: "game.example", Network: "tcp", Node: "node"}
	udp := tcp
	udp.Network = "udp"
	observation := Observation{
		Closed:          true,
		Success:         true,
		ConnectTime:     time.Second,
		FirstByte:       80 * time.Millisecond,
		UploadBytes:     1 * 1024 * 1024,
		DownloadBytes:   1 * 1024 * 1024,
		PeakUploadBPS:   300 * 1024,
		PeakDownloadBPS: 300 * 1024,
		Duration:        5 * time.Minute,
	}
	for range 2 {
		store.Record(now, tcp, observation)
		store.Record(now, udp, observation)
	}
	if !store.metrics[udp].modelInput(udp.Network).IsUDP {
		t.Fatal("UDP metric did not reach the UDP scoring model")
	}
	tcpWeight := store.Candidate(now, tcp).Weight
	udpWeight := store.Candidate(now, udp).Weight
	if tcpWeight == udpWeight {
		t.Fatalf("TCP and UDP candidates used the same score: %f", tcpWeight)
	}
}

func TestStoreIsolatesTargetAndNetwork(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	store := NewStore(Config{MaxFailedTimes: 2, BlockDuration: time.Minute})
	tcp := MetricKey{Group: "smart", Target: "one.example", Network: "tcp", Node: "node"}
	udp := tcp
	udp.Network = "udp"
	otherTarget := tcp
	otherTarget.Target = "two.example"
	store.Record(now, tcp, Observation{Success: false})
	store.Record(now, tcp, Observation{Success: false})
	if !store.Candidate(now, tcp).Blocked {
		t.Fatal("failed key was not blocked")
	}
	if store.Candidate(now, udp).Blocked || store.Candidate(now, otherTarget).Blocked {
		t.Fatal("failure state crossed target or network boundaries")
	}
}

func TestStoreSuccessResetsBlocker(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	key := MetricKey{Group: "smart", Target: "example.com", Network: "tcp", Node: "node"}
	store := NewStore(Config{MaxFailedTimes: 2, BlockDuration: time.Minute})
	store.Record(now, key, Observation{Success: false})
	store.Record(now, key, Observation{Success: false})
	store.Record(now.Add(time.Second), key, Observation{Closed: true, Success: true})
	if store.Candidate(now.Add(time.Second), key).Blocked {
		t.Fatal("success did not reset blocker")
	}
	if store.metrics[key].ConsecutiveFailures != 0 {
		t.Fatal("success did not reset failure streak")
	}
	if len(store.Snapshot(now.Add(time.Second), time.Hour, 10).Metrics) != 1 {
		t.Fatal("snapshot lost metric")
	}
}

func TestStoreUsesConfiguredMinimumSamples(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	key := MetricKey{Group: "smart", Target: "example.com", Network: "tcp", Node: "node"}
	store := NewStore(Config{MinSamples: 3})
	store.Record(now, key, Observation{Closed: true, Success: true})
	store.Record(now, key, Observation{Closed: true, Success: true})
	if store.Candidate(now, key).Known {
		t.Fatal("candidate became known before configured sample count")
	}
	store.Record(now, key, Observation{Closed: true, Success: true})
	if !store.Candidate(now, key).Known {
		t.Fatal("candidate stayed unknown at configured sample count")
	}
}

func TestStoreCapsMetricEntriesOnRecord(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	store := NewStore(Config{MaxEntries: 2})
	oldest := MetricKey{Group: "smart", Target: "oldest.example", Network: "tcp", Node: "node"}
	middle := MetricKey{Group: "smart", Target: "middle.example", Network: "tcp", Node: "node"}
	newest := MetricKey{Group: "smart", Target: "newest.example", Network: "tcp", Node: "node"}
	store.Record(now.Add(-2*time.Second), oldest, Observation{Closed: true, Success: true})
	store.Record(now.Add(-time.Second), middle, Observation{Closed: true, Success: true})
	store.Record(now, newest, Observation{Closed: true, Success: true})
	if store.Candidate(now, oldest).Samples != 0 {
		t.Fatal("oldest metric exceeded the configured entry cap")
	}
	if store.Candidate(now, middle).Samples != 1 || store.Candidate(now, newest).Samples != 1 {
		t.Fatal("newer metrics were discarded at the configured entry cap")
	}
}

func TestRestoreDoesNotRestoreBreaker(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	key := MetricKey{Group: "smart", Target: "example.com", Network: "tcp", Node: "node"}
	store := NewStore(Config{MaxFailedTimes: 1, BlockDuration: time.Hour})
	store.Record(now, key, Observation{Success: false})
	if !store.Candidate(now, key).Blocked {
		t.Fatal("test setup did not open breaker")
	}
	restored := NewStore(Config{MaxFailedTimes: 1, BlockDuration: time.Hour})
	if !restored.Restore(store.Snapshot(now, time.Hour, 10)) {
		t.Fatal("snapshot restore rejected its own version")
	}
	if restored.Candidate(now, key).Blocked {
		t.Fatal("runtime breaker state survived restore")
	}
}

func TestSnapshotHonorsRetentionAndLimit(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	store := NewStore(Config{})
	old := MetricKey{Group: "smart", Target: "old", Network: "tcp", Node: "old"}
	newer := MetricKey{Group: "smart", Target: "new", Network: "tcp", Node: "new"}
	store.Record(now.Add(-2*time.Hour), old, Observation{Closed: true, Success: true})
	store.Record(now, newer, Observation{Closed: true, Success: true})
	snapshot := store.Snapshot(now, time.Hour, 1)
	if len(snapshot.Metrics) != 1 || snapshot.Metrics[0].Key != newer {
		t.Fatalf("unexpected snapshot: %+v", snapshot.Metrics)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "" || containsBreakerState(encoded) {
		t.Fatalf("ephemeral breaker state escaped snapshot: %s", encoded)
	}
	store.GC(now, time.Hour, 1)
	if store.Candidate(now, old).Samples != 0 || store.Candidate(now, newer).Samples != 1 {
		t.Fatal("GC did not retain only the eligible newest metric")
	}
}

func TestPruneSnapshotHonorsRetentionAndLimit(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	makeMetric := func(target string, lastUsed time.Time) MetricSnapshot {
		return MetricSnapshot{Key: MetricKey{Group: "smart", Target: target, Network: "tcp", Node: "node"}, Success: 1, LastUsed: lastUsed}
	}
	snapshot := Snapshot{Version: SnapshotVersion, Metrics: []MetricSnapshot{
		makeMetric("expired", now.Add(-2*time.Hour)),
		makeMetric("older", now.Add(-time.Minute)),
		makeMetric("newer", now),
	}}
	pruned := PruneSnapshot(snapshot, now, time.Hour, 1)
	if len(pruned.Metrics) != 1 || pruned.Metrics[0].Key.Target != "newer" {
		t.Fatalf("unexpected pruned snapshot: %+v", pruned.Metrics)
	}
}

func TestRestoreAndMergeHonorStoreEntryCap(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	makeMetric := func(target string, lastUsed time.Time) MetricSnapshot {
		return MetricSnapshot{Key: MetricKey{Group: "smart", Target: target, Network: "tcp", Node: "node"}, Success: 1, LastUsed: lastUsed}
	}
	snapshot := Snapshot{Version: SnapshotVersion, Metrics: []MetricSnapshot{
		makeMetric("oldest", now.Add(-2*time.Minute)),
		makeMetric("middle", now.Add(-time.Minute)),
		makeMetric("newest", now),
	}}
	restored := NewStore(Config{MaxEntries: 2})
	if !restored.Restore(snapshot) {
		t.Fatal("restore rejected a valid snapshot")
	}
	if restored.Candidate(now, snapshot.Metrics[0].Key).Samples != 0 {
		t.Fatal("restore retained a metric over the configured cap")
	}
	for _, metric := range snapshot.Metrics[1:] {
		if restored.Candidate(now, metric.Key).Samples != 1 {
			t.Fatalf("restore dropped retained metric %s", metric.Key.Target)
		}
	}
	merged := NewStore(Config{MaxEntries: 2})
	merged.Record(now.Add(-3*time.Minute), makeMetric("existing", now.Add(-3*time.Minute)).Key, Observation{Closed: true, Success: true})
	if !merged.Merge(snapshot) {
		t.Fatal("merge rejected a valid snapshot")
	}
	if merged.Candidate(now, makeMetric("existing", now).Key).Samples != 0 {
		t.Fatal("merge retained an old metric over the configured cap")
	}
	for _, metric := range snapshot.Metrics[1:] {
		if merged.Candidate(now, metric.Key).Samples != 1 {
			t.Fatalf("merge dropped retained metric %s", metric.Key.Target)
		}
	}
}

func TestStoreTracksPendingChanges(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	key := MetricKey{Group: "smart", Target: "example.com", Network: "tcp", Node: "node"}
	store := NewStore(Config{})
	if store.HasPendingChanges() {
		t.Fatal("new store is unexpectedly dirty")
	}
	store.Record(now, key, Observation{Closed: true, Success: true})
	if !store.HasPendingChanges() {
		t.Fatal("record did not mark store dirty")
	}
	_, revision := store.SnapshotAndRevision(now, time.Hour, 10)
	store.MarkFlushed(revision)
	if store.HasPendingChanges() {
		t.Fatal("flushed snapshot left store dirty")
	}
	store.GC(now.Add(2*time.Hour), time.Hour, 10)
	if !store.HasPendingChanges() {
		t.Fatal("GC deletion did not mark store dirty")
	}
}

func containsBreakerState(snapshot []byte) bool {
	return bytes.Contains(snapshot, []byte("blocked_until")) || bytes.Contains(snapshot, []byte("consecutive_failures"))
}

func TestStoreConcurrentRecordRankAndSnapshot(t *testing.T) {
	store := NewStore(Config{})
	key := MetricKey{Group: "smart", Target: "example.com", Network: "tcp", Node: "node"}
	now := time.Unix(1_000_000, 0)
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			for range 100 {
				store.Record(now, key, Observation{Closed: true, Success: true, ConnectTime: time.Millisecond})
				_ = store.Rank(now, []MetricKey{key})
				_ = store.Snapshot(now, time.Hour, 100)
			}
		}()
	}
	group.Wait()
	if candidate := store.Candidate(now, key); candidate.Samples != 800 {
		t.Fatalf("got %d samples, want 800", candidate.Samples)
	}
}
