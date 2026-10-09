package smart

// Exit identity and suspicion state follow vernesong/mihomo at 512b09d055244f77b3d734d70fc3023b49acfa3e.
import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	ExitProbeBodyLimit = 2048

	ReasonRegionUnavailable = "region unavailable"

	// A suspicion needs independent nodes of the same key, one answer never condemns a whole
	// region or network.
	suspectThreshold          = 2
	suspectMaxControlExitKeys = 16
	suspectMaxTargets         = 4096

	// exit probes keep their own slots; a slow trace cannot hold back the status probes
	exitProbeMaxInflight = 2
)

// The intervals are variables so a test can shrink them instead of waiting them out.
var (
	suspectConfirmWindow = 10 * time.Minute
	exitRegionTTL        = 6 * time.Hour
	exitProbeRetryGap    = 15 * time.Minute
)

var exitProbeInflight = make(chan struct{}, exitProbeMaxInflight)

// ExitTraceURLs are tried in order, so a blocked endpoint costs a fallback instead of the
// answer; the first answers with `key=value` lines, the others with JSON.
var ExitTraceURLs = []string{
	"https://www.cloudflare.com/cdn-cgi/trace",
	"https://api.ip.sb/geoip",
	"https://ipwho.is/",
}

type ExitProbeResult struct {
	Region string
	ASN    string
	Key    string
}

// ExitProber reports where a node's traffic lands; the address behind the answer is
// consumed by the probe itself and never handed back.
type ExitProber interface {
	ExitProbe(ctx context.Context, wantASN bool) (*ExitProbeResult, error)
}

type ExitInfo struct {
	Region  string `json:"region,omitempty"`
	ASN     string `json:"asn,omitempty"`
	Key     string `json:"key,omitempty"`
	Updated int64  `json:"updated,omitempty"`
}

type exitRecord struct {
	info   ExitInfo
	failed int64
}

type suspectHalfOpen struct {
	owner   string
	until   int64
	expires int64
}

type ExitState struct {
	mu       sync.RWMutex
	nodes    map[string]*exitRecord
	refusals exitHold
	controls exitHold
}

// exitHold keeps one target per node, dropped once the confirm window passes.
type exitHold map[string]map[string]int64

// SuspectTracker raises a suspicion for one key domain, such as a region or a network:
// the other nodes of that key are only moved behind the rest until the suspicion expires.
// One exit machine counts once, however many nodes run on it.
type SuspectTracker struct {
	mu         sync.RWMutex
	pending    map[string]map[string]map[string]int64
	successes  map[string]map[string]map[string]int64
	suspected  map[string]map[string]int64
	halfOpen   map[string]map[string]suspectHalfOpen
	lastPruned atomic.Int64
	active     atomic.Bool
}

func (h *exitHold) add(target, node string, at int64) {
	if *h == nil {
		*h = make(exitHold)
	}
	for heldNode, targets := range *h {
		for heldTarget, heldAt := range targets {
			if at-heldAt > int64(suspectConfirmWindow) {
				delete(targets, heldTarget)
			}
		}
		if len(targets) == 0 {
			delete(*h, heldNode)
		}
	}
	if (*h)[node] == nil {
		(*h)[node] = make(map[string]int64)
	}
	(*h)[node][target] = at
}

func (h exitHold) take(node string, at int64) []string {
	targets := h[node]
	delete(h, node)
	if len(targets) == 0 {
		return nil
	}
	released := make([]string, 0, len(targets))
	for target, heldAt := range targets {
		if at-heldAt >= 0 && at-heldAt <= int64(suspectConfirmWindow) {
			released = append(released, target)
		}
	}
	return released
}

func (s *ExitState) Due(node string, ttl, retry time.Duration, now time.Time) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.nodes[node]
	if !ok {
		return true
	}
	if rec.info.Updated != 0 && now.Unix()-rec.info.Updated < int64(ttl.Seconds()) {
		return false
	}
	return rec.failed == 0 || now.Unix()-rec.failed >= int64(retry.Seconds())
}

func (s *ExitState) NoteFailure(node string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.record(node)
	rec.failed = now.Unix()
}

