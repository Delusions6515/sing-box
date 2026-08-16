### Structure

```json
{
  "type": "smart",
  "tag": "smart",
  "outbounds": ["proxy-a", "proxy-b"],
  "providers": ["provider-a"],
  "url": "https://www.gstatic.com/generate_204",
  "interval": "10m",
  "timeout": "5s",
  "tolerance": 50,
  "max_selected": 10,
  "min_samples": 2,
  "max_failed_times": 3,
  "history_path": "smart-history.json",
  "history_retention": "168h",
  "max_history_entries": 50000
}
```

### Fields

#### outbounds, providers, exclude, include, use_all_providers

Candidate outbound and provider configuration. Provider candidates are expanded to leaf outbounds.

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

### Behavior

Smart learns independently for each target, network, and leaf outbound. It uses the most recent closed connection for traffic-scene classification and historical success, connect time, and latency for mihomo-compatible weighting. URL tests provide only cold-start and fallback ordering and do not modify target metrics.

The Clash API exposes read-only Smart status in the `smart` object of group proxy responses. It reports the last successful candidate and the most recent ranking snapshot. Smart does not provide manual selection or interrupt existing connections.
