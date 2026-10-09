package group

import (
	"context"
	"io"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/smart"
	M "github.com/sagernet/sing/common/metadata"
)

type (
	smartResponseKey   struct{ target, node string }
	smartResponseBlock struct {
		host, target string
		until        time.Time
	}
)

type smartResponseContext struct {
	host, site, target string
	port               uint16
	node               adapter.Outbound
	epoch              uint64
	recheck            bool
}

func (s *Smart) initResponseProbes() {
	s.responseCtx, s.responseCancel = context.WithCancel(s.ctx)
	s.responseThrottle = new(smart.ProbeThrottle)
	s.responseBlocks = make(map[smartResponseKey]smartResponseBlock)
	s.responseTargets = make(map[string]string)
	s.resetExitWatcherLocked()
}

func (s *Smart) resetExitWatcherLocked() {
	epoch := s.responseEpoch
	watcher := smart.NewExitWatcher(smart.ExitWatcherOptions{
		Store: s.store, WantASN: func() bool { return s.preferASN },
		Apply: func(node string, prober smart.ExitProber, apply func()) bool {
			s.candidateAccess.RLock()
			defer s.candidateAccess.RUnlock()
			s.responseAccess.Lock()
			defer s.responseAccess.Unlock()
			if s.closed.Load() || epoch != s.responseEpoch {
				return false
			}
			source, ok := prober.(smartOutboundProber)
			if !ok {
				return false
			}
			for _, candidate := range s.candidates {
				if candidate.Tag() == node && candidate == source.outbound {
					apply()
					return true
				}
			}
			return false
		},
		Invalidate: func(site string) {
			s.responseAccess.Lock()
			defer s.responseAccess.Unlock()
			if s.closed.Load() || epoch != s.responseEpoch {
				return
			}
			s.dropResponsePinsLocked(site)
		},
	})
	s.exitWatcher.Store(watcher)
}

func (s *Smart) startResponseTask(epoch uint64, task func(context.Context)) bool {
	s.responseAccess.Lock()
	defer s.responseAccess.Unlock()
	if s.closed.Load() || s.responseCtx == nil || epoch != s.responseEpoch {
		return false
	}
	ctx := s.responseCtx
	s.responseWorkers.Go(func() { task(ctx) })
	return true
}

func (s *Smart) dropResponsePinsLocked(site string) {
	s.selection.access.Lock()
	defer s.selection.access.Unlock()
	for key := range s.selection.pins {
		_, target, _ := strings.Cut(key, "\x00")
		if target == site || s.responseTargets[target] == site {
			delete(s.selection.pins, key)
		}
	}
}

func (s *Smart) responseContext(ctx context.Context, node adapter.Outbound, target string, destination M.Socksaddr) smartResponseContext {
	host := smartTarget(adapter.ContextFrom(ctx), destination)
	if M.ParseSocksaddrHostPort(host, destination.Port).Addr.IsValid() {
		host = ""
	}
	s.responseAccess.Lock()
	epoch := s.responseEpoch
	s.responseAccess.Unlock()
	return smartResponseContext{host: host, site: smart.SiteKey(host), target: target, port: destination.Port, node: node, epoch: epoch}
}

func (s *Smart) maybeProbeExit(node string) {
	watcher := s.exitWatcher.Load()
	if watcher == nil || s.closed.Load() {
		return
	}
	for _, candidate := range s.candidateSnapshot() {
		if candidate.Tag() != node {
			continue
		}
		s.responseAccess.Lock()
		epoch := s.responseEpoch
		s.responseAccess.Unlock()
		watcher.MaybeProbe(node, smartOutboundProber{s: s, outbound: candidate}, func(task func(context.Context)) bool { return s.startResponseTask(epoch, task) })
		return
	}
}

