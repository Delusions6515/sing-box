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
  "interval": "5m",
  "timeout": "5s",
  "tolerance": 0,
  "max_failed_times": 5,
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

主动测速周期。默认使用 `5m`。

#### timeout

每次主动测速超时。默认使用 `5s`。

#### tolerance

用于稳定 URL 测速降级排序的延迟容差，单位为毫秒。默认使用 `0`，不启用容差。

#### max_failed_times

节点因同一目标连续失败而临时阻断前的失败次数。默认使用 `5`。

#### policy_priority

按顺序以分号分隔的 `pattern:factor` 规则，用于乘以节点的 Smart 权重。首个匹配的规则生效，系数必须大于零。模式中的冒号必须转义，例如 `name\\:edge:0.8`。

#### use_lightgbm

在共享 LightGBM 模型已加载且样本足够后使用模型预测；若本地模型不存在，启用此选项会在首次使用时后台下载；此前会回退到传统评分。默认 `false`。模型设置见[Smart](/zh/configuration/experimental/smart/)。

#### collect_data

将模型训练样本追加到共享 CSV 采集器。默认 `false`。采集器设置见[Smart](/zh/configuration/experimental/smart/)。

#### sample_rate

启用 `collect_data` 后记录合格观测的比例，必须在 `0` 到 `1` 之间；`0` 使用默认比例 `1`。

#### prefer_asn

使用路由规则身份和目标 ASN 证据归并目标。默认 `false`。窄范围域名或规则集保留独立身份；宽泛集合可复用从成功 TCP 连接学习的服务身份。相互冲突的归属不生效，未知 ASN 或共享托管/CDN ASN 回退到站点 key，避免将无关站点混在一起。数据库设置见[Smart](/zh/configuration/experimental/smart/)。

#### disable_udp

禁用该 Smart 组的 UDP。默认 `false`。

#### expected_status

主动 URL 测速允许的 HTTP 响应状态码。使用逗号分隔的状态码或闭区间，例如 `204,301-304`。留空或使用 `*` 接受全部 HTTP 状态。

### 行为

Smart 按目标、网络和叶子出站独立学习。域名使用基于公共后缀的站点 key，归并相关子域而区分无关站点；IP 目标保留原始地址。嵌套组展开为叶子节点，订阅成员变更会传播到 Smart，包括候选列表变空。

流量场景使用最近一次关闭连接识别，历史成功率、连接时间和延迟使用兼容 mihomo 的权重计算。峰值速率使用一秒采样窗口。TCP 计数可用时，累积丢包率为观测到的总重传包数除以总发送包数；不会通过平均各连接的丢包百分比计算累积丢包率。

目标维度至少积累两次观测后才使用学习权重。TCP 拨号先顺序尝试前三个候选，再将第四至第十个候选每批最多五个并发尝试；其余候选仍作为顺序降级节点。已绑定目标和 UDP 始终顺序尝试。

成功拨号将目标及传输协议绑定到该叶子节点十分钟。绑定到期，或节点被替换、移除、阻断后不再使用。每个拨号阶段都有超时预算，并遵守调用方的截止时间。UDP 套接字创建成功不代表远端可达。两秒内达到五十次失败观测后暂停失败学习并重置运行时阻断器，但不清除历史指标；成功或进入新窗口后恢复学习。

URL 测速只服务于冷启动和降级排序，不会修改目标业务指标。手动叶子测速与自动 Smart 探测结果独立保存；叶子测速不会触发 Smart 组探测，Smart 组测速使用自身配置的 `url`。不执行额外的连接结束后 HTTP 响应探测。

Clash API 会在组代理响应的 `smart` 对象中提供 Smart 状态，包含最后成功节点和最近一次排序快照。Smart 不提供手动选择。清除缓存同时清除目标绑定和 ASN 归属证据；归属证据平时随历史持久化。节点被阻断时，受影响目标的已有连接可能被关闭。

历史保存在配置基础目录下的 `smart-history.json`，各组使用独立快照。七天未使用的指标会过期，每组最多保留 50,000 个指标条目。仅读取当前历史格式；遇到旧版本或不支持的版本时记录警告并重新学习，新观测可能替换旧文件。不迁移先前 Smart 实现的配置字段或历史格式。

### 致谢

Smart 行为和 LightGBM 集成参考了 [vernesong/mihomo](https://github.com/vernesong/mihomo)。
