### Structure

```json
{
  "type": "smart",
  "tag": "smart",
  "outbounds": ["proxy-a", "proxy-b"],
  "providers": ["provider-a"],
  "exclude": "",
  "include": "",
  "use_all_providers": false,
  "url": "https://www.gstatic.com/generate_204",
  "interval": "5m",
  "timeout": "5s",
  "tolerance": 0,
  "max_failed_times": 5,
  "policy_priority": "premium:0.8;backup:1.2",
  "use_lightgbm": false,
  "collect_data": false,
  "sample_rate": 1,
  "prefer_asn": false,
  "disable_udp": false,
  "expected_status": "200-299,302"
}
```

### Fields

#### outbounds

List of candidate outbound tags.

#### providers

List of [Provider](/configuration/provider) tags. Provider candidates are expanded to leaf outbounds.

#### exclude

Regular expression for excluding candidates from `providers`.

#### include

Regular expression for including candidates from `providers`.

#### use_all_providers

Use every configured provider as a candidate. The default is `false`.

#### url

The URL used for active delay tests. The default is `https://www.gstatic.com/generate_204`.

#### interval

The active delay test interval. The default is `5m`.

#### timeout

The timeout for each active delay test. The default is `5s`.

#### tolerance

Delay tolerance in milliseconds used to keep URL-test fallback ordering stable. The default is `0` (no tolerance).

#### max_failed_times

Consecutive target-specific failures before a candidate is temporarily blocked. The default is `5`.

#### policy_priority

Ordered, semicolon-separated `pattern:factor` rules that multiply a node's Smart weight. The first matching rule wins; factors must be greater than zero. A colon in a pattern must be escaped, for example `name\\:edge:0.8`.

#### use_lightgbm

Use the shared LightGBM model after it has been loaded and enough samples exist. If no local model exists, enabling this starts a background download on first use; it falls back to traditional scoring until then. The default is `false`. See [Smart](/configuration/experimental/smart/) for model settings.

#### collect_data

Append model-training samples to the shared CSV collector. The default is `false`. See [Smart](/configuration/experimental/smart/) for collector settings.

#### sample_rate

Fraction of eligible observations recorded when `collect_data` is enabled. It must be between `0` and `1`; `0` uses the default rate of `1`.

#### prefer_asn

Use routing-rule identity and destination ASN evidence to group targets. The default is `false`. Narrow domain and rule-set matches retain their own identity; broad collections can reuse a service identity learned from successful TCP connections. Conflicting claims are ignored, and unknown or shared hosting/CDN ASNs fall back to site keys rather than merging unrelated sites. See [Smart](/configuration/experimental/smart/) for database settings.

#### disable_udp

Disable UDP for this Smart group. The default is `false`.

#### expected_status

Allowed HTTP response status codes for active URL tests. Use comma-separated status codes or inclusive ranges, for example `204,301-304`. Empty or `*` accepts every HTTP status.

### Behavior

Smart learns independently for each target, network, and leaf outbound. Domain targets use public-suffix-aware site keys, grouping related subdomains while keeping unrelated sites separate; IP targets remain literal addresses. Nested groups are flattened and provider membership changes propagate to Smart, including empty candidate lists.

It uses the most recent closed connection for traffic-scene classification and historical success, connect time, and latency for mihomo-compatible weighting. Peak transfer rates use one-second sampling windows. Where TCP counters are available, cumulative loss is total observed retransmitted packets divided by total observed sent packets; historical connection loss percentages are not averaged to obtain cumulative loss.

Two target-specific samples are required for learned weights. For TCP, the first three ranked candidates are tried sequentially; candidates through the tenth position are then tried in batches of at most five. Further candidates remain sequential fallbacks. Pinned targets and UDP always use sequential attempts.

A successful dial pins its target and transport to that leaf for ten minutes. Pins expire or become unusable when the leaf is replaced, removed, or blocked. Each dialing stage has a bounded timeout and respects the caller's deadline. UDP socket creation is not proof of remote reachability. Failure bursts of fifty observations within two seconds pause failure learning and reset runtime breakers without erasing historical metrics; success or a new window resumes learning.

URL tests provide only cold-start and fallback ordering and do not modify target metrics. Manual leaf delay results and automatic Smart probe results are stored independently. Testing a leaf does not trigger a Smart group probe; testing the Smart group uses its configured `url`.

After a successful HTTPS TCP connection closes with less than 0.03 MiB downloaded, Smart may request the original host's `/robots.txt` through the same leaf. A suspected exit can also trigger a response recheck. These are separate from URL delay tests: probe traffic does not become a business observation or destination ASN claim evidence. Responses are classified using mihomo's rules for challenges, rate limits, regional restrictions, redirects and errors. Refusals can temporarily avoid a node for the site and learned target; reachable answers clear the avoidance. Cross-host redirects are not followed. Response bodies are limited to 4 KiB and one second of reading; the probe timeout is eight seconds. Builds with `with_utls` use matching browser TLS fingerprints and request headers; other builds use native TLS with the same request headers.

Response probes share a process-wide budget of 120 starts per minute with a burst of ten, and at most four run concurrently. Each target allows six starts per minute; each target-node pair normally waits two minutes, or thirty seconds after a recent refusal. Hosts that repeatedly refuse without any reachable answer enter a thirty-minute blind window. Broad target failure also suspends further node avoidance so a host outage does not exhaust the pool. Temporary avoidance is rechecked every fifteen minutes, subject to the same budgets.

Smart also learns each used node's exit region through `https://www.cloudflare.com/cdn-cgi/trace`, with `https://api.ip.sb/geoip` and `https://ipwho.is/` as fallbacks. These requests also use that leaf and the shared probe budget, with two separate concurrent slots. Fresh answers are reused for six hours and failed queries wait fifteen minutes. Exit ASN lookup and ASN-based suspicion require `prefer_asn`; region-based suspicion does not. Two independent exits refusing a site, corroborated by a successful exit from another region or ASN, can defer other nodes of the suspected region or network. Nodes sharing one exit count once. A leased fallback remains available, and a reachable recheck clears the suspicion. No extra configuration fields are required.

The Clash API exposes Smart status in the `smart` object of group proxy responses. It reports the last successful candidate and the most recent ranking snapshot. Smart does not provide manual selection. Clearing its cache also cancels pending response and exit probes and clears target pins, response avoidance, exit identities, exit suspicions and destination ASN claim evidence. Destination ASN evidence otherwise persists with history. A blocked node's existing connections for the affected target may be closed.

History is stored in `smart-history.json` under the configured base path, with separate snapshots per group. Metrics unused for seven days expire; at most 50,000 metric entries are retained per group. Exit region, optional exit ASN, a hashed exit identity and query retry times persist alongside the metrics; raw exit IP addresses and runtime suspicions are not stored. Only the current history format is read. Old or unsupported versions log a warning and start fresh; new observations can replace the old file. Configuration fields and history formats from earlier Smart implementations are not migrated.

### Acknowledgements

Smart behavior and its LightGBM integration are based on [vernesong/mihomo](https://github.com/vernesong/mihomo).
