package smart

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type testExitProber func(context.Context, bool) (*ExitProbeResult, error)

func (f testExitProber) ExitProbe(ctx context.Context, wantASN bool) (*ExitProbeResult, error) {
	return f(ctx, wantASN)
}
func inlineExitTask(task func(context.Context)) bool { task(context.Background()); return true }

func TestExitWatcherReplaysHeldEvidence(t *testing.T) {
	store := NewStore(Config{})
	var invalidations atomic.Int64
	watcher := NewExitWatcher(ExitWatcherOptions{Store: store, Invalidate: func(string) { invalidations.Add(1) }})
	require.False(t, watcher.Note("site", "a"))
	require.False(t, watcher.Note("site", "b"))
	require.False(t, watcher.NoteSuccess("site", "control"))
	for _, node := range []string{"a", "b", "control"} {
		region := "us"
		if node == "control" {
			region = "jp"
		}
		watcher.MaybeProbe(node, testExitProber(func(context.Context, bool) (*ExitProbeResult, error) {
			return &ExitProbeResult{Region: region, Key: node}, nil
		}), inlineExitTask)
	}
	require.True(t, watcher.Suspected("site", "a"))
	require.True(t, watcher.Suspected("site", "b"))
	require.False(t, watcher.Suspected("site", "control"))
	require.EqualValues(t, 1, invalidations.Load())
	require.Empty(t, store.Snapshot(time.Now(), time.Hour, 100).Metrics)
	require.Len(t, store.Snapshot(time.Now(), time.Hour, 100).Exits, 3)
	watcher.Clear("site", "a")
	require.False(t, watcher.Suspected("site", "b"))
}

func TestExitWatcherRestoresFreshAnswersAndRetryGap(t *testing.T) {
	store := NewStore(Config{})
	now := time.Now()
	store.SetExitState("fresh", ExitNodeState{ExitInfo: ExitInfo{Region: "us", Key: "hash", Updated: now.Unix()}})
	store.SetExitState("failed", ExitNodeState{Failed: now.Unix()})
	watcher := NewExitWatcher(ExitWatcherOptions{Store: store})
	var calls atomic.Int64
	probe := testExitProber(func(context.Context, bool) (*ExitProbeResult, error) {
		calls.Add(1)
		return &ExitProbeResult{Region: "jp", Key: "new"}, nil
	})
	watcher.MaybeProbe("fresh", probe, inlineExitTask)
	watcher.MaybeProbe("failed", probe, inlineExitTask)
	require.Zero(t, calls.Load())
	require.Equal(t, "us", watcher.Info("fresh").Region)
}

func TestExitWatcherASNSuspicionIsOptional(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		watcher := NewExitWatcher(ExitWatcherOptions{WantASN: func() bool { return enabled }})
		now := time.Now()
		watcher.Store("a", ExitInfo{Region: "us", ASN: "64500", Key: "a"}, now)
		watcher.Store("b", ExitInfo{Region: "ca", ASN: "64500", Key: "b"}, now)
		watcher.Store("control", ExitInfo{Region: "jp", ASN: "64501", Key: "c"}, now)
		watcher.NoteSuccess("site", "control")
		watcher.Note("site", "a")
		watcher.Note("site", "b")
		require.Equal(t, enabled, watcher.Suspected("site", "a"))
	}
}

func TestResponseGlobalBudgetsAndIndependentSlots(t *testing.T) {
	var release []func()
	for range probeMaxInflight {
		done, ok := TryStartProbe()
		require.True(t, ok)
		release = append(release, done)
	}
	_, ok := TryStartProbe()
	require.False(t, ok)
	exitDone, ok := TryStartExitProbe()
	require.True(t, ok, "response saturation must leave exit slots available")
	exitDone()
	for _, done := range release {
		done()
	}
	globalProbeThrottle.mu.Lock()
	oldTokens, oldRefill := globalProbeThrottle.tokens, globalProbeThrottle.lastRefill
	globalProbeThrottle.tokens = 0
	globalProbeThrottle.lastRefill = time.Time{}
	globalProbeThrottle.mu.Unlock()
	defer func() {
		globalProbeThrottle.mu.Lock()
		globalProbeThrottle.tokens = oldTokens
		globalProbeThrottle.lastRefill = oldRefill
		globalProbeThrottle.mu.Unlock()
	}()
	now := time.Now()
	for range probeGlobalBurst {
		require.True(t, AllowGlobalProbe(now))
	}
	require.False(t, AllowGlobalProbe(now))
	require.True(t, AllowGlobalProbe(now.Add(time.Second)))
}
