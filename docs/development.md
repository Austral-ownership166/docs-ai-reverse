# 开发指南（docs-ai-reverse）

`docs-ai-reverse`（模块名：`claude-code-chat`）是 Go 1.26.0 项目。CLIProxyAPI SDK 负责网关生命周期、Bearer 鉴权、模型路由和公共格式转换；本项目负责四个文档 provider 的客户端、执行器、认证状态、代理适配和流式转换。

## 本地开发工作流

### 1. 准备环境

确认 Go 版本并下载依赖：

```bash
go version
go mod download
```

项目要求 Go `1.26.0`，模块名为 `claude-code-chat`。

### 2. 创建本地配置

不要直接修改或提交示例中的敏感值：

```bash
cp config.example.yaml config.yaml
mkdir -p auths
```

在 `config.yaml` 中设置本地网关 Bearer token。`api-keys` 只用于网关入口鉴权；provider 的 runtime-only auth 由 CLIProxyAPI auth 管理流程保存到 `auth-dir`。确认 `config.yaml` 和 `auths/` 不会进入 Git。

### 3. 运行网关

指定配置文件运行：

```bash
go run . config.yaml
```

如果配置文件位于当前目录，也可以省略参数：

```bash
go run .
```

构建并运行二进制：

```bash
go build -o docs-ai-reverse .
./docs-ai-reverse config.yaml
```

启动后检查：

```bash
curl -H "Authorization: Bearer ${GATEWAY_API_KEY}" \
  http://127.0.0.1:8317/v1/models
```

### 4. 测试

运行全部单元测试：

```bash
go test ./...
```

开发期间建议先运行受影响 package 的测试，再运行完整测试套件。需要构建检查时可以使用：

```bash
go build ./...
```

### 5. Mintlify 连通性探针

`cmd/probe` 只测试 Mintlify 直连链路，不启动 gateway。它会检查 cookie 加载、token 获取和一次消息发送：

```bash
go run ./cmd/probe
```

需要代理时：

```bash
MINTLIFY_PROXY=socks5://127.0.0.1:1080 go run ./cmd/probe
```

探针输出可能包含令牌长度和响应预览；不要把完整 token、cookie 或响应日志粘贴到公开 issue。

## 增加新的文档 provider

下面的清单适用于新增一个上游文档/知识库服务。假设 provider 名称为 `<name>`，对外模型名为 `<name>-docs`。

### 步骤 1：实现上游客户端

创建：

```text
internal/<name>/client.go
```

客户端负责：

- 上游 URL、HTTP/TLS 客户端和请求超时；
- 上游认证、challenge、cookie 或会话初始化；
- 请求、完整响应和上游流的解析；
- 明确的非 2xx 错误处理；
- 使用 context 支持取消；
- 如有需要，提供刷新 token、cookie 或 challenge 的方法。

不要把 provider 私有协议泄漏到公共 API 层。代理应通过公共的代理解析逻辑接入，只有确实需要特殊 TLS 指纹或 cookie 行为时才保留 provider-specific 实现。

### 步骤 2：实现执行器

创建：

```text
internal/provider/<name>_executor.go
```

定义一个执行器类型，嵌入或复用 `proxyDefaults`，并实现 `clipexec.ProviderExecutor` 的全部方法：

- `Identifier`
- `RequestToFormat`
- `Execute`
- `ExecuteStream`
- `CountTokens`
- `HttpRequest`
- `Refresh`

执行器是一个 provider 的完整适配闭环，应该协调请求格式、auth、代理、上游调用和响应格式。非流式和流式最终文本应保持语义一致；如果上游不支持真流式，要明确记录为模拟/伪流式，而不是伪装成原生 SSE。

### 步骤 3：选择公共格式或注册 translator

如果上游接近 OpenAI Chat Completions，可让 `RequestToFormat` 返回 `FormatOpenAI`，在执行器中做少量字段修补。这类 provider 不一定需要新的 SDK translator format。

如果上游使用私有协议，则在 `internal/provider/translator.go` 或对应的 provider translator 文件中注册新的 format。推荐在 `init()` 中调用 SDK translator 注册函数：

