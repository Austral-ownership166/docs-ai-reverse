package provider

import (
	"context"
	"fmt"
	"testing"

	clipexec "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestEstimatePromptTokens_OpenAIChat(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"hello world"}]}`)
	n, err := estimatePromptTokens(payload)
	if err != nil {
		t.Fatal(err)
	}
	if n <= 0 {
		t.Fatalf("expected positive token count, got %d", n)
	}
}

func TestEstimatePromptTokens_MintlifyMessages(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"搜索 SDK 文档","parts":[{"type":"text","text":"搜索 SDK 文档"}]}]}`)
	n, err := estimatePromptTokens(payload)
	if err != nil {
		t.Fatal(err)
	}
	if n <= 0 {
		t.Fatalf("expected positive token count, got %d", n)
	}
}

func TestEstimatePromptTokens_GeminiContents(t *testing.T) {
	payload := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello world token count test"}]}]}`)
	n, err := estimatePromptTokens(payload)
	if err != nil {
		t.Fatal(err)
	}
	if n <= 0 {
		t.Fatalf("expected positive token count, got %d", n)
	}
}

func assertCountTokens(t *testing.T, countTokens func(context.Context, clipexec.Request, clipexec.Options) (clipexec.Response, error), model string) {
	t.Helper()
	payload := []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}]}`, model))
	resp, err := countTokens(context.Background(), clipexec.Request{
		Model:   model,
		Payload: payload,
	}, clipexec.Options{
		SourceFormat:    sdktranslator.FormatOpenAI,
		OriginalRequest: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	n := gjson.GetBytes(resp.Payload, "usage.prompt_tokens").Int()
	if n <= 0 {
		t.Fatalf("payload=%s", resp.Payload)
	}
	if n > maxContextTokens {
		t.Fatalf("count %d exceeds max %d", n, maxContextTokens)
	}
}

func TestCountTokens_AllProviders(t *testing.T) {
	t.Run("mintlify", func(t *testing.T) {
		e := NewExecutor()
		assertCountTokens(t, func(ctx context.Context, req clipexec.Request, opts clipexec.Options) (clipexec.Response, error) {
			return e.CountTokens(ctx, nil, req, opts)
		}, "claude-docs")
	})
	t.Run("inkeep", func(t *testing.T) {
		e := NewInkeepExecutor()
		assertCountTokens(t, func(ctx context.Context, req clipexec.Request, opts clipexec.Options) (clipexec.Response, error) {
			return e.CountTokens(ctx, nil, req, opts)
		}, "anthropic-docs")
	})
	t.Run("stripe", func(t *testing.T) {
		e := NewStripeExecutor()
		assertCountTokens(t, func(ctx context.Context, req clipexec.Request, opts clipexec.Options) (clipexec.Response, error) {
			return e.CountTokens(ctx, nil, req, opts)
		}, "stripe-docs")
	})
	t.Run("readme", func(t *testing.T) {
		e := NewReadmeExecutor()
		assertCountTokens(t, func(ctx context.Context, req clipexec.Request, opts clipexec.Options) (clipexec.Response, error) {
			return e.CountTokens(ctx, nil, req, opts)
		}, "readme-docs")
	})
}

func TestMaxContextTokensConstant(t *testing.T) {
	if maxContextTokens != 200_000 {
		t.Fatalf("maxContextTokens=%d", maxContextTokens)
	}
}
