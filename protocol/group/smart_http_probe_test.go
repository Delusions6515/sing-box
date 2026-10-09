package group

import (
	"context"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/smart"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/service"

	"github.com/stretchr/testify/require"
)

func newHTTPProbeTestGroup(server *httptest.Server) *Smart {
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	group := newSmartTestGroup()
	group.ctx = service.ContextWith[adapter.CertificateStore](context.Background(), responseTestCertificateStore{pool: pool})
	return group
}

func TestSmartStatusProbeRedirectsStayOnLeaf(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/final" || r.Host != "example.com" {
			t.Errorf("unexpected plain request %s %s", r.Host, r.URL.Path)
		}
		_, _ = io.WriteString(w, "reachable")
	}))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/cross" {
			http.Redirect(w, r, "https://different.example/", http.StatusFound)
		} else {
			http.Redirect(w, r, "http://example.com/final", http.StatusFound)
		}
	}))
	defer secure.Close()
	group := newHTTPProbeTestGroup(secure)
	var access sync.Mutex
	var destinations []M.Socksaddr
	leaf := &smartWinnerTestOutbound{smartTestOutbound{tag: "leaf", dial: func(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
		access.Lock()
		destinations = append(destinations, destination)
		access.Unlock()
		require.Equal(t, N.NetworkTCP, network)
		target := secure.Listener.Addr().String()
		if destination.Port == 80 {
			target = plain.Listener.Addr().String()
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", target)
	}}}
	probe := smartOutboundProber{s: group, outbound: leaf}
	result, err := probe.StatusProbe(context.Background(), "https://example.com/robots.txt")
	require.NoError(t, err)
	require.Equal(t, 200, result.StatusCode)
	require.Equal(t, "reachable", string(result.Body))
	access.Lock()
	require.Equal(t, []M.Socksaddr{M.ParseSocksaddr("example.com:443"), M.ParseSocksaddr("example.com:80")}, destinations)
	access.Unlock()
	result, err = probe.StatusProbe(context.Background(), "https://example.com/cross")
	require.NoError(t, err)
	require.Equal(t, 302, result.StatusCode)
	access.Lock()
	require.Len(t, destinations, 3, "cross-host redirect must not contact the new host")
	access.Unlock()
}

func TestSmartStatusProbeBoundsStreamingBody(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/large" {
			_, _ = io.WriteString(w, strings.Repeat("x", 10000))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	group := newHTTPProbeTestGroup(server)
	leaf := &smartWinnerTestOutbound{smartTestOutbound{tag: "leaf", dial: func(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	}}}
	probe := smartOutboundProber{s: group, outbound: leaf}
	result, err := probe.StatusProbe(context.Background(), "https://example.com/large")
	require.NoError(t, err)
	require.Len(t, result.Body, smart.ProbeBodyLimit)
	started := time.Now()
	result, err = probe.StatusProbe(context.Background(), "https://example.com/stream")
	require.NoError(t, err)
	require.Equal(t, 403, result.StatusCode)
	require.Empty(t, result.Body)
	require.Less(t, time.Since(started), 3*time.Second, "streaming bodies must not consume the full probe timeout")
}

func TestSmartExitProbeFallsBackWithoutStoringAddress(t *testing.T) {
	original := smart.ExitTraceURLs
	smart.ExitTraceURLs = []string{"https://example.com/broken", "https://example.com/trace"}
	defer func() { smart.ExitTraceURLs = original }()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/broken" {
			w.WriteHeader(503)
			return
		}
		_, _ = io.WriteString(w, `{"country_code":"JP","ip":"203.0.113.8"}`)
	}))
	defer server.Close()
	group := newHTTPProbeTestGroup(server)
	leaf := &smartWinnerTestOutbound{smartTestOutbound{tag: "leaf", dial: func(ctx context.Context, _ string, _ M.Socksaddr) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	}}}
	result, err := (smartOutboundProber{s: group, outbound: leaf}).ExitProbe(context.Background(), false)
	require.NoError(t, err)
	require.Equal(t, "jp", result.Region)
	require.Len(t, result.Key, 12)
	require.Empty(t, result.ASN)
}
