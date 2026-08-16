### 结构

```json
{
  "type": "smart",
  "tag": "smart",
  "outbounds": ["proxy-a", "proxy-b"],
  "providers": ["provider-a"],
  "url": "https://www.gstatic.com/generate_204",
  "interval": "10m",
  "timeout": "5s",
  "tolerance": 50,
  "max_selected": 10,
  "min_samples": 2,
  "max_failed_times": 3,
  "history_path": "smart-history.json",
  "history_retention": "168h",
  "max_history_entries": 50000
}
```

### 字段

#### outbounds, providers, exclude, include, use_all_providers

候选出站与订阅配置。订阅中的组会递归展开为叶子出站。

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

### 行为

Smart 按目标、网络和叶子出站独立学习。流量场景使用最近一次关闭连接识别，历史成功率、连接时间和延迟使用兼容 mihomo 的权重计算。URL 测速只服务于冷启动和降级排序，不会修改目标业务指标。

Clash API 会在组代理响应的 `smart` 对象中提供只读状态，包含最后成功节点和最近一次排序快照。Smart 不提供手动选择，也不会中断已有连接。
