package group

import (
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/smart"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/batch"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/x/list"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/filemanager"
)

const (
	defaultSmartURL               = "https://www.gstatic.com/generate_204"
	defaultSmartInterval          = 10 * time.Minute
	defaultSmartTimeout           = 5 * time.Second
	defaultSmartTolerance         = 50
	defaultSmartMaxSelected       = 10
	defaultSmartMaxFailedTimes    = 3
	defaultSmartHistoryRetention  = 7 * 24 * time.Hour
	defaultSmartMaxHistoryEntries = 50000
	smartFlushInterval            = 5 * time.Minute
	smartGCInterval               = 2 * time.Hour
)

func RegisterSmart(registry *outbound.Registry) {
	outbound.Register[option.SmartOutboundOptions](registry, C.TypeSmart, NewSmart)
}

var (
	_ adapter.SmartGroup   = (*Smart)(nil)
	_ adapter.URLTestGroup = (*Smart)(nil)
)

type Smart struct {
	outbound.Adapter
	ctx        context.Context
	outbound   adapter.OutboundManager
	connection adapter.ConnectionManager
	logger     log.ContextLogger
	history    *urltest.HistoryStorage

	tags            []string
	provider        adapter.ProviderManager
	providers       map[string]adapter.Provider
	providerHandles map[string]*list.Element[adapter.ProviderUpdateCallback]
	providerTags    []string
	outboundsCache  map[string][]adapter.Outbound
	exclude         *regexp.Regexp
	include         *regexp.Regexp
	useAllProviders bool
	providerAccess  sync.Mutex

	candidateAccess sync.RWMutex
	candidates      []adapter.Outbound

	store             *smart.Store
	url               string
	interval          time.Duration
	timeout           time.Duration
	tolerance         uint16
	maxSelected       int
	historyPath       string
	historyPoolKey    string
	historyRetention  time.Duration
	maxHistoryEntries int
	historyEntry      *smartHistoryEntry
	activeAccess      sync.Mutex
	activeConnections map[io.Closer]struct{}

	statusAccess sync.RWMutex
	status       adapter.SmartGroupStatus

	probeAccess sync.Mutex
	probing     bool
	closed      atomic.Bool
	cancel      context.CancelFunc
	worker      sync.WaitGroup
}

func NewSmart(ctx context.Context, _ adapter.Router, logger log.ContextLogger, tag string, options option.SmartOutboundOptions) (adapter.Outbound, error) {
	if options.Interval < 0 || options.Timeout < 0 || options.HistoryRetention < 0 || options.MaxSelected < 0 || options.MinSamples < 0 || options.MaxFailedTimes < 0 || options.MaxHistoryEntries < 0 {
		return nil, E.New("invalid smart option")
	}
	interval := time.Duration(options.Interval)
	if interval == 0 {
		interval = defaultSmartInterval
	}
	timeout := time.Duration(options.Timeout)
	if timeout == 0 {
		timeout = defaultSmartTimeout
	}
	maxSelected := options.MaxSelected
	if maxSelected == 0 {
		maxSelected = defaultSmartMaxSelected
	}
	minSamples := options.MinSamples
	if minSamples == 0 {
		minSamples = smart.DefaultMinSamples
	}
	maxFailedTimes := options.MaxFailedTimes
	if maxFailedTimes == 0 {
		maxFailedTimes = defaultSmartMaxFailedTimes
	}
	historyRetention := time.Duration(options.HistoryRetention)
	if historyRetention == 0 {
		historyRetention = defaultSmartHistoryRetention
	}
	maxHistoryEntries := options.MaxHistoryEntries
	if maxHistoryEntries == 0 {
		maxHistoryEntries = defaultSmartMaxHistoryEntries
	}
	historyPath := options.HistoryPath
	if historyPath == "" {
		historyPath = "smart-history.json"
	}
	url := options.URL
	if url == "" {
		url = defaultSmartURL
	}
	tolerance := options.Tolerance
	if tolerance == 0 {
		tolerance = defaultSmartTolerance
	}
	return &Smart{
		Adapter:           outbound.NewAdapter(C.TypeSmart, tag, []string{N.NetworkTCP, N.NetworkUDP}, options.Outbounds),
		ctx:               ctx,
		outbound:          service.FromContext[adapter.OutboundManager](ctx),
		connection:        service.FromContext[adapter.ConnectionManager](ctx),
		logger:            logger,
		history:           service.PtrFromContext[urltest.HistoryStorage](ctx),
		tags:              slices.Clone(options.Outbounds),
		provider:          service.FromContext[adapter.ProviderManager](ctx),
		providers:         make(map[string]adapter.Provider),
		providerHandles:   make(map[string]*list.Element[adapter.ProviderUpdateCallback]),
		providerTags:      slices.Clone(options.Providers),
		outboundsCache:    make(map[string][]adapter.Outbound),
		exclude:           (*regexp.Regexp)(options.Exclude),
		include:           (*regexp.Regexp)(options.Include),
		useAllProviders:   options.UseAllProviders,
		store:             smart.NewStore(smart.Config{MinSamples: minSamples, MaxFailedTimes: maxFailedTimes, BlockDuration: interval, MaxEntries: maxHistoryEntries}),
		url:               url,
		interval:          interval,
		timeout:           timeout,
		tolerance:         tolerance,
		maxSelected:       maxSelected,
		historyPath:       historyPath,
		historyPoolKey:    filepath.Clean(filemanager.BasePath(ctx, historyPath)),
		historyRetention:  historyRetention,
		maxHistoryEntries: maxHistoryEntries,
	}, nil
}