func (s *ExitState) Store(node string, info ExitInfo, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	info.Updated = now.Unix()
	rec := s.record(node)
	rec.info = info
	rec.failed = 0
}

// Seed restores an earlier answer; a fresher in-memory answer always wins.
func (s *ExitState) Seed(node string, info ExitInfo) {
	if info.Updated == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec, ok := s.nodes[node]; ok && rec.info.Updated >= info.Updated {
		return
	}
	s.record(node).info = info
}

func (s *ExitState) Info(node string) ExitInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if rec, ok := s.nodes[node]; ok {
		return rec.info
	}
	return ExitInfo{}
}

// Withhold holds a refusal until its node's exit answer arrives; Release completes it.
func (s *ExitState) Withhold(target, node string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refusals.add(target, node, time.Now().UnixNano())
}

// WithholdSuccess holds a 2xx control until its node's exit answer arrives, so the answer can
// still corroborate the refusal of another key; ReleaseSuccess replays it.
func (s *ExitState) WithholdSuccess(target, node string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.controls.add(target, node, time.Now().UnixNano())
}

func (s *ExitState) Release(node string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refusals.take(node, time.Now().UnixNano())
}

func (s *ExitState) ReleaseSuccess(node string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.controls.take(node, time.Now().UnixNano())
}

func (s *ExitState) record(node string) *exitRecord {
	if s.nodes == nil {
		s.nodes = make(map[string]*exitRecord)
	}
	rec, ok := s.nodes[node]
	if !ok {
		rec = &exitRecord{}
		s.nodes[node] = rec
	}
	return rec
}

func (t *SuspectTracker) Note(target, key, identity string, now time.Time) (raised bool) {
	if target == "" || key == "" || identity == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneIfDueLocked(now)

	if until, ok := t.suspected[target][key]; ok && until > now.UnixNano() {
		// a fresh refusal is more evidence, it carries the suspicion forward
		t.suspected[target][key] = now.Add(probeRegionalBlockTTL).UnixNano()
		delete(t.halfOpen[target], key)
		return false
	}
	if _, pending := t.pending[target]; !pending {
		if _, suspected := t.suspected[target]; !suspected && len(t.pending)+len(t.suspected) >= suspectMaxTargets {
			return false
		}
	}

	if t.pending == nil {
		t.pending = make(map[string]map[string]map[string]int64)
	}
	keys := t.pending[target]
	if keys == nil {
		keys = make(map[string]map[string]int64)
		t.pending[target] = keys
	}
	seen := keys[key]
	if seen == nil {
		seen = make(map[string]int64)
		keys[key] = seen
	}
	seen[identity] = now.UnixNano()
	if len(seen) < suspectThreshold || !t.hasControlSuccess(target, key, seen) {
		return false
	}

	if t.suspected == nil {
		t.suspected = make(map[string]map[string]int64)
	}
	suspicions := t.suspected[target]
	if suspicions == nil {
		suspicions = make(map[string]int64)
		t.suspected[target] = suspicions
	}
	suspicions[key] = now.Add(probeRegionalBlockTTL).UnixNano()
	delete(t.halfOpen[target], key)
	delete(keys, key)
	if len(keys) == 0 {
		delete(t.pending, target)
	}
	t.active.Store(true)
	return true
}

