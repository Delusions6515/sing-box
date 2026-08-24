package smart

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

const SnapshotVersion = 2

const (
	rankCacheTTL  = 10 * time.Second
	maxRankCaches = 256
)

// MetricKey separates every Smart observation by group, target, transport,
// and leaf outbound. An empty target is intentionally not tracked.
type MetricKey struct {
	Group   string `json:"group"`
	Target  string `json:"target"`
	ASN     string `json:"asn,omitempty"`
	Network string `json:"network"`
	Node    string `json:"node"`
}

type Config struct {
	MinSamples     int
	MaxFailedTimes int
	BlockDuration  time.Duration
	MaxEntries     int
	Predict        func(ModelInput) (float64, bool)
	WeightFactor   func(string) float64
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
	LossRate        float64
	LossAvailable   bool
}

type Candidate struct {
	Key     MetricKey
	Weight  float64
	Samples int64
	Blocked bool
	Known   bool
}

// NodeRankItem is a single node's learned weight, matching mihomo's
// /group/{name}/weights response shape. Weight is normalized so the best
// node in the group scores 100.
type NodeRankItem struct {
	Name   string
	Rank   string
	Weight float64
}

type Snapshot struct {
	Version int              `json:"version"`
	Metrics []MetricSnapshot `json:"metrics"`
}

// MetricSnapshot deliberately excludes the in-memory breaker state.
type MetricSnapshot struct {
	Key               MetricKey         `json:"key"`
	Success           int64             `json:"success"`
	Failure           int64             `json:"failure"`
	ConnectMS         float64           `json:"connect_ms,omitempty"`
	LatencyMS         float64           `json:"latency_ms,omitempty"`
	ConnectSamples    int64             `json:"connect_samples,omitempty"`
	LatencySamples    int64             `json:"latency_samples,omitempty"`
	Latest            latestObservation `json:"latest,omitempty"`
	UploadTotalMB     float64           `json:"upload_total_mb,omitempty"`
	DownloadTotalMB   float64           `json:"download_total_mb,omitempty"`
	MaxUploadRateKB   float64           `json:"max_upload_rate_kb,omitempty"`
	MaxDownloadRateKB float64           `json:"max_download_rate_kb,omitempty"`
	DurationMS        float64           `json:"duration_ms,omitempty"`
	DurationSamples   int64             `json:"duration_samples,omitempty"`
	LossRate          float64           `json:"loss_rate,omitempty"`
	LossSamples       int64             `json:"loss_samples,omitempty"`
	LastUsed          time.Time         `json:"last_used"`
}

type latestObservation struct {
	Success           bool          `json:"success"`
	UploadMB          float64       `json:"upload_mb,omitempty"`
	DownloadMB        float64       `json:"download_mb,omitempty"`
	MaxUploadRateKB   float64       `json:"max_upload_rate_kb,omitempty"`
	MaxDownloadRateKB float64       `json:"max_download_rate_kb,omitempty"`
	Duration          time.Duration `json:"duration,omitempty"`
	LossRate          float64       `json:"loss_rate,omitempty"`
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
	UploadTotalMB       float64
	DownloadTotalMB     float64
	MaxUploadRateKB     float64
	MaxDownloadRateKB   float64
	DurationMS          float64
	DurationSamples     int64
	LossRate            float64
	LossSamples         int64
}

func (m *metric) modelInput(key MetricKey) ModelInput {
	return ModelInput{
		Success:                   m.Success,
		Failure:                   m.Failure,
		ConnectTime:               time.Duration(m.ConnectMS * float64(time.Millisecond)),
		Latency:                   time.Duration(m.LatencyMS * float64(time.Millisecond)),
		UploadMB:                  m.Latest.UploadMB,
		DownloadMB:                m.Latest.DownloadMB,
		MaxUploadRateKB:           m.Latest.MaxUploadRateKB,
		MaxDownloadRateKB:         m.Latest.MaxDownloadRateKB,
		ConnectionDuration:        m.Latest.Duration,
		HistoryUploadMB:           m.UploadTotalMB,
		HistoryDownloadMB:         m.DownloadTotalMB,
		HistoryMaxUploadRateKB:    m.MaxUploadRateKB,
		HistoryMaxDownloadRateKB:  m.MaxDownloadRateKB,
		HistoryConnectionDuration: time.Duration(m.DurationMS * float64(time.Millisecond)),
		LossRate:                  m.Latest.LossRate,
		CumulativeLossRate:        m.LossRate,
		LastUsed:                  m.LastUsed,
		ASN:                       key.ASN,
		Target:                    key.Target,
		IsUDP:                     key.Network == "udp",
		ConnectionFailed:          !m.Latest.Success,
	}
}

