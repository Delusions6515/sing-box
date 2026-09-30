package group

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/smart"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/stretchr/testify/require"
)

func successfulSmartDial(context.Context, string, M.Socksaddr) (net.Conn, error) {
	local, peer := net.Pipe()
	_ = peer.Close()
	return local, nil
}

func TestSmartPinReusesWinnerAndInvalidatesOnFailure(t *testing.T) {
	var firstCalls, secondCalls atomic.Int32
	failSecond := atomic.Bool{}
	first := &smartTestOutbound{tag: "first", dial: func(ctx context.Context, n string, d M.Socksaddr) (net.Conn, error) {
		firstCalls.Add(1)
		if !failSecond.Load() {
			return nil, errors.New("first unavailable")
		}
		return successfulSmartDial(ctx, n, d)
	}}
	second := &smartTestOutbound{tag: "second", dial: func(ctx context.Context, n string, d M.Socksaddr) (net.Conn, error) {
		secondCalls.Add(1)
		if failSecond.Load() {
			return nil, errors.New("second unavailable")
		}
		return successfulSmartDial(ctx, n, d)
	}}
	group := newSmartTestGroup()
	group.maxSelected = 10
	group.candidates = []adapter.Outbound{first, second}
	destination := M.ParseSocksaddr("www.example.com:443")
	for range 2 {
		conn, err := group.DialContext(context.Background(), "tcp", destination)
		require.NoError(t, err)
		require.NoError(t, conn.Close())
	}
	require.EqualValues(t, 1, firstCalls.Load())
	require.EqualValues(t, 2, secondCalls.Load())
	failSecond.Store(true)
	conn, err := group.DialContext(context.Background(), "tcp", destination)
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	require.Equal(t, "first", group.Now())
}

func TestSmartDeterministicPrefixDoesNotRaceHealthyPrimary(t *testing.T) {
	var secondCalls atomic.Int32
	first := &smartTestOutbound{tag: "first", dial: successfulSmartDial}
	second := &smartTestOutbound{tag: "second", dial: func(ctx context.Context, n string, d M.Socksaddr) (net.Conn, error) {
		secondCalls.Add(1)
		return successfulSmartDial(ctx, n, d)
	}}
	group := newSmartTestGroup()
	group.maxSelected = 10
	group.candidates = []adapter.Outbound{first, second}
	conn, err := group.DialContext(context.Background(), "tcp", M.ParseSocksaddr("www.example.com:443"))
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	require.Zero(t, secondCalls.Load())
}

