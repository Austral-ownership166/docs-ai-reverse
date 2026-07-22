package provider

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"claude-code-chat/internal/mintlify"

	sdktr "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const formatMintlify = sdktr.Format("mintlify")

func init() {
	sdktr.Register(sdktr.FormatOpenAI, formatMintlify,
		convertOpenAIRequestToMintlify,
		sdktr.ResponseTransform{
			Stream:    convertMintlifyStreamToOpenAI,
			NonStream: convertMintlifyNonStreamToOpenAI,
		},
	)
	sdktr.Register(sdktr.FormatOpenAIResponse, formatMintlify,
		convertOpenAIResponseRequestToMintlify,
		sdktr.ResponseTransform{
			Stream:    convertMintlifyStreamToOpenAIResponse,
			NonStream: convertMintlifyNonStreamToOpenAIResponse,
		},
	)
}

func extractContentText(content gjson.Result) string {
	if !content.Exists() {
		return ""
	}
	if content.Type == gjson.String {
		return content.String()
	}
	var parts []string
	content.ForEach(func(_, value gjson.Result) bool {
		switch value.Get("type").String() {
		case "text", "":
			if t := value.Get("text").String(); t != "" {
				parts = append(parts, t)
			} else if t := value.String(); t != "" && value.Type == gjson.String {
				parts = append(parts, t)
			}
		}
		return true
	})
	return strings.Join(parts, "")
}

func messagesFromOpenAIChat(raw []byte) []mintlify.Message {
	var out []mintlify.Message
	gjson.GetBytes(raw, "messages").ForEach(func(_, msg gjson.Result) bool {
		role := msg.Get("role").String()
		if role != "user" && role != "assistant" && role != "system" {
			return true
		}
		text := extractContentText(msg.Get("content"))
		if text == "" {
			return true
		}
		// Mintlify has no system role; fold system into user context.
		if role == "system" {
			role = "user"
			text = "[system]\n" + text
		}
		if role == "user" {
			out = append(out, mintlify.NewUserMessage(text))
		} else {
			out = append(out, mintlify.NewAssistantMessage(text))
		}
		return true
	})
	return out
}

func messagesFromOpenAIResponses(raw []byte) []mintlify.Message {
	input := gjson.GetBytes(raw, "input")
	if !input.Exists() {
		return nil
	}
	if input.Type == gjson.String {
		text := strings.TrimSpace(input.String())
		if text == "" {
			return nil
		}
		return []mintlify.Message{mintlify.NewUserMessage(text)}
	}

	var out []mintlify.Message
	input.ForEach(func(_, item gjson.Result) bool {
		if item.Type == gjson.String {
			text := strings.TrimSpace(item.String())
			if text != "" {
				out = append(out, mintlify.NewUserMessage(text))
			}
			return true
		}
		role := item.Get("role").String()
		if role == "" {
			role = "user"
		}
		text := extractContentText(item.Get("content"))
		if text == "" {
			if t := item.Get("text").String(); t != "" {
				text = t
			}
		}
		if text == "" {
			return true
		}
		if role == "system" || role == "developer" {
			role = "user"
			text = "[system]\n" + text
		}
		if role == "assistant" {
			out = append(out, mintlify.NewAssistantMessage(text))
		} else {
			out = append(out, mintlify.NewUserMessage(text))
		}
		return true
	})
	return out
}

func buildMintlifyRequestJSON(messages []mintlify.Message) []byte {
	site := mintlify.DefaultSiteConfig()
	req := mintlify.MessageRequest{
		ID:          site.Subdomain,
		Messages:    messages,
		FP:          site.Subdomain,
		Filter:      mintlify.Filter{Language: site.Language},
		CurrentPath: site.DocsPath,
	}
	b, _ := json.Marshal(req)
	return b
}

func convertOpenAIRequestToMintlify(_ string, raw []byte, _ bool) []byte {
	return buildMintlifyRequestJSON(messagesFromOpenAIChat(raw))
}

func convertOpenAIResponseRequestToMintlify(_ string, raw []byte, _ bool) []byte {
	return buildMintlifyRequestJSON(messagesFromOpenAIResponses(raw))
}

// streamDoneSentinel is emitted by the executor after upstream EOF so translators
// can flush finish / completed events.
const streamDoneSentinel = "[DONE]"

type openAIStreamState struct {
	ID        string
	Created   int64
	Started   bool
	Finished  bool
	FullText  strings.Builder
	ItemID    string
	Seq       int
	RespDone  bool
}

