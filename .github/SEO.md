# SEO setup (EN + ZH) — maintained via GitHub CLI

## Live repository metadata

Set with `gh`:

```bash
# Bilingual About description (GitHub allows one description field)
gh repo edit 6Kmfi6HP/docs-ai-reverse \
  --description 'OpenAI-compatible Docs AI gateway (Mintlify/Inkeep/Stripe/ReadMe → Chat Completions/Responses). | 将 Mintlify、Inkeep、Stripe、ReadMe 文档 AI 适配为 OpenAI 兼容本地网关。' \
  --homepage 'https://6kmfi6hp.github.io/docs-ai-reverse/'

# Topics hard-limit = 20 (ASCII slugs only; Chinese ranking relies on description + bilingual pages)
gh api -X PUT repos/6Kmfi6HP/docs-ai-reverse/topics \
  -H 'Accept: application/vnd.github+json' \
  --input - <<'EOF'
{"names":["openai","openai-compatible","openai-api","chat-completions","gateway","ai-gateway","docs-ai","docs-gateway","mintlify","inkeep","stripe","readme","claude","anthropic","claude-docs","llm","golang","go","proxy","documentation"]}
EOF

# Confirm
gh repo view --json description,homepageUrl,repositoryTopics
```

## Bilingual surfaces

| Surface | English | Chinese |
|---|---|---|
| GitHub About | shared bilingual description | same field |
| Pages landing | `/` (`docs/index.html`) | `/zh.html` |
| README | `README.en.md` | `README.md` |
| AI crawlers | `llms.txt` (EN+ZH) | same file |
| Sitemap hreflang | en + zh-CN + x-default | same |

## Social preview

Upload 1280×640 under GitHub → Settings → General → Social preview.
`gh` cannot upload the image file reliably; keep using the web UI.

## Verify Pages

```bash
gh api repos/6Kmfi6HP/docs-ai-reverse/pages --jq '{html_url,status,source}'
gh api repos/6Kmfi6HP/docs-ai-reverse/pages/builds -X POST
curl -sI https://6kmfi6hp.github.io/docs-ai-reverse/ | head -5
curl -sI https://6kmfi6hp.github.io/docs-ai-reverse/zh.html | head -5
curl -sI https://6kmfi6hp.github.io/docs-ai-reverse/sitemap.xml | head -5
curl -sI https://6kmfi6hp.github.io/docs-ai-reverse/llms.txt | head -5
```