type Store struct {
	access          sync.RWMutex
	metrics         map[MetricKey]*metric
	rankCache       map[string]rankCacheEntry
	config          Config
	revision        uint64
	flushedRevision uint64
}

type rankCacheEntry struct {
	revision   uint64
	createdAt  time.Time
	keys       []MetricKey
	candidates []Candidate
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
	return &Store{metrics: make(map[MetricKey]*metric), rankCache: make(map[string]rankCacheEntry), config: config}
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
			LossRate:          observation.LossRate,
		}
		entry.UploadTotalMB += entry.Latest.UploadMB
		entry.DownloadTotalMB += entry.Latest.DownloadMB
		entry.MaxUploadRateKB = max(entry.MaxUploadRateKB, entry.Latest.MaxUploadRateKB)
		entry.MaxDownloadRateKB = max(entry.MaxDownloadRateKB, entry.Latest.MaxDownloadRateKB)
		if observation.Duration > 0 {
			entry.DurationMS = updateEWMA(entry.DurationMS, float64(observation.Duration)/float64(time.Millisecond), entry.DurationSamples)
			entry.DurationSamples++
		}
	}
	if observation.LossAvailable {
		entry.LossRate = updateEWMA(entry.LossRate, observation.LossRate, entry.LossSamples)
		entry.LossSamples++
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
	entry := s.metricLocked(key)
	if entry == nil {
		return candidate
	}
	candidate.Samples = entry.Success + entry.Failure
	candidate.Blocked = entry.BlockedUntil.After(now)
	input := entry.modelInput(key)
	weight, insufficient := calculateWeight(input, now, s.config.MinSamples)
	if !insufficient && s.config.Predict != nil {
		if predicted, predictedOK := s.config.Predict(input); predictedOK {
			weight = predicted
		}
	}
	if !insufficient && s.config.WeightFactor != nil {
		weight *= s.config.WeightFactor(key.Node)
	}
	candidate.Weight = weight
	candidate.Known = !insufficient
	return candidate
}

func (s *Store) metricLocked(key MetricKey) *metric {
	entry := s.metrics[key]
	if entry == nil && key.ASN != "" {
		key.ASN = ""
		entry = s.metrics[key]
	}
	return entry
}

// ModelInput returns the current full metric input for collection. ASN-scoped
// lookups fall back to the generic target history until enough ASN samples exist.
func (s *Store) ModelInput(key MetricKey) (ModelInput, bool) {
	s.access.RLock()
	defer s.access.RUnlock()
	entry := s.metricLocked(key)
	if entry == nil {
		return ModelInput{}, false
	}
	return entry.modelInput(key), true
}

// Recover clears elapsed quarantine state. It is safe to call from periodic
// maintenance and lets groups resume candidates without discarding history.
func (s *Store) Recover(now time.Time) {
	s.access.Lock()
	defer s.access.Unlock()
	changed := false
	for _, entry := range s.metrics {
		if !entry.BlockedUntil.IsZero() && !entry.BlockedUntil.After(now) {
			entry.BlockedUntil = time.Time{}
			entry.ConsecutiveFailures = 0
			changed = true
		}
	}
	if changed {
		s.revision++
	}
}

// Prefetch builds cached ranks for all known group/target/network tuples.
// It avoids repeated sort work on hot destinations while Record invalidates
// the cache through the store revision.
func (s *Store) Prefetch(now time.Time, group string) {
	s.access.RLock()
	sets := make(map[string][]MetricKey)
	for key := range s.metrics {
		if key.Group != group {
			continue
		}
		setKey := fmt.Sprintf("%s\x00%s\x00%s", key.Group, key.Target, key.Network)
		sets[setKey] = append(sets[setKey], key)
	}
	s.access.RUnlock()
	for _, keys := range sets {
		s.Rank(now, keys)
	}
}

// Rank returns candidates in descending learned weight. It is stable for ties
// so callers retain their configuration order for equal candidates.
func (s *Store) Rank(now time.Time, keys []MetricKey) []Candidate {
	cacheKey := rankCacheKey(keys)
	s.access.RLock()
	if cached, loaded := s.rankCache[cacheKey]; loaded && cached.revision == s.revision && now.Sub(cached.createdAt) < rankCacheTTL && metricKeysEqual(cached.keys, keys) {
		candidates := append([]Candidate(nil), cached.candidates...)
		s.access.RUnlock()
		return candidates
	}
	candidates := make([]Candidate, len(keys))
	for index, key := range keys {
		candidates[index] = s.candidateLocked(now, key)
	}
	revision := s.revision
	s.access.RUnlock()
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
	s.access.Lock()
	if s.revision == revision {
		if len(s.rankCache) >= maxRankCaches {
			var oldestKey string
			var oldest time.Time
			for key, cached := range s.rankCache {
				if oldest.IsZero() || cached.createdAt.Before(oldest) {
					oldestKey, oldest = key, cached.createdAt
				}
			}
			delete(s.rankCache, oldestKey)
		}
		s.rankCache[cacheKey] = rankCacheEntry{revision: revision, createdAt: now, keys: append([]MetricKey(nil), keys...), candidates: append([]Candidate(nil), candidates...)}
	}
	s.access.Unlock()
	return candidates
}

