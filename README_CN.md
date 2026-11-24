# 工具模拟插件

[English](README.md)

用于 CLIProxyAPI 的原生工具模拟插件，通过文本协议为模型提供函数调用能力，
支持流式响应和稳定的提示前缀。

## 主要功能

- 匹配携带工具声明或工具历史的请求，路由到插件自己的 executor。
- 把函数定义、工具选择约束、历史工具调用及结果折叠为确定性的文本协议。
- 每次上游尝试均通过宿主 `host.model.execute`／`execute_stream` 执行，继续使用
  原生的鉴权、协议转换、thinking、payload 配置、网络传输和用量统计。
- 把文本工具调用恢复为 OpenAI Chat 原生工具调用；宿主再转换回 Claude、
  Responses 或 Gemini 客户端格式。工具的实际执行仍由客户端负责。
- 增量流式解析，支持标签跨分片、EOF 收尾、reasoning delta 和上游句柄清理。
- 非流式解析失败可重试；校验 `none`、`required`、指定工具、Gemini 允许名称子集、并行限制及工具声明。
  已输出部分内容的流式请求不会重启重试。
- 普通正文无需等待换行或 EOF 即可输出；协议标签及 Markdown 围栏前缀仍保留必要的判定缓冲。
  工具参数仍在工具块闭合后一次发出，不自动校验或修复结构化参数 JSON。

## 前缀缓存

默认 `prefix_cache: true`：固定工具文档／协议块放在首条用户消息的开头，
每轮工具选择约束和纠错消息放在动态部分。schema、标签和协议分隔符保持稳定；
Gemini 的工具协议也从易变 system 元数据移至首条用户消息。

Claude 原生请求使用 explicit cache 模式，插件管理两个断点：固定协议块和
最新文本块。原有 system／message 缓存标记会被替换，以保护固定断点，
避免超过宿主最多四个断点的预算；有 thread 保留槽时也不会超限。
本地 `prompt_cache_options` 由宿主在发送前移除。TTL 默认 `5m`，可配置 `1h`。

客户端缓存键、请求头和会话作用域继续传递，插件不会生成跨会话共用的合成 ID。
OpenAI 兼容提供商的缓存键支持仍由宿主 `support-prompt-cache-key` 控制。

**保证的是确定、可缓存的前缀及合法提示的传递，不是无条件的云端缓存命中。**
真实命中还取决于模型支持、最小长度、TTL、淘汰和缓存池／账号选择。更换工具、
schema、分隔符、标签、TTL 或前置提示会合理地改变前缀。后续轮次请继续携带工具
声明；未提供的工具声明不会从历史会话中推测补全。

用户 payload 配置仍是最终语义屏障：插件使用未配置的 `OriginalRequest`，
不重新使用已配置的 `Payload`；每个实际原生尝试在业务准备后只应用一次宿主规则。
用户有意过滤工具前缀或缓存标记时，插件不会在最终配置后恢复这些字段。

## 安装与配置

需要原生 ABI 1、RPC schema 10 或更高版本；SDK 依赖为 v8.0.60。
编译需要 Go 1.26.10+、CGO 和 GCC。发布目标为 Linux amd64／arm64、Windows amd64。

将对应共享库安装为 `plugins/tool-emulation.dll`（Windows）或
`plugins/tool-emulation.so`（Linux）。**安装后的文件名决定插件配置 ID**；
不要把带 `windows-amd64` 后缀的发布资产直接安装，却仍使用 `tool-emulation` 配置键。
宿主也支持 `tool-emulation-v0.1.0.dll` 这样的版本化安装名。

把 [示例配置子树](config.example.yaml) 合并到现有宿主配置：

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    tool-emulation:
      enabled: true
      priority: 10
      rules:
        - models: ["你的模型别名"]
      parse_retry: 1
      max_stream_buffer_bytes: 8388608
      on_parse_failure: parse_failed_to_content
      prefix_cache: true
      cache_ttl: "5m"
