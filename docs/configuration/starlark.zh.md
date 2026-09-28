# 配置注释中的 Starlark

本仓库中已接入的核心配置路径会在解析 JSON 时执行带标记的 Starlark 块注释，并变换注释所附着的值。这不是另一种配置格式；生成结果仍由核心按原有流程解码和校验。

## 语法和位置

块注释的首个非空行必须恰好是 `@starlark`（忽略首尾空白）。注释可放在根 JSON 值之前、对象键之前或数组元素之前。放在键之前时，脚本处理该字段的完整值。对于标量字段，也可将注释放在冒号与值之间；若字段值是对象或数组，应将注释放在键之前，以免注释绑定到第一个嵌套值。

值之后的尾随注释、未附着到根值/字段/数组元素的标记，以及附着到同一值的多个标记都会报错。普通注释、JSON 字符串中的标记文本以及 `//` 或 `#` 行注释都不会被当作脚本。

支持编辑器生成的逐行 `*` 装饰：每个非空正文行都必须以 `*` 开头（前面可有缩进）；解析器会移除装饰符和其后的一个空格或制表符：

```jsonc
/*
 * @starlark
 * value["log"]["level"] = "debug"
 * result = value
 */
{"log":{"level":"info"}}
```

根注释接收整份配置。对象字段注释应放在键之前：

```jsonc
{
  /* @starlark
  value["level"] = "debug"
  result = value
  */
  "log": {"level":"info"}
}
```

标量字段也可在冒号后使用注释：

```jsonc
{"log":{"level": /* @starlark
result = "debug"
*/ "info"}}
```

## `value`、`result` 和 `omit`

`value` 是解析后的附着 JSON 值，而非整份配置的字符串。对象和数组分别变成 Starlark 字典与列表；字符串、数字、布尔值保持相应标量，JSON `null` 变为 Starlark `None`。脚本可修改附着的字典或列表后返回。Starlark 的常规表达式、条件、循环和标准内置函数均可使用；没有配置专用辅助库，也不提供模块加载器。

每段脚本都必须显式赋值给顶层变量 `result`。结果须可表示为 JSON：键为字符串的字典、列表或元组、字符串、整数、有限浮点数、布尔值或 `None`。即使字符串内容看起来像 JSON，也只会输出 JSON 字符串，不会再解析为配置。

`omit` 会删除附着的对象字段或数组元素，但不能删除根值。`result = None` 则保留该值并生成 JSON `null`：

```jsonc
/* @starlark
result = None
*/ null
```

子值先于父值处理；父脚本看到已生成的子值，但每段脚本仍附着于原始源码值。省略数组元素不会改变查找兄弟脚本时使用的原始索引。

## 目标身份和核心接入范围

脚本可读取核心提供的只读 `host.os`、`host.arch` 和 `host.client`。系统与架构描述处理配置的核心构建目标，不取自配置，也不能由远程调用方指定。

| 配置处理器 | `host.client` | `host.os` | `host.arch` |
| --- | --- | --- | --- |
| CLI（`sing-box`，含 CLI 附属服务） | `cli` | CLI 二进制的 `GOOS`，如 `linux`、`windows`、`darwin` | CLI 二进制的 `GOARCH`，如 `amd64`、`arm64` |
| Android libbox | `sfa` | `android` | libbox 构建的 `GOARCH` |
| iOS libbox | `sfi` | `ios` | libbox 构建的 `GOARCH` |
| macOS libbox | `sfm` | `darwin` | libbox 构建的 `GOARCH` |
| tvOS libbox | `sft` | `tvos` | libbox 构建的 `GOARCH` |
| 其他 libbox 目标 | `libbox` | libbox 构建的 `GOOS` | libbox 构建的 `GOARCH` |
| Desktop daemon | `desktop` | daemon 二进制的 `GOOS` | daemon 二进制的 `GOARCH` |

下面的完整最小配置仅在 CLI 或 Desktop 中保留本机 mixed 入站，在其他身份下省略它。此处展示的是作者选择的策略，并不表示其他目标不能使用 mixed 入站。

```jsonc
{
  "inbounds": [
    /* @starlark
    if host.client == "cli" or host.client == "desktop":
      result = value
    else:
      result = omit
    */
    {"type":"mixed","tag":"local","listen":"127.0.0.1","listen_port":2080}
  ],
  "outbounds": [{"type":"direct","tag":"direct"}],
  "route": {"final":"direct"}
}
```

只有本仓库中已接入的 CLI、libbox 和 Desktop daemon 核心路径会执行这些注释；由实际接收配置的核心决定目标身份。远程调用方不能选择身份。应用必须采用包含此功能的核心；本次改动不会更新已安装的官方客户端。

## CLI、校验和重载

CLI 的持久 `-c` 标志指定配置文件，可以重复使用；`-C` 指定配置目录。`-D` 指定工作目录，不是配置目录。

```bash
sing-box check -c config.json
sing-box render -c config.json
sing-box merge merged.json -c config.json
sing-box run -c config.json
sing-box format -c ordinary.json
sing-box format -w -c ordinary.json
```

`check` 和 `render` 会构造并校验当前目标的选项，但不会启动监听，也不能证明运行时权限、端口或接口可用。`render` 只有预检成功后才输出生成的 JSON。`merge` 为当前 CLI 目标输出合并后的生成 JSON；该结果不是可复用的脚本源，也不能替代 `check` 完成完整选项校验。对于含标记脚本的源文件，`format` 无论是否使用 `-w` 都会拒绝，以免静默丢弃脚本。

本地 CLI 配置由 CLI 处理；通过已接入的 libbox 或 Desktop daemon 入口收到的配置由接收端核心处理，并使用其自身目标身份。CLI 与 daemon/libbox 重载会在替换活动服务前预检候选配置；候选被拒绝时会报告错误并保留运行中的实例。脚本生成配置的解码和校验错误会附带标记源码位置，但不会声称具体是哪一段脚本造成了无效值。CLI 检查不能代替对其他 libbox 或 daemon 目标的检查。

## 安全、限制和资源上限

脚本没有用于读取文件、访问文件系统、环境或网络、启动外部程序的配置脚本 API；`print` 也被禁用。这些限制不能防止恶意或失控脚本耗尽资源。

含脚本的源码总大小上限为 4 MiB，每段脚本上限为 256 KiB，每份配置的 Starlark 总执行上限为 1,000,000 步，生成 JSON 上限为 4 MiB，嵌套深度上限为 128 层。这些上限不是硬性堆内存配额：单个表达式可能在检查生成结果大小前创建很大的中间字符串或集合，并耗尽进程内存。仅使用可信脚本，并考虑移动设备和嵌入式目标的内存限制。
