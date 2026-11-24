# Tool Emulation Plugin

[简体中文](README_CN.md)

A native CLIProxyAPI plugin that emulates function calling through a textual tool
protocol, with streaming responses and cache-friendly prompt prefixes.

## What it does

- Routes matching tool-bearing requests to its own executor.
- Folds function declarations, tool choice, assistant calls, and tool results into
  a deterministic textual protocol. Non-tool messages and multimodal parts are
  preserved by the request folders.
- Delegates every upstream attempt to `host.model.execute` / `execute_stream`.
  Native credential selection, provider translation, thinking, user payload
  configuration, transport, and usage accounting stay with the host.
- Parses textual calls back into canonical OpenAI Chat tool calls; the host
  converts these into the original client's Claude, Responses, or Gemini format.
- Supports incremental streaming, split protocol tags, EOF flushing, reasoning
  deltas, and explicit upstream stream-handle cleanup.
- Retries malformed non-streaming responses without changing the fixed tool
  prefix. Enforces `none`, `required`, named choice, parallel limits, and declared
  names. It never executes tools itself: the client still owns tool execution.

## Compatibility

- Public SDK: `github.com/router-for-me/CLIProxyAPI/v8 v8.0.60`.
- Native ABI: **1**; RPC schema: **10 or newer**.
- Build environment: Go **1.26.10+**, CGO and a native GCC toolchain.
- Declared client inputs: `openai`, `openai-response`, `claude`, `gemini`.
- Executor output: `openai`. The host translates responses into the client's format.
- Request normalizer: Claude cache annotation after native protocol translation.
- Release targets: Linux amd64/arm64 and Windows amd64.

Supported requests use HTTP Chat Completions, Responses, Messages, or Gemini
endpoints with function tools. Provider-hosted tools, image generation, WebSocket
APIs, and token-count endpoints are not supported. Reliable tool calls depend on
the model's ability to follow the textual protocol.

## Prefix caching

`prefix_cache: true` (default) isolates a fixed tool-document/protocol block at
the beginning of the first user message. The tool schema, tag names, and delimiter
are deterministic. Turn-specific choice constraints and parse corrections are
outside that fixed block. Gemini protocol injection is moved out of mutable
system metadata into the first user message.

For native Claude requests, the normalizer sets explicit cache mode and owns two
breakpoints: the stable protocol block and the latest text block. Existing
system/message cache breakpoints are replaced to keep the stable block within
the host's four-breakpoint budget, including requests with a reserved thread
slot. The host strips the local `prompt_cache_options` setting before transport.
TTL is `5m` by default; `1h` must be supported by the upstream.

Caller cache keys, headers, and continuity scope are forwarded; this plugin does
not synthesize shared conversation IDs. OpenAI-compatible cache-key support is
still controlled by the host provider's `support-prompt-cache-key` setting.
There is no additional cache parameter injected into unsupported backends.

**This guarantees a deterministic cacheable prefix and legal hint propagation,
not unconditional upstream cache hits.** Minimum prompt length, model support,
TTL, eviction, and account/cache pool selection remain upstream concerns. Changes
to tools, schemas, tag names, delimiter, earlier prompts, or TTL naturally change
the prefix. Keep tool declarations on subsequent turns; absent declarations are
not inferred from conversation history.

User payload rules remain authoritative. The plugin starts from unconfigured
`OriginalRequest`, not the already finalized executor `Payload`; each nested
native attempt applies host payload configuration once after business
preparation. A user filter can intentionally remove the tool prefix or cache
markers; the plugin does not restore filtered fields after that barrier.

## Installation

Build or download the matching library, then install it as:

- Windows: `plugins/tool-emulation.dll`
- Linux: `plugins/tool-emulation.so`

The **installed basename** determines the config ID. Do not install the release
asset as `tool-emulation-windows-amd64.dll` while using `tool-emulation` as the
config ID. A versioned installed name such as `tool-emulation-v0.1.0.dll` is also
recognized by the host discovery rules.

Merge [the example subtree](config.example.yaml) into your host configuration:

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    tool-emulation:
      enabled: true
      priority: 10
      rules:
        - models: ["your-model-alias"]
      parse_retry: 1
      on_parse_failure: parse_failed_to_content
      prefix_cache: true
      cache_ttl: "5m"
