package provider

import (
	"context"
	"fmt"
	"strings"
	"sync"

	clipexec "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tiktoken-go/tokenizer"
)

// Advertised context window for docs models (pretend max).
const maxContextTokens = 200_000

var (
	tokenEncOnce sync.Once
	tokenEnc     tokenizer.Codec
	tokenEncErr  error
)

func tokenizerCodec() (tokenizer.Codec, error) {
	tokenEncOnce.Do(func() {
		// o200k_base approximates modern Claude / GPT-4o-class tokenizers.
		tokenEnc, tokenEncErr = tokenizer.Get(tokenizer.O200kBase)
	})
	return tokenEnc, tokenEncErr
}

func buildOpenAIUsageJSON(count int64) []byte {
	return []byte(fmt.Sprintf(`{"usage":{"prompt_tokens":%d,"completion_tokens":0,"total_tokens":%d}}`, count, count))
}

// countTokensResponse estimates prompt tokens and translates to the client response format.
// Used by all docs providers (none expose a native count API).
func countTokensResponse(ctx context.Context, from sdktranslator.Format, req clipexec.Request, opts clipexec.Options) (clipexec.Response, error) {
	payload := opts.OriginalRequest
	if len(payload) == 0 {
		payload = req.Payload
	}
	count, err := estimatePromptTokens(payload)
	if err != nil {
		return clipexec.Response{}, err
	}
	if count > maxContextTokens {
		count = maxContextTokens
	}
	responseFormat := clipexec.ResponseFormatOrSource(opts)
	out := sdktranslator.TranslateTokenCount(ctx, from, responseFormat, count, buildOpenAIUsageJSON(count))
	return clipexec.Response{Payload: out}, nil
}

// estimatePromptTokens approximates prompt tokens from an OpenAI / Mintlify JSON body.
func estimatePromptTokens(payload []byte) (int64, error) {
	enc, err := tokenizerCodec()
	if err != nil {
		return 0, fmt.Errorf("tokenizer init: %w", err)
	}
	if len(payload) == 0 {
		return 0, nil
	}

	root := gjson.ParseBytes(payload)
	segments := make([]string, 0, 32)
	collectTextSegments(root, &segments)

	joined := strings.TrimSpace(strings.Join(segments, "\n"))
	if joined == "" {
		return 0, nil
	}
	n, err := enc.Count(joined)
	if err != nil {
		return 0, err
	}
	return int64(n), nil
}

func collectTextSegments(root gjson.Result, segments *[]string) {
	if msgs := root.Get("messages"); msgs.Exists() && msgs.IsArray() {
		msgs.ForEach(func(_, msg gjson.Result) bool {
			addSeg(segments, msg.Get("role").String())
			collectContent(msg.Get("content"), segments)
			if parts := msg.Get("parts"); parts.Exists() && parts.IsArray() {
				parts.ForEach(func(_, part gjson.Result) bool {
					addSeg(segments, part.Get("text").String())
					return true
				})
			}
			if tcs := msg.Get("tool_calls"); tcs.Exists() && tcs.IsArray() {
				tcs.ForEach(func(_, tc gjson.Result) bool {
					addSeg(segments, tc.Get("id").String())
					addSeg(segments, tc.Get("function.name").String())
					addSeg(segments, tc.Get("function.arguments").String())
					return true
				})
			}
			return true
		})
	}

	if input := root.Get("input"); input.Exists() {
		if input.Type == gjson.String {
			addSeg(segments, input.String())
		} else if input.IsArray() {
			input.ForEach(func(_, item gjson.Result) bool {
				if item.Type == gjson.String {
					addSeg(segments, item.String())
					return true
				}
				addSeg(segments, item.Get("role").String())
				addSeg(segments, item.Get("type").String())
				addSeg(segments, item.Get("name").String())
				addSeg(segments, item.Get("arguments").String())
				addSeg(segments, item.Get("output").String())
				addSeg(segments, item.Get("call_id").String())
				collectContent(item.Get("content"), segments)
				addSeg(segments, item.Get("text").String())
				return true
			})
		}
	}

	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() {
		tools.ForEach(func(_, tool gjson.Result) bool {
			addSeg(segments, tool.Get("type").String())
			addSeg(segments, tool.Get("name").String())
			addSeg(segments, tool.Get("description").String())
			fn := tool.Get("function")
			if fn.Exists() {
				addSeg(segments, fn.Get("name").String())
				addSeg(segments, fn.Get("description").String())
				if params := fn.Get("parameters"); params.Exists() {
					addSeg(segments, params.Raw)
				}
			}
			if schema := tool.Get("input_schema"); schema.Exists() {
				addSeg(segments, schema.Raw)
			}
			return true
		})
	}

	addSeg(segments, root.Get("prompt").String())

	// Gemini countTokens / generateContent shape.
	if contents := root.Get("contents"); contents.Exists() && contents.IsArray() {
		contents.ForEach(func(_, item gjson.Result) bool {
			addSeg(segments, item.Get("role").String())
			if parts := item.Get("parts"); parts.Exists() && parts.IsArray() {
				parts.ForEach(func(_, part gjson.Result) bool {
					addSeg(segments, part.Get("text").String())
					return true
				})
			}
			return true
		})
	}
}

func collectContent(content gjson.Result, segments *[]string) {
	if !content.Exists() {
		return
	}
	if content.Type == gjson.String {
		addSeg(segments, content.String())
		return
	}
	if content.IsArray() {
		content.ForEach(func(_, part gjson.Result) bool {
			switch part.Get("type").String() {
			case "text", "input_text", "output_text", "":
				addSeg(segments, part.Get("text").String())
			default:
				if part.Type == gjson.String {
					addSeg(segments, part.String())
				} else if t := part.Get("text").String(); t != "" {
					addSeg(segments, t)
				} else {
					addSeg(segments, part.Raw)
				}
			}
			return true
		})
	}
}

func addSeg(segments *[]string, v string) {
	if s := strings.TrimSpace(v); s != "" {
		*segments = append(*segments, s)
	}
}
