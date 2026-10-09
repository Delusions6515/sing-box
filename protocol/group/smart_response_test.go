package group

import (
	"context"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/smart"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"

	"github.com/stretchr/testify/require"
)

type responseTestCertificateStore struct {
	adapter.CertificateStore
	pool *x509.CertPool
}

func (s responseTestCertificateStore) Pool() *x509.CertPool { return s.pool }

func TestSmartResponseProbeAfterClose(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			requests.Add(1)
			require.Equal(t, "example.com", r.Host)
			require.Equal(t, http.MethodGet, r.Method)
			w.WriteHeader(http.StatusForbidden)
		} else {
			_, _ = io.WriteString(w, "loc=US\nip=203.0.113.1\n")
		}
	}))
	defer server.Close()
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	ctx := service.ContextWith[adapter.CertificateStore](context.Background(), responseTestCertificateStore{pool: pool})
	instance, err := NewSmart(ctx, nil, log.NewNOPFactory().NewLogger("test"), "smart", option.SmartOutboundOptions{})
	require.NoError(t, err)
	group := instance.(*Smart)
	group.historyPath = t.TempDir() + "/history.json"
	group.historyPoolKey = group.historyPath
	node := &smartWinnerTestOutbound{smartTestOutbound{tag: "refusing", dial: func(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	}}}
	other := &smartWinnerTestOutbound{smartTestOutbound{tag: "other"}}
	group.candidates = []adapter.Outbound{node, other}
	defer group.Close()
	destination := M.ParseSocksaddr("example.com:443")
	connCtx := adapter.WithContext(ctx, &adapter.InboundContext{Destination: destination})
	local, peer := net.Pipe()
	wrapped := group.wrapConn(connCtx, local, smartCandidate{outbound: node}, "example.com", N.NetworkTCP, destination, 0)
	go func() { _, _ = peer.Write([]byte("x")); _ = peer.Close() }()
	_, err = wrapped.Read(make([]byte, 1))
	require.NoError(t, err)
	require.NoError(t, wrapped.Close())
	require.Eventually(t, func() bool { return requests.Load() > 0 }, time.Second, 5*time.Millisecond, "closed low-download HTTPS connection must trigger a response probe")
	require.Eventually(t, func() bool {
		candidates, _ := group.rank(ctx, N.NetworkTCP, destination)
		return len(candidates) > 0 && candidates[0].outbound == other
	}, time.Second, 5*time.Millisecond, "refusal must avoid only the original target-node")
	key := smart.MetricKey{Group: "smart", Target: "example.com", Network: N.NetworkTCP, Node: node.Tag()}
	require.EqualValues(t, 1, group.store.Candidate(time.Now(), key).Samples, "synthetic probe must not become a business observation")
}
