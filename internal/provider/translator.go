package provider

import (
	"context"
	"encoding/json"
	"fmt"
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
			Stream:     convertMintlifyStreamToOpenAI,
			NonStream:  convertMintlifyNonStreamToOpenAI,
			TokenCount: func(_ context.Context, count int64) []byte { return buildOpenAIUsageJSON(count) },
		},
	)
	sdktr.Register(sdktr.FormatOpenAIResponse, formatMintlify,
		convertOpenAIResponseRequestToMintlify,
		sdktr.ResponseTransform{
			Stream:     convertMintlifyStreamToOpenAIResponse,
			NonStream:  convertMintlifyNonStreamToOpenAIResponse,
			TokenCount: func(_ context.Context, count int64) []byte { return buildOpenAIUsageJSON(count) },
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
		case "text", "output_text", "input_text", "":
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

func formatToolCallsText(toolCalls gjson.Result) string {
	if !toolCalls.Exists() || !toolCalls.IsArray() {
		return ""
	}
	var b strings.Builder
	toolCalls.ForEach(func(_, tc gjson.Result) bool {
		id := tc.Get("id").String()
		name := tc.Get("function.name").String()
		if name == "" {
			name = tc.Get("name").String()
		}
		args := tc.Get("function.arguments").String()
		if args == "" {
			if raw := tc.Get("arguments").Raw; raw != "" {
				args = raw
			} else {
				args = "{}"
			}
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(formatPromptToolCall(id, name, args))
		return true
	})
	return b.String()
}

func messagesFromOpenAIChat(raw []byte) []mintlify.Message {
	var out []mintlify.Message
	gjson.GetBytes(raw, "messages").ForEach(func(_, msg gjson.Result) bool {
		role := msg.Get("role").String()
		switch role {
		case "system":
			text := extractContentText(msg.Get("content"))
			if text == "" {
				return true
			}
			out = append(out, mintlify.NewUserMessage("[system]\n"+text))
		case "user":
			text := extractContentText(msg.Get("content"))
			if text == "" {
				return true
			}
			out = append(out, mintlify.NewUserMessage(text))
		case "assistant":
			text := extractContentText(msg.Get("content"))
			tools := formatToolCallsText(msg.Get("tool_calls"))
			switch {
			case text != "" && tools != "":
				out = append(out, mintlify.NewAssistantMessage(text+"\n"+tools))
			case tools != "":
				out = append(out, mintlify.NewAssistantMessage(tools))
			case text != "":
				out = append(out, mintlify.NewAssistantMessage(text))
			}
		case "tool":
			id := msg.Get("tool_call_id").String()
			text := extractContentText(msg.Get("content"))
			if text == "" {
				text = msg.Get("content").String()
			}
			out = append(out, mintlify.NewUserMessage(fmt.Sprintf("[tool_result id=%s]\n%s", id, text)))
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

		switch item.Get("type").String() {
		case "function_call":
			id := item.Get("call_id").String()
			if id == "" {
				id = item.Get("id").String()
			}
			name := item.Get("name").String()
			args := item.Get("arguments").String()
			if args == "" {
				args = "{}"
			}
			out = append(out, mintlify.NewAssistantMessage(formatPromptToolCall(id, name, args)))
			return true
		case "function_call_output":
			id := item.Get("call_id").String()
			output := item.Get("output").String()
			if output == "" {
				output = item.Get("output").Raw
			}
			out = append(out, mintlify.NewUserMessage(fmt.Sprintf("[tool_result id=%s]\n%s", id, output)))
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
			out = append(out, mintlify.NewUserMessage("[system]\n"+text))
			return true
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
	msgs := messagesFromOpenAIChat(raw)
	msgs = mergeToolsPrompt(msgs, buildToolsPromptFromRequest(raw))
	return buildMintlifyRequestJSON(msgs)
}

func convertOpenAIResponseRequestToMintlify(_ string, raw []byte, _ bool) []byte {
	msgs := messagesFromOpenAIResponses(raw)
	msgs = mergeToolsPrompt(msgs, buildToolsPromptFromRequest(raw))
	return buildMintlifyRequestJSON(msgs)
}

// streamDoneSentinel is emitted by the executor after upstream EOF so translators
// can flush finish / completed events.
const streamDoneSentinel = "[DONE]"

type recordedTool struct {
	ID       string
	Name     string
	Args     string
	Result   string
	Resolved bool
	ItemID   string // Responses: fc_<id>
	OutIndex int    // Responses output index
}

type openAIStreamState struct {
	ID       string
	Created  int64
	Started  bool
	Finished bool
	FullText strings.Builder

	// Chat Completions
	RoleSent bool

	// Responses API
	ItemID          string
	Seq             int
	RespDone        bool
	MessageOpen     bool
	ContentPartOpen bool
	NextOutIndex    int
	MsgOutIndex     int

	Tools            []recordedTool
	ToolIndex        map[string]int
	HasText          bool
	FinishReason     string
	PromptTokens     int64
	CompletionTokens int64

	// Prompt-tool mode: buffer type-0 text until EOF, then parse <tool_call>.
	BufferTools   bool
	ToolsKnown    bool
	ContentEmitted bool
}

func ensureOpenAIStreamState(param *any) *openAIStreamState {
	if param == nil {
		return &openAIStreamState{}
	}
	if *param == nil {
		*param = &openAIStreamState{
			Created:   time.Now().Unix(),
			ToolIndex: make(map[string]int),
		}
	}
	st := (*param).(*openAIStreamState)
	if st.ToolIndex == nil {
		st.ToolIndex = make(map[string]int)
	}
	return st
}

func (st *openAIStreamState) pendingToolCount() int {
	n := 0
	for i := range st.Tools {
		if !st.Tools[i].Resolved {
			n++
		}
	}
	return n
}

func (st *openAIStreamState) noteOriginalRequest(original []byte) {
	if st.ToolsKnown {
		return
	}
	st.ToolsKnown = true
	st.BufferTools = requestHasTools(original)
}

func (st *openAIStreamState) chatFinishReason() string {
	if st.pendingToolCount() > 0 {
		return "tool_calls"
	}
	switch st.FinishReason {
	case "tool-calls", "tool_calls":
		return "stop"
	case "length", "max_tokens":
		return "length"
	case "content_filter":
		return "content_filter"
	default:
		return "stop"
	}
}

func mapMintlifyFinishToResponsesStatus(st *openAIStreamState) string {
	if st.pendingToolCount() > 0 {
		return "requires_action"
	}
	return "completed"
}

func (st *openAIStreamState) emitChatToolCalls(model string, tools []promptToolCall) [][]byte {
	var out [][]byte
	ensureChatRole(st, model, &out)
	for _, tc := range tools {
		idx := len(st.Tools)
		st.Tools = append(st.Tools, recordedTool{
			ID:   tc.ID,
			Name: tc.Name,
			Args: tc.Args,
		})
		st.ToolIndex[tc.ID] = idx
		chunk := openAIChatChunk(st, model)
		chunk, _ = sjson.SetBytes(chunk, "choices.0.delta.tool_calls.0.index", idx)
		chunk, _ = sjson.SetBytes(chunk, "choices.0.delta.tool_calls.0.id", tc.ID)
		chunk, _ = sjson.SetBytes(chunk, "choices.0.delta.tool_calls.0.type", "function")
		chunk, _ = sjson.SetBytes(chunk, "choices.0.delta.tool_calls.0.function.name", tc.Name)
		chunk, _ = sjson.SetBytes(chunk, "choices.0.delta.tool_calls.0.function.arguments", tc.Args)
		out = append(out, chunk)
	}
	return out
}

func openAIChatChunk(st *openAIStreamState, model string) []byte {
	chunk := []byte(`{"id":"","object":"chat.completion.chunk","created":0,"model":"","choices":[{"index":0,"delta":{},"finish_reason":null}]}`)
	chunk, _ = sjson.SetBytes(chunk, "id", st.ID)
	chunk, _ = sjson.SetBytes(chunk, "created", st.Created)
	chunk, _ = sjson.SetBytes(chunk, "model", model)
	return chunk
}

func ensureChatRole(st *openAIStreamState, model string, out *[][]byte) {
	if st.RoleSent {
		return
	}
	st.RoleSent = true
	st.Started = true
	chunk := openAIChatChunk(st, model)
	chunk, _ = sjson.SetBytes(chunk, "choices.0.delta.role", "assistant")
	*out = append(*out, chunk)
}

func convertMintlifyStreamToOpenAI(_ context.Context, model string, originalReq, _, raw []byte, param *any) [][]byte {
	st := ensureOpenAIStreamState(param)
	st.noteOriginalRequest(originalReq)
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
		var out [][]byte
		if st.BufferTools {
			clean, tools := parsePromptToolCalls(st.FullText.String())
			st.FullText.Reset()
			st.FullText.WriteString(clean)
			if len(tools) > 0 {
				out = append(out, st.emitChatToolCalls(model, tools)...)
				st.HasText = strings.TrimSpace(clean) != ""
			} else if clean != "" {
				ensureChatRole(st, model, &out)
				delta := openAIChatChunk(st, model)
				delta, _ = sjson.SetBytes(delta, "choices.0.delta.content", clean)
				out = append(out, delta)
				st.ContentEmitted = true
				st.HasText = true
			}
		} else if !st.ContentEmitted && st.FullText.Len() > 0 {
			// Non-buffer path already streamed; nothing to flush for content.
		}
		ensureChatRole(st, model, &out)
		chunk := openAIChatChunk(st, model)
		chunk, _ = sjson.SetBytes(chunk, "choices.0.finish_reason", st.chatFinishReason())
		if st.PromptTokens > 0 || st.CompletionTokens > 0 {
			chunk, _ = sjson.SetBytes(chunk, "usage.prompt_tokens", st.PromptTokens)
			chunk, _ = sjson.SetBytes(chunk, "usage.completion_tokens", st.CompletionTokens)
			chunk, _ = sjson.SetBytes(chunk, "usage.total_tokens", st.PromptTokens+st.CompletionTokens)
		}
		out = append(out, chunk)
		return out
	}

	if len(line) < 2 || line[1] != ':' {
		return nil
	}
	typ := line[:1]
	val := line[2:]

	switch typ {
	case "0":
		text, ok := mintlify.ParseTextDelta(val)
		if !ok || text == "" {
			return nil
		}
		st.FullText.WriteString(text)
		if st.BufferTools {
			return nil
		}
		st.HasText = true
		st.ContentEmitted = true
		var out [][]byte
		ensureChatRole(st, model, &out)
		delta := openAIChatChunk(st, model)
		delta, _ = sjson.SetBytes(delta, "choices.0.delta.content", text)
		out = append(out, delta)
		return out

	case "9", "a":
		// Mintlify server-side RAG tools are not client-executable; swallow them.
		return nil

	case "e", "d":
		if fi, ok := mintlify.ParseFinish(val); ok {
			if fi.Reason != "" {
				st.FinishReason = fi.Reason
			}
			if fi.PromptTokens > 0 {
				st.PromptTokens = fi.PromptTokens
			}
			if fi.CompletionTokens > 0 {
				st.CompletionTokens = fi.CompletionTokens
			}
		}
		return nil

	default:
		return nil
	}
}

func convertMintlifyNonStreamToOpenAI(_ context.Context, model string, originalReq, _, raw []byte, _ *any) []byte {
	var st openAIStreamState
	st.ToolIndex = make(map[string]int)
	st.noteOriginalRequest(originalReq)
	_ = mintlify.ReadLines(strings.NewReader(string(raw)), func(chunk mintlify.Chunk) error {
		switch chunk.Type {
		case "0":
			if text, ok := mintlify.ParseTextDelta(chunk.Value); ok {
				st.FullText.WriteString(text)
			}
		case "e", "d":
			if fi, ok := mintlify.ParseFinish(chunk.Value); ok {
				if fi.Reason != "" {
					st.FinishReason = fi.Reason
				}
				if fi.PromptTokens > 0 {
					st.PromptTokens = fi.PromptTokens
				}
				if fi.CompletionTokens > 0 {
					st.CompletionTokens = fi.CompletionTokens
				}
			}
		}
		return nil
	})

	clean, tools := parsePromptToolCalls(st.FullText.String())
	if len(tools) > 0 {
		for _, tc := range tools {
			st.Tools = append(st.Tools, recordedTool{ID: tc.ID, Name: tc.Name, Args: tc.Args})
			st.ToolIndex[tc.ID] = len(st.Tools) - 1
		}
		st.HasText = strings.TrimSpace(clean) != ""
	} else if strings.TrimSpace(clean) != "" {
		st.HasText = true
	}

	out := []byte(`{"id":"","object":"chat.completion","created":0,"model":"","choices":[{"index":0,"message":{"role":"assistant","content":null},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`)
	out, _ = sjson.SetBytes(out, "id", "chatcmpl-"+mintlify.GenerateID()[:24])
	out, _ = sjson.SetBytes(out, "created", time.Now().Unix())
	out, _ = sjson.SetBytes(out, "model", model)
	if st.HasText && strings.TrimSpace(clean) != "" {
		out, _ = sjson.SetBytes(out, "choices.0.message.content", clean)
	} else {
		out, _ = sjson.SetBytes(out, "choices.0.message.content", nil)
	}
	for i, t := range st.Tools {
		path := fmt.Sprintf("choices.0.message.tool_calls.%d", i)
		out, _ = sjson.SetBytes(out, path+".id", t.ID)
		out, _ = sjson.SetBytes(out, path+".type", "function")
		out, _ = sjson.SetBytes(out, path+".function.name", t.Name)
		out, _ = sjson.SetBytes(out, path+".function.arguments", t.Args)
	}
	out, _ = sjson.SetBytes(out, "choices.0.finish_reason", st.chatFinishReason())
	out, _ = sjson.SetBytes(out, "usage.prompt_tokens", st.PromptTokens)
	out, _ = sjson.SetBytes(out, "usage.completion_tokens", st.CompletionTokens)
	out, _ = sjson.SetBytes(out, "usage.total_tokens", st.PromptTokens+st.CompletionTokens)
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

func (st *openAIStreamState) nextSeq() int {
	st.Seq++
	return st.Seq
}

func (st *openAIStreamState) emitResponseCreated(model string) [][]byte {
	if st.Started {
		return nil
	}
	st.Started = true
	if st.ID == "" {
		st.ID = "resp_" + mintlify.GenerateID()[:24]
	}
	if st.Created == 0 {
		st.Created = time.Now().Unix()
	}
	created := []byte(`{"type":"response.created","response":{"id":"","object":"response","created_at":0,"status":"in_progress","model":"","output":[]}}`)
	created, _ = sjson.SetBytes(created, "response.id", st.ID)
	created, _ = sjson.SetBytes(created, "response.created_at", st.Created)
	created, _ = sjson.SetBytes(created, "response.model", model)
	_ = st.nextSeq()
	return [][]byte{sseEvent("response.created", created)}
}

func (st *openAIStreamState) emitFunctionCall(tc mintlify.ToolCall) [][]byte {
	return st.emitFunctionCallParts(tc.ID, tc.Name, tc.ArgsJSONString())
}

func (st *openAIStreamState) emitFunctionCallParts(id, name, args string) [][]byte {
	idx := len(st.Tools)
	outIndex := st.NextOutIndex
	st.NextOutIndex++
	itemID := "fc_" + id
	st.Tools = append(st.Tools, recordedTool{
		ID:       id,
		Name:     name,
		Args:     args,
		ItemID:   itemID,
		OutIndex: outIndex,
	})
	st.ToolIndex[id] = idx

	var out [][]byte
	added := []byte(`{"type":"response.output_item.added","output_index":0,"item":{"id":"","type":"function_call","status":"in_progress","arguments":"","call_id":"","name":""}}`)
	added, _ = sjson.SetBytes(added, "output_index", outIndex)
	added, _ = sjson.SetBytes(added, "item.id", itemID)
	added, _ = sjson.SetBytes(added, "item.call_id", id)
	added, _ = sjson.SetBytes(added, "item.name", name)
	_ = st.nextSeq()
	out = append(out, sseEvent("response.output_item.added", added))

	delta := []byte(`{"type":"response.function_call_arguments.delta","item_id":"","output_index":0,"delta":""}`)
	delta, _ = sjson.SetBytes(delta, "item_id", itemID)
	delta, _ = sjson.SetBytes(delta, "output_index", outIndex)
	delta, _ = sjson.SetBytes(delta, "delta", args)
	_ = st.nextSeq()
	out = append(out, sseEvent("response.function_call_arguments.delta", delta))

	done := []byte(`{"type":"response.function_call_arguments.done","item_id":"","output_index":0,"arguments":""}`)
	done, _ = sjson.SetBytes(done, "item_id", itemID)
	done, _ = sjson.SetBytes(done, "output_index", outIndex)
	done, _ = sjson.SetBytes(done, "arguments", args)
	_ = st.nextSeq()
	out = append(out, sseEvent("response.function_call_arguments.done", done))

	itemDone := []byte(`{"type":"response.output_item.done","output_index":0,"item":{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}}`)
	itemDone, _ = sjson.SetBytes(itemDone, "output_index", outIndex)
	itemDone, _ = sjson.SetBytes(itemDone, "item.id", itemID)
	itemDone, _ = sjson.SetBytes(itemDone, "item.call_id", id)
	itemDone, _ = sjson.SetBytes(itemDone, "item.name", name)
	itemDone, _ = sjson.SetBytes(itemDone, "item.arguments", args)
	_ = st.nextSeq()
	out = append(out, sseEvent("response.output_item.done", itemDone))
	return out
}

func (st *openAIStreamState) ensureMessageOpened() [][]byte {
	if st.MessageOpen {
		return nil
	}
	st.MessageOpen = true
	if st.ItemID == "" {
		st.ItemID = "msg_" + mintlify.GenerateID()[:20]
	}
	st.MsgOutIndex = st.NextOutIndex
	st.NextOutIndex++

	var out [][]byte
	itemAdded := []byte(`{"type":"response.output_item.added","output_index":0,"item":{"id":"","type":"message","status":"in_progress","role":"assistant","content":[]}}`)
	itemAdded, _ = sjson.SetBytes(itemAdded, "output_index", st.MsgOutIndex)
	itemAdded, _ = sjson.SetBytes(itemAdded, "item.id", st.ItemID)
	_ = st.nextSeq()
	out = append(out, sseEvent("response.output_item.added", itemAdded))

	partAdded := []byte(`{"type":"response.content_part.added","item_id":"","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`)
	partAdded, _ = sjson.SetBytes(partAdded, "item_id", st.ItemID)
	partAdded, _ = sjson.SetBytes(partAdded, "output_index", st.MsgOutIndex)
	_ = st.nextSeq()
	out = append(out, sseEvent("response.content_part.added", partAdded))
	st.ContentPartOpen = true
	return out
}

func convertMintlifyStreamToOpenAIResponse(_ context.Context, model string, originalReq, _, raw []byte, param *any) [][]byte {
	st := ensureOpenAIStreamState(param)
	st.noteOriginalRequest(originalReq)
	line := strings.TrimSpace(string(raw))
	if line == "" {
		return nil
	}

	if line == streamDoneSentinel {
		if st.RespDone {
			return nil
		}
		st.RespDone = true
		var out [][]byte
		out = append(out, st.emitResponseCreated(model)...)

		if st.BufferTools {
			clean, tools := parsePromptToolCalls(st.FullText.String())
			st.FullText.Reset()
			st.FullText.WriteString(clean)
			for _, tc := range tools {
				out = append(out, st.emitFunctionCallParts(tc.ID, tc.Name, tc.Args)...)
			}
			st.HasText = strings.TrimSpace(clean) != ""
		}

		if st.MessageOpen || st.HasText {
			out = append(out, st.ensureMessageOpened()...)
			if st.BufferTools && !st.ContentEmitted && st.FullText.Len() > 0 {
				delta := []byte(`{"type":"response.output_text.delta","item_id":"","output_index":0,"content_index":0,"delta":""}`)
				delta, _ = sjson.SetBytes(delta, "item_id", st.ItemID)
				delta, _ = sjson.SetBytes(delta, "output_index", st.MsgOutIndex)
				delta, _ = sjson.SetBytes(delta, "delta", st.FullText.String())
				_ = st.nextSeq()
				out = append(out, sseEvent("response.output_text.delta", delta))
				st.ContentEmitted = true
			}
			partDone := []byte(`{"type":"response.content_part.done","item_id":"","output_index":0,"content_index":0,"part":{"type":"output_text","text":""}}`)
			partDone, _ = sjson.SetBytes(partDone, "item_id", st.ItemID)
			partDone, _ = sjson.SetBytes(partDone, "output_index", st.MsgOutIndex)
			partDone, _ = sjson.SetBytes(partDone, "part.text", st.FullText.String())
			_ = st.nextSeq()

			itemDone := []byte(`{"type":"response.output_item.done","output_index":0,"item":{"id":"","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":""}]}}`)
			itemDone, _ = sjson.SetBytes(itemDone, "output_index", st.MsgOutIndex)
			itemDone, _ = sjson.SetBytes(itemDone, "item.id", st.ItemID)
			itemDone, _ = sjson.SetBytes(itemDone, "item.content.0.text", st.FullText.String())
			_ = st.nextSeq()

			out = append(out,
				sseEvent("response.content_part.done", partDone),
				sseEvent("response.output_item.done", itemDone),
			)
		}

		status := mapMintlifyFinishToResponsesStatus(st)
		completed := []byte(`{"type":"response.completed","response":{"id":"","object":"response","created_at":0,"status":"completed","model":"","output":[]}}`)
		completed, _ = sjson.SetBytes(completed, "response.id", st.ID)
		completed, _ = sjson.SetBytes(completed, "response.created_at", st.Created)
		completed, _ = sjson.SetBytes(completed, "response.model", model)
		completed, _ = sjson.SetBytes(completed, "response.status", status)

		outputs := []byte(`[]`)
		for _, t := range st.Tools {
			item := []byte(`{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}`)
			item, _ = sjson.SetBytes(item, "id", t.ItemID)
			item, _ = sjson.SetBytes(item, "call_id", t.ID)
			item, _ = sjson.SetBytes(item, "name", t.Name)
			item, _ = sjson.SetBytes(item, "arguments", t.Args)
			outputs, _ = sjson.SetRawBytes(outputs, fmt.Sprintf("%d", t.OutIndex), item)
		}
		if st.MessageOpen || st.HasText {
			msg := []byte(`{"id":"","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":""}]}`)
			msg, _ = sjson.SetBytes(msg, "id", st.ItemID)
			msg, _ = sjson.SetBytes(msg, "content.0.text", st.FullText.String())
			outputs, _ = sjson.SetRawBytes(outputs, fmt.Sprintf("%d", st.MsgOutIndex), msg)
		}
		completed, _ = sjson.SetRawBytes(completed, "response.output", outputs)
		if st.PromptTokens > 0 || st.CompletionTokens > 0 {
			completed, _ = sjson.SetBytes(completed, "response.usage.input_tokens", st.PromptTokens)
			completed, _ = sjson.SetBytes(completed, "response.usage.output_tokens", st.CompletionTokens)
			completed, _ = sjson.SetBytes(completed, "response.usage.total_tokens", st.PromptTokens+st.CompletionTokens)
		}
		_ = st.nextSeq()
		out = append(out, sseEvent("response.completed", completed))
		return out
	}

	if len(line) < 2 || line[1] != ':' {
		return nil
	}
	typ := line[:1]
	val := line[2:]

	switch typ {
	case "0":
		text, ok := mintlify.ParseTextDelta(val)
		if !ok || text == "" {
			return nil
		}
		st.FullText.WriteString(text)
		if st.BufferTools {
			return nil
		}
		st.HasText = true
		st.ContentEmitted = true
		var out [][]byte
		out = append(out, st.emitResponseCreated(model)...)
		out = append(out, st.ensureMessageOpened()...)
		delta := []byte(`{"type":"response.output_text.delta","item_id":"","output_index":0,"content_index":0,"delta":""}`)
		delta, _ = sjson.SetBytes(delta, "item_id", st.ItemID)
		delta, _ = sjson.SetBytes(delta, "output_index", st.MsgOutIndex)
		delta, _ = sjson.SetBytes(delta, "delta", text)
		_ = st.nextSeq()
		out = append(out, sseEvent("response.output_text.delta", delta))
		return out

	case "9", "a":
		return nil

	case "e", "d":
		if fi, ok := mintlify.ParseFinish(val); ok {
			if fi.Reason != "" {
				st.FinishReason = fi.Reason
			}
			if fi.PromptTokens > 0 {
				st.PromptTokens = fi.PromptTokens
			}
			if fi.CompletionTokens > 0 {
				st.CompletionTokens = fi.CompletionTokens
			}
		}
		return nil

	default:
		return nil
	}
}

func convertMintlifyNonStreamToOpenAIResponse(_ context.Context, model string, originalReq, _, raw []byte, _ *any) []byte {
	var st openAIStreamState
	st.ToolIndex = make(map[string]int)
	st.noteOriginalRequest(originalReq)
	_ = mintlify.ReadLines(strings.NewReader(string(raw)), func(chunk mintlify.Chunk) error {
		switch chunk.Type {
		case "0":
			if text, ok := mintlify.ParseTextDelta(chunk.Value); ok {
				st.FullText.WriteString(text)
			}
		case "e", "d":
			if fi, ok := mintlify.ParseFinish(chunk.Value); ok {
				if fi.Reason != "" {
					st.FinishReason = fi.Reason
				}
				if fi.PromptTokens > 0 {
					st.PromptTokens = fi.PromptTokens
				}
				if fi.CompletionTokens > 0 {
					st.CompletionTokens = fi.CompletionTokens
				}
			}
		}
		return nil
	})

	clean, tools := parsePromptToolCalls(st.FullText.String())
	for _, tc := range tools {
		outIndex := st.NextOutIndex
		st.NextOutIndex++
		st.ToolIndex[tc.ID] = len(st.Tools)
		st.Tools = append(st.Tools, recordedTool{
			ID: tc.ID, Name: tc.Name, Args: tc.Args,
			ItemID: "fc_" + tc.ID, OutIndex: outIndex,
		})
	}
	st.HasText = strings.TrimSpace(clean) != ""

	id := "resp_" + mintlify.GenerateID()[:24]
	msgID := "msg_" + mintlify.GenerateID()[:20]
	status := mapMintlifyFinishToResponsesStatus(&st)
	out := []byte(`{"id":"","object":"response","created_at":0,"status":"completed","model":"","output":[]}`)
	out, _ = sjson.SetBytes(out, "id", id)
	out, _ = sjson.SetBytes(out, "created_at", time.Now().Unix())
	out, _ = sjson.SetBytes(out, "model", model)
	out, _ = sjson.SetBytes(out, "status", status)

	outputs := []byte(`[]`)
	for _, t := range st.Tools {
		item := []byte(`{"id":"","type":"function_call","status":"completed","arguments":"","call_id":"","name":""}`)
		item, _ = sjson.SetBytes(item, "id", t.ItemID)
		item, _ = sjson.SetBytes(item, "call_id", t.ID)
		item, _ = sjson.SetBytes(item, "name", t.Name)
		item, _ = sjson.SetBytes(item, "arguments", t.Args)
		outputs, _ = sjson.SetRawBytes(outputs, fmt.Sprintf("%d", t.OutIndex), item)
	}
	if st.HasText || len(st.Tools) == 0 {
		msgIndex := st.NextOutIndex
		msg := []byte(`{"id":"","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":""}]}`)
		msg, _ = sjson.SetBytes(msg, "id", msgID)
		msg, _ = sjson.SetBytes(msg, "content.0.text", clean)
		outputs, _ = sjson.SetRawBytes(outputs, fmt.Sprintf("%d", msgIndex), msg)
	}
	out, _ = sjson.SetRawBytes(out, "output", outputs)
	out, _ = sjson.SetBytes(out, "usage.input_tokens", st.PromptTokens)
	out, _ = sjson.SetBytes(out, "usage.output_tokens", st.CompletionTokens)
	out, _ = sjson.SetBytes(out, "usage.total_tokens", st.PromptTokens+st.CompletionTokens)
	return out
}
