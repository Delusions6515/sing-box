package smart

import (
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestSiteKeyAndASNClaims(t *testing.T) {
	now := time.Unix(1000, 0)
	p := NewTargetPolicy()
	rule := RuleTarget{Key: "rule-set:video", Claimable: true}
	require.Equal(t, "rule-set:video", p.Select(now, "www.video.example", "64501", rule))
	for range 4 {
		p.RecordSuccess(now, rule, "64501")
	}
	require.Equal(t, "rule-set:video", p.Select(now, "cdn.other.example", "64501", RuleTarget{}))
	other := RuleTarget{Key: "rule-set:music", Claimable: true}
	for range 4 {
		p.RecordSuccess(now, other, "64501")
	}
	require.Equal(t, "asn:64501", p.Select(now, "cdn.other.example", "64501", RuleTarget{}))
	require.Equal(t, "*.example.com", p.Select(now, "api.example.com", "13335", RuleTarget{}))
	require.Equal(t, "*.example.com", p.Select(now, "www.example.com", "", RuleTarget{}))
	require.Equal(t, "192.0.2.1", p.Select(now, "192.0.2.1", "", RuleTarget{}))
	p.Clear()
	require.Equal(t, "asn:64501", p.Select(now, "api.example.com", "64501", RuleTarget{}))
}

func TestTargetPolicyBroadRulesAndDiversity(t *testing.T) {
	now := time.Unix(1000, 0)
	p := NewTargetPolicy()
	broad := RuleTarget{Key: "rule-set:global", Claimable: true, Count: 10000}
	require.Equal(t, "asn:64501", p.Select(now, "www.example.com", "64501", broad))
	require.Equal(t, "*.example.com", p.Select(now, "api.example.com", "13335", broad))
	p = NewTargetPolicy()
	rule := RuleTarget{Key: "rule-set:custom", Claimable: true}
	for i, asn := range []string{"1", "2", "3", "4", "5", "6"} {
		target := p.Select(now, "api.example.com", asn, rule)
		if i < 5 {
			require.Equal(t, rule.Key, target)
		} else {
			require.Equal(t, "asn:6", target)
		}
	}
}

func TestTargetPolicyInvalidatesEvidenceWhenRuleBroadens(t *testing.T) {
	for _, count := range []bool{true, false} {
		t.Run(map[bool]string{true: "count threshold", false: "broad flag"}[count], func(t *testing.T) {
			now := time.Unix(1000, 0)
			p := NewTargetPolicy()
			rule := RuleTarget{Key: "rule-set:video", Claimable: true, Count: 9999}
			for range 4 {
				p.RecordSuccess(now, rule, "64501")
			}
			require.Equal(t, rule.Key, p.Select(now, "cdn.other.example", "64501", RuleTarget{}))
			require.NotEmpty(t, p.Snapshot(now))
			if count {
				rule.Count = 10000
			} else {
				rule.Broad = true
			}
			require.Equal(t, "asn:64501", p.Select(now, "cdn.other.example", "64501", rule))
			require.Equal(t, "asn:64501", p.Select(now, "cdn.unrelated.example", "64501", RuleTarget{}))
			p.RecordSuccess(now, rule, "64501")
			require.Empty(t, p.Snapshot(now), "broad rules must neither retain nor relearn narrow claims")
		})
	}
}

func TestTargetPolicyEvidenceExpires(t *testing.T) {
	now := time.Unix(1000, 0)
	p := NewTargetPolicy()
	rule := RuleTarget{Key: "rule-set:video", Claimable: true}
	p.RecordSuccess(now, rule, "1")
	p.RecordSuccess(now, rule, "2")
	require.Equal(t, rule.Key, p.Select(now, "example.com", "1", RuleTarget{}))
	require.Equal(t, "asn:1", p.Select(now.Add(8*24*time.Hour), "example.com", "1", RuleTarget{}))
}
