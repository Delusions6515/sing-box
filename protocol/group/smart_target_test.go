package group

import (
	"context"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/smart"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/stretchr/testify/require"
)

func TestSmartTargetPolicySurvivesHistoryReload(t *testing.T) {
	now := time.Now()
	rule := smart.RuleTarget{Key: "rule-set:video", Claimable: true}
	group := newSmartTestGroup()
	group.targetPolicy = smart.NewTargetPolicy()
	group.historyPath = t.TempDir() + "/history.json"
	group.historyRetention = time.Hour
	group.maxHistoryEntries = 100
	for range 4 {
		group.targetPolicy.RecordSuccess(now, rule, "64501")
	}
	group.store.Record(now, smart.MetricKey{Group: "smart", Target: rule.Key, Network: "tcp", Node: "node"}, smart.Observation{Success: true})
	require.NoError(t, group.Close())
	restored := newSmartTestGroup()
	restored.targetPolicy = smart.NewTargetPolicy()
	restored.historyPath = group.historyPath
	restored.historyRetention = time.Hour
	restored.maxHistoryEntries = 100
	require.NoError(t, restored.loadHistory())
	require.Equal(t, rule.Key, restored.targetPolicy.Select(now, "api.example.com", "64501", smart.RuleTarget{}))
	require.NoError(t, restored.ClearCache())
	require.Equal(t, "asn:64501", restored.targetPolicy.Select(now, "api.example.com", "64501", smart.RuleTarget{}))
	require.NoError(t, restored.Close())
}

func TestSmartSharedASNUsesSiteRatherThanRulelessNetwork(t *testing.T) {
	group := newSmartTestGroup()
	group.targetPolicy = smart.NewTargetPolicy()
	group.preferASN = true
	group.candidates = []adapter.Outbound{&smartTestOutbound{tag: "node"}}
	ctx := adapter.WithContext(context.Background(), &adapter.InboundContext{})
	_, target := group.rank(ctx, "tcp", M.ParseSocksaddr("api.example.com:443"))
	require.Equal(t, "*.example.com", target)
	_, target = group.rank(ctx, "tcp", M.ParseSocksaddr("www.example.com:443"))
	require.Equal(t, "*.example.com", target)
}