```

An empty or omitted `rules` array intercepts no requests. Rules without an explicit
model or alias pattern also match nothing. Use `models: ['*']` only when intentionally
enabling all tool-bearing requests. No-tool requests continue on native routing.
Rules are ordered and first-match wins. `models` / `model_aliases` accept `*` and
`?`. `provider` is optional: omit it for ordinary native model routing. If set,
it must be an **exact native provider key**, not a display label. Built-ins use
keys such as `claude`, `codex`, or `gemini`; a compatibility group named `gateway`
uses **`openai-compatible-gateway`**.

Additional options:

| Option | Default | Meaning |
| --- | --- | --- |
| `parse_retry` | `1` | Additional non-streaming parse attempts, range 0–8 |
| `on_parse_failure` | `parse_failed_to_content` | `error` returns failure after syntax retry exhaustion; tool-choice/declaration violations always fail |
| `prefix_cache` | `true` | Fixed protocol block and explicit Claude breakpoints |
| `cache_ttl` | `5m` | `5m` or `1h`; upstream support required |
| `fence_token` | fixed built-in delimiter | Optional 4–32 character ASCII delimiter; never generate it per request |
| `tag_group` | CPA_TC / CPA_TA / CPA_TR | Optional `{tool, arg, result}` protocol tag names |

Configuration snapshots are immutable and swapped only after successful
validation. Unknown host-owned metadata such as `store` is ignored. Streaming
responses are not restarted after emitting partial output; retry configuration
applies only to non-streaming parsing.

## Custom configuration page

The plugin embeds its configuration page in the shared library and registers a
**Tool Emulation configuration** menu entry. No separate frontend service or
frontend dependency installation is required.

Open the menu in a management frontend that supports plugin resource menus, or
navigate directly on the proxy's origin:

```text
/v0/resource/plugins/tool-emulation/config
```

The page is served through the host's plugin resource interface. Configuration
reads and writes use `/v8/management/config/plugins/configs/tool-emulation`.

Enter the **management key**, not a client API key, and choose **Connect and load**.
The page includes enable/priority controls, reorderable model-rule editing, parse
retry/fallback choices, cache TTL controls, advanced delimiter/tag settings, and
a pending-configuration preview. Saving preserves plugin fields not shown in the
form and uses the host ETag/If-Match mechanism to avoid overwriting concurrent
edits. Reloading prompts before discarding unsaved changes.

The rule editor automatically loads registered credential models through the v8
management interfaces and provides searchable model/alias checkboxes. For inline
API-key providers, enter a CPA **client API key** under the optional complete
catalog section and refresh: this queries the same-origin `/v1/models` catalog.
Neither key is persisted or read from host configuration. Catalog errors preserve
existing rules and never replace the configuration's ETag.

The page resource is public, but never embeds live configuration or credentials.
Its configuration operations require normal management authentication. The key
is kept only in page memory and cleared on disconnect; it is not written into
URLs or persistent browser storage. The host must have management access enabled.
Disabling the plugin may remove its menu/resource routes after configuration
reload; the already loaded page can still use the host's configuration interface.

## Build and test

```bash
gofmt -w .
go test -count=1 ./...
go vet ./...
CGO_ENABLED=1 go test -race -count=1 ./...

# Linux (native compiler for each architecture)
CGO_ENABLED=1 go build -trimpath -buildmode=c-shared \
  -ldflags="-s -w -X main.pluginVersion=dev" \
  -o dist/tool-emulation-linux-amd64.so .

# Windows, in Git Bash with MinGW-w64 GCC on PATH
CGO_ENABLED=1 go build -trimpath -buildmode=c-shared \
  -ldflags="-s -w -X main.pluginVersion=dev" \
  -o dist/tool-emulation-windows-amd64.dll .
```

Tests cover tool-call parsing, stream fragmentation, supported client formats,
retry behavior, tool-choice validation, configuration updates, and cache markers.

Additional HTTP integration tests are available with the `integration` build tag.
They require a prepared host executable and plugin libraries; fixture locations
are defined in [the integration test setup](<main_integration_test.go>).

The release workflow embeds the Git tag as the plugin version and publishes
shared libraries for Linux amd64/arm64 and Windows amd64.

## License

MIT. See [LICENSE](LICENSE) and [third-party notices](NOTICE.md).