func rankCacheKey(keys []MetricKey) string {
	var builder strings.Builder
	for _, key := range keys {
		builder.WriteString(key.Group)
		builder.WriteByte(0)
		builder.WriteString(key.Target)
		builder.WriteByte(0)
		builder.WriteString(key.ASN)
		builder.WriteByte(0)
		builder.WriteString(key.Network)
		builder.WriteByte(0)
		builder.WriteString(key.Node)
		builder.WriteByte(0)
	}
	return builder.String()
}

func metricKeysEqual(left, right []MetricKey) bool {
	return len(left) == len(right) && allEqual(left, right)
}

func allEqual(left, right []MetricKey) bool {
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// GroupWeights aggregates a group's learned weights across targets and
// transports, normalized so the best node scores 100. Every node in the
// provided list is included; nodes without enough data score 0. Returns an
// empty slice when no node has a known weight.
func (s *Store) GroupWeights(now time.Time, group string, nodes []string) []NodeRankItem {
	s.access.RLock()
	defer s.access.RUnlock()
	type aggregate struct {
		success, failure           int64
		connectMS, latencyMS       float64
		connectSamples, latSamples int64
		latest                     latestObservation
		lastUsed                   time.Time
		samples                    int64
		udpSamples                 int64
		uploadTotal, downloadTotal float64
		maxUpload, maxDownload     float64
		durationMS                 float64
		durationSamples            int64
		lossRate                   float64
		lossSamples                int64
	}
	byNode := make(map[string]*aggregate)
	for key, entry := range s.metrics {
		if key.Group != group {
			continue
		}
		agg := byNode[key.Node]
		if agg == nil {
			agg = new(aggregate)
			byNode[key.Node] = agg
		}
		agg.success += entry.Success
		agg.failure += entry.Failure
		agg.connectMS = mergeAverage(agg.connectMS, agg.connectSamples, entry.ConnectMS, entry.ConnectSamples)
		agg.connectSamples += entry.ConnectSamples
		agg.latencyMS = mergeAverage(agg.latencyMS, agg.latSamples, entry.LatencyMS, entry.LatencySamples)
		agg.latSamples += entry.LatencySamples
		agg.samples += entry.Success + entry.Failure
		agg.uploadTotal += entry.UploadTotalMB
		agg.downloadTotal += entry.DownloadTotalMB
		agg.maxUpload = max(agg.maxUpload, entry.MaxUploadRateKB)
		agg.maxDownload = max(agg.maxDownload, entry.MaxDownloadRateKB)
		agg.durationMS = mergeAverage(agg.durationMS, agg.durationSamples, entry.DurationMS, entry.DurationSamples)
		agg.durationSamples += entry.DurationSamples
		agg.lossRate = mergeAverage(agg.lossRate, agg.lossSamples, entry.LossRate, entry.LossSamples)
		agg.lossSamples += entry.LossSamples
		if key.Network == "udp" {
			agg.udpSamples += entry.Success + entry.Failure
		}
		if entry.LastUsed.After(agg.lastUsed) {
			agg.lastUsed = entry.LastUsed
			agg.latest = entry.Latest
		}
	}
	items := make([]NodeRankItem, 0, len(nodes))
	var maxWeight float64
	seen := make(map[string]bool, len(nodes))
	for _, node := range nodes {
		if seen[node] {
			continue
		}
		seen[node] = true
		item := NodeRankItem{Name: node}
		if agg := byNode[node]; agg != nil {
			input := ModelInput{
				Success:                   agg.success,
				Failure:                   agg.failure,
				ConnectTime:               time.Duration(agg.connectMS * float64(time.Millisecond)),
				Latency:                   time.Duration(agg.latencyMS * float64(time.Millisecond)),
				UploadMB:                  agg.latest.UploadMB,
				DownloadMB:                agg.latest.DownloadMB,
				MaxUploadRateKB:           agg.latest.MaxUploadRateKB,
				MaxDownloadRateKB:         agg.latest.MaxDownloadRateKB,
				ConnectionDuration:        agg.latest.Duration,
				HistoryUploadMB:           agg.uploadTotal,
				HistoryDownloadMB:         agg.downloadTotal,
				HistoryMaxUploadRateKB:    agg.maxUpload,
				HistoryMaxDownloadRateKB:  agg.maxDownload,
				HistoryConnectionDuration: time.Duration(agg.durationMS * float64(time.Millisecond)),
				LossRate:                  agg.latest.LossRate,
				CumulativeLossRate:        agg.lossRate,
				LastUsed:                  agg.lastUsed,
				IsUDP:                     agg.udpSamples > agg.samples/2,
				ConnectionFailed:          !agg.latest.Success,
			}
			if weight, insufficient := calculateWeight(input, now, s.config.MinSamples); !insufficient {
				if s.config.WeightFactor != nil {
					weight *= s.config.WeightFactor(node)
				}
				item.Weight = weight
			}
		}
		if item.Weight > maxWeight {
			maxWeight = item.Weight
		}
		items = append(items, item)
	}
	if maxWeight <= 0 {
		return nil
	}
	for index := range items {
		items[index].Weight = math.Round(items[index].Weight/maxWeight*100*100) / 100
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Weight != items[j].Weight {
			return items[i].Weight > items[j].Weight
		}
		return items[i].Name < items[j].Name
	})
	return items
}

