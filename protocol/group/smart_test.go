package group

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/common/smart"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"
	"github.com/sagernet/sing/service/filemanager"
	"github.com/stretchr/testify/require"
)

func TestSmartTargetPrefersInboundNames(t *testing.T) {
	destination := M.ParseSocksaddr("203.0.113.1:443")
	require.Equal(t, "video.example", smartTarget(&adapter.InboundContext{SniffHost: "Video.Example."}, destination))
	require.Equal(t, "cache.example", smartTarget(&adapter.InboundContext{Domain: "Cache.Example."}, destination))
	require.Equal(t, "203.0.113.1", smartTarget(nil, destination))
}

func TestSmartRejectsInvalidTuningOptions(t *testing.T) {
	for name, options := range map[string]option.SmartOutboundOptions{
		"policy":            {PolicyPriority: "node:"},
		"sample below zero": {SampleRate: -0.1},
		"sample above one":  {SampleRate: 1.1},
		"status range":      {ExpectedStatus: "204-200"},
		"status code":       {ExpectedStatus: "600"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewSmart(context.Background(), nil, log.NewNOPFactory().NewLogger("test"), "smart", options)
			require.Error(t, err)
		})
	}
}

func TestSmartURLTestRejectsUnexpectedStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(server.Close)
	group := newSmartTestGroup()
	group.url = server.URL
	group.expectedStatus = smart.StatusRanges{{From: http.StatusNoContent, To: http.StatusNoContent}}
	_, err := group.urlTest(context.Background(), N.SystemDialer)
	require.Error(t, err)
}

func TestSmartFallbackToleranceKeepsConfigurationOrder(t *testing.T) {
	first := &smartTestOutbound{tag: "first"}
	second := &smartTestOutbound{tag: "second"}
	history := urltest.NewHistoryStorage()
	history.StoreURLTestHistory("first", &adapter.URLTestHistory{Delay: 120})
	history.StoreURLTestHistory("second", &adapter.URLTestHistory{Delay: 100})
	candidates := []smartCandidate{{outbound: first, index: 0}, {outbound: second, index: 1}}
	sortFallbackCandidates(candidates, history, 25)
	require.Same(t, first, candidates[0].outbound)
	sortFallbackCandidates(candidates, history, 10)
	require.Same(t, second, candidates[0].outbound)
}

func TestSmartFallbackToleranceUsesDeterministicBuckets(t *testing.T) {
	first := &smartTestOutbound{tag: "first"}
	second := &smartTestOutbound{tag: "second"}
	third := &smartTestOutbound{tag: "third"}
	history := urltest.NewHistoryStorage()
	history.StoreURLTestHistory("first", &adapter.URLTestHistory{Delay: 150})
	history.StoreURLTestHistory("second", &adapter.URLTestHistory{Delay: 100})
	history.StoreURLTestHistory("third", &adapter.URLTestHistory{Delay: 50})
	candidates := []smartCandidate{{outbound: first, index: 0}, {outbound: second, index: 1}, {outbound: third, index: 2}}
	sortFallbackCandidates(candidates, history, 75)
	require.Same(t, second, candidates[0].outbound)
	require.Same(t, third, candidates[1].outbound)
	require.Same(t, first, candidates[2].outbound)
}

func TestSmartStatusUsesEmptyCandidatesAtColdStart(t *testing.T) {
	status := newSmartTestGroup().SmartStatus()
	require.Empty(t, status.Selected)
	require.Nil(t, status.UpdatedAt)
	require.NotNil(t, status.Candidates)
	require.Empty(t, status.Candidates)
}

func TestSmartRaceReturnsFirstSuccessAndClosesLateWinner(t *testing.T) {
	lateClosed := make(chan struct{})
	fast := &smartTestOutbound{tag: "fast", dial: func(context.Context, string, M.Socksaddr) (net.Conn, error) {
		left, right := net.Pipe()
		_ = right.Close()
		return left, nil
	}}
	slow := &smartTestOutbound{tag: "slow", dial: func(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
		<-ctx.Done()
		left, right := net.Pipe()
		_ = right.Close()
		return &smartTestConn{Conn: left, closed: lateClosed}, nil
	}}
	group := newSmartTestGroup()
	conn, winner, _, err := group.raceDial(context.Background(), N.NetworkTCP, M.ParseSocksaddr("example.com:443"), "example.com", []smartCandidate{{outbound: slow}, {outbound: fast}})
	require.NoError(t, err)
	require.Same(t, fast, winner.outbound)
	require.NoError(t, conn.Close())
	select {
	case <-lateClosed:
	case <-time.After(time.Second):
		t.Fatal("late successful dial was not closed")
	}
}

