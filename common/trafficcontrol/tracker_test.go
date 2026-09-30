package trafficcontrol

import (
	"sync/atomic"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"

	"github.com/stretchr/testify/require"
)

func TestSmartChainUsesEachConnectionWinner(t *testing.T) {
	first := new(atomic.Pointer[adapter.Outbound])
	second := new(atomic.Pointer[adapter.Outbound])
	chain := []string{"smart", "Telegram"}
	one := TrackerMetadata{Chain: chain, Outbound: "smart", Metadata: adapter.InboundContext{SelectedOutbound: first}}
	two := TrackerMetadata{Chain: chain, Outbound: "smart", Metadata: adapter.InboundContext{SelectedOutbound: second}}
	require.Equal(t, chain, one.DisplayChain())
	var nodeA adapter.Outbound = &trackerTestOutbound{tag: "node-a"}
	var nodeB adapter.Outbound = &trackerTestOutbound{tag: "node-b"}
	first.Store(&nodeA)
	second.Store(&nodeB)
	require.Equal(t, []string{"node-a", "smart", "Telegram"}, one.DisplayChain())
	require.Equal(t, []string{"node-b", "smart", "Telegram"}, two.DisplayChain())
	require.Equal(t, chain, one.Chain)
}

type trackerTestOutbound struct {
	adapter.Outbound
	tag string
}

func (o *trackerTestOutbound) Tag() string { return o.tag }

func TestTrackerMetadataConnectionDomain(t *testing.T) {
	testCases := []struct {
		name        string
		destination M.Socksaddr
		domain      string
		sniffHost   string
		expected    string
	}{
		{
			name:      "sniff host",
			sniffHost: "sniff.example.com",
			expected:  "sniff.example.com",
		},
		{
			name:      "sniff host before reverse mapped domain",
			domain:    "mapped.example.com",
			sniffHost: "sniff.example.com",
			expected:  "sniff.example.com",
		},
		{
			name:     "reverse mapped domain fallback",
			domain:   "mapped.example.com",
			expected: "mapped.example.com",
		},
		{
			name:        "destination fqdn before cached domains",
			destination: M.ParseSocksaddr("destination.example.com:443"),
			domain:      "mapped.example.com",
			sniffHost:   "sniff.example.com",
			expected:    "destination.example.com",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			metadata := TrackerMetadata{Metadata: adapter.InboundContext{
				Destination: testCase.destination,
				Domain:      testCase.domain,
				SniffHost:   testCase.sniffHost,
			}}
			require.Equal(t, testCase.expected, metadata.ConnectionDomain())
		})
	}
}
