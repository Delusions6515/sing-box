### 结构

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

所有 Smart 出站组共享的 LightGBM 模型设置。

#### path

`Model.bin` 路径。默认 `smart/Model.bin`。

#### download_url

模型下载地址。默认使用 `https://github.com/vernesong/mihomo/releases/download/LightGBM-Model/Model.bin`。

#### auto_update

当某个 Smart 出站启用 `use_lightgbm` 且本地模型不存在时，会在首次使用时后台下载模型。此选项仅控制后续的周期更新。默认禁用。

#### update_interval

模型更新周期。默认 `72h`。

#### http_client

用于下载模型的 HTTP 客户端。为空时使用默认 HTTP 客户端。

### collector

供启用 `collect_data` 的 Smart 组共享的数据采集文件设置。

#### path

采集 CSV 路径。默认 `smart/smart_weight_data.csv`。

#### max_size

采集文件最大字节数。默认 `104857600` (100 MiB)。

### asn

共享 ASN 数据库设置。使用 [MetaCubeX/meta-rules-dat](https://github.com/MetaCubeX/meta-rules-dat) release 的 `GeoLite2-ASN.mmdb`（MaxMind 格式）。启动时加载本地数据库，并按周期通过 HTTP ETag/304 判断上游资产是否有变化，变化时下载并原子替换。数据库可用前，`prefer_asn` 使用普通目标权重。

#### path

本地 ASN 数据库文件路径。默认 `smart/asn/GeoLite2-ASN.mmdb`。

#### url

数据库下载地址。默认 `https://github.com/MetaCubeX/meta-rules-dat/releases/download/latest/GeoLite2-ASN.mmdb`。通过 HTTP ETag 判断变更：资源未变化时以 304 跳过下载。

#### update_interval

ASN 数据库更新周期。默认 `24h`。

#### http_client

用于同步 ASN 数据库的 HTTP 客户端。为空时使用默认 HTTP 客户端。

### 致谢

Smart 行为和 LightGBM 集成参考了 [vernesong/mihomo](https://github.com/vernesong/mihomo)。
