# Starlark in configuration comments

Core configuration paths integrated in this repository can execute marked Starlark block comments while parsing JSON and transform the value attached to each comment. This is not a separate configuration format; generated output proceeds through the core's normal decoding and validation.

## Syntax and placement

A block comment is a script when its first non-empty line is exactly `@starlark`, ignoring surrounding whitespace. Place it before the root value, immediately before an object key, or immediately before an array element. A comment in the value slot after an object key's colon can transform a scalar value; for an object- or array-valued field, put the comment before the key, since placing it after the colon can bind it to the first nested value.

Trailing comments after a value, markers not attached to a root/field/element, and multiple marked comments attached to one value are rejected. Ordinary comments, marker text in JSON strings, and `//` or `#` line comments are not scripts.

Editor-style `*` decoration is supported when every non-empty script-body line is prefixed with `*` (optionally preceded by indentation); the decoration and one following space or tab are removed:

```jsonc
/*
 * @starlark
 * value["log"]["level"] = "debug"
 * result = value
 */
{"log":{"level":"info"}}
```

A root comment receives the whole root value. An object-valued field can be transformed by placing its comment before the key:

```jsonc
{
  /* @starlark
  value["level"] = "debug"
  result = value
  */
  "log": {"level":"info"}
}
```

For a scalar field, a comment can appear between the colon and scalar value:

```jsonc
{"log":{"level": /* @starlark
result = "debug"
*/ "info"}}
```

## `value`, `result`, and `omit`

`value` is the attached JSON value after parsing, not the whole configuration as a serialized string. JSON objects become Starlark dictionaries, arrays become lists, strings and numbers remain scalar values, booleans become booleans, and JSON `null` becomes Starlark `None`. Scripts can mutate an attached dictionary or list before returning it. The language's normal expressions, conditionals, loops, and standard built-ins are available; there are no configuration-specific helper libraries or module loader.

Every script must assign an explicit top-level `result`. Results must be JSON-compatible: dictionaries with string keys, lists or tuples, strings, integers, finite floats, booleans, or `None`. Returning a string that contains JSON text produces a JSON string; it is not parsed again as a configuration object.

`omit` removes the attached object field or array element. It cannot omit the root. `None` is different: `result = None` retains the value and generates JSON `null`.

```jsonc
/* @starlark
result = None
*/ null
```

Children are transformed before their parent. A parent script sees the generated child values, while each script remains attached to its original source value. Omitting an array element does not change the original indices used to find sibling scripts.

## Target identity and supported core paths

Scripts receive read-only `host.os`, `host.arch`, and `host.client` values from the core processing the configuration. The OS and architecture describe that core's build target, not values provided by the configuration or a remote caller.

| Configuration processor | `host.client` | `host.os` | `host.arch` |
| --- | --- | --- | --- |
| CLI (`sing-box`, including CLI-attached services) | `cli` | CLI binary's `GOOS` (for example, `linux`, `windows`, or `darwin`) | CLI binary's `GOARCH` (for example, `amd64` or `arm64`) |
| Android libbox | `sfa` | `android` | libbox build's `GOARCH` |
| iOS libbox | `sfi` | `ios` | libbox build's `GOARCH` |
| macOS libbox | `sfm` | `darwin` | libbox build's `GOARCH` |
| tvOS libbox | `sft` | `tvos` | libbox build's `GOARCH` |
| Other libbox targets | `libbox` | libbox build's `GOOS` | libbox build's `GOARCH` |
| Desktop daemon | `desktop` | daemon binary's `GOOS` | daemon binary's `GOARCH` |

The following example keeps a localhost mixed inbound in CLI and Desktop configurations and omits it for other identities. The direct outbound and final route make it a complete minimal configuration; this is an example policy, not a claim that other targets cannot use a mixed inbound.

```jsonc
{
  "inbounds": [
    /*
     * @starlark
     * if host.client == "cli" or host.client == "desktop":
     *   result = value
     * else:
     *   result = omit
     */
    {
      "type": "mixed",
      "tag": "local",
      "listen": "127.0.0.1",
      "listen_port": 2080
    }
  ],
  "outbounds": [{"type":"direct","tag":"direct"}],
  "route": {"final":"direct"}
}
```

Only the CLI, libbox, and Desktop daemon core paths wired in this repository evaluate these comments. The core that receives configuration content determines the host identity: a remote caller cannot select it. An application must adopt a core with this integration; this change does not update installed official clients.

## CLI, validation, and reload behavior

The CLI's persistent `-c` flag selects a configuration file and may be repeated. Use `-C` for a configuration directory; `-D` selects the working directory, not a configuration directory.

```bash
sing-box check -c config.json
sing-box render -c config.json
sing-box merge merged.json -c config.json
sing-box run -c config.json
sing-box format -c ordinary.json
sing-box format -w -c ordinary.json
```

`check` and `render` construct and validate the current target's options, but do not start listeners or prove that runtime permissions, ports, or interfaces are available. `render` emits generated JSON only after this preflight succeeds. `merge` writes merged, generated JSON for the current CLI target; it is not reusable script source and does not replace `check` for full option validation. `format` refuses marked script sources, with or without `-w`, so it cannot silently remove their comments.

Local CLI configuration is processed in the CLI. Content received through an integrated libbox or Desktop daemon entry point is processed by that receiving core, using its host identity. CLI and daemon/libbox reload paths preflight candidates before replacing the active service; a rejected candidate is reported and the running instance is retained. A CLI check does not validate a different libbox or daemon target. Generated-config decode and validation errors include marked source positions as context without claiming which script caused the invalid value.

## Security, bounds, and limitations

Scripts have no configuration-script APIs for loading files, accessing the filesystem, environment or network, or starting external programs; `print` is disabled. These restrictions do not make arbitrary scripts safe from resource exhaustion.

For scripted source, the complete source is limited to 4 MiB, each script to 256 KiB, total Starlark execution to 1,000,000 steps per configuration, each script's JSON result and the final generated JSON to 4 MiB independently, and nesting to 128 levels. Output is measured in compact JSON-encoded bytes, not characters; converting the same subtree in child and parent scripts does not cumulatively consume this limit. An oversized child result is rejected even if a parent would later discard it. These limits are not a hard heap quota: one expression can create large intermediate strings or collections before the generated-output limit is checked and may exhaust process memory. Use trusted scripts and account for mobile and embedded target memory limits.