```go
func init() {
    sdktranslator.Register(
        sdktranslator.FormatOpenAI,
        formatProvider,
        convertOpenAIRequestToProvider,
        sdktranslator.ResponseTransform{
            Stream:     convertProviderStreamToOpenAI,
            NonStream:  convertProviderNonStreamToOpenAI,
            TokenCount: buildProviderUsage,
        },
    )
}
```

如果还要支持 Responses API，需要额外注册 `FormatOpenAIResponse -> formatProvider`，并提供 Responses 请求、流式响应和非流式响应转换：

```go
sdktranslator.Register(
    sdktranslator.FormatOpenAIResponse,
    formatProvider,
    convertOpenAIResponseRequestToProvider,
    sdktranslator.ResponseTransform{
        Stream:     convertProviderStreamToOpenAIResponse,
        NonStream:  convertProviderNonStreamToOpenAIResponse,
        TokenCount: buildProviderUsage,
    },
)
```

translator 的职责边界是：

- request transform：把 OpenAI Chat/Responses 请求变成上游请求；
- stream transform：把每个上游事件转换为公共流 chunk；
- non-stream transform：把完整上游响应转换为公共响应；
- token count transform：输出 SDK 需要的 usage/countTokens 形状。

上游 EOF、sentinel、`[DONE]`、channel close 或完整响应结束必须映射到明确的公共结束语义。对于有缓冲的工具文本，需在结束时 flush，而不是让最后一段内容丢失。

### 步骤 4：在 `main.go` 创建并注册执行器

在 `providers` 结构体中加入新执行器，在初始化处调用构造函数，并在 `setDefaultProxy` 中设置默认代理。随后在 `registerAllProviders` 中注册：

```go
core.RegisterExecutor(p.newProvider)
```

为 provider 注册 auth 和模型信息：

```go
registerAuth(
    core,
    "provider-default",
    "provider",
    "Provider Docs AI",
    attrs,
    proxyURL,
    []modelDef{{
        ID:          "provider-docs",
        DisplayName: "Provider Docs",
        Type:        "provider",
        Description: "Provider docs assistant",
    }},
)
```

`registerAuth` 会把 auth 标记为 `runtime_only`，把模型信息写入 CLIProxyAPI 的模型注册表，并建立模型到执行器的路由。模型 ID、provider `Identifier()` 和注册的 provider 名称必须完全一致。

### 步骤 5：定义 auth 属性和刷新策略

列出上游运行所需的最小属性，例如：

- Mintlify 类站点：`subdomain`、`site_origin`、`docs_path`、`language`；
- 需要来源头的服务：`origin`、`referer`；
- 多站点服务：`docs_url` 或类似站点地址。

不要把 provider 的密钥误放进网关 `api-keys`。认证令牌应进入 runtime-only auth；`Refresh` 负责刷新或返回适合该 provider 的 auth 状态。无独立刷新流程的 provider 可以返回原 auth，但要明确 token/cookie/challenge 是否在请求中惰性刷新。

### 步骤 6：实现流式和错误边界

至少覆盖以下路径：

- `stream: false` 的完整响应；
- `stream: true` 的增量响应或明确的模拟流；
- context 取消、上游 EOF 和 scanner/channel 错误；
- 上游非 2xx 状态；
- 可恢复的认证过期、challenge 失败和代理错误；
- 不能安全重放的请求，例如重复创建 thread 或重复提交问题。

不要对所有非 2xx 无条件重放。只有 provider 明确知道恢复动作时才重试，并确保重试不会重复产生副作用。

### 步骤 7：实现本地 token 计数

实现 `CountTokens`，通常复用 `countTokensResponse()` 和项目统一的 `o200k_base` 本地估算逻辑。对外的模型能力声明和 `ContextLength` 需要与 `registerAuth` 中的 `modelDef`/模型信息保持一致。上游没有 count API 时，文档中应明确这是估算值，而不是上游计费的精确值。

### 步骤 8：补充测试和文档

新增至少包括：

- 请求转换和响应转换的单元测试；
- 非流式和流式 fixture；
- 认证 challenge/token 解析测试；
- 错误状态和取消测试；
- `CountTokens` 测试；
- `/v1/models` 中模型注册的手工验证。

完成后运行 `go test ./...`、`go build ./...`，并在配置了对应 runtime auth 的环境中进行一次真实连通性测试。

## Executor 接口方法

