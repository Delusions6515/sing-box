# Introduction

sing-box uses JSON for configuration files.
### Structure

```json
{
  "$schema": "https://raw.githubusercontent.com/reF1nd/sing-box/reF1nd-testing/docs/schema.json",
  "log": {},
  "dns": {},
  "ntp": {},
  "certificate": {},
  "certificate_providers": [],
  "http_clients": [],
  "network_namespaces": [],
  "endpoints": [],
  "inbounds": [],
  "outbounds": [],
  "providers": [],
  "route": {},
  "services": [],
  "experimental": {}
}
```

### Fields

| Key            | Format                          |
|----------------|---------------------------------|
| `$schema`      | [JSON Schema](./schema/)        |
| `log`          | [Log](./log/)                   |
| `dns`          | [DNS](./dns/)                   |
| `ntp`          | [NTP](./ntp/)                   |
| `certificate`  | [Certificate](./certificate/)   |
| `certificate_providers` | [Certificate Provider](./shared/certificate-provider/) |
| `http_clients` | [HTTP Client](./shared/http-client/) |
| `network_namespaces` | [Network Namespace](./network-namespace/) |
| `endpoints`    | [Endpoint](./endpoint/)         |
| `inbounds`     | [Inbound](./inbound/)           |
| `outbounds`    | [Outbound](./outbound/)         |
| `providers`    | [Provider](./provider/)         |
| `route`        | [Route](./route/)               |
| `services`     | [Service](./service/)           |
| `experimental` | [Experimental](./experimental/) |

### Check

```bash
sing-box check
```

### Format

```bash
sing-box format -w -c config.json -D config_directory
```

### Merge

```bash
sing-box merge output.json -c config.json -D config_directory
```

## Starlark in JSON comments

Core paths integrated in this repository can transform attached JSON values with marked `@starlark` block comments. See the [Starlark configuration guide](./starlark.md) for syntax, target identities, CLI workflows, validation and reload behavior, limits, and examples.
