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

URL 测速只服务于冷启动和降级排序，不会修改目标业务指标。手动叶子测速与自动 Smart 探测结果独立保存；叶子测速不会触发 Smart 组探测，Smart 组测速使用自身配置的 `url`。

成功的 HTTPS TCP 连接关闭且下载量小于 0.03 MiB 时，Smart 可经原叶子节点请求原目标主机的 `/robots.txt`；已有出口疑点也可触发响应复查。它独立于 URL 延迟测速，探测流量不作为业务观测或目标 ASN 归属证据。按 mihomo 规则区分挑战、限流、区域限制、重定向及错误；拒绝可对站点和已学习目标临时回避该节点，可达回答则解除回避。不跟随跨主机重定向。响应体最多读取 4 KiB、等候一秒，整次探测超时八秒。带 `with_utls` 的构建使用匹配的浏览器 TLS 指纹及请求头；其他构建使用原生 TLS 和相同请求头。

响应探测共享进程级每分钟 120 次、突发十次的启动预算，最多四个并发。每个目标每分钟最多启动六次；目标—节点组合通常间隔两分钟，近期拒绝后间隔三十秒。主机反复拒绝且从未有可达回答时，进入三十分钟盲区；目标整体失败也会暂停继续回避节点，避免源站故障耗尽候选池。临时回避每十五分钟复查，仍受同一预算限制。

Smart 还通过 `https://www.cloudflare.com/cdn-cgi/trace` 学习已使用节点的出口地域，并以 `https://api.ip.sb/geoip`、`https://ipwho.is/` 降级。这些额外请求也经该叶子节点发出，共用启动预算，另设两个并发槽。成功回答复用六小时，查询失败后间隔十五分钟。出口 ASN 查询与 ASN 疑点联动仅在 `prefer_asn` 开启时使用；地域联动不受此开关限制。同一站点至少有两个独立出口拒绝，且另一地域或 ASN 的出口提供成功对照，才会延后疑似地域或网络的其他节点；共享同一出口的多个节点只计一份证据。保留有租约限制的降级尝试，可达复查可解除疑点。不新增额外配置字段。

Clash API 会在组代理响应的 `smart` 对象中提供 Smart 状态，包含最后成功节点和最近一次排序快照。Smart 不提供手动选择。清除缓存会取消在途响应及出口探测，并清除目标绑定、响应回避、出口身份、出口疑点和目标 ASN 归属证据；目标 ASN 归属证据平时随历史持久化。节点被阻断时，受影响目标的已有连接可能被关闭。

历史保存在配置基础目录下的 `smart-history.json`，各组使用独立快照。七天未使用的指标会过期，每组最多保留 50,000 个指标条目。出口地域、可选出口 ASN、出口地址的哈希身份及查询重试时间随指标持久化，不保存原始出口 IP 或运行时疑点。仅读取当前历史格式；遇到旧版本或不支持的版本时记录警告并重新学习，新观测可能替换旧文件。不迁移先前 Smart 实现的配置字段或历史格式。

### 致谢

Smart 行为和 LightGBM 集成参考了 [vernesong/mihomo](https://github.com/vernesong/mihomo)。
