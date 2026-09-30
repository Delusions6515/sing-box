package clashapi

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/smart"
	"github.com/sagernet/sing-box/common/urltest"
	C "github.com/sagernet/sing-box/constant"
	N "github.com/sagernet/sing/common/network"
	"github.com/stretchr/testify/require"
)

func TestProxyInfoRendersReadOnlySmartStatus(t *testing.T) {
	updated := time.Unix(1_000, 0).UTC()
	detour := &smartStatusTestGroup{status: adapter.SmartGroupStatus{
		Selected:   "fast",
		UpdatedAt:  &updated,
		Candidates: []adapter.SmartCandidateStatus{{Tag: "fast", Weight: 0.9, Samples: 3}},
	}}
	info := proxyInfo(&Server{
		outbound:       &smartOutboundManager{outbounds: []adapter.Outbound{&plainOutbound{tag: "fast"}}},
		urlTestHistory: urltest.NewHistoryStorage(),
	}, detour)
	content, err := info.MarshalJSON()
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(content, &decoded))
	smartInfo, ok := decoded["smart"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "fast", smartInfo["selected"])
	require.Contains(t, smartInfo, "updated_at")
	require.Len(t, smartInfo["candidates"], 1)
}

func TestProxyInfoShowsLatestSuccessfulDelayAfterSmartFailure(t *testing.T) {
	history := urltest.NewHistoryStorage()
	base := time.Now()
	history.StoreURLTestHistory("node", &adapter.URLTestHistory{Time: base, Delay: 42})
	history.StoreSmartURLTestHistory("node", &adapter.URLTestHistory{Time: base.Add(time.Second), Delay: 12})
	node := &historyTestOutbound{plainOutbound: plainOutbound{tag: "node"}}
	info := proxyInfo(&Server{urlTestHistory: history}, node)
	content, err := info.MarshalJSON()
	require.NoError(t, err)
	require.Contains(t, string(content), `"delay":12`)
	history.DeleteSmartURLTestHistory("node")
	info = proxyInfo(&Server{urlTestHistory: history}, node)
	content, err = info.MarshalJSON()
	require.NoError(t, err)
	require.Contains(t, string(content), `"delay":42`)
}

type historyTestOutbound struct{ plainOutbound }

func (*historyTestOutbound) Type() string      { return C.TypeDirect }
func (*historyTestOutbound) Network() []string { return []string{N.NetworkTCP} }

func TestProxyInfoRendersColdSmartStatus(t *testing.T) {
	detour := &smartStatusTestGroup{status: adapter.SmartGroupStatus{}}
	info := proxyInfo(&Server{urlTestHistory: urltest.NewHistoryStorage()}, detour)
	content, err := info.MarshalJSON()
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(content, &decoded))
	smartInfo, ok := decoded["smart"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "", smartInfo["selected"])
	require.Nil(t, smartInfo["updated_at"])
	candidates, ok := smartInfo["candidates"].([]any)
	require.True(t, ok)
	require.Empty(t, candidates)
}

type smartStatusTestGroup struct {
	adapter.Outbound
	status adapter.SmartGroupStatus
}

func (g *smartStatusTestGroup) Type() string { return C.TypeSmart }

func (g *smartStatusTestGroup) Tag() string { return "smart" }

func (g *smartStatusTestGroup) Network() []string { return []string{N.NetworkTCP, N.NetworkUDP} }

func (g *smartStatusTestGroup) Now() string { return g.status.Selected }

func (g *smartStatusTestGroup) All() []string { return []string{"fast", "slow"} }

func (g *smartStatusTestGroup) Selected(string) adapter.Outbound { return nil }

func (g *smartStatusTestGroup) AttachConnection(io.Closer) func() { return func() {} }

func (g *smartStatusTestGroup) SmartStatus() adapter.SmartGroupStatus {
	status := g.status
	status.Candidates = append([]adapter.SmartCandidateStatus{}, status.Candidates...)
	return status
}

func (g *smartStatusTestGroup) Weights() []smart.NodeRankItem {
	items := make([]smart.NodeRankItem, 0, len(g.status.Candidates))
	for _, candidate := range g.status.Candidates {
		items = append(items, smart.NodeRankItem{Name: candidate.Tag, Weight: candidate.Weight})
	}
	return items
}

func (g *smartStatusTestGroup) URLTest(context.Context) (map[string]uint16, error) {
	return map[string]uint16{}, nil
}

func (g *smartStatusTestGroup) ClearCache() error {
	g.status = adapter.SmartGroupStatus{}
	return nil
}
