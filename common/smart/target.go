package smart

import (
	"net/netip"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"
)

const (
	targetPolicyLimit       = 4096
	targetEvidenceRetention = 7 * 24 * time.Hour
)

// RuleTarget is structured routing identity, never a parsed diagnostic String().
// Count is read from the current rule set, so updates can broaden a target.
type RuleTarget struct {
	Key       string
	Count     uint64
	Broad     bool
	Claimable bool
}

type ASNEvidence struct {
	Rule     string    `json:"rule"`
	ASN      string    `json:"asn"`
	Hits     int       `json:"hits"`
	LastUsed time.Time `json:"last_used"`
}

type targetEvidence struct {
	lastUsed time.Time
	networks map[string]ASNEvidence
}

// TargetPolicy keeps evidence bounded and separate from per-node failure metrics.
// Claims are derived, not persisted: contradictory evidence revokes them immediately.
type TargetPolicy struct {
	access   sync.Mutex
	evidence map[string]*targetEvidence
	sites    map[string]time.Time
}

func NewTargetPolicy() *TargetPolicy {
	return &TargetPolicy{evidence: make(map[string]*targetEvidence), sites: make(map[string]time.Time)}
}

func (p *TargetPolicy) Clear() {
	p.access.Lock()
	defer p.access.Unlock()
	clear(p.evidence)
	clear(p.sites)
}

func (p *TargetPolicy) Select(now time.Time, host, asn string, rule RuleTarget) string {
	p.access.Lock()
	defer p.access.Unlock()
	p.prune(now)
	if rule.Broad || rule.Count >= 10000 {
		delete(p.evidence, rule.Key)
	}
	if rule.Claimable && rule.Key != "" && !rule.Broad && rule.Count < 10000 && asn != "" && !SharedASN(asn) {
		p.record(ASNEvidence{Rule: rule.Key, ASN: asn, LastUsed: now})
	}
	site := SiteKey(host)
	broad := rule.Key == "" || rule.Broad || rule.Count >= 10000
	if evidence := p.evidence[rule.Key]; evidence != nil && len(evidence.networks) >= 6 {
		broad = true
	}
	if !broad {
		return rule.Key
	}
	if asn == "" || SharedASN(asn) {
		if site != "" {
			p.sites[site] = now
			p.trimSites()
		}
		return site
	}
	if claim := p.claim(asn); claim != "" {
		return claim
	}
	if _, known := p.sites[site]; known {
		p.sites[site] = now
		return site
	}
	return "asn:" + asn
}

func (p *TargetPolicy) RecordSuccess(now time.Time, rule RuleTarget, asn string) {
	if !rule.Claimable || rule.Broad || rule.Count >= 10000 || rule.Key == "" || asn == "" || SharedASN(asn) {
		return
	}
	p.access.Lock()
	defer p.access.Unlock()
	p.prune(now)
	p.record(ASNEvidence{Rule: rule.Key, ASN: asn, Hits: 1, LastUsed: now})
}

func (p *TargetPolicy) record(e ASNEvidence) {
	if e.Hits < 0 || e.Rule == "" || e.ASN == "" || SharedASN(e.ASN) {
		return
	}
	current := p.evidence[e.Rule]
	if current == nil {
		if len(p.evidence) >= targetPolicyLimit {
			var oldest string
			var age time.Time
			for key, entry := range p.evidence {
				if age.IsZero() || entry.lastUsed.Before(age) {
					oldest, age = key, entry.lastUsed
				}
			}
			delete(p.evidence, oldest)
		}
		current = &targetEvidence{networks: make(map[string]ASNEvidence)}
		p.evidence[e.Rule] = current
	}
	if e.LastUsed.After(current.lastUsed) {
		current.lastUsed = e.LastUsed
	}
	previous, known := current.networks[e.ASN]
	// Six unrelated networks are enough to prove a broad collection; no more
	// evidence is required, and this prevents unbounded per-rule maps.
	if !known && len(current.networks) >= 6 {
		return
	}
	e.Hits = min(4, previous.Hits+e.Hits)
	if previous.LastUsed.After(e.LastUsed) {
		e.LastUsed = previous.LastUsed
	}
	current.networks[e.ASN] = e
}