func TestSmartRaceCallerCancellationDoesNotRecordFailure(t *testing.T) {
	group := newSmartTestGroup()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	candidate := &smartTestOutbound{tag: "blocked", dial: func(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	_, _, _, err := group.raceDial(ctx, N.NetworkTCP, M.ParseSocksaddr("example.com:443"), "example.com", []smartCandidate{{outbound: candidate}})
	require.ErrorIs(t, err, context.Canceled)
	status := group.store.Candidate(time.Now(), smart.MetricKey{Group: "smart", Target: "example.com", Network: N.NetworkTCP, Node: "blocked"})
	require.Zero(t, status.Samples)
}

func TestSmartRaceCanceledContextDoesNotStartCandidates(t *testing.T) {
	group := newSmartTestGroup()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls atomic.Int64
	candidate := &smartTestOutbound{tag: "candidate", dial: func(context.Context, string, M.Socksaddr) (net.Conn, error) {
		calls.Add(1)
		return nil, errors.New("dial should not start")
	}}
	_, _, _, err := group.raceDial(ctx, N.NetworkTCP, M.ParseSocksaddr("example.com:443"), "example.com", []smartCandidate{{outbound: candidate}})
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, calls.Load())
}

func TestSmartListenPacketRaceReturnsFirstSuccessAndClosesLateWinner(t *testing.T) {
	lateClosed := make(chan struct{})
	fast := &smartTestOutbound{tag: "fast", listen: func(context.Context, M.Socksaddr) (net.PacketConn, error) {
		return net.ListenPacket("udp", "127.0.0.1:0")
	}}
	slow := &smartTestOutbound{tag: "slow", listen: func(ctx context.Context, _ M.Socksaddr) (net.PacketConn, error) {
		<-ctx.Done()
		conn, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		return &smartTestPacketConn{PacketConn: conn, closed: lateClosed}, nil
	}}
	group := newSmartTestGroup()
	conn, winner, _, err := group.raceListenPacket(context.Background(), M.ParseSocksaddr("example.com:443"), "example.com", []smartCandidate{{outbound: slow}, {outbound: fast}})
	require.NoError(t, err)
	require.Same(t, fast, winner.outbound)
	require.NoError(t, conn.Close())
	select {
	case <-lateClosed:
	case <-time.After(time.Second):
		t.Fatal("late successful packet dial was not closed")
	}
}

func TestSmartConnCountsBufferedIOAndClosesOnce(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	var (
		access   sync.Mutex
		upload   int64
		download int64
		calls    int
	)
	conn := newSmartConn(left, func(_ bool, gotUpload, gotDownload int64, _, _ time.Duration, _ float64, _ bool) {
		access.Lock()
		defer access.Unlock()
		upload, download = gotUpload, gotDownload
		calls++
	})
	writeDone := make(chan error, 1)
	go func() { writeDone <- conn.WriteBuffer(buf.As([]byte("write"))) }()
	buffer := buf.NewSize(16)
	require.NoError(t, right.SetReadDeadline(time.Now().Add(time.Second)))
	_, err := right.Read(buffer.FreeBytes())
	require.NoError(t, err)
	go func() {
		_, _ = right.Write([]byte("read"))
	}()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(time.Second)))
	require.NoError(t, conn.ReadBuffer(buffer))
	require.NoError(t, <-writeDone)
	require.NoError(t, conn.Close())
	require.NoError(t, conn.Close())
	access.Lock()
	defer access.Unlock()
	require.Equal(t, int64(5), upload)
	require.Equal(t, int64(4), download)
	require.Equal(t, 1, calls)
}

func TestSmartConnectionsDoNotUnwrapPastTracking(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	conn := newSmartConn(left, func(bool, int64, int64, time.Duration, time.Duration, float64, bool) {})
	reader, counters := N.UnwrapCountReader(conn, nil)
	require.Same(t, conn, reader)
	require.Empty(t, counters)
	require.NoError(t, conn.Close())
}

