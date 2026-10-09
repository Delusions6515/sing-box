package smart

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ExitNodeState persists only exit identity and retry timing, never the raw exit IP.
type ExitNodeState struct {
	ExitInfo
	Failed int64 `json:"failed,omitempty"`
}

type ExitWatcherOptions struct {
	Store      *Store
	WantASN    func() bool
	Invalidate func(string)
	// Apply serializes a result with group cache clearing and membership changes.
	Apply func(string, ExitProber, func()) bool
}

type ExitWatcher struct {
	ExitState
	store      *Store
	wantASN    func() bool
	invalidate func(string)
	apply      func(string, ExitProber, func()) bool
	inflight   sync.Map
	seeded     sync.Map
	regions    SuspectTracker
	asns       SuspectTracker
}

func NewExitWatcher(options ExitWatcherOptions) *ExitWatcher {
	return &ExitWatcher{store: options.Store, wantASN: options.WantASN, invalidate: options.Invalidate, apply: options.Apply}
}

func TryStartExitProbe() (func(), bool) {
	select {
	case exitProbeInflight <- struct{}{}:
		return func() { <-exitProbeInflight }, true
	default:
		return nil, false
	}
}

// MaybeProbe checks freshness and deduplicates before the caller starts a task.
// The caller owns cancellation and joins every accepted task during shutdown.
func (w *ExitWatcher) MaybeProbe(node string, prober ExitProber, start func(func(context.Context)) bool) {
	w.EnsureLoaded(node)
	if !w.Due(node, exitRegionTTL, exitProbeRetryGap, time.Now()) {
		return
	}
	if _, loaded := w.inflight.LoadOrStore(node, struct{}{}); loaded {
		return
	}
	if !start(func(ctx context.Context) { defer w.inflight.Delete(node); w.probe(ctx, node, prober) }) {
		w.inflight.Delete(node)
	}
}

func (w *ExitWatcher) probe(parent context.Context, node string, prober ExitProber) {
	done, ok := TryStartExitProbe()
	if !ok {
		return
	}
	defer done()
	if !AllowGlobalProbe(time.Now()) {
		return
	}
	ctx, cancel := context.WithTimeout(parent, ProbeTimeout)
	defer cancel()
	result, err := prober.ExitProbe(ctx, w.wantASN != nil && w.wantASN())
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return
	}
	if err != nil {
		w.commit(node, prober, func() {
			now := time.Now()
			w.NoteFailure(node, now)
			if w.store != nil {
				state, _ := w.store.ExitState(node)
				state.Failed = now.Unix()
				w.store.SetExitState(node, state)
			}
		})
		return
	}
	if !w.commit(node, prober, func() {
		w.Store(node, ExitInfo{Region: result.Region, ASN: result.ASN, Key: result.Key}, time.Now())
		if w.store != nil {
			state, _ := w.store.ExitState(node)
			info := w.Info(node)
			if info.ASN == "" {
				info.ASN = state.ASN
			}
			w.store.SetExitState(node, ExitNodeState{ExitInfo: info})
		}
	}) {
		return
	}
	for _, target := range w.Release(node) {
		w.confirm(target, node)
	}
	for _, target := range w.ReleaseSuccess(node) {
		w.NoteSuccess(target, node)
	}
}

func (w *ExitWatcher) commit(node string, prober ExitProber, fn func()) bool {
	if w.apply != nil {
		return w.apply(node, prober, fn)
	}
	fn()
	return true
}

func (w *ExitWatcher) EnsureLoaded(node string) {
	if _, loaded := w.seeded.LoadOrStore(node, struct{}{}); loaded {
		return
	}
	if w.store == nil {
		return
	}
	if state, ok := w.store.ExitState(node); ok {
		w.Seed(node, state.ExitInfo)
		if state.Failed != 0 {
			w.NoteFailure(node, time.Unix(state.Failed, 0))
		}
	}
}

func (w *ExitWatcher) Note(target, node string) bool {
	if w.Info(node).Region == "" && w.asnOf(node) == "" {
		w.Withhold(target, node)
		return false
	}
	return w.confirm(target, node)
}

func (w *ExitWatcher) NoteSuccess(target, node string) bool {
	info := w.Info(node)
	asn := w.asnOf(node)
	if info.Region == "" && asn == "" {
		w.WithholdSuccess(target, node)
		return false
	}
	now := time.Now()
	identity := w.identity(node)
	raised := info.Region != "" && len(w.regions.NoteSuccess(target, info.Region, identity, now)) > 0
	if asn != "" && len(w.asns.NoteSuccess(target, asn, identity, now)) > 0 {
		raised = true
	}
	if raised {
		w.dropPin(target)
	}
	return raised
}

func (w *ExitWatcher) confirm(target, node string) bool {
	now := time.Now()
	identity := w.identity(node)
	raised := false
	if region := w.Info(node).Region; region != "" && w.regions.Note(target, region, identity, now) {
		raised = true
	}
	if asn := w.asnOf(node); asn != "" && w.asns.Note(target, asn, identity, now) {
		raised = true
	}
	if raised {
		w.dropPin(target)
	}
	return raised
}

func (w *ExitWatcher) dropPin(target string) {
	if w.invalidate != nil {
		w.invalidate(target)
	}
}

func (w *ExitWatcher) identity(node string) string {
	if key := w.Info(node).Key; key != "" {
		return key
	}
	return node
}

func (w *ExitWatcher) asnOf(node string) string {
	if w.wantASN == nil || !w.wantASN() {
		return ""
	}
	return w.Info(node).ASN
}

func (w *ExitWatcher) Suspected(target, node string) bool {
	if !w.regions.Active() && !w.asns.Active() {
		return false
	}
	now := time.Now()
	info := w.Info(node)
	return (info.Region != "" && w.regions.Suspected(target, info.Region, now)) || (w.wantASN != nil && w.wantASN() && info.ASN != "" && w.asns.Suspected(target, info.ASN, now))
}

func (w *ExitWatcher) Defer(target, node string, now time.Time, lease time.Duration) bool {
	if !w.regions.Active() && !w.asns.Active() {
		return false
	}
	info := w.Info(node)
	if w.wantASN != nil && w.wantASN() && info.ASN != "" && w.asns.Defer(target, info.ASN, node, now, lease) {
		return true
	}
	return info.Region != "" && w.regions.Defer(target, info.Region, node, now, lease)
}

func (w *ExitWatcher) AllowFallback(target, node string, now time.Time, lease time.Duration) bool {
	info := w.Info(node)
	regionSuspected := info.Region != "" && w.regions.Suspected(target, info.Region, now)
	asnSuspected := w.wantASN != nil && w.wantASN() && info.ASN != "" && w.asns.Suspected(target, info.ASN, now)
	if !regionSuspected && !asnSuspected {
		return true
	}
	claimedASN := false
	if asnSuspected {
		if !w.asns.TryHalfOpen(target, info.ASN, node, now, lease) {
			return false
		}
		claimedASN = true
	}
	if regionSuspected && !w.regions.TryHalfOpen(target, info.Region, node, now, lease) {
		if claimedASN {
			w.asns.ReleaseHalfOpen(target, info.ASN, node)
		}
		return false
	}
	return true
}

func (w *ExitWatcher) Clear(target, node string) {
	if region := w.Info(node).Region; region != "" {
		w.regions.Clear(target, region)
	}
	if asn := w.asnOf(node); asn != "" {
		w.asns.Clear(target, asn)
	}
}
