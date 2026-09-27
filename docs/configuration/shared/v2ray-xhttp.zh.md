# XHTTP 客户端传输

XHTTP 是兼容 Xray 的 V2Ray 传输，**仅支持出站客户端**（VLESS 和 VMess）。编译时需使用 `-tags with_xhttp`；HTTP/3 还需要 `with_quic`。没有 `with_xhttp` 时，配置 XHTTP 会导致传输创建失败。不支持 XHTTP 入站服务端。

```json
{
  "type": "xhttp",
  "host": "example.com",
  "path": "/xhttp/",
  "mode": "auto",
  "headers": {},
  "x_padding_bytes": "100-1000",
  "xmux": {}
}
```

`auto` 在 REALITY 下选择 `stream-one`，否则选择 `packet-up`。也可指定 `packet-up`（GET 下载流和依次发送的上传请求）、`stream-up`（POST 上传流和 GET 下载流）或 `stream-one`（单个双向流）。无 TLS 时使用 HTTP/1.1；有 TLS 时通常使用 HTTP/2。单个 ALPN 值为 `http/1.1` 或 `h3` 时选择对应版本；REALITY 始终使用 HTTP/2。

| 字段 | 作用 |
|------|------|
| `host`、`path`、`headers` | HTTP 主机名、路径前缀和附加标头；反向代理需要尾部斜杠时请保留。 |
| `mode` | `auto`（默认）、`packet-up`、`stream-up` 或 `stream-one`。 |
| `x_padding_bytes` | 填充长度的闭区间，默认 `"100-1000"`。 |
| `no_grpc_header` | 流式上传时不发送 `Content-Type: application/grpc`。 |
| `session_placement`、`seq_placement` | 将会话 ID / packet-up 序号放在 `path`（默认）、`query`、`header` 或 `cookie`。 |
| `session_key`、`seq_key` | 非路径放置方式使用的键名。 |
| `session_table`、`session_length` | 会话 ID 的字符集和长度区间；均省略时使用带连字符的 UUID。 |
| `uplink_data_placement`、`uplink_data_key`、`uplink_chunk_size` | packet-up 数据放置方式（`auto`、`body`、`header`、`cookie`）、键名和 base64 分块大小区间。 |
| `uplink_http_method` | 上传方法，默认 POST；GET 仅适用于 packet-up。 |
| `x_padding_obfs_mode`、`x_padding_placement`、`x_padding_key`、`x_padding_header`、`x_padding_method` | 可选的填充混淆方式。 |
| `sc_max_each_post_bytes`、`sc_min_posts_interval_ms` | packet-up 单次上传大小与请求间隔的区间。 |
| `xmux` | HTTP 连接复用设置，见下文。 |

区间写作 `"min-max"`，或写成字符串形式的单个整数（例如 `"3000-4000"`、`"4000"`）。`sc_max_concurrent_posts` 和仅服务端使用的 `server_max_header_bytes`、`no_sse_header`、`sc_max_buffered_posts`、`sc_stream_up_server_secs` 会被接受，但客户端会忽略它们。

## XMUX

即使省略 `xmux`，也会启用 XMUX。省略或留空时使用兼容 Xray 的默认值；一旦设置任意字段，其他未填写的区间则不设上限。XMUX 区间还接受数字或两个数字组成的 JSON 数组。

| 字段 | 作用 |
|------|------|
| `max_concurrency`、`max_connections` | 每个连接的流数量或池中连接数量上限；两者互斥。 |
| `c_max_reuse_times` | 每个连接可分配给新流的次数上限。 |
| `h_max_request_times` | 每个连接的 HTTP 请求数上限。 |
| `h_max_reusable_secs` | 连接可复用的时长（秒）。 |
| `h_keep_alive_period` | HTTP/2 保活 ping 间隔（秒）；负数表示禁用。 |
