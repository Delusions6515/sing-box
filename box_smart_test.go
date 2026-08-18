package box

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/certificate"
	"github.com/sagernet/sing-box/adapter/endpoint"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/adapter/provider"
	boxService "github.com/sagernet/sing-box/adapter/service"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/dns/transport/local"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/direct"
	"github.com/sagernet/sing-box/protocol/group"
	"github.com/sagernet/sing-box/protocol/vless"
	providerLocal "github.com/sagernet/sing-box/provider/local"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/stretchr/testify/require"
)

const smartStartupTestOutboundType = "smart-startup-test"

type smartStartupTestOutbound struct {
	outbound.Adapter
	dialed chan<- struct{}
}

func (o *smartStartupTestOutbound) DialContext(_ context.Context, _ string, destination M.Socksaddr) (net.Conn, error) {
	if destination.AddrString() == "api.github.com" {
		select {
		case o.dialed <- struct{}{}:
		default:
		}
	}
	return nil, errors.New("test outbound")
}

func (o *smartStartupTestOutbound) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, errors.New("test outbound")
}

func TestSmartASNStartsAfterSelector(t *testing.T) {
	dialed := make(chan struct{}, 1)
	outboundRegistry := outbound.NewRegistry()
	direct.RegisterOutbound(outboundRegistry)
	outbound.Register[option.StubOptions](outboundRegistry, smartStartupTestOutboundType, func(_ context.Context, _ adapter.Router, _ log.ContextLogger, tag string, _ option.StubOptions) (adapter.Outbound, error) {
		return &smartStartupTestOutbound{
			Adapter: outbound.NewAdapter(smartStartupTestOutboundType, tag, []string{N.NetworkTCP, N.NetworkUDP}, nil),
			dialed:  dialed,
		}, nil
	})
	group.RegisterSelector(outboundRegistry)
	group.RegisterSmart(outboundRegistry)
	dnsTransportRegistry := dns.NewTransportRegistry()
	local.RegisterTransport(dnsTransportRegistry)
	instance, err := New(Options{
		Context: Context(context.Background(), inbound.NewRegistry(), provider.NewRegistry(), outboundRegistry, endpoint.NewRegistry(), dnsTransportRegistry, boxService.NewRegistry(), certificate.NewRegistry()),
		Options: option.Options{
			HTTPClients: []option.HTTPClient{{
				Tag:           "default",
				DialerOptions: option.DialerOptions{Detour: "selector"},
			}},
			Outbounds: []option.Outbound{
				{Type: smartStartupTestOutboundType, Tag: "test", Options: &option.StubOptions{}},
				{Type: C.TypeSelector, Tag: "selector", Options: &option.SelectorOutboundOptions{GroupCommonOption: option.GroupCommonOption{Outbounds: []string{"test"}}}},
				{Type: C.TypeSmart, Tag: "smart", Options: &option.SmartOutboundOptions{GroupCommonOption: option.GroupCommonOption{Outbounds: []string{"test"}}, URL: "https://example.com", HistoryPath: filepath.Join(t.TempDir(), "smart-history.json")}},
			},
			Route: &option.RouteOptions{DefaultHTTPClient: "default"},
			Experimental: &option.ExperimentalOptions{
				Smart: &option.SmartOptions{ASN: option.SmartASNOptions{Path: filepath.Join(t.TempDir(), "asn")}},
			},
		},
	})
	require.NoError(t, err)

	require.NoError(t, instance.Start())
	t.Cleanup(func() { require.NoError(t, instance.Close()) })
	select {
	case <-dialed:
	case <-time.After(time.Second):
		t.Fatal("Smart ASN update did not use the default HTTP client")
	}
}

func TestSmartStartsWithProviderCandidates(t *testing.T) {
	providerPath := filepath.Join(t.TempDir(), "provider.json")
	require.NoError(t, os.WriteFile(providerPath, []byte("vless://11111111-1111-1111-1111-111111111111@192.0.2.1:443?type=tcp#test"), 0o644))

	outboundRegistry := outbound.NewRegistry()
	direct.RegisterOutbound(outboundRegistry)
	group.RegisterSmart(outboundRegistry)
	vless.RegisterOutbound(outboundRegistry)
	providerRegistry := provider.NewRegistry()
	providerLocal.RegisterProviderLocal(providerRegistry)
	dnsTransportRegistry := dns.NewTransportRegistry()
	local.RegisterTransport(dnsTransportRegistry)
	instance, err := New(Options{
		Context: Context(context.Background(), inbound.NewRegistry(), providerRegistry, outboundRegistry, endpoint.NewRegistry(), dnsTransportRegistry, boxService.NewRegistry(), certificate.NewRegistry()),
		Options: option.Options{
			Providers: []option.Provider{{
				Type:    C.ProviderTypeLocal,
				Tag:     "provider",
				Options: &option.ProviderLocalOptions{Path: providerPath},
			}},
			Outbounds: []option.Outbound{{
				Type: C.TypeSmart,
				Tag:  "smart",
				Options: &option.SmartOutboundOptions{
					GroupCommonOption: option.GroupCommonOption{Providers: []string{"provider"}},
					URL:               "https://example.com",
					HistoryPath:       filepath.Join(t.TempDir(), "smart-history.json"),
				},
			}},
		},
	})
	require.NoError(t, err)
	require.NoError(t, instance.Start())
	t.Cleanup(func() { require.NoError(t, instance.Close()) })
	smartOutbound, loaded := instance.Outbound().Outbound("smart")
	require.True(t, loaded)
	require.Equal(t, []string{"provider/test"}, smartOutbound.(adapter.OutboundGroup).All())
}
