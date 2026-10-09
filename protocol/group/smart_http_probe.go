package group

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/smart"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type smartOutboundProber struct {
	s        *Smart
	outbound adapter.Outbound
}

var (
	_ smart.StatusProber = smartOutboundProber{}
	_ smart.ExitProber   = smartOutboundProber{}
)

func (p smartOutboundProber) request(ctx context.Context, rawURL string) (*http.Response, *http.Transport, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, nil, err
	}
	if req.URL.Scheme != "https" && req.URL.Scheme != "http" {
		return nil, nil, errors.New("unsupported probe URL scheme")
	}
	preset := randSmartBrowserPreset()
	req.Header = preset.Headers.Clone()
	req.Header.Set("Accept-Encoding", "identity")
	dialLeaf := func(ctx context.Context, _ string, address string) (net.Conn, error) {
		return p.outbound.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddr(address))
	}
	transport := &http.Transport{
		DialContext: dialLeaf,
		DialTLSContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			serverName, _, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			conn, err := dialLeaf(ctx, network, address)
			if err != nil {
				return nil, err
			}
			tlsConn, err := newSmartProbeTLS(ctx, p.s, conn, serverName, preset.FingerprintName)
			if err != nil {
				_ = conn.Close()
				return nil, err
			}
			return tlsConn, nil
		},
		ForceAttemptHTTP2:   false,
		DisableCompression:  true,
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 || !sameProbeHostPort(req.URL, via[0].URL) {
			return http.ErrUseLastResponse
		}
		return nil
	}}
	response, err := client.Do(req)
	return response, transport, err
}

func sameProbeHostPort(a, b *url.URL) bool {
	normalize := func(u *url.URL) string {
		port := u.Port()
		if port == "" || (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
			return u.Hostname()
		}
		return net.JoinHostPort(u.Hostname(), port)
	}
	return strings.EqualFold(normalize(a), normalize(b))
}

func (p smartOutboundProber) StatusProbe(ctx context.Context, rawURL string) (*smart.ProbeResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	response, transport, err := p.request(ctx, rawURL)
	if transport != nil {
		defer transport.CloseIdleConnections()
	}
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	snippet := make(chan []byte, 1)
	go func() { data, _ := io.ReadAll(io.LimitReader(response.Body, smart.ProbeBodyLimit)); snippet <- data }()
	timer := time.NewTimer(smart.ProbeBodyWait)
	defer timer.Stop()
	var body []byte
	select {
	case body = <-snippet:
	case <-timer.C:
		cancel()
		<-snippet
	case <-ctx.Done():
		cancel()
		<-snippet
		return nil, ctx.Err()
	}
	return &smart.ProbeResult{StatusCode: response.StatusCode, Header: response.Header, Body: body}, nil
}

func (p smartOutboundProber) ExitProbe(ctx context.Context, wantASN bool) (*smart.ExitProbeResult, error) {
	var lastErr error
	for _, rawURL := range smart.ExitTraceURLs {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		result, err := p.exitProbe(ctx, rawURL, wantASN)
		if err == nil {
			return result, nil
		}
		lastErr = fmt.Errorf("exit trace %s: %w", rawURL, err)
	}
	return nil, lastErr
}

func (p smartOutboundProber) exitProbe(ctx context.Context, rawURL string, wantASN bool) (*smart.ExitProbeResult, error) {
	response, transport, err := p.request(ctx, rawURL)
	if transport != nil {
		defer transport.CloseIdleConnections()
	}
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("exit trace answered %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, smart.ExitProbeBodyLimit))
	if err != nil {
		return nil, err
	}
	region, ip := smart.ParseExitAnswer(body)
	if region == "" {
		return nil, errors.New("exit trace has no region")
	}
	result := &smart.ExitProbeResult{Region: region}
	if ip.IsValid() {
		result.Key = smart.ExitKey(ip)
		if wantASN && p.s.smartService != nil {
			result.ASN = p.s.smartService.LookupASN(ip)
		}
	}
	return result, nil
}
