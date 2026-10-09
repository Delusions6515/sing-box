# XHTTP

XHTTP 支持客户端和服务端。构建需要 `with_xhttp`；HTTP/3 还需要 `with_quic`。
实现移植自 [MiChongs/sing-box](https://github.com/MiChongs/sing-box)，对齐 Xray-core v26.9.30 的 `splithttp`。

## 结构

```json
{
  "type": "xhttp",
  "host": "example.com",
  "path": "/xhttp",
  "mode": "auto",
  "headers": {},
  "x_padding_bytes": "100-1000",
  "x_padding_obfs_mode": false,
  "x_padding_key": "",
  "x_padding_header": "",
  "x_padding_placement": "",
  "x_padding_method": "",
  "uplink_http_method": "",
  "session_placement": "",
  "session_key": "",
  "session_id_table": "",
  "session_id_length": 0,
  "seq_placement": "",
  "seq_key": "",
  "uplink_data_placement": "",
  "uplink_data_key": "",
  "uplink_chunk_size": 0,
  "no_grpc_header": false,
  "no_sse_header": false,
  "sc_max_each_post_bytes": 0,
  "sc_min_posts_interval_ms": 0,
  "sc_max_buffered_posts": 0,
  "sc_stream_up_server_secs": 0,
  "server_max_header_bytes": 0,
  "xmux": {},
  "download_settings": {},
  "user_agent": "chrome",
  "quic_congestion": "bbr",
  "quic_up": 0
}
```

省略可选字段以使用默认值。不需要上下行分离时，应省略 `download_settings`。

## 连接与模式

`host` 接受字符串或数组，客户端随机选择一个主机。不要在 `headers` 中设置 `Host`。
`path` 支持查询字符串。客户端、服务端配置需要一致，反向代理应保留路径。

模式为 `auto`、`packet-up`、`stream-up` 和 `stream-one`。
客户端的 `auto` 在 REALITY 且没有下行独立配置时选择 `stream-one`，有下行独立配置时选择 `stream-up`，其他情况选择 `packet-up`。
REALITY 使用 HTTP/2；其他 TLS 连接由 ALPN 选择 HTTP 版本，明文 HTTP 使用 HTTP/1.1。
HTTP/3 使用标准 TLS 配置，忽略 uTLS 指纹，但保留证书校验、SNI 和 ECH。

## 区间与请求字段

区间字段接受整数或 `"100-1000"` 形式的字符串，不接受数组或 JSON 对象。
包括 `x_padding_bytes`、`session_id_length`、`uplink_chunk_size`、`sc_max_each_post_bytes`、`sc_min_posts_interval_ms`、`sc_stream_up_server_secs` 和下面的 XMUX 区间。

| 字段 | 用途 |
| --- | --- |
| `x_padding_*` | 填充长度、位置和编码方式；客户端与服务端需一致。 |
| `uplink_http_method` | 上传 HTTP 方法；下载使用 GET。 |
| `session_placement`、`session_key` | 会话 ID 的位置和名称。 |
| `session_id_table`、`session_id_length` | 会话 ID 的字符表和长度。 |
| `seq_placement`、`seq_key` | packet-up 序号的位置和名称。 |
| `uplink_data_placement`、`uplink_data_key`、`uplink_chunk_size` | 上传数据在 body/header/cookie 中的位置及分块。 |
| `no_grpc_header` | 不发送上传 gRPC Content-Type。 |
| `no_sse_header` | 服务端不发送 stream-down SSE Content-Type。 |
| `sc_max_each_post_bytes`、`sc_min_posts_interval_ms`、`sc_max_buffered_posts` | packet-up 请求大小、间隔和服务端缓冲。 |
| `sc_stream_up_server_secs`、`server_max_header_bytes` | 服务端流存活时间和最大请求头字节数。 |
| `user_agent` | 浏览器请求头：`chrome`、`firefox`、`safari`、`edge`、`curl`、`golang`，或自定义 User-Agent。 |
| `quic_congestion`、`quic_up` | HTTP/3 拥塞控制：`bbr`、`reno`、`force-brutal`；Brutal 带宽单位为 bytes/s，至少 65536。 |

## XMUX

```json
{
  "max_concurrency": 0,
  "max_connections": 3,
  "c_max_reuse_times": 0,
  "h_max_request_times": "600-900",
  "h_max_reusable_secs": "1800-3000",
  "h_keep_alive_period": 0
}
```

省略或空 `xmux` 使用上述默认值。只填写部分参数时，不补齐整套默认值。
前五个字段是区间。`h_keep_alive_period` 单位为秒：零使用 H2/H3 默认值，负数关闭 keepalive。

## 下行独立配置

```json
{
  "download_settings": {
    "server": "download.example.com",
    "server_port": 443,
    "tls": {"enabled": true, "server_name": "download.example.com"},
    "host": "download.example.com",
    "path": "/download"
  }
}
```

上传使用出站的服务器、TLS 和拨号配置。下载使用 `download_settings`，支持服务器、TLS、拨号及 XHTTP 字段。
XHTTP 参数独立，不继承上行的 path、host、headers、padding 或 XMUX。未填写 server/port 时沿用上行地址；下行拨号字段均为空时复用上行拨号器。

## 相比此前 lx 客户端的变化

此实现替换原 lx 客户端，新增 XHTTP 入站和 VLESS `decryption`。
将 `session_table`、`session_length` 分别改为 `session_id_table`、`session_id_length`。
删除不再接受的 `sc_max_concurrent_posts`，将旧 XMUX 数组/对象区间改为整数或区间字符串。
XMUX 默认值由并发数一改为连接数三。配置使用上述字段，不提供旧字段别名。

VLESS 加密密钥和 Vision 参见[出站](../../outbound/vless/)与[入站](../../inbound/vless/)文档。
