### Structure

```json
{
  "type": "vless",
  "tag": "vless-out",
  
  "server": "127.0.0.1",
  "server_port": 1080,
  "uuid": "bf000d23-0752-40b4-affe-68f7707a9661",
  "flow": "xtls-rprx-vision",
  "encryption": "none",
  "network": "tcp",
  "tls": {},
  "packet_encoding": "",
  "multiplex": {},
  "transport": {},

  ... // Dial Fields
}
```

### Fields

#### server

==Required==

The server address.

#### server_port

==Required==

The server port.

#### uuid

==Required==

VLESS user id.

#### flow

VLESS Sub-protocol.

Available values:

* `xtls-rprx-vision`

#### encryption

Optional client-side VLESS encryption layer. Omit or set `"none"` to disable it. For a server configured with VLESS post-quantum encryption, use its public key in the form `mlkem768x25519plus.native.0rtt.<base64url-public-key>`; the appearance may also be `xorpub` or `random`, and the RTT mode may be `1rtt`. This layer is independent of TLS/REALITY. Only the outbound client is supported; VLESS inbound `decryption` is not implemented. Match the server's settings exactly.

#### network

Enabled network

One of `tcp` `udp`.

Both is enabled by default.

#### tls

TLS configuration, see [TLS](/configuration/shared/tls/#outbound).

#### packet_encoding

UDP packet encoding, xudp is used by default.

| Encoding   | Description           |
|------------|-----------------------|
| (none)     | Disabled              |
| packetaddr | Supported by v2ray 5+ |
| xudp       | Supported by xray     |

#### multiplex

See [Multiplex](/configuration/shared/multiplex#outbound) for details.

#### transport

V2Ray Transport configuration, see [V2Ray Transport](/configuration/shared/v2ray-transport/).

### Dial Fields

See [Dial Fields](/configuration/shared/dial/) for details.
