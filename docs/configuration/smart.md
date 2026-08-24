### Structure

```json
{
  "smart": {
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
      "path": "smart/asn",
      "repository": "MetaCubeX/meta-rules-dat",
      "branch": "sing",
      "asset_path": "asn",
      "update_interval": "24h",
      "http_client": ""
    }
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

Shared ASN SRS mirror settings. The mirror is synchronized in the background on startup and then periodically. The source commit archive is fully extracted before a new index is published. Until a complete local index is available, `prefer_asn` uses normal target weights.

#### path

Local ASN mirror directory. The default is `smart/asn`.

#### repository

GitHub repository in `owner/repository` format. The default is `MetaCubeX/meta-rules-dat`. There is no fallback repository.

#### branch

Repository branch. The default is `sing`.

#### asset_path

Directory containing `AS<number>.srs` assets. The default is `asn`.

#### update_interval

ASN mirror update interval. The default is `24h`.

#### http_client

HTTP client used to synchronize the ASN mirror. The default HTTP client is used if empty.

### Acknowledgements

Smart behavior and its LightGBM integration are based on [vernesong/mihomo](https://github.com/vernesong/mihomo).
