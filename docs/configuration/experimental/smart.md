### Structure

```json
{
  "model": {
    "path": "smart/Model.bin",
    "download_url": "",
    "auto_update": false,
    "update_interval": "72h",
    "http_client": ""
  },
  "collector": {
    "path": "smart/smart_weight_data.csv",
    "max_size": 104857600
  },
  "asn": {
    "path": "smart/asn/GeoLite2-ASN.mmdb",
    "url": "https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/GeoLite2-ASN.mmdb",
    "update_interval": "24h",
    "http_client": ""
  }
}
```

### model

Shared LightGBM model settings for Smart outbounds.

#### path

Path to `Model.bin`. The default is `smart/Model.bin`.

#### download_url

Model download URL. The default is `https://github.com/vernesong/mihomo/releases/download/LightGBM-Model/Model.bin`.

#### auto_update

When a Smart outbound enables `use_lightgbm` and the local model is absent, it is downloaded in the background on first use. This option controls subsequent periodic updates only. Disabled by default.

#### update_interval

Model update interval. The default is `72h`.

#### http_client

HTTP client used to download the model. The default HTTP client is used if empty.

### collector

Shared collection file settings for Smart groups with `collect_data` enabled.

#### path

Collection CSV path. The default is `smart/smart_weight_data.csv`.

#### max_size

Maximum collection file size in bytes. The default is `104857600` (100 MiB).

### asn

Shared ASN database settings. Uses the `GeoLite2-ASN.mmdb` (MaxMind format) release asset of [MetaCubeX/meta-rules-dat](https://github.com/MetaCubeX/meta-rules-dat). The local database is loaded at startup, and the upstream release asset is checked periodically over HTTP with ETag/304 change detection; a changed asset is downloaded and published atomically. Until the database is available, `prefer_asn` uses normal target weights.

#### path

Local ASN database file path. The default is `smart/asn/GeoLite2-ASN.mmdb`.

#### url

Database download URL. The default is `https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/GeoLite2-ASN.mmdb`. An HTTP ETag tracks changes: an unchanged asset is skipped with a 304 response.

#### update_interval

ASN database update interval. The default is `24h`.

#### http_client

HTTP client used to synchronize the ASN database. The default HTTP client is used if empty.

### Acknowledgements

Smart behavior and its LightGBM integration are based on [vernesong/mihomo](https://github.com/vernesong/mihomo).
