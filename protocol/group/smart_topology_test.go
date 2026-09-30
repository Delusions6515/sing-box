package group

import (
	"context"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/interrupt"
	U "github.com/sagernet/sing-box/common/urltest"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/stretchr/testify/require"
)

func TestSmartNestedProviderMembership(t *testing.T) {
	first := &smartTestOutbound{tag: "first"}
	second := &smartTestOutbound{tag: "second"}
	replacement := &smartTestOutbound{tag: "second"}
	provider := &providerUpdateTestProvider{tag: "provider", outbounds: []adapter.Outbound{first}}
	manager := &providerUpdateTestOutboundManager{outbounds: map[string]adapter.Outbound{"first": first, "second": second}}
	nested := &URLTest{outbound: manager, group: &URLTestGroup{history: U.NewHistoryStorage(), interruptGroup: interrupt.NewGroup()}, providers: map[string]adapter.Provider{"provider": provider}, providerTags: []string{"provider"}, outboundsCache: make(map[string][]adapter.Outbound)}
	nested.group.storeOutbounds([]adapter.Outbound{first})
	manager.outbounds["nested"] = nested
	group := newSmartTestGroup()
	group.outbound = manager
	group.tags = []string{"nested"}
	require.NoError(t, group.rebuildCandidates(""))
	require.Equal(t, []string{"first"}, group.All())
	provider.outbounds = []adapter.Outbound{second}
	require.NoError(t, nested.onProviderUpdated("provider"))
	require.Equal(t, []string{"second"}, group.All())
	manager.outbounds["second"] = replacement
	provider.outbounds = []adapter.Outbound{replacement}
	require.NoError(t, nested.onProviderUpdated("provider"))
	require.Same(t, replacement, group.candidateSnapshot()[0])
	require.NoError(t, group.Close())
}

func TestSmartEmptyProviderRemovesOldCandidates(t *testing.T) {
	first := &smartTestOutbound{tag: "first"}
	provider := &providerUpdateTestProvider{tag: "provider", outbounds: []adapter.Outbound{first}}
	group := newSmartTestGroup()
	group.outbound = &providerUpdateTestOutboundManager{outbounds: map[string]adapter.Outbound{}}
	group.providers = map[string]adapter.Provider{"provider": provider}
	group.providerTags = []string{"provider"}
	group.outboundsCache = make(map[string][]adapter.Outbound)
	require.NoError(t, group.rebuildCandidates(""))
	provider.outbounds = nil
	require.NoError(t, group.onProviderUpdated("provider"))
	require.Empty(t, group.All())
	_, err := group.DialContext(context.Background(), "tcp", M.ParseSocksaddr("example.com:443"))
	require.ErrorContains(t, err, "no supported candidate")
}
