package route

import (
	"io"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/smart"
	N "github.com/sagernet/sing/common/network"
	"github.com/stretchr/testify/require"
)

type routingSmartLeaf struct{ adapter.Outbound }

func (l *routingSmartLeaf) Tag() string { return "leaf" }

type routingSmartGroup struct {
	adapter.Outbound
	selected adapter.Outbound
}

func (g *routingSmartGroup) Tag() string                           { return "smart" }
func (g *routingSmartGroup) Network() []string                     { return []string{N.NetworkTCP, N.NetworkUDP} }
func (g *routingSmartGroup) All() []string                         { return []string{g.selected.Tag()} }
func (g *routingSmartGroup) Selected(string) adapter.Outbound      { return g.selected }
func (g *routingSmartGroup) AttachConnection(io.Closer) func()     { return func() {} }
func (g *routingSmartGroup) SmartStatus() adapter.SmartGroupStatus { return adapter.SmartGroupStatus{} }
func (g *routingSmartGroup) Weights() []smart.NodeRankItem         { return nil }
func (g *routingSmartGroup) ClearCache() error                     { return nil }

func TestResolveOutboundKeepsSmartAsDialer(t *testing.T) {
	leaf := &routingSmartLeaf{}
	smart := &routingSmartGroup{selected: leaf}
	chain, err := resolveOutbound(smart, N.NetworkTCP, &adapter.InboundContext{})
	require.NoError(t, err)
	require.Equal(t, []adapter.Outbound{smart}, chain)
}

func TestSmartDoesNotPreMatchSelectedLeaf(t *testing.T) {
	leaf := &routingSmartLeaf{}
	smart := &routingSmartGroup{selected: leaf}
	chain, action := (&Router{}).selectPreMatchOutbound(&adapter.InboundContext{Network: N.NetworkTCP}, smart, 0)
	require.Nil(t, chain)
	require.Equal(t, adapter.PreMatchContinue, action)
}
