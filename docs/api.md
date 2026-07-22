# API 文档（docs-ai-reverse）

`docs-ai-reverse`（模块名：`claude-code-chat`）通过 CLIProxyAPI SDK 暴露 OpenAI-compatible gateway。客户端只需要面向网关使用 OpenAI Chat Completions 或 Responses API 格式，不需要直接理解 Mintlify、Inkeep、Stripe 或 ReadMe 的上游协议。

默认本地地址：

```text
http://127.0.0.1:8317
```

## Endpoint 总览

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| `POST` | `/v1/chat/completions` | OpenAI Chat Completions 格式的对话请求，支持非流式和 `stream: true`。 |
| `POST` | `/v1/responses` | OpenAI Responses API 格式的请求，支持 `input` 和响应对象格式。 |
| `GET` | `/v1/models` | 列出当前网关注册的模型。 |

所有 endpoint 都由 CLIProxyAPI SDK 处理入口鉴权和模型路由。

## 鉴权

请求必须携带 `config.yaml` 中 `api-keys` 之一作为 Bearer token：

```http
Authorization: Bearer <gateway-api-key>
```

示例使用环境变量 `${GATEWAY_API_KEY}`，避免把 token 写进命令或文档：

```bash
export GATEWAY_API_KEY='替换为本地网关 token'
```

`api-keys` 是网关唯一的 endpoint 鉴权机制。`auth-dir` 中保存的是 runtime-only auth 及其令牌，两者用途不同。

## 模型列表

网关启动时固定注册以下四个模型。`model` 字段是网关路由名，不是上游服务要求的模型 ID。

| 模型 ID | Provider | 上游服务 | 用途与行为 |
| --- | --- | --- | --- |
| `claude-docs` | Mintlify | `code.claude.com` | Claude Code 文档助手。使用 Mintlify 私有请求和自定义流式协议。 |
| `anthropic-docs` | Inkeep | `platform.claude.com` | Anthropic/Claude 平台文档助手。上游接近 OpenAI Chat Completions，并使用 challenge 认证。 |
| `stripe-docs` | Stripe | `ai.stripe.com` | Stripe 文档 AI。将最后一条用户问题提交给 Stripe，并通过轮询取得答案。 |
| `readme-docs` | ReadMe | `docs.readme.com` | ReadMe Ask AI。请求会根据 ReadMe 文档站点子域名访问 `/ask`。 |

模型注册在服务启动 hook 中完成。如果 `/v1/models` 没有预期模型，应先检查网关启动日志和 provider 注册，而不是直接排查上游请求格式。

## Chat Completions

### 非流式请求

```bash
curl http://127.0.0.1:8317/v1/chat/completions \
  -H "Authorization: Bearer ${GATEWAY_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-docs",
    "messages": [
      {"role": "user", "content": "如何配置这个文档项目？"}
    ],
    "stream": false
  }'
```

省略 `stream` 时使用非流式路径；为了明确客户端行为，建议显式传入 `false`。

### 流式请求

```bash
curl -N http://127.0.0.1:8317/v1/chat/completions \
  -H "Authorization: Bearer ${GATEWAY_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "anthropic-docs",
    "messages": [
      {"role": "user", "content": "请介绍认证流程。"}
    ],
    "stream": true
  }'
```

网关向客户端输出 OpenAI 风格的 SSE chunk，通常以 `data: ` 行传输，并以 `data: [DONE]` 表示 Chat Completions 流结束。上游的自定义行协议、轮询结果或完整 Markdown 不会直接暴露给客户端。

### Chat 请求骨架

```json
{
  "model": "claude-docs",
  "messages": [
    {"role": "system", "content": "你是文档助手。"},
    {"role": "user", "content": "请解释配置方法。"}
  ],
  "stream": false
}
```

### 非流式响应骨架

```json
{
  "id": "chatcmpl-...",
  "object": "chat.completion",
  "created": 0,
  "model": "claude-docs",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "文档说明……"
      },
      "finish_reason": "stop"
    }
  ],
  "usage": {
    "prompt_tokens": 0,
    "completion_tokens": 0,
    "total_tokens": 0
  }
}
```

### 流式 chunk 骨架

```text
data: {"id":"chatcmpl-...","object":"chat.completion.chunk","model":"anthropic-docs","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}

data: {"id":"chatcmpl-...","object":"chat.completion.chunk","model":"anthropic-docs","choices":[{"index":0,"delta":{"content":"认证"},"finish_reason":null}]}

data: {"id":"chatcmpl-...","object":"chat.completion.chunk","model":"anthropic-docs","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]
```

实际 chunk 的字段数量会随 provider、工具调用和上游 usage 信息变化。客户端应按 SSE 和 OpenAI chunk 语义解析，不要依赖某一个固定 chunk 数量。

## Responses API

### 请求示例

```bash
curl http://127.0.0.1:8317/v1/responses \
  -H "Authorization: Bearer ${GATEWAY_API_KEY}" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "claude-docs",
    "input": "请总结这个文档项目的认证方式。",
    "stream": false
  }'
```

也可以将 `model` 替换为 `anthropic-docs`、`stripe-docs` 或 `readme-docs`。Responses 请求会在 provider 边界转换；尤其 Mintlify 有专门的 OpenAI Responses → Mintlify translator。

### Responses 请求骨架

```json
{
  "model": "claude-docs",
  "input": "请说明如何配置代理。",
  "stream": false
}
```

`input` 可以使用简单字符串；需要多轮或工具语义时，应按照客户端所使用的 OpenAI Responses 结构传入对应的 input items。

### Responses 响应骨架