所有 provider executor 都必须实现以下方法。方法签名中的 `clipexec`、`coreauth` 和 `sdktranslator` 分别来自 CLIProxyAPI SDK：

| 方法 | 作用 |
| --- | --- |
| `Identifier()` | 返回稳定的 provider 标识，例如 `mintlify`、`inkeep`。SDK 用它识别和路由执行器。 |
| `RequestToFormat(req, opts)` | 告诉 SDK 当前 provider 接受哪一种上游 request format。OpenAI-compatible 上游通常返回 `FormatOpenAI`；私有协议返回自定义 format。 |
| `Execute(ctx, auth, req, opts)` | 执行一次非流式请求，完成翻译、认证、代理、上游调用和完整响应转换。 |
| `ExecuteStream(ctx, auth, req, opts)` | 执行一次流式请求，返回 `StreamResult`，把上游事件逐步转换成公共流 chunk；不支持原生流时实现等价的模拟流。 |
| `CountTokens(ctx, auth, req, opts)` | 返回本地 token 估算结果。项目 provider 复用统一计数逻辑，上游没有精确 count API。 |
| `HttpRequest(ctx, auth, request)` | 提供底层 HTTP 请求能力的扩展点。若 provider 使用专用 TLS client 或不需要该能力，应返回清晰的“不支持”错误，而不是伪造成功响应。 |
| `Refresh(ctx, auth)` | 刷新 provider auth、token、cookie 或 challenge 状态；没有独立刷新流程时可以返回原 auth，但请求路径仍需处理惰性刷新。 |

对应的 Go 形状可以概括为：

```go
type ProviderExecutor interface {
    Identifier() string
    RequestToFormat(clipexec.Request, clipexec.Options) sdktranslator.Format
    Execute(context.Context, *coreauth.Auth, clipexec.Request, clipexec.Options) (clipexec.Response, error)
    ExecuteStream(context.Context, *coreauth.Auth, clipexec.Request, clipexec.Options) (*clipexec.StreamResult, error)
    CountTokens(context.Context, *coreauth.Auth, clipexec.Request, clipexec.Options) (clipexec.Response, error)
    HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error)
    Refresh(context.Context, *coreauth.Auth) (*coreauth.Auth, error)
}
```

`RequestToFormat` 负责选择格式；具体的字段级 request/response 转换由 SDK translator 或执行器内部完成。不要只实现 `Execute` 而忽略流式、token count 或 refresh，因为 SDK 可能在不同 endpoint 和运行模式调用这些方法。

## Translator 注册原则

- 上游如果已经接受 OpenAI Chat 格式，可以使用 `FormatOpenAI`，在执行器中修补 model、message ID 或 provider 必需字段。
- 上游如果是私有 JSON、文本行协议或自定义 SSE，应创建唯一的 provider format，并注册 OpenAI Chat 到该 format 的转换。
- 需要 `/v1/responses` 时，同时注册 OpenAI Responses 到该 provider format；不能只注册 Chat 转换后假设 Responses 输入结构相同。
- 每个 format 都应同时提供 stream、non-stream 和 token-count transform；缺少其中一项会在对应调用模式中出现未转换的上游结构或错误。
- translator 只处理协议边界，不应承担 provider 的 token 获取、cookie 生命周期、代理 fallback 或 thread 状态存储。
- 流结束时使用 provider 的真实终止信号，例如 Mintlify sentinel、标准 SSE `[DONE]`、channel close 或 ReadMe 完整响应包装。

## 调试技巧

### 开启 SDK debug

把 `config.yaml` 的 `debug` 设为 `true`：

```yaml
debug: true
```

该值会传给 CLIProxyAPI SDK。调试时应注意日志中可能出现 URL、状态、请求元数据或上游错误文本；不要公开 auth、cookie、Bearer token 或完整请求内容。

### 分层排查

1. 先请求 `/v1/models`，确认 SDK 启动 hook 已注册模型。
2. 再用一个最小的 Chat Completions 非流式请求验证网关 Bearer token。
3. 再测试 `stream: true`，区分网关鉴权、模型路由和上游流解析问题。
4. 最后测试 Responses API，因为它有独立的 input/output 转换路径。
5. 检查 `auth-dir` 是否存在、权限是否正确，以及 runtime-only auth 是否有 provider 所需属性。
6. 检查代理解析顺序：auth 级别 proxy、默认 `proxy-url`、空值直连。