func ensureOpenAIStreamState(param *any) *openAIStreamState {
	if param == nil {
		return &openAIStreamState{}
	}
	if *param == nil {
		*param = &openAIStreamState{
			Created: time.Now().Unix(),
		}
	}
	return (*param).(*openAIStreamState)
}

func convertMintlifyStreamToOpenAI(_ context.Context, model string, _, _, raw []byte, param *any) [][]byte {
	st := ensureOpenAIStreamState(param)
	if st.ID == "" {
		st.ID = "chatcmpl-" + mintlify.GenerateID()[:24]
	}
	if st.Created == 0 {
		st.Created = time.Now().Unix()
	}
	line := strings.TrimSpace(string(raw))
	if line == "" {
		return nil
	}
	if line == streamDoneSentinel {
		if st.Finished {
			return nil
		}
		st.Finished = true
		// Handler wraps with `data: ...` and appends terminal `data: [DONE]`.
		chunk := []byte(`{"id":"","object":"chat.completion.chunk","created":0,"model":"","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		chunk, _ = sjson.SetBytes(chunk, "id", st.ID)
		chunk, _ = sjson.SetBytes(chunk, "created", st.Created)
		chunk, _ = sjson.SetBytes(chunk, "model", model)
		return [][]byte{chunk}
	}

	if len(line) < 2 || line[1] != ':' {
		return nil
	}
	typ := line[:1]
	val := line[2:]
	if typ != "0" {
		return nil
	}
	text, ok := mintlify.ParseTextDelta(val)
	if !ok || text == "" {
		return nil
	}
	st.FullText.WriteString(text)

	var out [][]byte
	if !st.Started {
		st.Started = true
		roleChunk := []byte(`{"id":"","object":"chat.completion.chunk","created":0,"model":"","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`)
		roleChunk, _ = sjson.SetBytes(roleChunk, "id", st.ID)
		roleChunk, _ = sjson.SetBytes(roleChunk, "created", st.Created)
		roleChunk, _ = sjson.SetBytes(roleChunk, "model", model)
		out = append(out, roleChunk)
	}
	delta := []byte(`{"id":"","object":"chat.completion.chunk","created":0,"model":"","choices":[{"index":0,"delta":{"content":""},"finish_reason":null}]}`)
	delta, _ = sjson.SetBytes(delta, "id", st.ID)
	delta, _ = sjson.SetBytes(delta, "created", st.Created)
	delta, _ = sjson.SetBytes(delta, "model", model)
	delta, _ = sjson.SetBytes(delta, "choices.0.delta.content", text)
	out = append(out, delta)
	return out
}

func convertMintlifyNonStreamToOpenAI(_ context.Context, model string, _, _, raw []byte, _ *any) []byte {
	var full strings.Builder
	_ = mintlify.ReadLines(strings.NewReader(string(raw)), func(chunk mintlify.Chunk) error {
		if chunk.Type != "0" {
			return nil
		}
		if text, ok := mintlify.ParseTextDelta(chunk.Value); ok {
			full.WriteString(text)
		}
		return nil
	})
	out := []byte(`{"id":"","object":"chat.completion","created":0,"model":"","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`)
	out, _ = sjson.SetBytes(out, "id", "chatcmpl-"+mintlify.GenerateID()[:24])
	out, _ = sjson.SetBytes(out, "created", time.Now().Unix())
	out, _ = sjson.SetBytes(out, "model", model)
	out, _ = sjson.SetBytes(out, "choices.0.message.content", full.String())
	return out
}

func sseEvent(event string, payload []byte) []byte {
	var b strings.Builder
	b.WriteString("event: ")
	b.WriteString(event)
	b.WriteString("\ndata: ")
	b.Write(payload)
	b.WriteString("\n\n")
	return []byte(b.String())
}

func convertMintlifyStreamToOpenAIResponse(_ context.Context, model string, _, _, raw []byte, param *any) [][]byte {
	st := ensureOpenAIStreamState(param)
	line := strings.TrimSpace(string(raw))
	if line == "" {
		return nil
	}

	emitCreated := func() [][]byte {
		if st.Started {
			return nil
		}
		st.Started = true
		if st.ID == "" {
			st.ID = "resp_" + mintlify.GenerateID()[:24]
		}
		if st.ItemID == "" {
			st.ItemID = "msg_" + mintlify.GenerateID()[:20]
		}
		if st.Created == 0 {
			st.Created = time.Now().Unix()
		}

		created := []byte(`{"type":"response.created","response":{"id":"","object":"response","created_at":0,"status":"in_progress","model":"","output":[]}}`)
		created, _ = sjson.SetBytes(created, "response.id", st.ID)
		created, _ = sjson.SetBytes(created, "response.created_at", st.Created)
		created, _ = sjson.SetBytes(created, "response.model", model)
		st.Seq++

		itemAdded := []byte(`{"type":"response.output_item.added","output_index":0,"item":{"id":"","type":"message","status":"in_progress","role":"assistant","content":[]}}`)
		itemAdded, _ = sjson.SetBytes(itemAdded, "item.id", st.ItemID)
		st.Seq++

		partAdded := []byte(`{"type":"response.content_part.added","item_id":"","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`)
		partAdded, _ = sjson.SetBytes(partAdded, "item_id", st.ItemID)
		st.Seq++

		return [][]byte{
			sseEvent("response.created", created),
			sseEvent("response.output_item.added", itemAdded),
			sseEvent("response.content_part.added", partAdded),
		}
	}

	if line == streamDoneSentinel {
		if st.RespDone {
			return nil
		}
		st.RespDone = true
		var out [][]byte
		out = append(out, emitCreated()...)

		partDone := []byte(`{"type":"response.content_part.done","item_id":"","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`)
		partDone, _ = sjson.SetBytes(partDone, "item_id", st.ItemID)
		partDone, _ = sjson.SetBytes(partDone, "part.text", st.FullText.String())

		itemDone := []byte(`{"type":"response.output_item.done","output_index":0,"item":{"id":"","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":""}]}}`)
		itemDone, _ = sjson.SetBytes(itemDone, "item.id", st.ItemID)
		itemDone, _ = sjson.SetBytes(itemDone, "item.content.0.text", st.FullText.String())

		completed := []byte(`{"type":"response.completed","response":{"id":"","object":"response","created_at":0,"status":"completed","model":"","output":[{"id":"","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":""}]}]}}`)
		completed, _ = sjson.SetBytes(completed, "response.id", st.ID)
		completed, _ = sjson.SetBytes(completed, "response.created_at", st.Created)
		completed, _ = sjson.SetBytes(completed, "response.model", model)
		completed, _ = sjson.SetBytes(completed, "response.output.0.id", st.ItemID)
		completed, _ = sjson.SetBytes(completed, "response.output.0.content.0.text", st.FullText.String())

		out = append(out,
			sseEvent("response.content_part.done", partDone),
			sseEvent("response.output_item.done", itemDone),
			sseEvent("response.completed", completed),
		)
		return out
	}

	if len(line) < 2 || line[1] != ':' || line[0] != '0' {
		return nil
	}
	text, ok := mintlify.ParseTextDelta(line[2:])
	if !ok || text == "" {
		return nil
	}
	st.FullText.WriteString(text)

	var out [][]byte
	out = append(out, emitCreated()...)
	delta := []byte(`{"type":"response.output_text.delta","item_id":"","output_index":0,"content_index":0,"delta":""}`)
	delta, _ = sjson.SetBytes(delta, "item_id", st.ItemID)
	delta, _ = sjson.SetBytes(delta, "delta", text)
	out = append(out, sseEvent("response.output_text.delta", delta))
	return out
}

func convertMintlifyNonStreamToOpenAIResponse(_ context.Context, model string, _, _, raw []byte, _ *any) []byte {
	var full strings.Builder
	_ = mintlify.ReadLines(strings.NewReader(string(raw)), func(chunk mintlify.Chunk) error {
		if chunk.Type != "0" {
			return nil
		}
		if text, ok := mintlify.ParseTextDelta(chunk.Value); ok {
			full.WriteString(text)
		}
		return nil
	})
	id := "resp_" + mintlify.GenerateID()[:24]
	itemID := "msg_" + mintlify.GenerateID()[:20]
	out := []byte(`{"id":"","object":"response","created_at":0,"status":"completed","model":"","output":[{"id":"","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":""}]}]}`)
	out, _ = sjson.SetBytes(out, "id", id)
	out, _ = sjson.SetBytes(out, "created_at", time.Now().Unix())
	out, _ = sjson.SetBytes(out, "model", model)
	out, _ = sjson.SetBytes(out, "output.0.id", itemID)
	out, _ = sjson.SetBytes(out, "output.0.content.0.text", full.String())
	return out
}