func TestSmartPacketCopyRecordsErrorWithoutUnwrapping(t *testing.T) {
	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	defer packetConn.Close()
	readErr := errors.New("packet read failed")
	var succeeded bool
	conn := newSmartPacketConn(packetConn, &smartFailingExtendedPacketConn{PacketConn: packetConn, err: readErr}, func(success bool, _, _ int64, _, _ time.Duration) {
		succeeded = success
	})
	reader, counters := N.UnwrapCountPacketReader(conn, nil)
	require.Same(t, conn, reader)
	require.Empty(t, counters)
	_, err = bufio.CopyPacket(smartDiscardPacketWriter{}, conn)
	require.ErrorIs(t, err, readErr)
	require.NoError(t, conn.Close())
	require.False(t, succeeded)
}

func TestSmartPacketCopyPreservesOutboundHeadroom(t *testing.T) {
	udpConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	defer udpConn.Close()
	// Mimics a UDP outbound (e.g. vmess XUDP) that prepends a 28-byte header
	// in WritePacket; CopyPacket must allocate buffers with that headroom.
	outbound := &smartHeadroomPacketConn{PacketConn: udpConn}
	var succeeded bool
	conn := newSmartPacketConn(outbound, outbound, func(success bool, _, _ int64, _, _ time.Duration) {
		succeeded = success
	})
	// EOF is the copy loop's normal termination; reaching it at all means
	// WritePacket survived ExtendHeader(28) instead of panicking.
	_, err = bufio.CopyPacket(conn, &smartOnePacketReader{})
	require.ErrorIs(t, err, io.EOF)
	require.NoError(t, conn.Close())
	require.True(t, succeeded)
}

func TestSmartWeightsAggregatesStore(t *testing.T) {
	group := newSmartTestGroup()
	group.candidates = []adapter.Outbound{
		&smartTestOutbound{tag: "fast"},
		&smartTestOutbound{tag: "slow"},
		&smartTestOutbound{tag: "missing"},
	}
	now := time.Now()
	good := smart.Observation{Closed: true, Success: true, ConnectTime: 50 * time.Millisecond, FirstByte: 100 * time.Millisecond, UploadBytes: 1024 * 1024, DownloadBytes: 1024 * 1024, PeakUploadBPS: 300 * 1024, PeakDownloadBPS: 300 * 1024, Duration: 5 * time.Minute}
	for range 8 {
		group.store.Record(now, smart.MetricKey{Group: "smart", Target: "web.example", Network: N.NetworkTCP, Node: "fast"}, good)
	}
	items := group.Weights()
	require.Len(t, items, 3)
	require.Equal(t, "fast", items[0].Name)
	require.Equal(t, 100.0, items[0].Weight)
	// The two unknown nodes tie at 0 and sort by name.
	require.Equal(t, "missing", items[1].Name)
	require.Equal(t, "slow", items[2].Name)
	require.Zero(t, items[1].Weight)
	require.Zero(t, items[2].Weight)
}

func TestSmartClearCacheDropsMetricsAndHistory(t *testing.T) {
	path := t.TempDir() + "/smart-history.json"
	group := newSmartTestGroup()
	group.historyPath = path
	group.historyRetention = time.Hour
	group.maxHistoryEntries = 100
	key := smart.MetricKey{Group: "smart", Target: "example.com", Network: N.NetworkTCP, Node: "node"}
	group.store.Record(time.Now(), key, smart.Observation{Closed: true, Success: true})
	require.NoError(t, group.loadHistory())
	require.Equal(t, int64(1), group.store.Candidate(time.Now(), key).Samples)
	require.NoError(t, group.ClearCache())
	require.Zero(t, group.store.Candidate(time.Now(), key).Samples)
	restored := newSmartTestGroup()
	restored.historyPath = path
	restored.historyRetention = time.Hour
	restored.maxHistoryEntries = 100
	require.NoError(t, restored.loadHistory())
	require.Zero(t, restored.store.Candidate(time.Now(), key).Samples)
	require.NoError(t, restored.Close())
	require.NoError(t, group.Close())
}

