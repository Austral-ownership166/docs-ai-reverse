package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"claude-code-chat/internal/inkeep"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	clipexec "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const inkeepProviderKey = "inkeep"

// InkeepExecutor implements auth.ProviderExecutor for Inkeep (Anthropic docs AI).
type InkeepExecutor struct {
	proxyDefaults
}

// NewInkeepExecutor constructs an Inkeep executor.
func NewInkeepExecutor() *InkeepExecutor { return &InkeepExecutor{} }

func (e *InkeepExecutor) Identifier() string { return inkeepProviderKey }

func (e *InkeepExecutor) RequestToFormat(_ clipexec.Request, _ clipexec.Options) sdktranslator.Format {
	return sdktranslator.FormatOpenAI
}

func inkeepConfigFromAuth(a *coreauth.Auth) inkeep.Config {
	cfg := inkeep.DefaultClaudeConfig()
	if a == nil || a.Attributes == nil {
		return cfg
	}
	if v := strings.TrimSpace(a.Attributes["api_key"]); v != "" {
		cfg.APIKey = v
	}
	if v := strings.TrimSpace(a.Attributes["origin"]); v != "" {
		cfg.Origin = v
	}
	if v := strings.TrimSpace(a.Attributes["referer"]); v != "" {
		cfg.Referer = v
	}
	return cfg
}

func (e *InkeepExecutor) clientFor(a *coreauth.Auth) *inkeep.Client {
	return inkeep.NewClient(inkeepConfigFromAuth(a), httpClientForProxy(e.resolveProxy(a), 120*time.Second))
}

// prepareInkeepBody rewrites an OpenAI chat/responses payload into Inkeep chat completions JSON.
func prepareInkeepBody(payload []byte, stream bool) ([]byte, error) {
	if !gjson.ValidBytes(payload) {
		return nil, fmt.Errorf("inkeep: invalid request json")
	}

	// If this is Responses API shape, synthesize a minimal chat body from user text.
	if gjson.GetBytes(payload, "input").Exists() && !gjson.GetBytes(payload, "messages").Exists() {
		q := lastUserQuestion(payload)
		msgID := inkeep.NewMessageID()
		body := []byte(`{"model":"","messages":[],"stream":false}`)
		body, _ = sjson.SetBytes(body, "model", inkeep.DefaultModel)
		body, _ = sjson.SetBytes(body, "stream", stream)
		body, _ = sjson.SetBytes(body, "messages.0.id", msgID)
		body, _ = sjson.SetBytes(body, "messages.0.role", "user")
		body, _ = sjson.SetBytes(body, "messages.0.content", q)
		body = ensureProvideLinksTool(body)
		return body, nil
	}

	body := append([]byte(nil), payload...)
	body, _ = sjson.SetBytes(body, "model", inkeep.DefaultModel)
	body, _ = sjson.SetBytes(body, "stream", stream)

	// Ensure each message has an id (Inkeep expects it).
	msgs := gjson.GetBytes(body, "messages")
	if msgs.Exists() && msgs.IsArray() {
		msgs.ForEach(func(i, msg gjson.Result) bool {
			if msg.Get("id").String() == "" {
				path := fmt.Sprintf("messages.%d.id", i.Int())
				body, _ = sjson.SetBytes(body, path, inkeep.NewMessageID())
			}
			// Flatten array content to string when needed.
			content := msg.Get("content")
			if content.Exists() && content.IsArray() {
				text := extractContentText(content)
				path := fmt.Sprintf("messages.%d.content", i.Int())
				body, _ = sjson.SetBytes(body, path, text)
			}
			return true
		})
	}

	body = ensureProvideLinksTool(body)
	// Drop fields Inkeep may reject
	for _, drop := range []string{"max_tokens", "max_completion_tokens", "temperature", "top_p", "n", "user", "response_format"} {
		body, _ = sjson.DeleteBytes(body, drop)
	}
	return body, nil
}

func ensureProvideLinksTool(body []byte) []byte {
	tools := gjson.GetBytes(body, "tools")
	hasProvide := false
	if tools.Exists() && tools.IsArray() {
		tools.ForEach(func(_, t gjson.Result) bool {
			name := t.Get("function.name").String()
			if name == "" {
				name = t.Get("name").String()
			}
			if name == "provideLinks" {
				hasProvide = true
			}
			return true
		})
	}
	if !hasProvide {
		toolJSON, _ := json.Marshal(inkeep.ProvideLinksTool())
		if !tools.Exists() {
			body, _ = sjson.SetRawBytes(body, "tools", []byte("["+string(toolJSON)+"]"))
		} else {
			arr := tools.Raw
			if arr == "null" || arr == "" {
				arr = "[]"
			}
			if strings.HasSuffix(strings.TrimSpace(arr), "]") {
				inner := strings.TrimSpace(arr)
				inner = strings.TrimSuffix(inner, "]")
				inner = strings.TrimSpace(inner)
				if inner == "[" || inner == "" {
					arr = "[" + string(toolJSON) + "]"
				} else {
					arr = inner + "," + string(toolJSON) + "]"
				}
				body, _ = sjson.SetRawBytes(body, "tools", []byte(arr))
			}
		}
	}
	if !gjson.GetBytes(body, "tool_choice").Exists() {
		body, _ = sjson.SetBytes(body, "tool_choice", "auto")
	}
	return body
}

