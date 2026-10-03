# 引言

sing-box 使用 JSON 作为配置文件格式。
### 结构

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

### 字段

| Key            | Format                 |
|----------------|------------------------|
| `$schema`      | [JSON Schema](./schema/) |
| `log`          | [日志](./log/)           |
| `dns`          | [DNS](./dns/)          |
| `ntp`          | [NTP](./ntp/)          |
| `certificate`  | [证书](./certificate/)   |
| `certificate_providers` | [证书提供者](./shared/certificate-provider/) |
| `http_clients` | [HTTP 客户端](./shared/http-client/) |
| `network_namespaces` | [网络命名空间](./network-namespace/) |
| `endpoints`    | [端点](./endpoint/)      |
| `inbounds`     | [入站](./inbound/)       |
| `outbounds`    | [出站](./outbound/)      |
| `providers`    | [提供者](./provider/)         |
| `route`        | [路由](./route/)         |
| `services`     | [服务](./service/)       |
| `experimental` | [实验性](./experimental/) |

### 检查

```bash
sing-box check
```

### 格式化

```bash
sing-box format -w -c config.json -D config_directory
```

### 合并

```bash
sing-box merge output.json -c config.json -D config_directory
```

## 注释中的 Starlark

本仓库中已接入功能的核心路径可以使用带 `@starlark` 标记的块注释变换其附着的 JSON 值。语法、目标身份、CLI 工作流、校验与重载行为、限制和示例详见 [Starlark 配置指南](./starlark.zh.md)。