func TestSmartDialFallsBackAfterExhaustedRace(t *testing.T) {
	first := &smartTestOutbound{tag: "first", dial: func(context.Context, string, M.Socksaddr) (net.Conn, error) {
		return nil, errors.New("first failed")
	}}
	second := &smartTestOutbound{tag: "second", dial: func(context.Context, string, M.Socksaddr) (net.Conn, error) {
		return nil, errors.New("second failed")
	}}
	third := &smartTestOutbound{tag: "third", dial: func(context.Context, string, M.Socksaddr) (net.Conn, error) {
		return nil, errors.New("third failed")
	}}
	fallback := &smartTestOutbound{tag: "fallback", dial: func(context.Context, string, M.Socksaddr) (net.Conn, error) {
		left, right := net.Pipe()
		_ = right.Close()
		return left, nil
	}}
	group := newSmartTestGroup()
	group.maxSelected = 2
	group.candidates = []adapter.Outbound{first, second, third, fallback}
	destination := M.ParseSocksaddr("example.com:443")
	conn, err := group.DialContext(context.Background(), N.NetworkTCP, destination)
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	require.Equal(t, "fallback", group.Now())
	for _, tag := range []string{"first", "second", "third", "fallback"} {
		status := group.store.Candidate(time.Now(), smart.MetricKey{Group: "smart", Target: "example.com", Network: N.NetworkTCP, Node: tag})
		require.Equalf(t, int64(1), status.Samples, "%s sample count", tag)
	}
}

func TestSmartListenPacketFallsBackAfterExhaustedRace(t *testing.T) {
	first := &smartTestOutbound{tag: "first", listen: func(context.Context, M.Socksaddr) (net.PacketConn, error) {
		return nil, errors.New("first failed")
	}}
	second := &smartTestOutbound{tag: "second", listen: func(context.Context, M.Socksaddr) (net.PacketConn, error) {
		return nil, errors.New("second failed")
	}}
	third := &smartTestOutbound{tag: "third", listen: func(context.Context, M.Socksaddr) (net.PacketConn, error) {
		return nil, errors.New("third failed")
	}}
	fallback := &smartTestOutbound{tag: "fallback", listen: func(context.Context, M.Socksaddr) (net.PacketConn, error) {
		return net.ListenPacket("udp", "127.0.0.1:0")
	}}
	group := newSmartTestGroup()
	group.maxSelected = 2
	group.candidates = []adapter.Outbound{first, second, third, fallback}
	conn, err := group.ListenPacket(context.Background(), M.ParseSocksaddr("example.com:443"))
	require.NoError(t, err)
	require.NoError(t, conn.Close())
	require.Equal(t, "fallback", group.Now())
}

func TestSmartProbeDoesNotChangeBusinessMetrics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		time.Sleep(10 * time.Millisecond)
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	probe := &smartTestOutbound{tag: "probe", dial: func(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
		return new(net.Dialer).DialContext(ctx, network, destination.String())
	}}
	group := newSmartTestGroup()
	group.candidates = []adapter.Outbound{probe}
	group.history = urltest.NewHistoryStorage()
	group.url = server.URL
	group.timeout = time.Second
	key := smart.MetricKey{Group: "smart", Target: "business.example", Network: N.NetworkTCP, Node: "probe"}
	for range 3 {
		group.store.Record(time.Now(), key, smart.Observation{Success: false})
	}
	before := group.store.Candidate(time.Now(), key)
	require.True(t, before.Blocked)
	result, err := group.probe(context.Background())
	require.NoError(t, err)
	require.Contains(t, result, "probe")
	after := group.store.Candidate(time.Now(), key)
	require.Equal(t, before.Samples, after.Samples)
	require.Equal(t, before.Blocked, after.Blocked)
	require.Equal(t, before.Weight, after.Weight)
	require.NotNil(t, group.history.LoadURLTestHistory("probe"))
}

