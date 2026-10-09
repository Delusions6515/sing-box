package smart

import (
	"context"
	"crypto/x509"
	"errors"
	"net/http"
	"net/netip"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestResponseClassificationFollowsMihomo(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name    string
		status  int
		headers http.Header
		body    string
		action  VerdictAction
		ttl     time.Duration
		control bool
	}{
		{name: "success", status: 200, action: VerdictReachable, control: true},
		{name: "missing robots still reachable", status: 404, action: VerdictReachable},
		{name: "ordinary redirect neutral", status: 302, headers: http.Header{"Location": []string{"https://login.example/"}}, action: VerdictIgnore},
		{name: "region redirect", status: 302, headers: http.Header{"Location": []string{"/app-unavailable-in-region"}}, action: VerdictRecord, ttl: 30 * time.Minute},
		{name: "region body", status: 200, body: "not available in your country", action: VerdictRecord, ttl: 30 * time.Minute},
		{name: "forbidden", status: 403, action: VerdictRecord, ttl: 10 * time.Minute},
		{name: "challenge", status: 200, headers: http.Header{"Cf-Mitigated": []string{"challenge"}}, action: VerdictRecord, ttl: 10 * time.Minute},
		{name: "retry after lower bound", status: 429, headers: http.Header{"Retry-After": []string{"1"}}, action: VerdictRecord, ttl: time.Minute},
		{name: "retry after cap", status: 429, headers: http.Header{"Retry-After": []string{"999999"}}, action: VerdictRecord, ttl: 30 * time.Minute},
		{name: "retry after date", status: 503, headers: http.Header{"Retry-After": []string{now.Add(3 * time.Minute).UTC().Format(http.TimeFormat)}}, action: VerdictRecord, ttl: 3 * time.Minute},
		{name: "origin error", status: 500, action: VerdictRecord, ttl: 5 * time.Minute},
		{name: "edge error", status: 502, body: "cf-error-details", action: VerdictRecord, ttl: 5 * time.Minute},
		{name: "misdirected", status: 421, action: VerdictIgnore},
		{name: "method refused", status: 405, action: VerdictIgnore},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			verdict := ClassifyResponse(test.status, test.headers, []byte(test.body), now)
			require.Equal(t, test.action, verdict.Action)
			require.Equal(t, test.control, verdict.ControlSuccess)
			require.InDelta(t, float64(test.ttl), float64(verdict.TTL), float64(time.Second))
		})
	}
	require.Equal(t, VerdictIgnore, ClassifyProbeError(context.Canceled).Action)
	require.Equal(t, VerdictRecord, ClassifyProbeError(context.DeadlineExceeded).Action)
	require.Equal(t, VerdictRecord, ClassifyProbeError(x509.HostnameError{}).Action)
	require.Equal(t, VerdictIgnore, ClassifyProbeError(errors.New("local dial failed")).Action)
}

func TestResponseProbeEligibility(t *testing.T) {
	require.True(t, ResponseProbeEligible("example.com", 443, 100, false, false))
	require.False(t, ResponseProbeEligible("example.com", 443, 40000, false, false))
	require.False(t, ResponseProbeEligible("example.com", 80, 100, false, false))
	require.False(t, ResponseProbeEligible("example.com", 443, 100, true, false))
	require.False(t, ResponseProbeEligible("example.com", 443, 100, false, true))
	require.False(t, ResponseProbeEligible("", 443, 100, false, false))
	require.Equal(t, "https://api.example.com/robots.txt", ProbeURL("api.example.com"))
}

func TestResponseProbeThrottleAndHostBlindness(t *testing.T) {
	var throttle ProbeThrottle
	now := time.Now()
	require.True(t, throttle.AllowNode("site", "node", now))
	require.False(t, throttle.AllowNode("site", "node", now.Add(time.Minute)))
	throttle.NoteFailure("site", "node", now)
	require.True(t, throttle.AllowNode("site", "node", now.Add(31*time.Second)))
	for i := range 6 {
		allowed := throttle.AllowHostRecord("host", Verdict{Action: VerdictRecord}, now)
		require.Equal(t, i < 5, allowed)
	}
	require.True(t, throttle.Blind("host", now))
	require.True(t, throttle.AllowHostRecord("host", Verdict{Action: VerdictReachable}, now))
	require.False(t, throttle.Blind("host", now))
}

func TestExitSuspicionNeedsDistinctExitsAndControl(t *testing.T) {
	var tracker SuspectTracker
	now := time.Now()
	require.False(t, tracker.Note("site", "us", "exit-a", now))
	require.False(t, tracker.Note("site", "us", "exit-a", now))
	require.False(t, tracker.Note("site", "us", "exit-b", now))
	require.False(t, tracker.Suspected("site", "us", now))
	require.Empty(t, tracker.NoteSuccess("site", "jp", "exit-a", now), "the same exit cannot corroborate its own refusal")
	require.Equal(t, []string{"us"}, tracker.NoteSuccess("site", "jp", "exit-c", now))
	require.True(t, tracker.Suspected("site", "us", now))
	require.False(t, tracker.Suspected("other", "us", now))
	require.True(t, tracker.Defer("site", "us", "node-a", now, ProbeTimeout))
	require.True(t, tracker.TryHalfOpen("site", "us", "node-a", now, ProbeTimeout))
	require.False(t, tracker.TryHalfOpen("site", "us", "node-b", now, ProbeTimeout))
	tracker.Clear("site", "us")
	require.False(t, tracker.Suspected("site", "us", now))
}

func TestExitIdentityAndHistory(t *testing.T) {
	for _, body := range []string{"loc=US\nip=203.0.113.8\n", `{"country_code":"US","ip":"203.0.113.8"}`} {
		region, ip := ParseExitAnswer([]byte(body))
		require.Equal(t, "us", region)
		require.Equal(t, netip.MustParseAddr("203.0.113.8"), ip)
	}
	require.Len(t, ExitKey(netip.MustParseAddr("203.0.113.8")), 12)
	store := NewStore(Config{MaxEntries: 10})
	state := ExitNodeState{ExitInfo: ExitInfo{Region: "us", ASN: "64500", Key: "hashed", Updated: time.Now().Unix()}, Failed: time.Now().Unix()}
	store.SetExitState("node", state)
	require.True(t, store.HasPendingChanges())
	snapshot := store.Snapshot(time.Now(), time.Hour, 10)
	require.Empty(t, snapshot.Metrics, "exit probe is not a traffic observation")
	restored := NewStore(Config{})
	require.True(t, restored.Restore(snapshot))
	loaded, ok := restored.ExitState("node")
	require.True(t, ok)
	require.Equal(t, state, loaded)
	restored.Clear()
	_, ok = restored.ExitState("node")
	require.False(t, ok)
}
