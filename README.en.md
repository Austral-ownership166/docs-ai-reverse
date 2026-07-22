# docs-ai-reverse

[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![CI](https://github.com/6Kmfi6HP/docs-ai-reverse/actions/workflows/ci.yml/badge.svg)](https://github.com/6Kmfi6HP/docs-ai-reverse/actions/workflows/ci.yml)
[![Release](https://github.com/6Kmfi6HP/docs-ai-reverse/actions/workflows/release.yml/badge.svg)](https://github.com/6Kmfi6HP/docs-ai-reverse/actions/workflows/release.yml)
[![GHCR](https://img.shields.io/badge/GHCR-docs--ai--reverse-blue?logo=docker&logoColor=white)](https://github.com/6Kmfi6HP/docs-ai-reverse/pkgs/container/docs-ai-reverse)
[![OpenAI Compatible](https://img.shields.io/badge/API-OpenAI%20compatible-412991)](./docs/api.md)
[![中文 README](https://img.shields.io/badge/README-中文-1f6b4f)](./README.md)
[![Site EN](https://img.shields.io/badge/site-English-222222)](https://6kmfi6hp.github.io/docs-ai-reverse/)
[![Site ZH](https://img.shields.io/badge/site-中文-222222)](https://6kmfi6hp.github.io/docs-ai-reverse/zh.html)
[![llms.txt](https://img.shields.io/badge/llms.txt-ready-111111)](./llms.txt)

**OpenAI-compatible Docs AI gateway** — adapt Mintlify, Inkeep, Stripe, and ReadMe documentation assistants into local Chat Completions / Responses APIs.

> 中文文档请看 [README.md](./README.md)。This page is the English SEO / onboarding entry.

## Why this project

`docs-ai-reverse` (Go module: `claude-code-chat`) uses `CLIProxyAPI/v7` to expose:

- `GET /v1/models`
- `POST /v1/chat/completions`
- `POST /v1/responses`

Built-in models:

| Model | Provider | Upstream |
|---|---|---|
| `claude-docs` | Mintlify | Claude / Mintlify docs assistant |
| `anthropic-docs` | Inkeep | Anthropic docs |
| `stripe-docs` | Stripe | `ai.stripe.com` |
| `readme-docs` | ReadMe | ReadMe Ask AI |

## Quick start

```bash
cp config.example.yaml config.yaml
mkdir -p auths
# edit api-keys in config.yaml (gateway Bearer tokens only)
go run . ./config.yaml
```

Default listen address: `127.0.0.1:8317`.

Example:

```bash
curl http://127.0.0.1:8317/v1/models \
  -H "Authorization: Bearer sk-local-demo"
```

## Docker

```bash
# config.yaml must use host: "0.0.0.0" for container networking
docker run --rm -p 8317:8317 \
  -v "$(pwd)/config.yaml:/config/config.yaml:ro" \
  -v "$(pwd)/auths:/app/auths" \
  ghcr.io/6kmfi6hp/docs-ai-reverse:latest
```

Push a SemVer tag (`v0.1.0`) to publish GitHub Release binaries and multi-arch GHCR images. See [docs/ci-cd.md](./docs/ci-cd.md).

## Docs

| Doc | Language / notes |
|---|---|
| [README.md](./README.md) | Full Chinese guide |
| [docs/index.md](./docs/index.md) | Docs hub |
| [docs/api.md](./docs/api.md) | API details |
| [docs/architecture.md](./docs/architecture.md) | Architecture |
| [docs/configuration.md](./docs/configuration.md) | Config reference |
| [docs/development.md](./docs/development.md) | Development |
| [docs/ci-cd.md](./docs/ci-cd.md) | CI/CD, Docker, releases |
| [llms.txt](./llms.txt) | AI crawler index (EN + ZH) |
| [Site EN](https://6kmfi6hp.github.io/docs-ai-reverse/) | GitHub Pages |
| [Site ZH](https://6kmfi6hp.github.io/docs-ai-reverse/zh.html) | 中文落地页 |

## Keywords

OpenAI compatible · Docs AI gateway · Mintlify · Inkeep · Stripe docs AI · ReadMe Ask AI · Claude docs · Anthropic docs · Chat Completions · Responses API · Go reverse proxy · OpenAI 兼容 · 文档 AI 网关

## License

No `LICENSE` file is declared yet. Do not assume redistribution rights without confirmation.
