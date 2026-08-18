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
  "interval": "10m",
  "timeout": "5s",
  "tolerance": 50,
  "max_selected": 10,
  "min_samples": 2,
  "max_failed_times": 3,
  "history_path": "smart-history.json",
  "history_retention": "168h",
  "max_history_entries": 50000,
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

The active delay test interval. The default is `10m`.

#### timeout

The timeout for each active delay test. The default is `5s`.

#### tolerance

Delay tolerance in milliseconds used to keep URL-test fallback ordering stable. The default is `50`.

#### max_selected

Maximum candidates considered for one parallel dial. The default is `10`; at most five dials run concurrently.

#### min_samples

Minimum target-specific samples required before a candidate participates through its Smart weight. The default is `2`.

#### max_failed_times

Consecutive target-specific failures before a candidate is temporarily blocked. The default is `3`.

#### history_path

Path of the Smart history file. Relative paths use the configured base path. Smart groups sharing this path safely store separate group snapshots. The default is `smart-history.json`.

#### history_retention

Maximum age of an unused metric entry. The default is `168h`.

#### max_history_entries

Maximum retained metric entries per Smart group. The default is `50000`.

#### policy_priority

Ordered, semicolon-separated `pattern:factor` rules that multiply a node's Smart weight. The first matching rule wins; factors must be greater than zero. A colon in a pattern must be escaped, for example `name\\:edge:0.8`.

#### use_lightgbm

Use the shared LightGBM model after it has been loaded and enough samples exist. If no local model exists, enabling this starts a background download on first use; it falls back to traditional scoring until then. The default is `false`. See [Smart](/configuration/experimental/smart/) for model settings.

#### collect_data

Append model-training samples to the shared CSV collector. The default is `false`. See [Smart](/configuration/experimental/smart/) for collector settings.

#### sample_rate

Fraction of eligible observations recorded when `collect_data` is enabled. It must be between `0` and `1`; `0` uses the default rate of `1`.

#### prefer_asn

Include the destination ASN in Smart metrics. The default is `false`. It has no effect until the shared ASN mirror is fully available. See [Smart](/configuration/experimental/smart/) for mirror settings.

#### disable_udp

Disable UDP for this Smart group. The default is `false`.

#### expected_status

Allowed HTTP response status codes for active URL tests. Use comma-separated status codes or inclusive ranges, for example `204,301-304`. Empty or `*` accepts every HTTP status.

### Behavior

Smart learns independently for each target, network, and leaf outbound. It uses the most recent closed connection for traffic-scene classification and historical success, connect time, and latency for mihomo-compatible weighting. URL tests provide only cold-start and fallback ordering and do not modify target metrics.

The Clash API exposes read-only Smart status in the `smart` object of group proxy responses. It reports the last successful candidate and the most recent ranking snapshot. Smart does not provide manual selection or interrupt existing connections.

### Acknowledgements

Smart behavior and its LightGBM integration are based on [vernesong/mihomo](https://github.com/vernesong/mihomo).