func (s *Smart) Start() error {
	if s.outbound == nil {
		return E.New("missing outbound manager")
	}
	if len(s.tags)+len(s.providerTags) == 0 && !s.useAllProviders {
		return E.New("missing outbound and provider tags")
	}
	if s.provider != nil {
		if s.useAllProviders {
			for _, provider := range s.provider.Providers() {
				tag := provider.Tag()
				s.providerTags = append(s.providerTags, tag)
				s.providers[tag] = provider
				s.providerHandles[tag] = provider.RegisterCallback(s.onProviderUpdated)
			}
		} else {
			for index, tag := range s.providerTags {
				provider, loaded := s.provider.Get(tag)
				if !loaded {
					return E.New("outbound provider ", index, " not found: ", tag)
				}
				s.providers[tag] = provider
				s.providerHandles[tag] = provider.RegisterCallback(s.onProviderUpdated)
			}
		}
	}
	if err := s.rebuildCandidates(""); err != nil {
		return err
	}
	if err := s.loadHistory(); err != nil {
		s.warnSmartHistory("read smart history: ", err)
	}
	return nil
}

func (s *Smart) PostStart() error {
	ctx, cancel := context.WithCancel(s.ctx)
	s.cancel = cancel
	s.worker.Add(1)
	go s.loop(ctx)
	return nil
}

func (s *Smart) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	if s.cancel != nil {
		s.cancel()
	}
	s.worker.Wait()
	s.unregisterProviderCallbacks()
	closeErr := s.closeActiveConnections()
	flushErr := s.flushHistory(true)
	s.releaseHistory()
	return errors.Join(closeErr, flushErr)
}

func (s *Smart) loop(ctx context.Context) {
	defer s.worker.Done()
	s.runProbe(ctx)
	probeTicker := time.NewTicker(s.interval)
	flushTicker := time.NewTicker(smartFlushInterval)
	gcTicker := time.NewTicker(smartGCInterval)
	defer probeTicker.Stop()
	defer flushTicker.Stop()
	defer gcTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-probeTicker.C:
			s.runProbe(ctx)
		case <-flushTicker.C:
			if err := s.flushHistory(false); err != nil {
				s.errorSmartHistory("write smart history: ", err)
			}
		case <-gcTicker.C:
			s.store.GC(time.Now(), s.historyRetention, s.maxHistoryEntries)
		}
	}
}

func (s *Smart) Now() string {
	s.statusAccess.RLock()
	defer s.statusAccess.RUnlock()
	return s.status.Selected
}

func (s *Smart) All() []string {
	return common.Map(s.candidateSnapshot(), func(candidate adapter.Outbound) string {
		return candidate.Tag()
	})
}