func TestSmartDialRecordsFallbackFailure(t *testing.T) {
	first := &smartTestOutbound{tag: "first", dial: func(context.Context, string, M.Socksaddr) (net.Conn, error) {
		return nil, errors.New("first failed")
	}}
	second := &smartTestOutbound{tag: "second", dial: func(context.Context, string, M.Socksaddr) (net.Conn, error) {
		return nil, errors.New("second failed")
	}}
	fallback := &smartTestOutbound{tag: "fallback", dial: func(context.Context, string, M.Socksaddr) (net.Conn, error) {
		return nil, errors.New("fallback failed")
	}}
	group := newSmartTestGroup()
	group.maxSelected = 2
	group.candidates = []adapter.Outbound{first, second, fallback}
	_, err := group.DialContext(context.Background(), N.NetworkTCP, M.ParseSocksaddr("example.com:443"))
	require.Error(t, err)
	status := group.store.Candidate(time.Now(), smart.MetricKey{Group: "smart", Target: "example.com", Network: N.NetworkTCP, Node: "fallback"})
	require.Equal(t, int64(1), status.Samples)
}

func TestSmartHistoryPreservesGroupsSharingOnePath(t *testing.T) {
	path := t.TempDir() + "/smart-history.json"
	first := newSmartTestGroupWithTag("first")
	second := newSmartTestGroupWithTag("second")
	first.historyPath, second.historyPath = path, path
	first.historyRetention, second.historyRetention = time.Hour, time.Hour
	first.maxHistoryEntries, second.maxHistoryEntries = 100, 100
	require.NoError(t, first.loadHistory())
	first.store.Record(time.Now(), smart.MetricKey{Group: "first", Target: "one.example", Network: N.NetworkTCP, Node: "node"}, smart.Observation{Closed: true, Success: true})
	first.flushHistory(true)
	require.NoError(t, second.loadHistory())
	second.store.Record(time.Now(), smart.MetricKey{Group: "second", Target: "two.example", Network: N.NetworkTCP, Node: "node"}, smart.Observation{Closed: true, Success: true})
	second.flushHistory(true)
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	var history smartHistoryFile
	require.NoError(t, json.Unmarshal(content, &history))
	require.Contains(t, history.Groups, "first")
	require.Contains(t, history.Groups, "second")
	restored := newSmartTestGroupWithTag("first")
	restored.historyPath = path
	require.NoError(t, restored.loadHistory())
	status := restored.store.Candidate(time.Now(), smart.MetricKey{Group: "first", Target: "one.example", Network: N.NetworkTCP, Node: "node"})
	require.Equal(t, int64(1), status.Samples)
}

func TestSmartCloseReleasesUnusedHistoryEntry(t *testing.T) {
	path := t.TempDir() + "/smart-history.json"
	group := newSmartTestGroup()
	group.historyPath = path
	group.historyRetention = time.Hour
	group.maxHistoryEntries = 100
	require.NoError(t, group.loadHistory())
	require.NoError(t, group.Close())
	smartHistoryPool.Lock()
	_, loaded := smartHistoryPool.entries[path]
	smartHistoryPool.Unlock()
	require.False(t, loaded)
}

func TestSmartHistoryRetriesLoadBeforeFlush(t *testing.T) {
	path := t.TempDir() + "/smart-history.json"
	require.NoError(t, os.Mkdir(path, 0o755))
	group := newSmartTestGroup()
	group.historyPath = path
	group.historyRetention = time.Hour
	group.maxHistoryEntries = 100
	require.Error(t, group.loadHistory())
	key := smart.MetricKey{Group: "smart", Target: "fresh.example", Network: N.NetworkTCP, Node: "node"}
	group.store.Record(time.Now(), key, smart.Observation{Closed: true, Success: true})
	require.Error(t, group.flushHistory(true))
	require.NoError(t, os.Remove(path))
	persistedKey := smart.MetricKey{Group: "smart", Target: "persisted.example", Network: N.NetworkTCP, Node: "node"}
	persisted := smartHistoryFile{Version: smartHistoryVersion, Groups: map[string]smart.Snapshot{
		"smart": {Version: smart.SnapshotVersion, Metrics: []smart.MetricSnapshot{{Key: persistedKey, Success: 1, LastUsed: time.Now()}}},
	}}
	content, err := json.Marshal(persisted)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, content, 0o600))
	require.NoError(t, group.loadHistory())
	require.NoError(t, group.flushHistory(true))
	for _, candidateKey := range []smart.MetricKey{key, persistedKey} {
		status := group.store.Candidate(time.Now(), candidateKey)
		require.Equalf(t, int64(1), status.Samples, "%s was lost during retry", candidateKey.Target)
	}
	require.NoError(t, group.Close())
}