### Mintlify probe

遇到 Mintlify 连接问题时先绕过 gateway 运行 probe：

```bash
MINTLIFY_PROXY=socks5://127.0.0.1:1080 go run ./cmd/probe
```

探针可以帮助区分 cookie、JWT/token、上游发送和网关翻译问题。`MINTLIFY_PROXY` 只作为配置中的 `proxy-url` 为空时的 fallback；显式 `proxy-url` 优先。

## 重要 provider 注意事项

### Mintlify：TLS 与 cookies

Mintlify 不是普通 `net/http` 请求：适配器使用带 Chrome TLS fingerprint 的客户端，并可能依赖 Chrome Cloudflare cookies。cookie 加载失败、VPN/数据中心出口被拦截、Cloudflare bot 检测和 token 过期分别可能表现为不同的上游状态。不要为了简化代码而把 Mintlify 客户端改成普通 HTTP client。

常见状态：`402` 表示 payment/额度，`418` 表示出口来源阻断，`419` 表示 token 过期，`420` 表示 Cloudflare bot 检测。通过代理遇到 `418` 时，现有适配器可能尝试直连 fallback；这不是所有 provider 都应复制的通用重试策略。

### Inkeep：challenge

Inkeep 请求前要执行 SHA-256 challenge-response，并使用 auth 属性中的 API key、Origin 和 Referer。challenge 失败应在 provider auth/请求边界报告，不能被误判为公共网关 Bearer token 失效。上游接近 OpenAI 格式，但仍需补齐消息 ID 和 `provideLinks` 工具等 provider 要求。

### Stripe：轮询

Stripe 使用“创建 thread + 轮询答案”流程，轮询间隔为 500ms，当前实现最多轮询 120 次，总等待时间约 60 秒。必须传递 context 取消，避免客户端断开后继续等待。流式输出只是轮询结果经 channel 转发的模拟流；不可回答的问题会返回默认消息。不要对创建 thread 的请求做无条件重试，以免重复创建状态。

### ReadMe：subdomain 检测

ReadMe 上游通过文档站点的 `/ask` 接口工作。适配器会抓取 HTML 并用正则检测 ReadMe subdomain，auth 属性中的 `docs_url` 可以指定站点，默认是 `https://docs.readme.com`。新增或修改 ReadMe 逻辑时，要测试重定向、非标准文档域名、无法识别 subdomain 和错误站点配置；不要只用固定默认域名覆盖所有项目。

## 测试指导

### 单元测试重点

- translator 请求：Chat 和 Responses 的字符串、数组 content、assistant/tool 消息及工具参数；
- translator 响应：完整响应、增量响应、终止事件和工具调用；
- Mintlify 自定义 `type:value` 行、sentinel flush，以及类型 `9`/`a` 的静默丢弃；
- Inkeep SHA-256 challenge、标准 SSE 和 `[DONE]`；
- Stripe 轮询状态、超时、取消和默认未回答消息；
- ReadMe HTML/subdomain 检测、Markdown 响应和伪流 chunk；
- 空问题、无效 JSON、非 2xx 上游状态和 token 计数。

仓库中已有的测试方向包括 `internal/provider/tokens_test.go`、`internal/provider/translator_test.go`、`internal/mintlify/stream_test.go` 和 `internal/inkeep/client_test.go`。新增 provider 时应按同样结构建立 client、executor 和 translator 测试。

### 集成测试与人工验证

真实上游测试需要相应的 runtime-only auth、cookies、网络出口和可能的代理。建议：

1. 用未跟踪的 `config.yaml` 配置测试 token 和 `auth-dir`；
2. 先执行 `go test ./...` 和 `go build ./...`；
3. 启动网关并检查 `/v1/models`；
4. 对每个模型分别执行一次非流式、一次流式请求；
5. 对至少一个模型验证 `/v1/responses`；
6. 保存失败的 HTTP 状态和截断后的错误文本，但删除 token、cookie、代理凭据和完整用户内容。

没有稳定网络或上游 auth 时，不要把 live provider 请求硬编码进默认单元测试。使用固定 fixture、`httptest` 或可控的 transport 测试协议转换；把真实连通性验证留给显式的 probe 或集成测试流程。
