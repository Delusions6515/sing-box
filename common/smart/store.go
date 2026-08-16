package smart

import (
	"sort"
	"sync"
	"time"
)

const SnapshotVersion = 1

// MetricKey separates every Smart observation by group, target, transport,
// and leaf outbound. An empty target is intentionally not tracked.
type MetricKey struct {
	Group   string `json:"group"`
	Target  string `json:"target"`
	Network string `json:"network"`
	Node    string `json:"node"`
}

type Config struct {
	MinSamples     int
	MaxFailedTimes int
	BlockDuration  time.Duration
	MaxEntries     int
}

// Observation is supplied either for a failed dial or a closed connection.
// Closed observations update traffic data used by scene detection.
type Observation struct {
	Closed          bool
	Success         bool
	ConnectTime     time.Duration
	FirstByte       time.Duration
	UploadBytes     int64
	DownloadBytes   int64
	PeakUploadBPS   float64
	PeakDownloadBPS float64
	Duration        time.Duration
}

type Candidate struct {
	Key     MetricKey
	Weight  float64
	Samples int64
	Blocked bool
	Known   bool
}

type Snapshot struct {
	Version int              `json:"version"`
	Metrics []MetricSnapshot `json:"metrics"`
}

// MetricSnapshot deliberately excludes the in-memory breaker state.
type MetricSnapshot struct {
	Key            MetricKey         `json:"key"`
	Success        int64             `json:"success"`
	Failure        int64             `json:"failure"`
	ConnectMS      float64           `json:"connect_ms,omitempty"`
	LatencyMS      float64           `json:"latency_ms,omitempty"`
	ConnectSamples int64             `json:"connect_samples,omitempty"`
	LatencySamples int64             `json:"latency_samples,omitempty"`
	Latest         latestObservation `json:"latest,omitempty"`
	LastUsed       time.Time         `json:"last_used"`
}

type latestObservation struct {
	Success           bool          `json:"success"`
	UploadMB          float64       `json:"upload_mb,omitempty"`
	DownloadMB        float64       `json:"download_mb,omitempty"`
	MaxUploadRateKB   float64       `json:"max_upload_rate_kb,omitempty"`
	MaxDownloadRateKB float64       `json:"max_download_rate_kb,omitempty"`
	Duration          time.Duration `json:"duration,omitempty"`
}

type metric struct {
	Success             int64
	Failure             int64
	ConnectMS           float64
	LatencyMS           float64
	ConnectSamples      int64
	LatencySamples      int64
	Latest              latestObservation
	LastUsed            time.Time
	ConsecutiveFailures int
	BlockedUntil        time.Time
}

func (m *metric) modelInput(network string) ModelInput {
	return ModelInput{
		Success:            m.Success,
		Failure:            m.Failure,
		ConnectTime:        time.Duration(m.ConnectMS * float64(time.Millisecond)),
		Latency:            time.Duration(m.LatencyMS * float64(time.Millisecond)),
		UploadMB:           m.Latest.UploadMB,
		DownloadMB:         m.Latest.DownloadMB,
		MaxUploadRateKB:    m.Latest.MaxUploadRateKB,
		MaxDownloadRateKB:  m.Latest.MaxDownloadRateKB,
		ConnectionDuration: m.Latest.Duration,
		LastUsed:           m.LastUsed,
		IsUDP:              network == "udp",
		ConnectionFailed:   !m.Latest.Success,
	}
}

type Store struct {
	access          sync.RWMutex
	metrics         map[MetricKey]*metric
	config          Config
	revision        uint64
	flushedRevision uint64
}

func NewStore(config Config) *Store {
	if config.MinSamples <= 0 {
		config.MinSamples = DefaultMinSamples
	}
	if config.MaxFailedTimes <= 0 {
		config.MaxFailedTimes = 3
	}
	if config.BlockDuration <= 0 {
		config.BlockDuration = 10 * time.Minute
	}
	return &Store{metrics: make(map[MetricKey]*metric), config: config}
}

func (s *Store) Record(now time.Time, key MetricKey, observation Observation) {
	if key.Target == "" {
		return
	}
	s.access.Lock()
	defer s.access.Unlock()
	entry := s.metrics[key]
	if entry == nil {
		s.trimLocked(s.config.MaxEntries)
		entry = new(metric)
		s.metrics[key] = entry
	}
	entry.LastUsed = now
	if observation.Success {
		entry.Success++
		entry.ConsecutiveFailures = 0
		entry.BlockedUntil = time.Time{}
	} else {
		entry.Failure++
		entry.ConsecutiveFailures++
		if entry.ConsecutiveFailures >= s.config.MaxFailedTimes {
			entry.BlockedUntil = now.Add(s.config.BlockDuration)
		}
	}
	if observation.ConnectTime > 0 {
		entry.ConnectMS = updateEWMA(entry.ConnectMS, float64(observation.ConnectTime)/float64(time.Millisecond), entry.ConnectSamples)
		entry.ConnectSamples++
	}
	if observation.FirstByte > 0 {
		entry.LatencyMS = updateEWMA(entry.LatencyMS, float64(observation.FirstByte)/float64(time.Millisecond), entry.LatencySamples)
		entry.LatencySamples++
	}
	if observation.Closed {
		entry.Latest = latestObservation{
			Success:           observation.Success,
			UploadMB:          float64(observation.UploadBytes) / (1024 * 1024),
			DownloadMB:        float64(observation.DownloadBytes) / (1024 * 1024),
			MaxUploadRateKB:   observation.PeakUploadBPS / 1024,
			MaxDownloadRateKB: observation.PeakDownloadBPS / 1024,
			Duration:          observation.Duration,
		}
	}
	s.revision++
}

