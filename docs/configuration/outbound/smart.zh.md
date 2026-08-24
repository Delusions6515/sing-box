### 结构

```json
{
  "type": "smart",
  "tag": "smart",
  "outbounds": ["proxy-a", "proxy-b"],
  "providers": ["provider-a"],
  "exclude": "",
  "include": "",
  "use_all_providers": false,
  "url": "https://www.gstatic.com/generate_204",
  "interval": "10m",
  "timeout": "5s",
  "tolerance": 50,
  "max_selected": 10,
  "min_samples": 2,
  "max_failed_times": 3,
  "history_path": "smart-history.json",
  "history_retention": "168h",
  "max_history_entries": 50000,
  "policy_priority": "premium:0.8;backup:1.2",
  "use_lightgbm": false,
  "collect_data": false,
  "sample_rate": 1,
  "prefer_asn": false,
  "disable_udp": false,
  "expected_status": "200-299,302"
}
```

### 字段

#### outbounds

候选出站标签列表。

#### providers

[订阅](/zh/configuration/provider)标签列表。订阅候选会展开为叶子出站。

#### exclude

从 `providers` 排除候选的正则表达式。

#### include

从 `providers` 包含候选的正则表达式。

#### use_all_providers

将所有已配置的订阅用作候选。默认 `false`。

#### url

主动测速地址。默认使用 `https://www.gstatic.com/generate_204`。

#### interval

主动测速周期。默认使用 `10m`。

#### timeout

每次主动测速超时。默认使用 `5s`。

#### tolerance

用于稳定 URL 测速降级排序的延迟容差，单位为毫秒。默认使用 `50`。

#### max_selected

一次并行拨号最多考虑的候选数。默认使用 `10`，并发拨号最多为五个。

#### min_samples

节点进入目标维度 Smart 权重排序所需的最少样本数。默认使用 `2`。

#### max_failed_times

节点因同一目标连续失败而临时阻断前的失败次数。默认使用 `3`。

#### history_path

Smart 历史文件路径。相对路径使用配置的基础目录；共享同一路径的 Smart 组会安全保存各自的快照。默认使用 `smart-history.json`。

#### history_retention

未使用指标的最长保留时间。默认使用 `168h`。

#### max_history_entries

每个 Smart 组保留的最大指标条目数。默认使用 `50000`。

#### policy_priority

按顺序以分号分隔的 `pattern:factor` 规则，用于乘以节点的 Smart 权重。首个匹配的规则生效，系数必须大于零。模式中的冒号必须转义，例如 `name\\:edge:0.8`。

#### use_lightgbm

在共享 LightGBM 模型已加载且样本足够后使用模型预测；若本地模型不存在，启用此选项会在首次使用时后台下载；此前会回退到传统评分。默认 `false`。模型设置见[Smart](/zh/configuration/experimental/smart/)。

#### collect_data

将模型训练样本追加到共享 CSV 采集器。默认 `false`。采集器设置见[Smart](/zh/configuration/experimental/smart/)。

#### sample_rate

启用 `collect_data` 后记录合格观测的比例，必须在 `0` 到 `1` 之间；`0` 使用默认比例 `1`。

#### prefer_asn

将目标 ASN 纳入 Smart 指标。默认 `false`。在共享 ASN 镜像完全可用前不会生效。镜像设置见[Smart](/zh/configuration/experimental/smart/)。

#### disable_udp

禁用该 Smart 组的 UDP。默认 `false`。

#### expected_status

主动 URL 测速允许的 HTTP 响应状态码。使用逗号分隔的状态码或闭区间，例如 `204,301-304`。留空或使用 `*` 接受全部 HTTP 状态。

### 行为

Smart 按目标、网络和叶子出站独立学习。流量场景使用最近一次关闭连接识别，历史成功率、连接时间和延迟使用兼容 mihomo 的权重计算。URL 测速只服务于冷启动和降级排序，不会修改目标业务指标。

Clash API 会在组代理响应的 `smart` 对象中提供只读状态，包含最后成功节点和最近一次排序快照。Smart 不提供手动选择，也不会中断已有连接。

### 致谢

Smart 行为和 LightGBM 集成参考了 [vernesong/mihomo](https://github.com/vernesong/mihomo)。
