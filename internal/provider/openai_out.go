package provider

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// lastUserQuestion extracts the last user text from OpenAI chat or Responses JSON.
func lastUserQuestion(payload []byte) string {
	if len(payload) == 0 {
		return ""
	}
	root := gjson.ParseBytes(payload)

	// Chat Completions messages
	if msgs := root.Get("messages"); msgs.Exists() && msgs.IsArray() {
		var last string
		msgs.ForEach(func(_, msg gjson.Result) bool {
			if msg.Get("role").String() != "user" {
				return true
			}
			if t := extractContentText(msg.Get("content")); t != "" {
				last = t
			}
			return true
		})
		if last != "" {
			return last
		}
	}

	// Responses API input
	if input := root.Get("input"); input.Exists() {
		if input.Type == gjson.String {
			return input.String()
		}
		var last string
		input.ForEach(func(_, item gjson.Result) bool {
			role := item.Get("role").String()
			typ := item.Get("type").String()
			if role == "user" || typ == "message" && role == "user" || (role == "" && typ == "input_text") {
				if t := extractContentText(item.Get("content")); t != "" {
					last = t
				} else if t := item.Get("text").String(); t != "" {
					last = t
				} else if item.Type == gjson.String {
					last = item.String()
				}
			}
			if typ == "input_text" {
				if t := item.Get("text").String(); t != "" {
					last = t
				}
			}
			return true
		})
		if last != "" {
			return last
		}
	}

	return strings.TrimSpace(root.Get("prompt").String())
}

// questionWithOptionalTools prepends prompt-tool instructions when the request has tools.
func questionWithOptionalTools(payload []byte, question string) string {
	if tools := buildToolsPromptFromRequest(payload); tools != "" {
		return tools + "\n\n" + question
	}
	return question
}

func newCompletionID() string {
	return fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
}

func buildChatCompletion(model, content string) []byte {
	id := newCompletionID()
	created := time.Now().Unix()
	out := []byte(`{"id":"","object":"chat.completion","created":0,"model":"","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`)
	out, _ = sjson.SetBytes(out, "id", id)
	out, _ = sjson.SetBytes(out, "created", created)
	out, _ = sjson.SetBytes(out, "model", model)
	out, _ = sjson.SetBytes(out, "choices.0.message.content", content)
	return out
}

func buildChatChunk(model, deltaContent string, finish *string) []byte {
	id := newCompletionID()
	created := time.Now().Unix()
	type chunk struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		Model   string `json:"model"`
		Choices []struct {
			Index        int             `json:"index"`
			Delta        json.RawMessage `json:"delta"`
			FinishReason *string         `json:"finish_reason"`
		} `json:"choices"`
	}
	c := chunk{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   model,
	}
	c.Choices = make([]struct {
		Index        int             `json:"index"`
		Delta        json.RawMessage `json:"delta"`
		FinishReason *string         `json:"finish_reason"`
	}, 1)
	c.Choices[0].Index = 0
	c.Choices[0].FinishReason = finish
	if deltaContent != "" {
		b, _ := json.Marshal(map[string]string{"content": deltaContent})
		c.Choices[0].Delta = b
	} else if finish != nil {
		c.Choices[0].Delta = json.RawMessage(`{}`)
	} else {
		c.Choices[0].Delta = json.RawMessage(`{"role":"assistant"}`)
	}
	out, _ := json.Marshal(c)
	return out
}

func buildResponsesOutput(model, content string) []byte {
	id := fmt.Sprintf("resp_%d", time.Now().UnixNano())
	out := []byte(`{"id":"","object":"response","created_at":0,"status":"completed","model":"","output":[{"type":"message","id":"","role":"assistant","status":"completed","content":[{"type":"output_text","text":""}]}],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}`)
	out, _ = sjson.SetBytes(out, "id", id)
	out, _ = sjson.SetBytes(out, "created_at", time.Now().Unix())
	out, _ = sjson.SetBytes(out, "model", model)
	out, _ = sjson.SetBytes(out, "output.0.id", "msg_"+id)
	out, _ = sjson.SetBytes(out, "output.0.content.0.text", content)
	return out
}
