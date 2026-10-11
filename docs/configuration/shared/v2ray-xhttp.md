# XHTTP

XHTTP supports client and server transports. Build with `with_xhttp`; HTTP/3 also requires `with_quic`.
The implementation is ported from [MiChongs/sing-box](https://github.com/MiChongs/sing-box), targeting Xray-core v26.9.30 `splithttp`.

## Structure

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

Omit optional fields to use defaults. In particular, omit `download_settings` unless separate download settings are wanted.

## Connection and mode

`host` accepts a string or an array; the client randomly selects a host. Do not put `Host` in `headers`.
`path` supports a query string. Client and server settings must agree; preserve the path through reverse proxies.

Modes are `auto`, `packet-up`, `stream-up` and `stream-one`.
On the client, `auto` chooses `stream-one` for REALITY without separate download settings, `stream-up` for REALITY with separate download settings, and `packet-up` otherwise.
REALITY uses HTTP/2. Otherwise TLS/ALPN selects the HTTP version; plain HTTP uses HTTP/1.1.
HTTP/3 uses a standard TLS configuration, ignoring the uTLS fingerprint but retaining certificate verification, SNI and ECH.

## Ranges and request fields

Range fields accept an integer or a string such as `"100-1000"`, not an array or a JSON object.
They include `x_padding_bytes`, `session_id_length`, `uplink_chunk_size`, `sc_max_each_post_bytes`, `sc_min_posts_interval_ms`, `sc_stream_up_server_secs` and the XMUX ranges below.

| Fields | Purpose |
| --- | --- |
| `x_padding_*` | Padding size, placement and encoding. Client and server must agree. |
| `uplink_http_method` | Upload HTTP method; downloads use GET. |
| `session_placement`, `session_key` | Session ID placement and name. |
| `session_id_table`, `session_id_length` | Session ID alphabet and length. |
| `seq_placement`, `seq_key` | Packet-up sequence placement and name. |
| `uplink_data_placement`, `uplink_data_key`, `uplink_chunk_size` | Upload body/header/cookie placement and chunking. |
| `no_grpc_header` | Omit the upload gRPC content type. |
| `no_sse_header` | Omit the server's stream-down SSE content type. |
| `sc_max_each_post_bytes`, `sc_min_posts_interval_ms`, `sc_max_buffered_posts` | Packet-up request size, interval and server buffering. |
| `sc_stream_up_server_secs`, `server_max_header_bytes` | Server stream lifetime and maximum header bytes. |
| `user_agent` | Browser headers: `chrome`, `firefox`, `safari`, `edge`, `curl`, `golang`, or a custom User-Agent. |
| `quic_congestion`, `quic_up` | HTTP/3 congestion control: `bbr`, `reno`, `force-brutal`; Brutal bandwidth in bytes/s, at least 65536. |

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

An omitted or empty `xmux` uses these defaults. Partially supplied XMUX options do not acquire the full default set.
The first five fields are ranges. `h_keep_alive_period` is seconds: zero uses H2/H3 defaults; a negative value disables keepalive.

## Separate download settings

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

Uploads use the outbound's server, TLS and dialer. Downloads use `download_settings`, which accepts server, TLS, dialer and XHTTP fields.
XHTTP fields are independent: path, host, headers, padding and XMUX are not inherited. An omitted server/port uses the upload address; an empty download dialer reuses the upload dialer.

## Changes from the previous lx client

This replaces the lx-based client and adds XHTTP inbound and VLESS `decryption`.
Rename `session_table` and `session_length` to `session_id_table` and `session_id_length`.
Remove `sc_max_concurrent_posts`; it is not accepted. Convert old XMUX array/object ranges to integers or range strings.
The default XMUX is now three connections rather than concurrency one. Configuration must use the fields above; legacy aliases are not provided.

For VLESS encryption keys and Vision, see the [outbound](../../outbound/vless/) and [inbound](../../inbound/vless/) documentation.