func (s *Store) Candidate(now time.Time, key MetricKey) Candidate {
	s.access.RLock()
	defer s.access.RUnlock()
	return s.candidateLocked(now, key)
}

func (s *Store) candidateLocked(now time.Time, key MetricKey) Candidate {
	candidate := Candidate{Key: key}
	entry := s.metrics[key]
	if entry == nil {
		return candidate
	}
	candidate.Samples = entry.Success + entry.Failure
	candidate.Blocked = entry.BlockedUntil.After(now)
	weight, insufficient := calculateWeight(entry.modelInput(key.Network), now, s.config.MinSamples)
	candidate.Weight = weight
	candidate.Known = !insufficient
	return candidate
}

// Rank returns candidates in descending learned weight. It is stable for ties
// so callers retain their configuration order for equal candidates.
func (s *Store) Rank(now time.Time, keys []MetricKey) []Candidate {
	s.access.RLock()
	defer s.access.RUnlock()
	candidates := make([]Candidate, len(keys))
	for index, key := range keys {
		candidates[index] = s.candidateLocked(now, key)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if left.Blocked != right.Blocked {
			return !left.Blocked
		}
		if left.Known != right.Known {
			return left.Known
		}
		return left.Weight > right.Weight
	})
	return candidates
}

// GC removes expired entries and then oldest entries over maxEntries.
func (s *Store) GC(now time.Time, retention time.Duration, maxEntries int) {
	s.access.Lock()
	defer s.access.Unlock()
	removed := false
	if retention > 0 {
		for key, entry := range s.metrics {
			if !entry.LastUsed.IsZero() && now.Sub(entry.LastUsed) > retention {
				delete(s.metrics, key)
				removed = true
			}
		}
	}
	if maxEntries <= 0 || len(s.metrics) <= maxEntries {
		if removed {
			s.revision++
		}
		return
	}
	type item struct {
		key      MetricKey
		lastUsed time.Time
	}
	items := make([]item, 0, len(s.metrics))
	for key, entry := range s.metrics {
		items = append(items, item{key, entry.LastUsed})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].lastUsed.After(items[j].lastUsed) })
	for _, item := range items[maxEntries:] {
		delete(s.metrics, item.key)
		removed = true
	}
	if removed {
		s.revision++
	}
}

func (s *Store) Snapshot(now time.Time, retention time.Duration, maxEntries int) Snapshot {
	snapshot, _ := s.SnapshotAndRevision(now, retention, maxEntries)
	return snapshot
}

func (s *Store) SnapshotAndRevision(now time.Time, retention time.Duration, maxEntries int) (Snapshot, uint64) {
	s.access.RLock()
	revision := s.revision
	metrics := make([]MetricSnapshot, 0, len(s.metrics))
	for key, entry := range s.metrics {
		if retention > 0 && !entry.LastUsed.IsZero() && now.Sub(entry.LastUsed) > retention {
			continue
		}
		metrics = append(metrics, MetricSnapshot{
			Key:            key,
			Success:        entry.Success,
			Failure:        entry.Failure,
			ConnectMS:      entry.ConnectMS,
			LatencyMS:      entry.LatencyMS,
			ConnectSamples: entry.ConnectSamples,
			LatencySamples: entry.LatencySamples,
			Latest:         entry.Latest,
			LastUsed:       entry.LastUsed,
		})
	}
	s.access.RUnlock()
	sort.Slice(metrics, func(i, j int) bool { return metrics[i].LastUsed.After(metrics[j].LastUsed) })
	if maxEntries > 0 && len(metrics) > maxEntries {
		metrics = metrics[:maxEntries]
	}
	return Snapshot{Version: SnapshotVersion, Metrics: metrics}, revision
}

