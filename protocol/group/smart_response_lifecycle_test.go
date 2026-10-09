package group

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/smart"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/stretchr/testify/require"
)

func TestSmartProbeLateResultsRespectMembershipAndClear(t *testing.T) {
	originalURLs := smart.ExitTraceURLs
	smart.ExitTraceURLs = []string{"https://example.com/trace"}
	defer func() { smart.ExitTraceURLs = originalURLs }()
	for _, kind := range []string{"response", "exit"} {
		for _, change := range []string{"replace", "clear"} {
			t.Run(kind+"/"+change, func(t *testing.T) {
				started := make(chan struct{}, 2)
				release := make(chan struct{})
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					started <- struct{}{}
					<-release
					if r.URL.Path == "/trace" {
						_, _ = io.WriteString(w, "loc=US\nip=203.0.113.8\n")
					} else {
						w.WriteHeader(403)
					}
				}))
				defer server.Close()
				defer close(release)
				group := newHTTPProbeTestGroup(server)
				group.historyPath = t.TempDir() + "/history.json"
				group.historyPoolKey = group.historyPath
				group.initResponseProbes()
				leaf := &smartWinnerTestOutbound{smartTestOutbound{tag: "node", dial: func(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
				}}}
				group.candidates = []adapter.Outbound{leaf}
				defer group.Close()
				if kind == "exit" {
					group.maybeProbeExit("node")
				} else {
					meta := group.responseContext(context.Background(), leaf, "example.com", M.ParseSocksaddr("example.com:443"))
					group.probeAfterClose(meta, 1, 0, 0, true)
				}
				select {
				case <-started:
				case <-time.After(time.Second):
					t.Fatal("probe did not start")
				}
				if change == "replace" {
					group.candidateAccess.Lock()
					group.candidates = []adapter.Outbound{&smartWinnerTestOutbound{smartTestOutbound{tag: "node"}}}
					group.candidateAccess.Unlock()
				} else {
					require.NoError(t, group.ClearCache())
				}
				// Let the delayed HTTP result complete without shutting down the group first.
				release <- struct{}{}
				joined := make(chan struct{})
				go func() { group.responseWorkers.Wait(); close(joined) }()
				select {
				case <-joined:
				case <-time.After(time.Second):
					t.Fatal("probe task leaked")
				}
				require.Empty(t, group.store.Snapshot(time.Now(), time.Hour, 100).Exits, "late exit result must not seed the replacement or repopulate cleared history")
				require.False(t, group.responseBlocked("example.com", "example.com", "node", time.Now(), 1), "late response result must not quarantine the replacement")
			})
		}
	}
}

func TestSmartExitSuspicionsReorderWithoutDroppingAllNodes(t *testing.T) {
	group := newSmartTestGroup()
	group.initResponseProbes()
	a := &smartWinnerTestOutbound{smartTestOutbound{tag: "a"}}
	b := &smartWinnerTestOutbound{smartTestOutbound{tag: "b"}}
	c := &smartWinnerTestOutbound{smartTestOutbound{tag: "control"}}
	group.candidates = []adapter.Outbound{a, b, c}
	watcher := group.exitWatcher.Load()
	now := time.Now()
	watcher.Store("a", smart.ExitInfo{Region: "us", Key: "exit-a"}, now)
	watcher.Store("b", smart.ExitInfo{Region: "us", Key: "exit-b"}, now)
	watcher.Store("control", smart.ExitInfo{Region: "jp", Key: "exit-c"}, now)
	group.selection.pin(N.NetworkTCP+"\x00example.com", a)
	watcher.NoteSuccess("example.com", "control")
	watcher.Note("example.com", "a")
	watcher.Note("example.com", "b")
	ranked, _ := group.rank(context.Background(), N.NetworkTCP, M.ParseSocksaddr("example.com:443"))
	require.Same(t, c, ranked[0].outbound)
	_, pinned := group.selection.order(N.NetworkTCP+"\x00example.com", ranked)
	require.False(t, pinned)
	require.Len(t, ranked, 2, "keep one suspected exit as a leased fallback")
	watcher.Clear("example.com", "a")
	ranked, _ = group.rank(context.Background(), N.NetworkTCP, M.ParseSocksaddr("example.com:443"))
	require.Len(t, ranked, 3)
	group.responseCancel()
}

func TestSmartSuspectedFallbackCannotOverrideHealthyRankingViaPin(t *testing.T) {
	group := newSmartTestGroup()
	group.initResponseProbes()
	defer group.responseCancel()
	a := &smartWinnerTestOutbound{smartTestOutbound{tag: "a"}}
	b := &smartWinnerTestOutbound{smartTestOutbound{tag: "b"}}
	control := &smartWinnerTestOutbound{smartTestOutbound{tag: "control"}}
	watcher := group.exitWatcher.Load()
	for _, node := range []adapter.Outbound{a, b, control} {
		region := "us"
		if node == control {
			region = "jp"
		}
		watcher.Store(node.Tag(), smart.ExitInfo{Region: region, Key: node.Tag()}, time.Now())
	}
	group.responseTargets["rule:shared-service"] = "another-site.com"
	pinKey := N.NetworkTCP + "\x00rule:shared-service"
	group.selection.pin(pinKey, a)
	watcher.NoteSuccess("example.com", control.Tag())
	watcher.Note("example.com", a.Tag())
	watcher.Note("example.com", b.Tag())
	var candidates []smartCandidate
	for _, node := range []adapter.Outbound{a, b, control} {
		candidates = append(candidates, smartCandidate{outbound: node, status: smart.Candidate{Key: smart.MetricKey{Target: "rule:shared-service", Network: N.NetworkTCP, Node: node.Tag()}}})
	}
	ranked := group.filterExitSuspicions("example.com", candidates)
	ordered, pinned := group.selection.order(pinKey, ranked)
	require.False(t, pinned, "a service shared by sites must not keep its suspected fallback as the pinned first choice")
	require.Same(t, control, ordered[0].outbound)
}