func (p *TargetPolicy) claim(asn string) string {
	claim := ""
	for rule, evidence := range p.evidence {
		if len(evidence.networks) >= 6 {
			continue
		}
		kinds := 0
		for _, e := range evidence.networks {
			if e.Hits > 0 {
				kinds++
			}
		}
		e, exists := evidence.networks[asn]
		if !exists || e.Hits == 0 || kinds < 2 && e.Hits < 4 {
			continue
		}
		if claim != "" && claim != rule {
			return ""
		}
		claim = rule
	}
	return claim
}

func (p *TargetPolicy) prune(now time.Time) {
	for rule, evidence := range p.evidence {
		for asn, e := range evidence.networks {
			if now.Sub(e.LastUsed) > targetEvidenceRetention {
				delete(evidence.networks, asn)
			}
		}
		if len(evidence.networks) == 0 {
			delete(p.evidence, rule)
		}
	}
	for site, used := range p.sites {
		if now.Sub(used) > targetEvidenceRetention {
			delete(p.sites, site)
		}
	}
}

func (p *TargetPolicy) trimSites() {
	if len(p.sites) <= targetPolicyLimit {
		return
	}
	var oldest string
	var age time.Time
	for site, used := range p.sites {
		if age.IsZero() || used.Before(age) {
			oldest, age = site, used
		}
	}
	delete(p.sites, oldest)
}

func (p *TargetPolicy) Snapshot(now time.Time) []ASNEvidence {
	p.access.Lock()
	defer p.access.Unlock()
	p.prune(now)
	var result []ASNEvidence
	for _, evidence := range p.evidence {
		for _, e := range evidence.networks {
			result = append(result, e)
		}
	}
	return result
}

func (p *TargetPolicy) Merge(now time.Time, evidence []ASNEvidence) {
	p.access.Lock()
	defer p.access.Unlock()
	p.prune(now)
	for _, e := range evidence {
		if now.Sub(e.LastUsed) <= targetEvidenceRetention {
			p.record(e)
		}
	}
}

// SharedASN identifies networks hosting unrelated services. Keep this in step
// with the reference implementation instead of claiming an entire CDN.
func SharedASN(asn string) bool {
	switch asn {
	case "13335", "12222", "16625", "20940", "31110", "35994", "54113", "22822", "15133", "19551", "20446", "5065", "60068", "16509", "36408", "4809", "4847", "199524", "212238", "55933", "43260", "43317", "43996", "33438", "396982", "16276", "30081", "12389", "37888", "45090", "207143", "14061", "24940", "31898", "36351", "14618", "45102", "132203", "55990", "12876", "51167", "197540", "20473", "63949", "9009", "60781", "36236", "39572", "400618", "4134", "4808", "4837":
		return true
	default:
		return false
	}
}

func BroadRuleSet(name string) bool {
	name, _, _ = strings.Cut(strings.ToLower(name), "@")
	switch name {
	case "cn", "private", "gfw", "greatfire", "ads-all", "oc-cn-domain", "china-domain", "china-ip", "tor":
		return true
	}
	if strings.HasPrefix(name, "category-") || strings.HasPrefix(name, "geolocation-") || strings.HasPrefix(name, "tld-") {
		return true
	}
	return strings.HasPrefix(name, "as") && SharedASN(strings.TrimPrefix(name, "as"))
}

// SiteKey follows the reference's public-suffix-aware wildcard grouping. A
// meaningful penultimate subdomain is retained, while random/sharded labels
// collapse to their registered domain. IP literals are never wildcarded.
func SiteKey(host string) string {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if _, err := netip.ParseAddr(host); err == nil {
		return host
	}
	domain, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil || host == domain {
		return host
	}
	sub := strings.TrimSuffix(host, "."+domain)
	labels := strings.Split(sub, ".")
	last := labels[len(labels)-1]
	random := strings.Contains(last, "-")
	hex := len(last) >= 8
	letters, digits := 0, 0
	for _, c := range last {
		if c >= 'a' && c <= 'z' {
			letters++
		} else if c >= '0' && c <= '9' {
			digits++
		} else {
			random = true
		}
		if !(c >= 'a' && c <= 'f' || c >= '0' && c <= '9') {
			hex = false
		}
	}
	random = random || hex || letters > 0 && digits > 0 && (len(last) > 10 || float64(digits)/float64(len(last)) > 0.6)
	if len(labels) == 1 || random {
		return "*." + domain
	}
	return "*." + last + "." + domain
}