```

- `rules` 首个匹配规则生效；`models`／`model_aliases` 支持 `*`、`?`。
- 省略／清空 `rules`，或规则未填写模型／别名时，不接管任何请求。只有显式填写 `models: ['*']` 才匹配所有携带工具的请求；普通无工具请求走原生路径。
- 默认不指定 `provider`，由宿主按模型正常路由。
- 若指定 `provider`，必须用**原生提供商键**，不能用显示名。例如 `claude`、
  `codex`、`gemini`；名为 `gateway` 的 OpenAI 兼容配置组对应
  **`openai-compatible-gateway`**。
- `parse_retry` 为额外非流式解析次数，范围 0–8，默认 1。
- `max_stream_buffer_bytes` 默认 8388608（8 MiB），范围 1024–134217728。限制单个未完成工具调用的原始参数、元数据及当前行缓冲，或未完成协议／围栏行；超限立即终止上游并返回错误，不静默截断参数。普通正文会增量发送，不计入累计响应限制。
- 原始流式诊断只保留最多 64 KiB 的头尾内容，并标明省略的字节数；该诊断预算独立于原始协议缓冲上限，不限制客户端接收的正文长度。
- `on_parse_failure` 可用 `error` 或默认的 `parse_failed_to_content`；工具选择和
  未声明工具违规不会被降级为正文。
- `fence_token` 可指定固定 ASCII 分隔符，不应每轮随机生成。
- `tag_group` 可覆盖 `{tool, arg, result}` 标签名；应用默认值后三个标签必须互异。
- 配置验证成功才会原子替换旧快照；未知宿主管理元数据（如 `store`）会被忽略。

## 自定义配置页面

共享库内已打包 HTML／CSS／JavaScript 配置页面，无需额外运行前端服务或
单独安装前端依赖。支持插件资源菜单的管理前端会显示“工具模拟配置”入口；
也可在代理的地址下直接访问：

```text
/v0/resource/plugins/tool-emulation/config
```

页面通过宿主的插件资源接口提供，配置读写使用
`/v8/management/config/plugins/configs/tool-emulation`。

输入**管理密钥**（不是客户端 API key），点击“连接并加载”。页面支持启用状态／
优先级、规则新增／删除／排序、解析重试／失败策略、流式缓冲上限、缓存 TTL、协议高级设置及
待保存配置预览。保存时保留未显示的插件字段，并通过 ETag／If-Match 防止覆盖
其他页面的并发修改；重新加载前会提示确认丢弃未保存内容。

模型目录在连接后通过 v8 凭据接口自动获取已登记模型，可搜索并勾选写入模型／别名匹配。
内联 API key 提供商的模型可在“获取完整调用目录”中输入 CPA **客户端 API key**，
然后刷新模型；此目录来自同源 `/v1/models`，不是静态模型清单。模型查询失败不会
清空规则，也不会改变配置保存的 ETag。两种密钥都只在本页内存中使用。

页面资源公开可访问，但不嵌入实时配置或密钥；配置操作仍需正常管理鉴权。
密钥仅在本页内存中使用，断开连接时清除，不写入 URL 或浏览器持久存储。
宿主需启用管理访问。禁用插件并重载后，菜单／页面资源可能消失；已加载的页面
仍可通过宿主配置接口重新启用插件。

## 构建与测试

构建与测试命令见 [英文说明](<README.md#build-and-test>)。
测试覆盖工具调用解析、流式分片、四种客户端格式、工具选择约束、配置更新和缓存标记。
带 `integration` 标签的 HTTP 集成测试需要预先准备宿主可执行文件和插件库；
所需路径见 [集成测试配置](<main_integration_test.go>)。

## 使用限制

支持通过 HTTP Chat Completions／Responses／Messages／Gemini 接口发送函数工具请求。
不支持提供商原生托管工具、图片生成、WebSocket API 或 token count 接口。
模型本身仍须具有足够的文本协议遵循能力；工具的实际执行由客户端负责。

MIT 许可；保留上游代码的许可与归属，见 [LICENSE](LICENSE) 和
[第三方声明](NOTICE.md)。