// Clear drops all learned metrics and resets circuit-breaker state so the
// group relearns from scratch.
func (s *Store) Clear() {
	s.access.Lock()
	defer s.access.Unlock()
	s.metrics = make(map[MetricKey]*metric)
	s.rankCache = make(map[string]rankCacheEntry)
	s.revision++
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
			Key:               key,
			Success:           entry.Success,
			Failure:           entry.Failure,
			ConnectMS:         entry.ConnectMS,
			LatencyMS:         entry.LatencyMS,
			ConnectSamples:    entry.ConnectSamples,
			LatencySamples:    entry.LatencySamples,
			Latest:            entry.Latest,
			UploadTotalMB:     entry.UploadTotalMB,
			DownloadTotalMB:   entry.DownloadTotalMB,
			MaxUploadRateKB:   entry.MaxUploadRateKB,
			MaxDownloadRateKB: entry.MaxDownloadRateKB,
			DurationMS:        entry.DurationMS,
			DurationSamples:   entry.DurationSamples,
			LossRate:          entry.LossRate,
			LossSamples:       entry.LossSamples,
			LastUsed:          entry.LastUsed,
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
	if snapshot.Version == 0 || snapshot.Version > SnapshotVersion {
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
	if snapshot.Version == 0 || snapshot.Version > SnapshotVersion {
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
			Success:           persisted.Success,
			Failure:           persisted.Failure,
			ConnectMS:         persisted.ConnectMS,
			LatencyMS:         persisted.LatencyMS,
			ConnectSamples:    persisted.ConnectSamples,
			LatencySamples:    persisted.LatencySamples,
			Latest:            persisted.Latest,
			UploadTotalMB:     persisted.UploadTotalMB,
			DownloadTotalMB:   persisted.DownloadTotalMB,
			MaxUploadRateKB:   persisted.MaxUploadRateKB,
			MaxDownloadRateKB: persisted.MaxDownloadRateKB,
			DurationMS:        persisted.DurationMS,
			DurationSamples:   persisted.DurationSamples,
			LossRate:          persisted.LossRate,
			LossSamples:       persisted.LossSamples,
			LastUsed:          persisted.LastUsed,
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
	if snapshot.Version == 0 || snapshot.Version > SnapshotVersion {
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
				Success:           persisted.Success,
				Failure:           persisted.Failure,
				ConnectMS:         persisted.ConnectMS,
				LatencyMS:         persisted.LatencyMS,
				ConnectSamples:    persisted.ConnectSamples,
				LatencySamples:    persisted.LatencySamples,
				Latest:            persisted.Latest,
				UploadTotalMB:     persisted.UploadTotalMB,
				DownloadTotalMB:   persisted.DownloadTotalMB,
				MaxUploadRateKB:   persisted.MaxUploadRateKB,
				MaxDownloadRateKB: persisted.MaxDownloadRateKB,
				DurationMS:        persisted.DurationMS,
				DurationSamples:   persisted.DurationSamples,
				LossRate:          persisted.LossRate,
				LossSamples:       persisted.LossSamples,
				LastUsed:          persisted.LastUsed,
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
		current.UploadTotalMB += persisted.UploadTotalMB
		current.DownloadTotalMB += persisted.DownloadTotalMB
		current.MaxUploadRateKB = max(current.MaxUploadRateKB, persisted.MaxUploadRateKB)
		current.MaxDownloadRateKB = max(current.MaxDownloadRateKB, persisted.MaxDownloadRateKB)
		current.DurationMS = mergeAverage(persisted.DurationMS, persisted.DurationSamples, current.DurationMS, current.DurationSamples)
		current.DurationSamples += persisted.DurationSamples
		current.LossRate = mergeAverage(persisted.LossRate, persisted.LossSamples, current.LossRate, current.LossSamples)
		current.LossSamples += persisted.LossSamples
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
