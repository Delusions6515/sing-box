package group

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/smart"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"github.com/stretchr/testify/require"
)

func TestSmartResponseReachableRecheckClearsBothIdentities(t *testing.T) {
	var status atomic.Int64
	status.Store(403)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(int(status.Load())) }))
	defer server.Close()
	group := newHTTPProbeTestGroup(server)
	group.historyPath = t.TempDir() + "/history.json"
	group.historyPoolKey = group.historyPath
	group.initResponseProbes()
	defer group.Close()
	leaf := &smartWinnerTestOutbound{smartTestOutbound{tag: "node", dial: func(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	}}}
	group.candidates = []adapter.Outbound{leaf}
	meta := group.responseContext(context.Background(), leaf, "rule:service", M.ParseSocksaddr("example.com:443"))
	group.probeAfterClose(meta, 1, 0, 0, true)
	require.Eventually(t, func() bool { return group.responseBlocked("rule:service", "example.com", "node", time.Now(), 1) }, time.Second, 5*time.Millisecond)
	status.Store(200)
	// A fresh throttle models the next permitted recheck window without sleeping thirty seconds.
	group.responseAccess.Lock()
	group.responseThrottle = new(smart.ProbeThrottle)
	group.responseAccess.Unlock()
	meta.recheck = true
	group.probeAfterClose(meta, 1, 0, 0, true)
	require.Eventually(t, func() bool { return !group.responseBlocked("rule:service", "example.com", "node", time.Now(), 1) }, time.Second, 5*time.Millisecond)
	require.Empty(t, group.store.Snapshot(time.Now(), time.Hour, 100).Metrics)
}

func TestSmartResponseBroadFailureDoesNotExhaustPool(t *testing.T) {
	group := newSmartTestGroup()
	group.initResponseProbes()
	defer group.responseCancel()
	for _, name := range []string{"a", "b", "c", "d"} {
		group.candidates = append(group.candidates, &smartWinnerTestOutbound{smartTestOutbound{tag: name}})
	}
	until := time.Now().Add(time.Minute)
	for _, name := range []string{"a", "b"} {
		group.responseBlocks[smartResponseKey{"example.com", name}] = smartResponseBlock{host: "example.com", target: "example.com", until: until}
	}
	ranked, _ := group.rank(context.Background(), N.NetworkTCP, M.ParseSocksaddr("example.com:443"))
	require.Len(t, ranked, 2)
	group.responseBlocks[smartResponseKey{"example.com", "c"}] = smartResponseBlock{host: "example.com", target: "example.com", until: until}
	ranked, _ = group.rank(context.Background(), N.NetworkTCP, M.ParseSocksaddr("example.com:443"))
	require.Len(t, ranked, 4, "more than the mihomo host-failure limit restores the pool")
	ranked, _ = group.rank(context.Background(), N.NetworkTCP, M.ParseSocksaddr("other.com:443"))
	require.Len(t, ranked, 4, "one site's refusal cannot affect another site")
}
