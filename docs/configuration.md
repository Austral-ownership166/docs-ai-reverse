# 配置说明（docs-ai-reverse）

`docs-ai-reverse`（模块名：`claude-code-chat`）使用 CLIProxyAPI SDK 的 `config.LoadConfig(cfgPath)` 加载 YAML 配置。启动命令的第一个参数是配置文件路径；未提供时使用当前目录的 `config.yaml`。

仓库只提交 `config.example.yaml`，实际使用的 `config.yaml` 已加入 `.gitignore`。首次运行可以复制示例：

```bash
cp config.example.yaml config.yaml
mkdir -p auths
```

## 配置项

| 名称 | 类型 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `host` | `string` | `"127.0.0.1"` | 网关监听地址。默认只监听本机，适合本地开发。Docker / 远端访问请设为 `"0.0.0.0"`。 |
| `port` | `int` | `8317` | 网关监听端口。默认地址为 `127.0.0.1:8317`。 |
| `proxy-url` | `string` | `""` | 所有文档 provider 的出站代理。支持 `socks5://`、`http://`、`https://`，也支持 `direct` 或 `none` 表示绕过环境代理直连。空值表示不在配置中指定代理。 |
| `auth-dir` | `string` | `"./auths"` | 项目本地的 runtime-only auth 存储目录，用于保存运行时认证令牌和相关状态。 |
| `api-keys` | `[]string` | `["sk-mintlify-local"]` | 网关 `/v1/*` endpoint 接受的 Bearer token 列表。这里的 token 只保护网关入口，不是任何上游 provider 的 API key。 |
| `debug` | `bool` | `false` | 是否开启 CLIProxyAPI SDK 的 debug 行为。开启后会把 debug 配置传递给 SDK。 |
| `remote-management.allow-remote` | `bool` | `false` | 是否允许远程管理。默认关闭。 |
| `remote-management.secret-key` | `string` | `""` | 远程管理使用的 secret。默认未设置；若启用远程管理，应通过未提交的本地配置提供。 |
| `remote-management.disable-control-panel` | `bool` | `true` | 是否禁用控制面板。默认禁用。 |

> `api-keys` 的默认值是示例配置中的本地占位 token。部署到共享环境前应改成随机生成、仅用于该网关的 token，并通过安全的配置注入方式管理。

## 推荐的本地配置

下面的配置适合仅在本机运行。`CHANGE_ME_TO_A_LOCAL_TOKEN` 只是占位符，不要原样用于生产，也不要把真实 token 写入 Git：

```yaml
host: "127.0.0.1"
port: 8317
proxy-url: ""
auth-dir: "./auths"
api-keys:
  - "CHANGE_ME_TO_A_LOCAL_TOKEN"
debug: false
remote-management:
  allow-remote: false
  secret-key: ""
  disable-control-panel: true
```

如果本地网络需要代理，可以仅修改 `proxy-url`，例如：

```yaml
proxy-url: "socks5://127.0.0.1:1080"
```

如果不想把代理写入 YAML，也可以在 `proxy-url` 为空时设置环境变量：

```bash
MINTLIFY_PROXY=socks5://127.0.0.1:1080 go run . config.yaml
```

启动逻辑优先使用配置中的 `proxy-url`；只有它为空时，才把 `MINTLIFY_PROXY` 作为 proxy-url fallback。显式的 `direct` 或 `none` 可用于绕过环境代理并直连。

## 安全注意事项

### 网关鉴权

- 网关 endpoint 的唯一鉴权机制是 `api-keys`。
- 客户端必须发送 `Authorization: Bearer <api-key>`；没有有效 token 的请求由 CLIProxyAPI SDK 返回 `401`。
- `api-keys` 不是 Mintlify、Inkeep、Stripe 或 ReadMe 的上游凭据。不要把上游密钥混入这个列表，也不要假设配置该列表就完成了 provider 登录。
- 不要把真实 token 写入 `config.example.yaml`、README、日志或 shell 历史。建议通过本地未跟踪的 `config.yaml`、环境变量模板或 secrets manager 注入。

### `auth-dir`

`auth-dir` 保存 runtime-only auth 及其认证令牌，是敏感目录。默认目录 `./auths` 位于项目目录中，使用时应确认：

- 目录权限只允许运行网关的用户读取；
- 不要提交 `auths/`，仓库的 `.gitignore` 已忽略它；
- 不要把 auth 文件复制到 issue、日志或公开构建产物中；
- 更换环境或怀疑泄露时，按 CLIProxyAPI 的认证管理流程撤销或重新生成相关 auth。

### 代理与隐私

代理会承载网关到文档 provider 的出站请求，可能看到请求内容、认证握手和响应元数据。只使用可信代理，并确认其 TLS、日志和访问策略。`direct`/`none` 会绕过环境代理，但不等同于“匿名”或“安全隔离”。

Mintlify 对 VPN、数据中心出口和 Cloudflare bot 检测敏感；遇到 `418` 或 `420` 时，先检查出口和 cookie/TLS 条件，而不是把网关 Bearer token 当成上游认证问题。

### 配置文件版本控制

- `config.example.yaml` 是提交到仓库的无敏感信息模板。
- `config.yaml` 用于本地或部署环境，已在 `.gitignore` 中忽略。
- `auths/`、日志和生成的二进制文件同样不应作为文档或源码提交。
