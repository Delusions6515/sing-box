package clashapi

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
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
	info := proxyInfo(&Server{urlTestHistory: urltest.NewHistoryStorage()}, detour)
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

func (g *smartStatusTestGroup) SmartStatus() adapter.SmartGroupStatus {
	status := g.status
	status.Candidates = append([]adapter.SmartCandidateStatus{}, status.Candidates...)
	return status
}