func TestSmartHistoryPrunesExpiredMetricsOnLoad(t *testing.T) {
	path := t.TempDir() + "/smart-history.json"
	staleKey := smart.MetricKey{Group: "smart", Target: "stale.example", Network: N.NetworkTCP, Node: "node"}
	history := smartHistoryFile{Version: smartHistoryVersion, Groups: map[string]smart.Snapshot{
		"smart": {Version: smart.SnapshotVersion, Metrics: []smart.MetricSnapshot{{Key: staleKey, Success: 1, LastUsed: time.Now().Add(-2 * time.Hour)}}},
	}}
	content, err := json.Marshal(history)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, content, 0o600))
	group := newSmartTestGroup()
	group.historyPath = path
	group.historyRetention = time.Hour
	group.maxHistoryEntries = 100
	require.NoError(t, group.loadHistory())
	require.Zero(t, group.store.Candidate(time.Now(), staleKey).Samples)
	require.NoError(t, group.Close())
}

func TestSmartCloseReturnsFinalHistoryFlushError(t *testing.T) {
	group := newSmartTestGroup()
	group.historyPath = t.TempDir()
	group.store.Record(time.Now(), smart.MetricKey{Group: "smart", Target: "example.com", Network: N.NetworkTCP, Node: "node"}, smart.Observation{Closed: true, Success: true})
	require.Error(t, group.Close())
}

func TestSmartCloseRecordsAndFlushesActiveConnection(t *testing.T) {
	path := t.TempDir() + "/smart-history.json"
	group := newSmartTestGroup()
	group.historyPath = path
	group.historyRetention = time.Hour
	group.maxHistoryEntries = 100
	left, right := net.Pipe()
	defer right.Close()
	wrapped := group.wrapConn(context.Background(), left, smartCandidate{outbound: &smartTestOutbound{tag: "node"}}, "example.com", N.NetworkTCP, time.Millisecond)
	require.NoError(t, group.Close())
	key := smart.MetricKey{Group: "smart", Target: "example.com", Network: N.NetworkTCP, Node: "node"}
	require.Equal(t, int64(1), group.store.Candidate(time.Now(), key).Samples)
	restored := newSmartTestGroup()
	restored.historyPath = path
	restored.historyRetention = time.Hour
	restored.maxHistoryEntries = 100
	require.NoError(t, restored.loadHistory())
	require.Equal(t, int64(1), restored.store.Candidate(time.Now(), key).Samples)
	require.NoError(t, restored.Close())
	_ = wrapped
}

func TestSmartHistoryPrunesOversizedSnapshotBeforeRestore(t *testing.T) {
	path := t.TempDir() + "/smart-history.json"
	now := time.Now()
	metrics := make([]smart.MetricSnapshot, 0, 3)
	for index, target := range []string{"old.example", "middle.example", "new.example"} {
		metrics = append(metrics, smart.MetricSnapshot{
			Key:      smart.MetricKey{Group: "smart", Target: target, Network: N.NetworkTCP, Node: "node"},
			Success:  1,
			LastUsed: now.Add(time.Duration(index-2) * time.Minute),
		})
	}
	content, err := json.Marshal(smartHistoryFile{Version: smartHistoryVersion, Groups: map[string]smart.Snapshot{
		"smart": {Version: smart.SnapshotVersion, Metrics: metrics},
	}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, content, 0o600))
	group := newSmartTestGroup()
	group.historyPath = path
	group.historyRetention = time.Hour
	group.maxHistoryEntries = 2
	require.NoError(t, group.loadHistory())
	for _, target := range []string{"middle.example", "new.example"} {
		key := smart.MetricKey{Group: "smart", Target: target, Network: N.NetworkTCP, Node: "node"}
		require.Equalf(t, int64(1), group.store.Candidate(now, key).Samples, "%s was not restored", target)
	}
	oldest := smart.MetricKey{Group: "smart", Target: "old.example", Network: N.NetworkTCP, Node: "node"}
	require.Zero(t, group.store.Candidate(now, oldest).Samples)
	require.NoError(t, group.Close())
}

func TestSmartHistoryUsesFileManager(t *testing.T) {
	blocked := errors.New("restricted history path")
	group := newSmartTestGroup()
	group.ctx = service.ContextWith[filemanager.Manager](context.Background(), smartRejectingFileManager{err: blocked})
	group.historyPath = "../smart-history.json"
	require.ErrorIs(t, group.loadHistory(), blocked)
}