func TestSmartStageTimeoutLeavesTimeForFallback(t *testing.T) {
	first := &smartTestOutbound{tag: "first", dial: func(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	second := &smartTestOutbound{tag: "second", dial: successfulSmartDial}
	group := newSmartTestGroup()
	group.maxSelected = 10
	group.candidates = []adapter.Outbound{first, second}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	conn, err := group.DialContext(ctx, "tcp", M.ParseSocksaddr("www.example.com:443"))
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	require.Equal(t, "second", group.Now())
}

func smartDialTestCandidates(tags ...string) []smartCandidate {
	candidates := make([]smartCandidate, 0, len(tags))
	for index, tag := range tags {
		candidates = append(candidates, smartCandidate{
			outbound: &smartTestOutbound{tag: tag},
			status: smart.Candidate{Key: smart.MetricKey{
				Group: "smart", Target: "example.com", Network: "tcp", Node: tag,
			}},
			index: index,
		})
	}
	return candidates
}

func TestSmartLeafDeadlineExceededCountsOnce(t *testing.T) {
	group := newSmartTestGroup()
	candidates := smartDialTestCandidates("leaf")
	_, _, _, err := dialSmartConnection(group, context.Background(), "tcp", "example.com", candidates, func(context.Context, smartCandidate) (net.Conn, error) {
		return nil, context.DeadlineExceeded
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	input, loaded := group.store.ModelInput(candidates[0].status.Key)
	require.True(t, loaded)
	require.EqualValues(t, 1, input.Failure)
}

func TestSmartBatchEarlyFailureAndStageExpiryCountOnce(t *testing.T) {
	group := newSmartTestGroup()
	candidates := smartDialTestCandidates("first", "second", "third", "early", "pending")
	for _, candidate := range candidates[3:] {
		group.store.Record(time.Now(), candidate.status.Key, smart.Observation{Success: true, ConnectTime: 10 * time.Millisecond})
	}
	_, _, _, err := dialSmartConnection(group, context.Background(), "tcp", "example.com", candidates, func(ctx context.Context, candidate smartCandidate) (net.Conn, error) {
		if candidate.outbound.Tag() == "pending" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return nil, errors.New("unavailable")
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	for _, candidate := range candidates {
		input, loaded := group.store.ModelInput(candidate.status.Key)
		require.True(t, loaded)
		require.EqualValues(t, 1, input.Failure, candidate.outbound.Tag())
	}
}

func TestSmartParallelBudgetAllowsSlowHealthyPeer(t *testing.T) {
	for _, known := range []bool{true, false} {
		t.Run(map[bool]string{true: "learned", false: "unknown"}[known], func(t *testing.T) {
			group := newSmartTestGroup()
			candidates := smartDialTestCandidates("first", "second", "third", "fast-failed", "slow-healthy")
			group.store.Record(time.Now(), candidates[3].status.Key, smart.Observation{Success: true, ConnectTime: 10 * time.Millisecond})
			if known {
				group.store.Record(time.Now(), candidates[4].status.Key, smart.Observation{Success: true, ConnectTime: 200 * time.Millisecond})
			}
			conn, winner, _, err := dialSmartConnection(group, context.Background(), "tcp", "example.com", candidates, func(ctx context.Context, candidate smartCandidate) (net.Conn, error) {
				if candidate.outbound.Tag() != "slow-healthy" {
					return nil, errors.New("unavailable")
				}
				timer := time.NewTimer(200 * time.Millisecond)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-timer.C:
					return successfulSmartDial(ctx, "tcp", M.Socksaddr{})
				}
			})
			require.NoError(t, err)
			require.Same(t, candidates[4].outbound, winner.outbound)
			require.NoError(t, conn.Close())
			input, _ := group.store.ModelInput(candidates[4].status.Key)
			require.Zero(t, input.Failure)
			input, _ = group.store.ModelInput(candidates[3].status.Key)
			require.EqualValues(t, 1, input.Failure)
		})
	}
}

func TestSmartBatchWinnerDoesNotCountCanceledLoser(t *testing.T) {
	group := newSmartTestGroup()
	candidates := smartDialTestCandidates("first", "second", "third", "winner", "loser")
	started := make(chan struct{})
	finished := make(chan struct{})
	conn, _, _, err := dialSmartConnection(group, context.Background(), "tcp", "example.com", candidates, func(ctx context.Context, candidate smartCandidate) (net.Conn, error) {
		switch candidate.outbound.Tag() {
		case "winner":
			<-started
			return successfulSmartDial(ctx, "tcp", M.Socksaddr{})
		case "loser":
			close(started)
			<-ctx.Done()
			defer close(finished)
			return nil, ctx.Err()
		default:
			return nil, errors.New("unavailable")
		}
	})
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("race loser was not canceled")
	}
	require.Zero(t, group.store.Candidate(time.Now(), candidates[4].status.Key).Samples)
}

func TestSmartStageDoesNotCountCallerCancellation(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "canceled", true: "deadline"}[deadline], func(t *testing.T) {
			group := newSmartTestGroup()
			candidates := smartDialTestCandidates("pending")
			caller, cancel := context.WithCancel(context.Background())
			if deadline {
				cancel()
				caller, cancel = context.WithTimeout(context.Background(), 30*time.Millisecond)
			}
			defer cancel()
			stage, stop := context.WithTimeout(caller, time.Second)
			defer stop()
			_, _, _, err := raceSmartConnection(group, caller, stage, candidates, func(ctx context.Context, _ smartCandidate) (net.Conn, error) {
				if !deadline {
					cancel()
				}
				<-ctx.Done()
				return nil, ctx.Err()
			})
			require.ErrorIs(t, err, caller.Err())
			require.Zero(t, group.store.Candidate(time.Now(), candidates[0].status.Key).Samples)
		})
	}
}

func TestSmartStageExpiryCountsOnlyStartedAttempts(t *testing.T) {
	group := newSmartTestGroup()
	candidates := smartDialTestCandidates("one", "two", "three", "four", "five", "not-started")
	caller := context.Background()
	stage, cancel := context.WithTimeout(caller, 30*time.Millisecond)
	defer cancel()
	_, _, _, err := raceSmartConnection(group, caller, stage, candidates, func(ctx context.Context, _ smartCandidate) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	for _, candidate := range candidates[:5] {
		require.EqualValues(t, 1, group.store.Candidate(time.Now(), candidate.status.Key).Samples)
	}
	require.Zero(t, group.store.Candidate(time.Now(), candidates[5].status.Key).Samples)
}

func TestSmartFailureFloodStopsMetricGrowth(t *testing.T) {
	group := newSmartTestGroup()
	key := smart.MetricKey{Group: "smart", Target: "example.com", Network: "tcp", Node: "node"}
	for range 50 {
		group.recordObservation(key, smart.Observation{Success: false})
	}
	before := group.store.Candidate(time.Now(), key).Samples
	for range 100 {
		group.recordObservation(key, smart.Observation{Success: false})
	}
	require.Equal(t, before, group.store.Candidate(time.Now(), key).Samples)
	group.recordObservation(key, smart.Observation{Success: true})
	group.recordObservation(key, smart.Observation{Success: false})
	require.Equal(t, before+2, group.store.Candidate(time.Now(), key).Samples)
}
