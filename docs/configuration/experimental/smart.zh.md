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
    "path": "smart/asn",
    "repository": "MetaCubeX/meta-rules-dat",
    "branch": "sing",
    "asset_path": "asn",
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

共享 ASN SRS 镜像设置。镜像会在启动后后台同步，并按周期更新。会先完整提取来源提交的归档，再发布新索引。完整本地索引可用前，`prefer_asn` 使用普通目标权重。

#### path

本地 ASN 镜像目录。默认 `smart/asn`。

#### repository

GitHub 仓库，格式为 `owner/repository`。默认 `MetaCubeX/meta-rules-dat`。

#### branch

仓库分支。默认 `sing`。

#### asset_path

包含 `AS<number>.srs` 资源的目录。默认 `asn`。

#### update_interval

ASN 镜像更新周期。默认 `24h`。

#### http_client

用于同步 ASN 镜像的 HTTP 客户端。为空时使用默认 HTTP 客户端。

### 致谢

Smart 行为和 LightGBM 集成参考了 [vernesong/mihomo](https://github.com/vernesong/mihomo)。
