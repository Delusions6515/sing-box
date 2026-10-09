package rule

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/smart"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/stretchr/testify/require"
)

func TestSmartRuleMixedDestinationAlternativesDeclineIdentity(t *testing.T) {
	prefix := badoption.Prefixable(netip.MustParsePrefix("192.0.2.0/24"))
	for _, test := range []struct {
		name    string
		options option.RawDefaultRule
		host    string
		address string
	}{
		{name: "suffix and keyword", options: option.RawDefaultRule{DomainSuffix: []string{"video.example"}, DomainKeyword: []string{"music"}}, host: "music.other.example"},
		{name: "suffix and regex", options: option.RawDefaultRule{DomainSuffix: []string{"video.example"}, DomainRegex: []string{`^music\.`}}, host: "music.other.example"},
		{name: "suffix and CIDR", options: option.RawDefaultRule{DomainSuffix: []string{"video.example"}, IPCIDR: []*badoption.Prefixable{&prefix}}, host: "cdn.other.example", address: "192.0.2.1"},
		{name: "suffix and private IP", options: option.RawDefaultRule{DomainSuffix: []string{"video.example"}, IPIsPrivate: true}, host: "cdn.other.example", address: "10.0.0.1"},
		{name: "domain and keyword", options: option.RawDefaultRule{Domain: []string{"video.example"}, DomainKeyword: []string{"music"}}, host: "music.other.example"},
		{name: "rule-set and keyword", options: option.RawDefaultRule{RuleSet: []string{"video"}, DomainKeyword: []string{"music"}}, host: "music.other.example"},
		{name: "rule-set and suffix", options: option.RawDefaultRule{RuleSet: []string{"video"}, DomainSuffix: []string{"other.example"}}, host: "cdn.other.example"},
		{name: "rule-set and CIDR", options: option.RawDefaultRule{RuleSet: []string{"video"}, IPCIDR: []*badoption.Prefixable{&prefix}}, host: "cdn.other.example", address: "192.0.2.1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			rule, err := NewDefaultRule(context.Background(), log.NewNOPFactory().NewLogger("test"), option.DefaultRule{RawDefaultRule: test.options})
			require.NoError(t, err)
			if rule.ruleSetItem != nil {
				inner := headlessDefaultRule(t, func(rule *abstractDefaultRule) {
					addDestinationAddressItem(t, rule, nil, []string{"video.example"})
				})
				rule.ruleSetItem.setList = []adapter.RuleSet{newLocalRuleSetForTest("video", inner)}
			}
			metadata := adapter.InboundContext{Destination: M.ParseSocksaddr(test.host + ":443")}
			if test.address != "" {
				metadata.DestinationAddresses = []netip.Addr{netip.MustParseAddr(test.address)}
			}
			narrow, err := NewDomainItem(nil, []string{"video.example"}, rule.domainMatchStrategy)
			require.NoError(t, err)
			require.False(t, narrow.Match(&metadata), "the narrow destination did not match")
			require.True(t, rule.Match(&metadata), "an alternative must satisfy the destination group")
			identity := rule.SmartTarget()
			require.Equal(t, smart.RuleTarget{}, identity)
			policy := smart.NewTargetPolicy()
			now := time.Now()
			require.Equal(t, smart.SiteKey(test.host), policy.Select(now, test.host, "13335", identity))
			for range 4 {
				policy.RecordSuccess(now, identity, "64501")
			}
			require.Empty(t, policy.Snapshot(now), "a mixed rule must not acquire an ASN claim")
		})
	}
}

func TestSmartRuleSingleDestinationRetainsIdentity(t *testing.T) {
	for _, test := range []struct {
		options option.RawDefaultRule
		key     string
	}{
		{options: option.RawDefaultRule{Domain: []string{"video.example"}}, key: "domain:video.example"},
		{options: option.RawDefaultRule{DomainSuffix: []string{"video.example"}, Port: []uint16{443}}, key: "domain-suffix:video.example"},
		{options: option.RawDefaultRule{RuleSet: []string{"video"}}, key: "rule-set:video"},
	} {
		t.Run(test.key, func(t *testing.T) {
			rule, err := NewDefaultRule(context.Background(), log.NewNOPFactory().NewLogger("test"), option.DefaultRule{RawDefaultRule: test.options})
			require.NoError(t, err)
			if rule.ruleSetItem != nil {
				inner := headlessDefaultRule(t, func(rule *abstractDefaultRule) {
					addDestinationAddressItem(t, rule, nil, []string{"video.example"})
				})
				rule.ruleSetItem.setList = []adapter.RuleSet{newLocalRuleSetForTest("video", inner)}
			}
			metadata := adapter.InboundContext{Destination: M.ParseSocksaddr("video.example:443")}
			require.True(t, rule.Match(&metadata))
			metadata = adapter.InboundContext{Destination: M.ParseSocksaddr("other.example:443")}
			require.False(t, rule.Match(&metadata))
			require.Equal(t, test.key, rule.SmartTarget().Key)
			policy := smart.NewTargetPolicy()
			require.Equal(t, test.key, policy.Select(time.Now(), "video.example", "64501", rule.SmartTarget()))
		})
	}
}

func TestSmartRuleIdentityIsStructuredAndConservative(t *testing.T) {
	for _, test := range []struct {
		options      option.DefaultRule
		key          string
		broad, claim bool
	}{
		{options: option.DefaultRule{RawDefaultRule: option.RawDefaultRule{RuleSet: badoption.Listable[string]{"video"}}}, key: "rule-set:video", claim: true},
		{options: option.DefaultRule{RawDefaultRule: option.RawDefaultRule{RuleSet: badoption.Listable[string]{"category-media"}}}, key: "rule-set:category-media", broad: true, claim: true},
		{options: option.DefaultRule{RawDefaultRule: option.RawDefaultRule{RuleSet: badoption.Listable[string]{"video", "music"}}}},
		{options: option.DefaultRule{RawDefaultRule: option.RawDefaultRule{RuleSet: badoption.Listable[string]{"video"}, Invert: true}}},
		{options: option.DefaultRule{RawDefaultRule: option.RawDefaultRule{DomainSuffix: badoption.Listable[string]{".Example.COM"}}}, key: "domain-suffix:example.com"},
		{options: option.DefaultRule{RawDefaultRule: option.RawDefaultRule{Domain: badoption.Listable[string]{"Example.COM."}}}, key: "domain:example.com"},
	} {
		rule, err := NewDefaultRule(context.Background(), log.NewNOPFactory().NewLogger("test"), test.options)
		require.NoError(t, err)
		identity := rule.SmartTarget()
		require.Equal(t, test.key, identity.Key)
		require.Equal(t, test.broad, identity.Broad)
		require.Equal(t, test.claim, identity.Claimable)
	}
}