```json
{
  "id": "resp_...",
  "object": "response",
  "created_at": 0,
  "status": "completed",
  "model": "claude-docs",
  "output": [
    {
      "id": "msg_...",
      "type": "message",
      "status": "completed",
      "role": "assistant",
      "content": [
        {"type": "output_text", "text": "文档说明……"}
      ]
    }
  ],
  "usage": {
    "input_tokens": 0,
    "output_tokens": 0,
    "total_tokens": 0
  }
}
```

当使用 Responses 的 `stream: true` 时，客户端应按照 Responses SSE event 类型解析，例如 `response.created`、输出文本增量和 `response.completed`。不同 provider 对 Responses 流的内部实现并不相同，详见下文的流式差异。

## 模型行为差异

### Mintlify：`claude-docs`

- 上游是 Mintlify 文档助手，默认站点为 `code.claude.com`。
- 网关使用 JWT、Cloudflare cookies 和带 Chrome TLS fingerprint 的客户端；这些细节不会出现在公共 API 请求中。
- 上游使用自定义 SSE-like `type:value` 行，而不是标准 `data: JSON` SSE；网关 translator 将其转换成 OpenAI 响应。
- 工具调用使用 prompt-tool 文本协议：工具描述和调用可能被编码在文本/XML 标记中，而不是直接作为标准 OpenAI `tool_calls` 传输。
- Mintlify 的服务端工具调用类型 `9` 和 `a` 会被静默丢弃，不会转发给客户端执行。
- 多轮请求依赖上游会话状态；网关会维护 thread ID、thread key 等状态。

### Inkeep：`anthropic-docs`

- 上游是 Anthropic/Claude 平台文档的 Inkeep 助手，默认来源为 `platform.claude.com`。
- 请求前需要完成 Inkeep 的 SHA-256 challenge，并按 auth 属性使用 API key、Origin 和 Referer。
- 上游请求和响应接近 OpenAI Chat Completions；网关主要补齐消息 ID、model 字段和必要的 `provideLinks` 工具。
- Chat Completions 的上游流是标准 SSE。Responses 模式需要把 Responses 输入转换成 Inkeep chat-shaped 请求，输出再转换回 Responses 结构。

### Stripe：`stripe-docs`

- 上游是 Stripe 文档 AI，默认地址为 `ai.stripe.com`。
- 适配器从请求中提取最后一条用户问题，而不是把完整消息历史原样提交给上游。
- 上游流程是创建 thread 后轮询答案，轮询间隔约 500ms；客户端看到的流不是 Stripe 原生 SSE。
- 对不可回答的问题，上游/适配器会返回默认消息，而不是一定返回 HTTP 错误。

### ReadMe：`readme-docs`

- 上游是 ReadMe Ask AI，默认文档地址为 `https://docs.readme.com`，实际请求会访问该站点的 `/ask` 接口。
- 适配器提取最后一条用户问题，并通过抓取 HTML 和正则检测 ReadMe subdomain；站点配置不正确时可能导致请求发往错误文档站点。
- 上游返回完整 Markdown，不提供原生增量流。
- Chat 的 `stream: true` 由网关把完整结果包装成 role、content、stop 等兼容 chunk，因此它不代表上游响应时间变短。

## 流式行为对比

| Provider | 网关对外行为 | 上游实现 | 客户端注意事项 |
| --- | --- | --- | --- |
| Mintlify | 真流式，转换为 OpenAI Chat/Responses SSE | 自定义 `type:value` 行 | 不要直接连接 Mintlify；类型 `9`/`a` 服务端工具调用被静默丢弃。 |
| Inkeep | Chat 路径是真流式 | 标准 SSE | Responses 路径会把 Responses 输入转为 chat-shaped 请求；某些 Responses 流结果可能在转换后聚合。 |
| Stripe | 模拟流式 | 500ms 轮询结果通过 channel 产生 | 首字节受 thread 创建和轮询影响；Responses 流可能在收集结果后输出完整 Responses payload。 |
| ReadMe | 伪流式 | 非流式 `/ask` 返回完整 Markdown | `stream: true` 主要是客户端兼容层，不提供上游增量延迟优势。 |

## 错误行为

### 网关鉴权错误

没有有效 Bearer token 时，CLIProxyAPI SDK 返回 `401 Unauthorized`。请检查：

1. `Authorization` header 是否存在；
2. 是否使用 `Bearer <token>` 形式；
3. token 是否与运行中的 `config.yaml` 的 `api-keys` 完全一致。

### Provider 错误

上游非成功响应和 provider 内部认证、代理、challenge 或解析失败会被映射为 provider 相关的错误文本，由网关返回给客户端。流式请求在已经输出部分内容后发生错误时，客户端应把流视为未完整结束，不要把部分文本当成可靠的完整答案。

Mintlify 的常见状态含义如下：

| 状态码 | 含义 | 典型处理 |
| ---: | --- | --- |
| `402` | payment、订阅或额度问题 | 检查上游账户状态；不是网关 Bearer token 错误。 |
| `418` | VPN、数据中心或出口来源被阻断 | 检查代理和出口；适配器在部分代理场景会尝试直连 fallback。 |
| `419` | Mintlify token 过期 | 令牌会被失效并在后续请求中惰性刷新。 |
| `420` | Cloudflare bot 检测 | 检查 TLS fingerprint、cookies、代理出口并重新尝试。 |

其他 provider 的 challenge 失败、上游不可用、空用户问题或轮询超时会以各自 provider 的错误路径返回。Stripe 对未回答问题有默认消息，这是业务结果，不应简单等同于 HTTP 401。

## 工具与协议注意事项

公共接口尽量保持 OpenAI 语义，但 Mintlify 的 prompt-tool 协议属于适配器内部协议。不要假设所有 provider 都支持完全相同的原生 OpenAI tool-call streaming；如需工具调用，请同时测试非流式和流式路径，并处理 provider-specific 的 finish reason、output item 或默认消息。