func (s *Smart) probeAfterClose(meta smartResponseContext, upload, download int64, duration time.Duration, success bool) {
	watcher := s.exitWatcher.Load()
	if watcher == nil || meta.host == "" || !success || (duration > 100*time.Millisecond && upload == 0 && download == 0) {
		return
	}
	if !watcher.Suspected(meta.site, meta.node.Tag()) && !smart.ResponseProbeEligible(meta.host, meta.port, download, false, false) {
		return
	}
	s.responseAccess.Lock()
	throttle := s.responseThrottle
	valid := meta.epoch == s.responseEpoch && !s.closed.Load()
	s.responseAccess.Unlock()
	if !valid || throttle == nil {
		return
	}
	now := time.Now()
	if !throttle.AllowNode(meta.site, meta.node.Tag(), now) || throttle.Blind(meta.host, now) || !smart.AllowGlobalProbe(now) {
		return
	}
	done, ok := smart.TryStartProbe()
	if !ok {
		return
	}
	if !s.startResponseTask(meta.epoch, func(ctx context.Context) {
		defer done()
		verdict := s.responseVerdict(ctx, meta.node, meta.host)
		if verdict.Action == smart.VerdictIgnore {
			return
		}
		s.candidateAccess.RLock()
		if !slices.Contains(s.candidates, meta.node) {
			s.candidateAccess.RUnlock()
			return
		}
		s.responseAccess.Lock()
		if meta.epoch != s.responseEpoch || s.closed.Load() || (!meta.recheck && s.responseStoppedLocked(meta.site, time.Now(), len(s.candidates))) || !throttle.AllowHostRecord(meta.host, verdict, time.Now()) {
			s.responseAccess.Unlock()
			s.candidateAccess.RUnlock()
			return
		}
		s.applyResponseLocked(meta, verdict, throttle)
		var closeList []io.Closer
		if verdict.Action == smart.VerdictRecord {
			closeList = s.responseCloseList(meta.target)
		}
		s.responseAccess.Unlock()
		s.candidateAccess.RUnlock()
		for _, conn := range closeList {
			_ = conn.Close()
		}
		// ExitWatcher callbacks may drop pins, so publish the evidence outside the response lock.
		if verdict.Action == smart.VerdictReachable {
			if verdict.ControlSuccess {
				watcher.NoteSuccess(meta.site, meta.node.Tag())
			}
			watcher.Clear(meta.site, meta.node.Tag())
		} else if smart.RegionEvidence(verdict.Reason) {
			watcher.Note(meta.site, meta.node.Tag())
		}
		if s.logger != nil {
			s.logger.Debug("response probe ", meta.node.Tag(), " / ", meta.host, ": ", verdict.Reason)
		}
	}) {
		done()
	}
}

func (s *Smart) applyResponseLocked(meta smartResponseContext, verdict smart.Verdict, throttle *smart.ProbeThrottle) {
	node := meta.node.Tag()
	keys := []smartResponseKey{{meta.site, node}, {meta.target, node}}
	for _, key := range keys {
		if verdict.Action == smart.VerdictReachable {
			delete(s.responseBlocks, key)
		} else {
			s.trimResponseBlocksLocked(time.Now())
			s.responseBlocks[key] = smartResponseBlock{host: meta.host, target: meta.target, until: time.Now().Add(verdict.TTL)}
		}
	}
	if verdict.Action == smart.VerdictRecord {
		throttle.NoteFailure(meta.site, node, time.Now())
		s.dropResponsePinsLocked(meta.site)
	}
}

func (s *Smart) responseVerdict(ctx context.Context, node adapter.Outbound, host string) smart.Verdict {
	ctx, cancel := context.WithTimeout(ctx, smart.ProbeTimeout)
	defer cancel()
	result, err := (smartOutboundProber{s: s, outbound: node}).StatusProbe(ctx, smart.ProbeURL(host))
	if err != nil {
		return smart.ClassifyProbeError(err)
	}
	return smart.ClassifyResponse(result.StatusCode, result.Header, result.Body, time.Now())
}

func (s *Smart) markResponseCandidates(target, site string, candidates []smart.Candidate, candidateCount int) {
	s.responseAccess.Lock()
	defer s.responseAccess.Unlock()
	if len(s.responseTargets) >= smartPinLimit {
		for oldTarget := range s.responseTargets {
			delete(s.responseTargets, oldTarget)
			break
		}
	}
	s.responseTargets[target] = site
	now := time.Now()
	if s.responseStoppedLocked(site, now, candidateCount) {
		return
	}
	for index := range candidates {
		candidates[index].Blocked = candidates[index].Blocked || s.responseBlockedLocked(target, site, candidates[index].Key.Node, now)
	}
}

