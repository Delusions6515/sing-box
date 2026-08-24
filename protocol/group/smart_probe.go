package group

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing/common"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/ntp"
)

func (s *Smart) urlTest(ctx context.Context, detour N.Dialer) (uint16, error) {
	if len(s.expectedStatus) == 0 {
		return urltest.URLTest(ctx, s.url, detour)
	}
	if multiplexOutbound, multiplexEnabled := common.Cast[adapter.OutboundWithMultiplex](detour); multiplexEnabled && multiplexOutbound.MultiplexEnabled() {
		if _, err := s.urlTestOnce(ctx, detour); err != nil {
			return 0, err
		}
	}
	return s.urlTestOnce(ctx, detour)
}

func (s *Smart) urlTestOnce(ctx context.Context, detour N.Dialer) (uint16, error) {
	link := s.url
	if link == "" {
		link = defaultSmartURL
	}
	linkURL, err := url.Parse(link)
	if err != nil {
		return 0, err
	}
	port := linkURL.Port()
	if port == "" {
		switch linkURL.Scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	start := time.Now()
	instance, err := detour.DialContext(ctx, N.NetworkTCP, M.ParseSocksaddrHostPortStr(linkURL.Hostname(), port))
	if err != nil {
		return 0, err
	}
	defer instance.Close()
	if N.NeedHandshakeForWrite(instance) {
		start = time.Now()
	}
	request, err := http.NewRequest(http.MethodHead, link, nil)
	if err != nil {
		return 0, err
	}
	client := http.Client{
		Transport: &http.Transport{
			DialContext: func(context.Context, string, string) (net.Conn, error) { return instance, nil },
			TLSClientConfig: &tls.Config{
				Time:    ntp.TimeFuncFromContext(ctx),
				RootCAs: adapter.RootPoolFromContext(ctx),
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       C.TCPTimeout,
	}
	defer client.CloseIdleConnections()
	response, err := client.Do(request.WithContext(ctx))
	if err != nil {
		return 0, err
	}
	response.Body.Close()
	if C.URLTestUnifiedDelay {
		secondStart := time.Now()
		secondResponse, secondErr := client.Do(request.WithContext(ctx))
		if secondErr == nil {
			response = secondResponse
			response.Body.Close()
			start = secondStart
		}
	}
	if !s.expectedStatus.Contains(uint16(response.StatusCode)) {
		return 0, fmt.Errorf("unexpected HTTP status: %s", response.Status)
	}
	return uint16(time.Since(start) / time.Millisecond), nil
}