func (e *InkeepExecutor) Execute(ctx context.Context, a *coreauth.Auth, req clipexec.Request, opts clipexec.Options) (clipexec.Response, error) {
	payload := opts.OriginalRequest
	if len(payload) == 0 {
		payload = req.Payload
	}
	body, err := prepareInkeepBody(payload, false)
	if err != nil {
		return clipexec.Response{}, err
	}
	client := e.clientFor(a)
	resp, err := client.ChatCompletions(ctx, body, false)
	if err != nil {
		return clipexec.Response{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return clipexec.Response{}, err
	}

	responseFormat := clipexec.ResponseFormatOrSource(opts)
	if responseFormat == sdktranslator.FormatOpenAIResponse {
		content := extractInkeepContent(data)
		return clipexec.Response{Payload: buildResponsesOutput(req.Model, content)}, nil
	}
	// Rewrite model field to the client-facing model id.
	data, _ = sjson.SetBytes(data, "model", req.Model)
	return clipexec.Response{Payload: data}, nil
}

func extractInkeepContent(data []byte) string {
	msg := gjson.GetBytes(data, "choices.0.message")
	if c := msg.Get("content").String(); c != "" {
		return c
	}
	// Fall back to provideLinks tool text
	var text string
	msg.Get("tool_calls").ForEach(func(_, tc gjson.Result) bool {
		if tc.Get("function.name").String() != "provideLinks" {
			return true
		}
		args := tc.Get("function.arguments").String()
		if t := gjson.Get(args, "text").String(); t != "" {
			text = t
		}
		return true
	})
	return text
}

func (e *InkeepExecutor) ExecuteStream(ctx context.Context, a *coreauth.Auth, req clipexec.Request, opts clipexec.Options) (*clipexec.StreamResult, error) {
	payload := opts.OriginalRequest
	if len(payload) == 0 {
		payload = req.Payload
	}
	body, err := prepareInkeepBody(payload, true)
	if err != nil {
		return nil, err
	}
	client := e.clientFor(a)
	resp, err := client.ChatCompletions(ctx, body, true)
	if err != nil {
		return nil, err
	}

	responseFormat := clipexec.ResponseFormatOrSource(opts)
	out := make(chan clipexec.StreamChunk)
	go func() {
		defer close(out)
		defer resp.Body.Close()

		if responseFormat == sdktranslator.FormatOpenAIResponse {
			// Aggregate then emit a single Responses payload (Inkeep SSE is chat-shaped).
			var contentParts []string
			scanner := bufio.NewScanner(resp.Body)
			buf := make([]byte, 0, 64*1024)
			scanner.Buffer(buf, 2<<20)
			var toolArgs strings.Builder
			for scanner.Scan() {
				line := scanner.Text()
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				raw := strings.TrimPrefix(line, "data: ")
				if raw == "[DONE]" {
					break
				}
				if c := gjson.Get(raw, "choices.0.delta.content").String(); c != "" {
					contentParts = append(contentParts, c)
				}
				gjson.Get(raw, "choices.0.delta.tool_calls").ForEach(func(_, tc gjson.Result) bool {
					if a := tc.Get("function.arguments").String(); a != "" {
						toolArgs.WriteString(a)
					}
					return true
				})
			}
			content := strings.Join(contentParts, "")
			if content == "" && toolArgs.Len() > 0 {
				content = gjson.Get(toolArgs.String(), "text").String()
			}
			select {
			case out <- clipexec.StreamChunk{Payload: buildResponsesOutput(req.Model, content)}:
			case <-ctx.Done():
			}
			return
		}

		scanner := bufio.NewScanner(resp.Body)
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 2<<20)
		for scanner.Scan() {
			line := scanner.Bytes()
			if !bytes.HasPrefix(line, []byte("data: ")) {
				continue
			}
			raw := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data: ")))
			if bytes.Equal(raw, []byte("[DONE]")) {
				stop := "stop"
				select {
				case out <- clipexec.StreamChunk{Payload: buildChatChunk(req.Model, "", &stop)}:
				case <-ctx.Done():
				}
				return
			}
			patched := append([]byte(nil), raw...)
			patched, _ = sjson.SetBytes(patched, "model", req.Model)
			select {
			case out <- clipexec.StreamChunk{Payload: patched}:
			case <-ctx.Done():
				return
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			select {
			case out <- clipexec.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
		}
	}()

	return &clipexec.StreamResult{Headers: resp.Header.Clone(), Chunks: out}, nil
}

func (e *InkeepExecutor) CountTokens(ctx context.Context, _ *coreauth.Auth, req clipexec.Request, opts clipexec.Options) (clipexec.Response, error) {
	return countTokensResponse(ctx, sdktranslator.FormatOpenAI, req, opts)
}

func (e *InkeepExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("inkeep: HttpRequest not supported")
}

func (e *InkeepExecutor) Refresh(_ context.Context, a *coreauth.Auth) (*coreauth.Auth, error) {
	return a, nil
}