func newSmartTestGroup() *Smart {
	return newSmartTestGroupWithTag("smart")
}

func newSmartTestGroupWithTag(tag string) *Smart {
	return &Smart{
		Adapter: outbound.NewAdapter(C.TypeSmart, tag, []string{N.NetworkTCP, N.NetworkUDP}, nil),
		ctx:     context.Background(),
		store:   smart.NewStore(smart.Config{}),
	}
}

type smartRejectingFileManager struct {
	err error
}

func (m smartRejectingFileManager) BasePath(name string) string { return name }
func (m smartRejectingFileManager) TempPath() string            { return "." }
func (m smartRejectingFileManager) OpenFile(string, int, os.FileMode) (*os.File, error) {
	return nil, m.err
}
func (m smartRejectingFileManager) Create(string) (*os.File, error) { return nil, m.err }
func (m smartRejectingFileManager) CreateTemp(string) (*os.File, error) {
	return nil, m.err
}
func (m smartRejectingFileManager) Chown(string) error                 { return m.err }
func (m smartRejectingFileManager) Mkdir(string, os.FileMode) error    { return m.err }
func (m smartRejectingFileManager) MkdirAll(string, os.FileMode) error { return m.err }
func (m smartRejectingFileManager) Remove(string) error                { return m.err }
func (m smartRejectingFileManager) RemoveAll(string) error             { return m.err }
func (m smartRejectingFileManager) Rename(string, string) error        { return m.err }

type smartTestOutbound struct {
	adapter.Outbound
	tag    string
	dial   func(context.Context, string, M.Socksaddr) (net.Conn, error)
	listen func(context.Context, M.Socksaddr) (net.PacketConn, error)
}

func (o *smartTestOutbound) Tag() string { return o.tag }

func (o *smartTestOutbound) Network() []string { return []string{N.NetworkTCP, N.NetworkUDP} }

func (o *smartTestOutbound) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	if o.dial == nil {
		return nil, errors.New("unexpected dial")
	}
	return o.dial(ctx, network, destination)
}

func (o *smartTestOutbound) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	if o.listen == nil {
		return nil, errors.New("unexpected packet listen")
	}
	return o.listen(ctx, destination)
}

type smartTestConn struct {
	net.Conn
	closed chan<- struct{}
	once   sync.Once
}

func (c *smartTestConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { close(c.closed) })
	return err
}

type smartTestPacketConn struct {
	net.PacketConn
	closed chan<- struct{}
	once   sync.Once
}

type smartFailingExtendedPacketConn struct {
	net.PacketConn
	err error
}

func (c *smartFailingExtendedPacketConn) ReadPacket(*buf.Buffer) (M.Socksaddr, error) {
	return M.Socksaddr{}, c.err
}

func (c *smartFailingExtendedPacketConn) WritePacket(buffer *buf.Buffer, _ M.Socksaddr) error {
	buffer.Release()
	return nil
}

type smartHeadroomPacketConn struct {
	net.PacketConn
}

func (c *smartHeadroomPacketConn) ReadPacket(*buf.Buffer) (M.Socksaddr, error) {
	return M.Socksaddr{}, io.EOF
}

func (c *smartHeadroomPacketConn) WritePacket(buffer *buf.Buffer, _ M.Socksaddr) error {
	buffer.ExtendHeader(28)
	buffer.Release()
	return nil
}

func (c *smartHeadroomPacketConn) FrontHeadroom() int {
	return 28
}

type smartOnePacketReader struct {
	sent bool
}

func (r *smartOnePacketReader) ReadPacket(buffer *buf.Buffer) (M.Socksaddr, error) {
	if r.sent {
		return M.Socksaddr{}, io.EOF
	}
	r.sent = true
	buffer.Write([]byte("hello"))
	return M.Socksaddr{}, nil
}

type smartDiscardPacketWriter struct{}

func (smartDiscardPacketWriter) WritePacket(buffer *buf.Buffer, _ M.Socksaddr) error {
	buffer.Release()
	return nil
}

func (c *smartTestPacketConn) Close() error {
	err := c.PacketConn.Close()
	c.once.Do(func() { close(c.closed) })
	return err
}