func (t *SuspectTracker) NoteSuccess(target, key, identity string, now time.Time) []string {
	if target == "" || key == "" || identity == "" {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneIfDueLocked(now)
	failures := t.pending[target]
	for failedKey, identities := range failures {
		for failedIdentity, at := range identities {
			if now.Sub(time.Unix(0, at)) > suspectConfirmWindow {
				delete(identities, failedIdentity)
			}
		}
		if len(identities) == 0 {
			delete(failures, failedKey)
		}
	}
	if len(failures) == 0 {
		delete(t.pending, target)
	}
	controlSuccesses := t.successes[target]
	for successKey, identities := range controlSuccesses {
		for successIdentity, at := range identities {
			if now.Sub(time.Unix(0, at)) > suspectConfirmWindow {
				delete(identities, successIdentity)
			}
		}
		if len(identities) == 0 {
			delete(controlSuccesses, successKey)
		}
	}
	if len(controlSuccesses) == 0 {
		delete(t.successes, target)
	}
	if t.successes == nil {
		t.successes = make(map[string]map[string]map[string]int64)
	}
	keys := t.successes[target]
	if keys == nil {
		if len(t.successes) >= probeMaxEntries {
			return nil
		}
		keys = make(map[string]map[string]int64)
		t.successes[target] = keys
	}
	seen := keys[key]
	if _, exists := seen[identity]; !exists {
		controlCount := 0
		for _, identities := range keys {
			controlCount += len(identities)
		}
		if controlCount >= suspectMaxControlExitKeys {
			return nil
		}
	}
	if seen == nil {
		seen = make(map[string]int64)
		keys[key] = seen
	}
	seen[identity] = now.UnixNano()

	var raised []string
	for failedKey, identities := range t.pending[target] {
		if len(identities) < suspectThreshold || !t.hasControlSuccess(target, failedKey, identities) {
			continue
		}
		if t.suspected == nil {
			t.suspected = make(map[string]map[string]int64)
		}
		suspicions := t.suspected[target]
		if suspicions == nil {
			suspicions = make(map[string]int64)
			t.suspected[target] = suspicions
		}
		suspicions[failedKey] = now.Add(probeRegionalBlockTTL).UnixNano()
		delete(t.halfOpen[target], failedKey)
		delete(failures, failedKey)
		raised = append(raised, failedKey)
	}
	if len(failures) == 0 {
		delete(t.pending, target)
	}
	if len(raised) > 0 {
		t.active.Store(true)
	}
	return raised
}

func (t *SuspectTracker) hasControlSuccess(target, failedKey string, failures map[string]int64) bool {
	for successKey, identities := range t.successes[target] {
		if successKey == failedKey {
			continue
		}
		for identity := range identities {
			if _, sameExit := failures[identity]; !sameExit {
				return true
			}
		}
	}
	return false
}

func (t *SuspectTracker) Suspected(target, key string, now time.Time) bool {
	if !t.active.Load() || target == "" || key == "" {
		return false
	}
	// the cleanup mutates the maps, the lookup itself only reads them
	t.pruneIfDue(now)
	t.mu.RLock()
	defer t.mu.RUnlock()
	if until, ok := t.suspected[target][key]; ok {
		if until > now.UnixNano() {
			return true
		}
	}
	state, ok := t.halfOpen[target][key]
	return ok && state.owner != "" && state.until > now.UnixNano()
}

func (t *SuspectTracker) Active() bool {
	return t.active.Load()
}

func (t *SuspectTracker) Defer(target, key, owner string, now time.Time, lease time.Duration) bool {
	if !t.active.Load() || target == "" || key == "" || owner == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	nowNano := now.UnixNano()
	t.pruneIfDueLocked(now)
	until, suspected := t.suspected[target][key]
	if suspected && until > nowNano {
		return true
	}
	state, leased := t.halfOpen[target][key]
	if !suspected && !leased {
		return false
	}
	if suspected {
		delete(t.suspected[target], key)
		if len(t.suspected[target]) == 0 {
			delete(t.suspected, target)
		}
	}
	if leased && state.expires <= nowNano {
		delete(t.halfOpen[target], key)
		leased = false
	}
	if leased && state.owner != "" && state.until > nowNano {
		return true
	}
	if t.halfOpen == nil {
		t.halfOpen = make(map[string]map[string]suspectHalfOpen)
	}
	keys := t.halfOpen[target]
	if keys == nil {
		keys = make(map[string]suspectHalfOpen)
		t.halfOpen[target] = keys
	}
	state.owner = owner
	state.until = now.Add(lease).UnixNano()
	if state.expires == 0 {
		state.expires = now.Add(exitRegionTTL).UnixNano()
	}
	keys[key] = state
	t.active.Store(true)
	return true
}

func (t *SuspectTracker) ReleaseHalfOpen(target, key, owner string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if state, ok := t.halfOpen[target][key]; ok && state.owner == owner {
		state.owner = ""
		state.until = 0
		t.halfOpen[target][key] = state
	}
}

func (t *SuspectTracker) TryHalfOpen(target, key, owner string, now time.Time, lease time.Duration) bool {
	if target == "" || key == "" || owner == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	nowNano := now.UnixNano()
	until := t.suspected[target][key]
	state, leased := t.halfOpen[target][key]
	if leased && state.owner != "" && state.until > nowNano {
		return state.owner == owner
	}
	if until <= nowNano && (!leased || state.expires <= nowNano) {
		return false
	}
	if t.halfOpen == nil {
		t.halfOpen = make(map[string]map[string]suspectHalfOpen)
	}
	keys := t.halfOpen[target]
	if keys == nil {
		keys = make(map[string]suspectHalfOpen)
		t.halfOpen[target] = keys
	}
	if until <= nowNano {
		until = state.expires
	}
	keys[key] = suspectHalfOpen{owner: owner, until: now.Add(lease).UnixNano(), expires: until}
	t.active.Store(true)
	return true
}

// Clear drops a suspicion that a later answer has disproved.
func (t *SuspectTracker) Clear(target, key string) {
	if target == "" || key == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if keys := t.pending[target]; keys != nil {
		delete(keys, key)
		if len(keys) == 0 {
			delete(t.pending, target)
		}
	}
	if keys := t.suspected[target]; keys != nil {
		delete(keys, key)
		if len(keys) == 0 {
			delete(t.suspected, target)
		}
	}
	if keys := t.halfOpen[target]; keys != nil {
		delete(keys, key)
		if len(keys) == 0 {
			delete(t.halfOpen, target)
		}
	}
	t.active.Store(len(t.suspected) > 0 || len(t.halfOpen) > 0)
}

func (t *SuspectTracker) prune(now time.Time) {
	nowNano := now.UnixNano()
	for target, keys := range t.pending {
		for key, seen := range keys {
			for identity, at := range seen {
				if now.Sub(time.Unix(0, at)) > suspectConfirmWindow {
					delete(seen, identity)
				}
			}
			if len(seen) == 0 {
				delete(keys, key)
			}
		}
		if len(keys) == 0 {
			delete(t.pending, target)
		}
	}
	for target, keys := range t.suspected {
		for key, until := range keys {
			if until <= nowNano {
				if t.halfOpen == nil {
					t.halfOpen = make(map[string]map[string]suspectHalfOpen)
				}
				halfOpen := t.halfOpen[target]
				if halfOpen == nil {
					halfOpen = make(map[string]suspectHalfOpen)
					t.halfOpen[target] = halfOpen
				}
				if _, exists := halfOpen[key]; !exists {
					halfOpen[key] = suspectHalfOpen{expires: now.Add(exitRegionTTL).UnixNano()}
				}
				delete(keys, key)
			}
		}
		if len(keys) == 0 {
			delete(t.suspected, target)
		}
	}
	for target, keys := range t.halfOpen {
		for key, state := range keys {
			if state.owner != "" && state.until <= nowNano {
				state.owner = ""
				state.until = 0
				keys[key] = state
			}
			if state.expires <= nowNano {
				delete(keys, key)
			}
		}
		if len(keys) == 0 {
			delete(t.halfOpen, target)
		}
	}
	for target, keys := range t.successes {
		for key, seen := range keys {
			for identity, at := range seen {
				if now.Sub(time.Unix(0, at)) > suspectConfirmWindow {
					delete(seen, identity)
				}
			}
			if len(seen) == 0 {
				delete(keys, key)
			}
		}
		if len(keys) == 0 {
			delete(t.successes, target)
		}
	}
	t.lastPruned.Store(nowNano)
	t.active.Store(len(t.suspected) > 0 || len(t.halfOpen) > 0)
}

// pruneDue reports whether the periodic cleanup is due; the caller decides which lock to take.
func (t *SuspectTracker) pruneDue(now time.Time) bool {
	interval := min(suspectConfirmWindow, time.Minute)
	return now.UnixNano()-t.lastPruned.Load() >= int64(interval)
}

// pruneIfDue runs the cleanup for a read path, it takes the write lock itself.
func (t *SuspectTracker) pruneIfDue(now time.Time) {
	if !t.pruneDue(now) {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	// another reader may have pruned while this one waited for the lock
	if !t.pruneDue(now) {
		return
	}
	t.prune(now)
}

// pruneIfDueLocked runs the cleanup for a caller that already holds the write lock.
func (t *SuspectTracker) pruneIfDueLocked(now time.Time) {
	if !t.pruneDue(now) {
		return
	}
	t.prune(now)
}

// ParseExitAnswer reads the region and address out of any of the exit endpoints; the
// address is only for the caller's own lookup and must not be stored.
func ParseExitAnswer(body []byte) (region string, ip netip.Addr) {
	if region, ip = ParseExitTrace(body); region != "" {
		return region, ip
	}
	var answer struct {
		CountryCode string `json:"country_code"`
		Country     string `json:"country"`
		IP          string `json:"ip"`
	}
	if json.Unmarshal(body, &answer) != nil {
		return "", netip.Addr{}
	}
	code := answer.CountryCode
	if code == "" && len(answer.Country) == 2 {
		code = answer.Country
	}
	if code = strings.ToLower(strings.TrimSpace(code)); len(code) != 2 {
		return "", netip.Addr{}
	}
	if parsed, err := netip.ParseAddr(strings.TrimSpace(answer.IP)); err == nil {
		ip = parsed
	}
	return code, ip
}

func ParseExitTrace(body []byte) (region string, ip netip.Addr) {
	for line := range strings.SplitSeq(string(body), "\n") {
		line = strings.TrimSpace(line)
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "loc":
			if v := strings.ToLower(strings.TrimSpace(value)); len(v) == 2 {
				region = v
			}
		case "ip":
			if v, err := netip.ParseAddr(strings.TrimSpace(value)); err == nil {
				ip = v
			}
		}
	}
	return region, ip
}

// IsRegionUnavailableLocation tells a page that blames the region from an ordinary
// relocation: only the former may record a block for the whole region. A region word next
// to a refusal word counts as one; either word alone is an ordinary page.
func IsRegionUnavailableLocation(location string) bool {
	if location == "" {
		return false
	}
	location = strings.ToLower(location)
	for _, pattern := range []string{
		"app-unavailable-in-region",
		"/welcome/unavailable",
		"unavailable-in-region",
		"region-unavailable",
		"not-available-in-your-region",
		"unsupported-region",
	} {
		if strings.Contains(location, pattern) {
			return true
		}
	}
	region := false
	for _, word := range []string{"region", "country", "location", "geo-block", "geoblock", "geo-restrict", "georestrict"} {
		if strings.Contains(location, word) {
			region = true
			break
		}
	}
	if !region {
		return false
	}
	for _, word := range []string{"unavailable", "not-available", "notavailable", "unsupported", "blocked", "restricted", "denied"} {
		if strings.Contains(location, word) {
			return true
		}
	}
	return false
}

// IsRegionUnavailableText recognizes a refusal that is only stated in the page body.
func IsRegionUnavailableText(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	text := strings.ReplaceAll(strings.ToLower(string(body)), "_", " ")
	for _, phrase := range []string{
		"not available in your region",
		"not available in your country",
		"not available in your area",
		"not available in your location",
		"not available in your territory",
		"unavailable in your region",
		"unavailable in your country",
		"not available in this region",
		"not available in this country",
		"not available in the region",
		"unsupported country",
		"unsupported region",
		"region is not supported",
		"country is not supported",
		"not supported in your country",
		"not supported in your region",
		"blocked in your region",
		"blocked in your country",
		"geo-blocked",
		"geoblocked",
		"geo-restricted",
		"georestricted",
		"地区不可用",
		"所在地区不可用",
		"不支持您所在的",
		"您所在的地区",
		"お住まいの地域",
		"お住まいの国",
		"地域ではご利用",
	} {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

// ExitKey derives a pseudonymous identity of an exit address: nodes behind the same
// machine share it, and the address itself is never kept.
func ExitKey(ip netip.Addr) string {
	sum := sha256.Sum256(ip.AsSlice())
	return hex.EncodeToString(sum[:6])
}