func (s *Smart) responseBlocked(target, site, node string, now time.Time, candidateCount int) bool {
	s.responseAccess.Lock()
	defer s.responseAccess.Unlock()
	return !s.responseStoppedLocked(site, now, candidateCount) && s.responseBlockedLocked(target, site, node, now)
}

func (s *Smart) responseBlockedLocked(target, site, node string, now time.Time) bool {
	for _, key := range []smartResponseKey{{target, node}, {site, node}} {
		if state, exists := s.responseBlocks[key]; exists {
			if state.until.After(now) {
				return true
			}
			delete(s.responseBlocks, key)
		}
	}
	return false
}

func (s *Smart) recheckResponses() {
	s.responseAccess.Lock()
	epoch := s.responseEpoch
	blocks := maps.Clone(s.responseBlocks)
	throttle := s.responseThrottle
	s.responseAccess.Unlock()
	if throttle == nil {
		return
	}
	for key, block := range blocks {
		if rand.Float64() >= 0.5 {
			continue
		}
		for _, node := range s.candidateSnapshot() {
			if node.Tag() == key.node {
				meta := smartResponseContext{host: block.host, site: smart.SiteKey(block.host), target: block.target, port: 443, node: node, epoch: epoch, recheck: true}
				// Rechecks use the same bounded path and may clear the temporary response avoidance.
				s.probeAfterClose(meta, 1, 0, 0, true)
				break
			}
		}
	}
}

func (s *Smart) responseStoppedLocked(site string, now time.Time, candidateCount int) bool {
	failures := 0
	for key, block := range s.responseBlocks {
		if key.target == site && block.until.After(now) {
			failures++
		}
	}
	return failures > max(2, candidateCount/3)
}

func (s *Smart) trimResponseBlocksLocked(now time.Time) {
	for key, block := range s.responseBlocks {
		if !block.until.After(now) {
			delete(s.responseBlocks, key)
		}
	}
	limit := s.maxHistoryEntries
	if limit <= 0 {
		limit = defaultSmartMaxHistoryEntries
	}
	if len(s.responseBlocks) < limit {
		return
	}
	var oldest smartResponseKey
	var until time.Time
	for key, block := range s.responseBlocks {
		if until.IsZero() || block.until.Before(until) {
			oldest, until = key, block.until
		}
	}
	delete(s.responseBlocks, oldest)
}

func (s *Smart) responseCloseList(target string) []io.Closer {
	s.activeAccess.Lock()
	defer s.activeAccess.Unlock()
	var result []io.Closer
	for conn, key := range s.activeConnections {
		if key.Target != target {
			continue
		}
		if tracked, ok := conn.(*smartConn); ok {
			tracked.controlled.Store(true)
		}
		result = append(result, conn)
	}
	return result
}

func (s *Smart) filterExitSuspicions(site string, candidates []smartCandidate) []smartCandidate {
	watcher := s.exitWatcher.Load()
	if watcher == nil {
		return candidates
	}
	now := time.Now()
	healthy := make([]smartCandidate, 0, len(candidates))
	deferred := make([]smartCandidate, 0)
	for _, candidate := range candidates {
		node := candidate.outbound.Tag()
		watcher.EnsureLoaded(node)
		if watcher.Defer(site, node, now, smart.ProbeTimeout) {
			// A shared service identity may have last been used by a different site.
			// Do not let its old pin promote a leased suspicious fallback above healthy exits.
			pinKey := candidate.status.Key.Network + "\x00" + candidate.status.Key.Target
			s.selection.access.Lock()
			if pin, exists := s.selection.pins[pinKey]; exists && pin.outbound == candidate.outbound {
				delete(s.selection.pins, pinKey)
			}
			s.selection.access.Unlock()
			deferred = append(deferred, candidate)
		} else {
			healthy = append(healthy, candidate)
		}
	}
	limit := s.maxSelected
	if limit <= 0 {
		limit = defaultSmartMaxSelected
	}
	if len(healthy) < limit {
		for _, candidate := range deferred {
			if watcher.AllowFallback(site, candidate.outbound.Tag(), now, smart.ProbeTimeout) {
				healthy = append(healthy, candidate)
				break
			}
		}
	}
	return healthy
}
