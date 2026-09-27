# XHTTP client transport

XHTTP is an Xray-compatible V2Ray transport for **outbound clients only** (VLESS and VMess). Build with `-tags with_xhttp`; HTTP/3 also needs `with_quic`. Without `with_xhttp`, a configured XHTTP transport fails to start. XHTTP inbound/server support is not implemented.

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

`auto` chooses `stream-one` with REALITY and `packet-up` otherwise. The explicit modes are `packet-up` (GET download and sequential upload requests), `stream-up` (streamed POST upload and GET download), and `stream-one` (one bidirectional stream). Without TLS the client uses HTTP/1.1; with TLS it normally uses HTTP/2. A single ALPN value of `http/1.1` or `h3` selects that version instead; REALITY always uses HTTP/2.

| Field | Meaning |
|-------|---------|
| `host`, `path`, `headers` | HTTP host override, path prefix, and extra headers. Keep a trailing slash when the reverse proxy requires it. |
| `mode` | `auto` (default), `packet-up`, `stream-up`, or `stream-one`. |
| `x_padding_bytes` | Inclusive padding length range; defaults to `"100-1000"`. |
| `no_grpc_header` | Suppress `Content-Type: application/grpc` on streamed uploads. |
| `session_placement`, `seq_placement` | Put the session ID / packet-up sequence in `path` (default), `query`, `header`, or `cookie`. |
| `session_key`, `seq_key` | Names used for non-path placement. |
| `session_table`, `session_length` | Session-ID alphabet and length range; omit both for a dashed UUID. |
| `uplink_data_placement`, `uplink_data_key`, `uplink_chunk_size` | Packet-up payload placement (`auto`, `body`, `header`, `cookie`), key and base64 chunk-size range. |
| `uplink_http_method` | Upload method; defaults to POST. GET is valid only in packet-up mode. |
| `x_padding_obfs_mode`, `x_padding_placement`, `x_padding_key`, `x_padding_header`, `x_padding_method` | Optional configurable padding placement and generation. |
| `sc_max_each_post_bytes`, `sc_min_posts_interval_ms` | Packet-up upload size and interval ranges. |
| `xmux` | HTTP connection reuse settings; see below. |

Ranges use `"min-max"` or a single integer written as a string (for example `"3000-4000"` or `"4000"`). `sc_max_concurrent_posts` and the server-only `server_max_header_bytes`, `no_sse_header`, `sc_max_buffered_posts`, and `sc_stream_up_server_secs` are accepted but ignored by this client.

## XMUX

XMUX runs even when the `xmux` object is absent. An absent or empty object uses Xray-compatible defaults. If any setting is present, omitted range settings are unlimited. XMUX ranges also accept a number or a two-number JSON array.

| Field | Meaning |
|-------|---------|
| `max_concurrency`, `max_connections` | Streams per connection or maximum pooled connections; mutually exclusive. |
| `c_max_reuse_times` | Maximum stream assignments per connection. |
| `h_max_request_times` | Maximum HTTP requests per connection. |
| `h_max_reusable_secs` | Connection reusable lifetime in seconds. |
| `h_keep_alive_period` | HTTP/2 keepalive ping period in seconds; a negative value disables pings. |