func (s *Smart) Weights() []smart.NodeRankItem {
	return s.store.GroupWeights(time.Now(), s.Tag(), common.Map(s.candidateSnapshot(), func(candidate adapter.Outbound) string {
		return candidate.Tag()
	}))
}

// ClearCache drops all learned metrics and their persisted history so the
// group relearns from scratch.
func (s *Smart) ClearCache() error {
	s.store.Clear()
	return s.flushHistory(true)
}

func (s *Smart) SmartStatus() adapter.SmartGroupStatus {
	s.statusAccess.RLock()
	defer s.statusAccess.RUnlock()
	status := s.status
	if status.UpdatedAt != nil {
		updated := *status.UpdatedAt
		status.UpdatedAt = &updated
	}
	status.Candidates = append([]adapter.SmartCandidateStatus{}, status.Candidates...)
	return status
}

func (s *Smart) NewConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	s.connection.NewConnection(ctx, s, conn, metadata, onClose)
}

func (s *Smart) NewPacketConnection(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	s.connection.NewPacketConnection(ctx, s, conn, metadata, onClose)
}

func (s *Smart) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	transport := N.NetworkName(network)
	if transport != N.NetworkTCP && transport != N.NetworkUDP {
		return nil, E.Extend(N.ErrUnknownNetwork, network)
	}
	ordered, target := s.rank(ctx, transport, destination)
	if len(ordered) == 0 {
		return nil, E.New("smart group has no supported candidate")
	}
	limit := min(s.maxSelected, len(ordered))
	conn, winner, elapsed, err := s.raceDial(ctx, network, destination, target, ordered[:limit])
	if err == nil {
		return s.wrapConn(ctx, conn, winner, target, transport, elapsed), nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	for _, fallback := range ordered[limit:] {
		started := time.Now()
		fallbackConn, fallbackErr := fallback.outbound.DialContext(ctx, network, destination)
		elapsed = time.Since(started)
		if fallbackErr == nil {
			return s.wrapConn(ctx, fallbackConn, fallback, target, transport, elapsed), nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		s.recordFailure(target, transport, fallback.outbound.Tag(), elapsed)
		err = errors.Join(err, E.Cause(fallbackErr, "smart fallback ", fallback.outbound.Tag()))
	}
	return nil, err
}

func (s *Smart) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	ordered, target := s.rank(ctx, N.NetworkUDP, destination)
	if len(ordered) == 0 {
		return nil, E.New("smart group has no supported UDP candidate")
	}
	limit := min(s.maxSelected, len(ordered))
	conn, winner, elapsed, err := s.raceListenPacket(ctx, destination, target, ordered[:limit])
	if err == nil {
		return s.wrapPacketConn(ctx, conn, winner, target, elapsed), nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	for _, candidate := range ordered[limit:] {
		started := time.Now()
		fallbackConn, fallbackErr := candidate.outbound.ListenPacket(ctx, destination)
		elapsed = time.Since(started)
		if fallbackErr == nil {
			return s.wrapPacketConn(ctx, fallbackConn, candidate, target, elapsed), nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		s.recordFailure(target, N.NetworkUDP, candidate.outbound.Tag(), elapsed)
		err = errors.Join(err, E.Cause(fallbackErr, "smart fallback ", candidate.outbound.Tag()))
	}
	return nil, err
}

type smartCandidate struct {
	outbound adapter.Outbound
	status   smart.Candidate
	index    int
}

func (s *Smart) rank(ctx context.Context, network string, destination M.Socksaddr) ([]smartCandidate, string) {
	target := smartTarget(adapter.ContextFrom(ctx), destination)
	candidates := s.candidateSnapshot()
	keys := make([]smart.MetricKey, 0, len(candidates))
	byTag := make(map[string]adapter.Outbound, len(candidates))
	indexes := make(map[string]int, len(candidates))
	for index, candidate := range candidates {
		if !common.Contains(candidate.Network(), network) {
			continue
		}
		key := smart.MetricKey{Group: s.Tag(), Target: target, Network: network, Node: candidate.Tag()}
		keys = append(keys, key)
		byTag[candidate.Tag()] = candidate
		indexes[candidate.Tag()] = index
	}
	ranked := s.store.Rank(time.Now(), keys)
	result := make([]smartCandidate, 0, len(ranked))
	used := make(map[string]bool, len(ranked))
	for _, status := range ranked {
		candidate := byTag[status.Key.Node]
		if candidate == nil || status.Blocked || !status.Known || status.Weight < smart.AllowedWeight {
			continue
		}
		result = append(result, smartCandidate{candidate, status, indexes[candidate.Tag()]})
		used[candidate.Tag()] = true
	}
	fallback := make([]smartCandidate, 0, len(ranked))
	for _, status := range ranked {
		candidate := byTag[status.Key.Node]
		if candidate == nil || used[candidate.Tag()] || status.Blocked {
			continue
		}
		fallback = append(fallback, smartCandidate{candidate, status, indexes[candidate.Tag()]})
	}
	allBlocked := len(result) == 0 && len(fallback) == 0
	if allBlocked {
		for _, status := range ranked {
			candidate := byTag[status.Key.Node]
			if candidate != nil {
				fallback = append(fallback, smartCandidate{candidate, status, indexes[candidate.Tag()]})
			}
		}
	}
	sortFallbackCandidates(fallback, s.history, s.tolerance)
	statusCandidates := append([]smartCandidate(nil), result...)
	statusCandidates = append(statusCandidates, fallback...)
	if allBlocked && len(fallback) > 1 {
		fallback = fallback[:1]
	}
	result = append(result, fallback...)
	s.updateStatus(statusCandidates)
	return result, target
}

func sortFallbackCandidates(candidates []smartCandidate, history *urltest.HistoryStorage, tolerance uint16) {
	type delay struct {
		value uint16
		known bool
	}
	delays := make(map[string]delay, len(candidates))
	var lowest uint16
	lowestKnown := false
	if history != nil {
		for _, candidate := range candidates {
			entry := history.LoadURLTestHistory(candidate.outbound.Tag())
			if entry != nil && entry.Delay > 0 {
				delays[candidate.outbound.Tag()] = delay{entry.Delay, true}
				if !lowestKnown || entry.Delay < lowest {
					lowest = entry.Delay
					lowestKnown = true
				}
			}
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := delays[candidates[i].outbound.Tag()], delays[candidates[j].outbound.Tag()]
		if left.known != right.known {
			return left.known
		}
		if !left.known {
			return candidates[i].index < candidates[j].index
		}
		if tolerance == 0 {
			if left.value != right.value {
				return left.value < right.value
			}
			return candidates[i].index < candidates[j].index
		}
		leftBucket := uint32(left.value-lowest) / (uint32(tolerance) + 1)
		rightBucket := uint32(right.value-lowest) / (uint32(tolerance) + 1)
		if leftBucket != rightBucket {
			return leftBucket < rightBucket
		}
		return candidates[i].index < candidates[j].index
	})
}

func (s *Smart) updateStatus(candidates []smartCandidate) {
	updated := time.Now()
	statuses := make([]adapter.SmartCandidateStatus, 0, len(candidates))
	for _, candidate := range candidates {
		statuses = append(statuses, adapter.SmartCandidateStatus{
			Tag:     candidate.outbound.Tag(),
			Weight:  candidate.status.Weight,
			Samples: candidate.status.Samples,
			Blocked: candidate.status.Blocked,
		})
	}
	s.statusAccess.Lock()
	s.status.UpdatedAt = &updated
	s.status.Candidates = statuses
	s.statusAccess.Unlock()
}

func (s *Smart) markSelected(candidate smartCandidate) {
	updated := time.Now()
	s.statusAccess.Lock()
	s.status.Selected = candidate.outbound.Tag()
	s.status.UpdatedAt = &updated
	s.statusAccess.Unlock()
}

type smartDialResult[T interface{ Close() error }] struct {
	candidate smartCandidate
	conn      T
	err       error
	elapsed   time.Duration
}

func (s *Smart) raceDial(ctx context.Context, network string, destination M.Socksaddr, target string, candidates []smartCandidate) (net.Conn, smartCandidate, time.Duration, error) {
	return raceSmartConnection(s, ctx, network, destination, target, candidates, func(ctx context.Context, candidate smartCandidate) (net.Conn, error) {
		return candidate.outbound.DialContext(ctx, network, destination)
	})
}

func (s *Smart) raceListenPacket(ctx context.Context, destination M.Socksaddr, target string, candidates []smartCandidate) (net.PacketConn, smartCandidate, time.Duration, error) {
	return raceSmartConnection(s, ctx, N.NetworkUDP, destination, target, candidates, func(ctx context.Context, candidate smartCandidate) (net.PacketConn, error) {
		return candidate.outbound.ListenPacket(ctx, destination)
	})
}

func raceSmartConnection[T interface{ Close() error }](s *Smart, ctx context.Context, network string, destination M.Socksaddr, target string, candidates []smartCandidate, dial func(context.Context, smartCandidate) (T, error)) (T, smartCandidate, time.Duration, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, smartCandidate{}, 0, err
	}
	child, cancel := context.WithCancel(ctx)
	results := make(chan smartDialResult[T], min(5, len(candidates)))
	start := func(candidate smartCandidate) {
		go func() {
			started := time.Now()
			conn, err := dial(child, candidate)
			results <- smartDialResult[T]{candidate: candidate, conn: conn, err: err, elapsed: time.Since(started)}
		}()
	}
	active, next := 0, 0
	for active < min(5, len(candidates)) {
		if err := ctx.Err(); err != nil {
			cancel()
			drainSmartResults(results, active)
			return zero, smartCandidate{}, 0, err
		}
		start(candidates[next])
		next++
		active++
	}
	var errs []error
	for active > 0 {
		select {
		case <-ctx.Done():
			cancel()
			drainSmartResults(results, active)
			return zero, smartCandidate{}, 0, ctx.Err()
		case result := <-results:
			active--
			if err := ctx.Err(); err != nil {
				cancel()
				if any(result.conn) != nil {
					_ = result.conn.Close()
				}
				drainSmartResults(results, active)
				return zero, smartCandidate{}, 0, err
			}
			if result.err == nil {
				cancel()
				drainSmartResults(results, active)
				return result.conn, result.candidate, result.elapsed, nil
			}
			if child.Err() == nil && ctx.Err() == nil {
				s.recordFailure(target, N.NetworkName(network), result.candidate.outbound.Tag(), result.elapsed)
				errs = append(errs, E.Cause(result.err, "smart candidate ", result.candidate.outbound.Tag()))
			}
			if next < len(candidates) && child.Err() == nil && ctx.Err() == nil {
				start(candidates[next])
				next++
				active++
			}
		}
	}
	cancel()
	if len(errs) == 0 {
		return zero, smartCandidate{}, 0, E.New("all smart candidates were cancelled")
	}
	return zero, smartCandidate{}, 0, errors.Join(errs...)
}

func drainSmartResults[T interface{ Close() error }](results <-chan smartDialResult[T], pending int) {
	if pending == 0 {
		return
	}
	go func() {
		for range pending {
			result := <-results
			if any(result.conn) != nil {
				_ = result.conn.Close()
			}
		}
	}()
}

func (s *Smart) recordFailure(target, network, node string, elapsed time.Duration) {
	s.store.Record(time.Now(), smart.MetricKey{Group: s.Tag(), Target: target, Network: network, Node: node}, smart.Observation{Success: false, ConnectTime: elapsed})
}

func (s *Smart) wrapConn(ctx context.Context, conn net.Conn, candidate smartCandidate, target, network string, connectTime time.Duration) net.Conn {
	s.markSelected(candidate)
	if metadata := adapter.ContextFrom(ctx); metadata != nil {
		metadata.AppendRealOutbound(candidate.outbound.Tag())
	}
	key := smart.MetricKey{Group: s.Tag(), Target: target, Network: network, Node: candidate.outbound.Tag()}
	var wrapped *smartConn
	wrapped = newSmartConn(conn, func(success bool, upload, download int64, firstByte, duration time.Duration) {
		s.untrackConnection(wrapped)
		s.store.Record(time.Now(), key, smart.Observation{Closed: true, Success: success, ConnectTime: connectTime, FirstByte: firstByte, UploadBytes: upload, DownloadBytes: download, PeakUploadBPS: rate(upload, duration), PeakDownloadBPS: rate(download, duration), Duration: duration})
	})
	s.trackConnection(wrapped)
	return wrapped
}

func (s *Smart) wrapPacketConn(ctx context.Context, conn net.PacketConn, candidate smartCandidate, target string, connectTime time.Duration) net.PacketConn {
	s.markSelected(candidate)
	if metadata := adapter.ContextFrom(ctx); metadata != nil {
		metadata.AppendRealOutbound(candidate.outbound.Tag())
	}
	key := smart.MetricKey{Group: s.Tag(), Target: target, Network: N.NetworkUDP, Node: candidate.outbound.Tag()}
	packetConn, ok := conn.(N.PacketConn)
	if !ok {
		var wrapped *smartPlainPacketConn
		wrapped = newSmartPlainPacketConn(conn, func(success bool, upload, download int64, firstByte, duration time.Duration) {
			s.untrackConnection(wrapped)
			s.store.Record(time.Now(), key, smart.Observation{Closed: true, Success: success, ConnectTime: connectTime, FirstByte: firstByte, UploadBytes: upload, DownloadBytes: download, PeakUploadBPS: rate(upload, duration), PeakDownloadBPS: rate(download, duration), Duration: duration})
		})
		s.trackConnection(wrapped)
		return wrapped
	}
	var wrapped *smartPacketConn
	wrapped = newSmartPacketConn(conn, packetConn, func(success bool, upload, download int64, firstByte, duration time.Duration) {
		s.untrackConnection(wrapped)
		s.store.Record(time.Now(), key, smart.Observation{Closed: true, Success: success, ConnectTime: connectTime, FirstByte: firstByte, UploadBytes: upload, DownloadBytes: download, PeakUploadBPS: rate(upload, duration), PeakDownloadBPS: rate(download, duration), Duration: duration})
	})
	s.trackConnection(wrapped)
	return wrapped
}

func (s *Smart) trackConnection(conn io.Closer) {
	s.activeAccess.Lock()
	if s.closed.Load() {
		s.activeAccess.Unlock()
		_ = conn.Close()
		return
	}
	if s.activeConnections == nil {
		s.activeConnections = make(map[io.Closer]struct{})
	}
	s.activeConnections[conn] = struct{}{}
	s.activeAccess.Unlock()
}

func (s *Smart) untrackConnection(conn io.Closer) {
	s.activeAccess.Lock()
	delete(s.activeConnections, conn)
	s.activeAccess.Unlock()
}

func (s *Smart) closeActiveConnections() error {
	s.activeAccess.Lock()
	connections := make([]io.Closer, 0, len(s.activeConnections))
	for conn := range s.activeConnections {
		connections = append(connections, conn)
	}
	s.activeAccess.Unlock()
	var err error
	for _, conn := range connections {
		err = errors.Join(err, conn.Close())
	}
	return err
}

func rate(bytes int64, duration time.Duration) float64 {
	if bytes <= 0 || duration <= 0 {
		return 0
	}
	return float64(bytes) / duration.Seconds()
}

func (s *Smart) URLTest(ctx context.Context) (map[string]uint16, error) {
	return s.probe(ctx)
}

func (s *Smart) runProbe(ctx context.Context) {
	probeCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	_, _ = s.probe(probeCtx)
}

func (s *Smart) probe(ctx context.Context) (map[string]uint16, error) {
	s.probeAccess.Lock()
	if s.probing {
		s.probeAccess.Unlock()
		return map[string]uint16{}, nil
	}
	s.probing = true
	s.probeAccess.Unlock()
	defer func() {
		s.probeAccess.Lock()
		s.probing = false
		s.probeAccess.Unlock()
	}()
	candidates := s.candidateSnapshot()
	result := make(map[string]uint16)
	failed := make([]string, 0, len(candidates))
	var access sync.Mutex
	b, _ := batch.New(ctx, batch.WithConcurrencyNum[any](5))
	for _, candidate := range candidates {
		candidate := candidate
		if !common.Contains(candidate.Network(), N.NetworkTCP) {
			continue
		}
		b.Go(candidate.Tag(), func() (any, error) {
			testCtx, cancel := context.WithTimeout(ctx, s.timeout)
			delay, err := urltest.URLTest(testCtx, s.url, candidate)
			cancel()
			access.Lock()
			if err == nil && delay > 0 {
				result[candidate.Tag()] = delay
			} else {
				failed = append(failed, candidate.Tag())
			}
			access.Unlock()
			return nil, nil
		})
	}
	b.Wait()
	if len(candidates) > 0 && len(result) == 0 {
		return result, E.New("all smart probes failed")
	}
	if s.history != nil {
		now := time.Now()
		for tag, delay := range result {
			s.history.StoreURLTestHistory(tag, &adapter.URLTestHistory{Time: now, Delay: delay})
		}
		for _, tag := range failed {
			s.history.DeleteURLTestHistory(tag)
		}
	}
	return result, nil
}

func (s *Smart) candidateSnapshot() []adapter.Outbound {
	s.candidateAccess.RLock()
	defer s.candidateAccess.RUnlock()
	return slices.Clone(s.candidates)
}

func (s *Smart) rebuildCandidates(updatedProvider string) error {
	s.providerAccess.Lock()
	defer s.providerAccess.Unlock()
	var roots []adapter.Outbound
	for index, tag := range s.tags {
		candidate, loaded := s.outbound.Outbound(tag)
		if !loaded {
			return E.New("outbound ", index, " not found: ", tag)
		}
		roots = append(roots, candidate)
	}
	for _, providerTag := range s.providerTags {
		if providerTag != updatedProvider && s.outboundsCache[providerTag] != nil {
			roots = append(roots, s.outboundsCache[providerTag]...)
			continue
		}
		provider := s.providers[providerTag]
		if provider == nil {
			continue
		}
		var cached []adapter.Outbound
		for _, candidate := range provider.Outbounds() {
			if s.exclude != nil && s.exclude.MatchString(candidate.Tag()) {
				continue
			}
			if s.include != nil && !s.include.MatchString(candidate.Tag()) {
				continue
			}
			cached = append(cached, candidate)
		}
		s.outboundsCache[providerTag] = cached
		roots = append(roots, cached...)
	}
	var candidates []adapter.Outbound
	seen := make(map[string]bool)
	for _, candidate := range roots {
		s.flattenCandidate(candidate, make(map[string]bool), seen, &candidates)
	}
	if len(candidates) == 0 {
		return E.New("smart group has no leaf candidates")
	}
	s.candidateAccess.Lock()
	s.candidates = candidates
	s.candidateAccess.Unlock()
	return nil
}

func (s *Smart) flattenCandidate(candidate adapter.Outbound, visiting map[string]bool, seen map[string]bool, result *[]adapter.Outbound) {
	if candidate == nil || candidate == s {
		return
	}
	tag := candidate.Tag()
	if visiting[tag] {
		return
	}
	if group, isGroup := candidate.(adapter.OutboundGroup); isGroup {
		visiting[tag] = true
		for _, childTag := range group.All() {
			child, loaded := s.outbound.Outbound(childTag)
			if loaded {
				s.flattenCandidate(child, visiting, seen, result)
			}
		}
		delete(visiting, tag)
		return
	}
	if !seen[tag] {
		seen[tag] = true
		*result = append(*result, candidate)
	}
}

func (s *Smart) onProviderUpdated(tag string) error {
	if s.closed.Load() {
		return nil
	}
	if _, loaded := s.providers[tag]; !loaded {
		return E.New("outbound provider not found: ", tag)
	}
	return s.rebuildCandidates(tag)
}

func (s *Smart) unregisterProviderCallbacks() {
	s.providerAccess.Lock()
	defer s.providerAccess.Unlock()
	for tag, handle := range s.providerHandles {
		if provider := s.providers[tag]; provider != nil && handle != nil {
			provider.UnregisterCallback(handle)
		}
	}
	clear(s.providerHandles)
}

func smartTarget(metadata *adapter.InboundContext, destination M.Socksaddr) string {
	var target string
	if metadata != nil {
		if metadata.SniffHost != "" {
			target = metadata.SniffHost
		} else if metadata.Domain != "" {
			target = metadata.Domain
		}
	}
	if target == "" {
		target = destination.Fqdn
	}
	if target == "" {
		target = destination.AddrString()
	}
	return strings.ToLower(strings.TrimSuffix(target, "."))
}
