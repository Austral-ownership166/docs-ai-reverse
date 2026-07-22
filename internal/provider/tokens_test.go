package provider

import (
	"context"
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

func TestCountTokens_Executor(t *testing.T) {
	e := NewExecutor()
	payload := []byte(`{"model":"claude-docs","messages":[{"role":"user","content":"hi"}]}`)
	resp, err := e.CountTokens(context.Background(), nil, clipexec.Request{
		Model:   "claude-docs",
		Payload: payload,
	}, clipexec.Options{
		SourceFormat:     sdktranslator.FormatOpenAI,
		OriginalRequest:  payload,
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

func TestMaxContextTokensConstant(t *testing.T) {
	if maxContextTokens != 200_000 {
		t.Fatalf("maxContextTokens=%d", maxContextTokens)
	}
}