// PruneSnapshot filters expired and invalid metrics before they enter the
// Store. Loading an oversized history file must not temporarily retain every
// decoded metric in memory.
func PruneSnapshot(snapshot Snapshot, now time.Time, retention time.Duration, maxEntries int) Snapshot {
	if snapshot.Version != SnapshotVersion {
		return snapshot
	}
	metrics := make([]MetricSnapshot, 0, len(snapshot.Metrics))
	for _, metric := range snapshot.Metrics {
		if metric.Key.Target == "" || retention > 0 && !metric.LastUsed.IsZero() && now.Sub(metric.LastUsed) > retention {
			continue
		}
		metrics = append(metrics, metric)
	}
	sort.SliceStable(metrics, func(i, j int) bool {
		return metrics[i].LastUsed.After(metrics[j].LastUsed)
	})
	if maxEntries > 0 && len(metrics) > maxEntries {
		metrics = metrics[:maxEntries]
	}
	snapshot.Metrics = metrics
	return snapshot
}

func (s *Store) HasPendingChanges() bool {
	s.access.RLock()
	defer s.access.RUnlock()
	return s.revision != s.flushedRevision
}

func (s *Store) MarkFlushed(revision uint64) {
	s.access.Lock()
	defer s.access.Unlock()
	s.flushedRevision = max(s.flushedRevision, revision)
}

// Restore replaces persisted metric data. Circuit-breaker state remains empty
// by design because it is runtime-only.
func (s *Store) Restore(snapshot Snapshot) bool {
	if snapshot.Version != SnapshotVersion {
		return false
	}
	snapshot = PruneSnapshot(snapshot, time.Time{}, 0, s.config.MaxEntries)
	s.access.Lock()
	defer s.access.Unlock()
	s.metrics = make(map[MetricKey]*metric, len(snapshot.Metrics))
	for _, persisted := range snapshot.Metrics {
		if persisted.Key.Target == "" {
			continue
		}
		s.metrics[persisted.Key] = &metric{
			Success:        persisted.Success,
			Failure:        persisted.Failure,
			ConnectMS:      persisted.ConnectMS,
			LatencyMS:      persisted.LatencyMS,
			ConnectSamples: persisted.ConnectSamples,
			LatencySamples: persisted.LatencySamples,
			Latest:         persisted.Latest,
			LastUsed:       persisted.LastUsed,
		}
	}
	s.revision = 0
	s.flushedRevision = 0
	return true
}

// Merge restores persisted metrics without discarding observations recorded
// while a shared history file was temporarily unreadable. Breaker state is
// always kept in memory because it is intentionally not persisted.
func (s *Store) Merge(snapshot Snapshot) bool {
	if snapshot.Version != SnapshotVersion {
		return false
	}
	snapshot = PruneSnapshot(snapshot, time.Time{}, 0, s.config.MaxEntries)
	s.access.Lock()
	defer s.access.Unlock()
	for _, persisted := range snapshot.Metrics {
		if persisted.Key.Target == "" {
			continue
		}
		current := s.metrics[persisted.Key]
		if current == nil {
			s.trimLocked(s.config.MaxEntries)
			s.metrics[persisted.Key] = &metric{
				Success:        persisted.Success,
				Failure:        persisted.Failure,
				ConnectMS:      persisted.ConnectMS,
				LatencyMS:      persisted.LatencyMS,
				ConnectSamples: persisted.ConnectSamples,
				LatencySamples: persisted.LatencySamples,
				Latest:         persisted.Latest,
				LastUsed:       persisted.LastUsed,
			}
			continue
		}
		current.ConnectMS = mergeAverage(persisted.ConnectMS, persisted.ConnectSamples, current.ConnectMS, current.ConnectSamples)
		current.LatencyMS = mergeAverage(persisted.LatencyMS, persisted.LatencySamples, current.LatencyMS, current.LatencySamples)
		current.Success += persisted.Success
		current.Failure += persisted.Failure
		current.ConnectSamples += persisted.ConnectSamples
		current.LatencySamples += persisted.LatencySamples
		if persisted.LastUsed.After(current.LastUsed) {
			current.LastUsed = persisted.LastUsed
			current.Latest = persisted.Latest
		}
	}
	return true
}

func mergeAverage(left float64, leftSamples int64, right float64, rightSamples int64) float64 {
	if leftSamples <= 0 {
		return right
	}
	if rightSamples <= 0 {
		return left
	}
	return (left*float64(leftSamples) + right*float64(rightSamples)) / float64(leftSamples+rightSamples)
}

func (s *Store) trimLocked(maxEntries int) {
	if maxEntries <= 0 || len(s.metrics) < maxEntries {
		return
	}
	var oldestKey MetricKey
	var oldest time.Time
	for key, entry := range s.metrics {
		if oldest.IsZero() || entry.LastUsed.Before(oldest) {
			oldestKey = key
			oldest = entry.LastUsed
		}
	}
	delete(s.metrics, oldestKey)
}

func updateEWMA(current, value float64, samples int64) float64 {
	if samples <= 0 {
		return value
	}
	alpha := 2 / (float64(min(samples, 9)) + 2)
	return current + alpha*(value-current)
}
