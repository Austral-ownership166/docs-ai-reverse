package mintlify

import (
	"encoding/json"

	"github.com/tidwall/gjson"
)

// ToolCall is a Mintlify data-stream type-9 tool invocation.
type ToolCall struct {
	ID   string
	Name string
	Args json.RawMessage
}

// ToolResult is a Mintlify data-stream type-a tool result.
type ToolResult struct {
	ID     string
	Result json.RawMessage
}

// FinishInfo is a Mintlify type-e / type-d finish payload.
type FinishInfo struct {
	Reason            string
	PromptTokens      int64
	CompletionTokens  int64
	IsContinued       bool
}

// ParseToolCall unmarshals a type-9 chunk value.
func ParseToolCall(value string) (ToolCall, bool) {
	id := gjson.Get(value, "toolCallId").String()
	name := gjson.Get(value, "toolName").String()
	if id == "" || name == "" {
		return ToolCall{}, false
	}
	args := json.RawMessage(gjson.Get(value, "args").Raw)
	if len(args) == 0 || string(args) == "" {
		args = json.RawMessage(`{}`)
	}
	return ToolCall{ID: id, Name: name, Args: args}, true
}

// ParseToolResult unmarshals a type-a chunk value.
func ParseToolResult(value string) (ToolResult, bool) {
	id := gjson.Get(value, "toolCallId").String()
	if id == "" {
		return ToolResult{}, false
	}
	raw := gjson.Get(value, "result").Raw
	if raw == "" {
		raw = "null"
	}
	return ToolResult{ID: id, Result: json.RawMessage(raw)}, true
}

// ParseFinish unmarshals a type-e / type-d chunk value.
func ParseFinish(value string) (FinishInfo, bool) {
	if !gjson.Valid(value) {
		return FinishInfo{}, false
	}
	reason := gjson.Get(value, "finishReason").String()
	fi := FinishInfo{
		Reason:           reason,
		PromptTokens:     gjson.Get(value, "usage.promptTokens").Int(),
		CompletionTokens: gjson.Get(value, "usage.completionTokens").Int(),
		IsContinued:      gjson.Get(value, "isContinued").Bool(),
	}
	return fi, reason != "" || fi.PromptTokens > 0 || fi.CompletionTokens > 0
}

// ParseMessageID unmarshals a type-f chunk value.
func ParseMessageID(value string) (string, bool) {
	id := gjson.Get(value, "messageId").String()
	return id, id != ""
}

// ArgsJSONString returns tool args as a JSON object string for OpenAI function.arguments.
func (t ToolCall) ArgsJSONString() string {
	if len(t.Args) == 0 {
		return "{}"
	}
	s := string(t.Args)
	if s == "" || s == "null" {
		return "{}"
	}
	return s
}

// ResultJSONString returns tool result JSON for embedding / Responses output.
func (t ToolResult) ResultJSONString() string {
	if len(t.Result) == 0 {
		return "null"
	}
	return string(t.Result)
}
