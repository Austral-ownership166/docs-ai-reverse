package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

const sampleMintlifyDocsStream = `f:{"messageId":"msg-1"}
9:{"toolCallId":"toolu_01Q9fK9d885ZpZD8G4ExQArk","toolName":"search","args":{"query":"对话"}}
a:{"toolCallId":"toolu_01Q9fK9d885ZpZD8G4ExQArk","result":{"type":"search","results":[{"path":"en/x","content":"hello"}]}}
e:{"finishReason":"tool-calls","usage":{"promptTokens":100,"completionTokens":10},"isContinued":false}
f:{"messageId":"msg-2"}
0:"找到了"
0:"文档"
e:{"finishReason":"stop","usage":{"promptTokens":200,"completionTokens":20},"isContinued":false}
d:{"finishReason":"stop","usage":{"promptTokens":200,"completionTokens":20}}
`

const samplePromptToolStream = `0:"<tool_call>\n"
0:"<name>get_weather</name>\n"
0:"<input>{\"city\":\"上海\"}</input>\n"
0:"</tool_call>"
e:{"finishReason":"stop","usage":{"promptTokens":10,"completionTokens":5}}
`

const toolsRequest = `{"model":"claude-docs","tools":[{"type":"function","function":{"name":"get_weather","description":"Weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}]}`

func feedStream(t *testing.T, fn func(ctx context.Context, model string, a, b, raw []byte, param *any) [][]byte, body string, original []byte) [][]byte {
	t.Helper()
	var param any
	var out [][]byte
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		chunks := fn(context.Background(), "claude-docs", original, nil, []byte(line), &param)
		out = append(out, chunks...)
	}
	chunks := fn(context.Background(), "claude-docs", original, nil, []byte(streamDoneSentinel), &param)
	out = append(out, chunks...)
	return out
}

func TestConvertMintlifyStreamToOpenAI_SwallowsServerSearch(t *testing.T) {
	chunks := feedStream(t, convertMintlifyStreamToOpenAI, sampleMintlifyDocsStream, nil)

	var sawTool, sawText, finish string
	for _, c := range chunks {
		root := gjson.ParseBytes(c)
		if name := root.Get("choices.0.delta.tool_calls.0.function.name").String(); name != "" {
			sawTool = name
		}
		if txt := root.Get("choices.0.delta.content").String(); txt != "" {
			sawText += txt
		}
		if fr := root.Get("choices.0.finish_reason").String(); fr != "" && fr != "null" {
			finish = fr
		}
	}
	if sawTool != "" {
		t.Fatalf("server search must not surface as client tool, got %q", sawTool)
	}
	if sawText != "找到了文档" {
		t.Fatalf("text=%q", sawText)
	}
	if finish != "stop" {
		t.Fatalf("finish_reason=%q want stop", finish)
	}
}

func TestConvertMintlifyStreamToOpenAI_PromptToolCalls(t *testing.T) {
	chunks := feedStream(t, convertMintlifyStreamToOpenAI, samplePromptToolStream, []byte(toolsRequest))

	var name, args, finish string
	for _, c := range chunks {
		root := gjson.ParseBytes(c)
		if n := root.Get("choices.0.delta.tool_calls.0.function.name").String(); n != "" {
			name = n
			args = root.Get("choices.0.delta.tool_calls.0.function.arguments").String()
		}
		if fr := root.Get("choices.0.finish_reason").String(); fr != "" && fr != "null" {
			finish = fr
		}
	}
	if name != "get_weather" {
		t.Fatalf("name=%q", name)
	}
	if !strings.Contains(args, "上海") {
		t.Fatalf("args=%q", args)
	}
	if finish != "tool_calls" {
		t.Fatalf("finish=%q want tool_calls", finish)
	}
}

func TestConvertMintlifyNonStreamToOpenAI_PromptToolCalls(t *testing.T) {
	out := convertMintlifyNonStreamToOpenAI(context.Background(), "claude-docs", []byte(toolsRequest), nil, []byte(samplePromptToolStream), nil)
	root := gjson.ParseBytes(out)
	if root.Get("choices.0.message.tool_calls.0.function.name").String() != "get_weather" {
		t.Fatalf("missing tool_calls: %s", out)
	}
	if root.Get("choices.0.message.content").Type != gjson.Null && root.Get("choices.0.message.content").String() != "" {
		t.Fatalf("content should be null/empty for tool-only: %s", out)
	}
	if root.Get("choices.0.finish_reason").String() != "tool_calls" {
		t.Fatalf("finish=%s", root.Get("choices.0.finish_reason").String())
	}
}

func TestConvertMintlifyNonStreamToOpenAI_DocsTextOnly(t *testing.T) {
	out := convertMintlifyNonStreamToOpenAI(context.Background(), "claude-docs", nil, nil, []byte(sampleMintlifyDocsStream), nil)
	root := gjson.ParseBytes(out)
	if root.Get("choices.0.message.tool_calls").Exists() {
		t.Fatalf("unexpected tool_calls: %s", out)
	}
	if root.Get("choices.0.message.content").String() != "找到了文档" {
		t.Fatalf("content=%s", root.Get("choices.0.message.content").String())
	}
	if root.Get("usage.prompt_tokens").Int() != 200 {
		t.Fatalf("usage prompt=%d", root.Get("usage.prompt_tokens").Int())
	}
}

func TestConvertMintlifyStreamToOpenAIResponse_PromptFunctionCall(t *testing.T) {
	chunks := feedStream(t, convertMintlifyStreamToOpenAIResponse, samplePromptToolStream, []byte(toolsRequest))

	var fcName, fcArgs, status string
	for _, c := range chunks {
		s := string(c)
		for _, line := range strings.Split(s, "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			root := gjson.Parse(data)
			switch root.Get("type").String() {
			case "response.output_item.added":
				if root.Get("item.type").String() == "function_call" {
					fcName = root.Get("item.name").String()
				}
			case "response.function_call_arguments.done":
				fcArgs = root.Get("arguments").String()
			case "response.completed":
				status = root.Get("response.status").String()
				if root.Get("response.output.0.type").String() != "function_call" {
					t.Fatalf("output[0] should be function_call: %s", data)
				}
			}
		}
	}
	if fcName != "get_weather" {
		t.Fatalf("fc name=%q", fcName)
	}
	if !strings.Contains(fcArgs, "上海") {
		t.Fatalf("fc args=%q", fcArgs)
	}
	if status != "requires_action" {
		t.Fatalf("status=%q", status)
	}
}

func TestConvertMintlifyNonStreamToOpenAIResponse_PromptFunctionCall(t *testing.T) {
	out := convertMintlifyNonStreamToOpenAIResponse(context.Background(), "claude-docs", []byte(toolsRequest), nil, []byte(samplePromptToolStream), nil)
	root := gjson.ParseBytes(out)
	if root.Get("status").String() != "requires_action" {
		t.Fatalf("status=%s body=%s", root.Get("status").String(), out)
	}
	if root.Get("output.0.type").String() != "function_call" {
		t.Fatalf("output[0]=%s body=%s", root.Get("output.0.type").String(), out)
	}
	if root.Get("output.0.name").String() != "get_weather" {
		t.Fatalf("name=%s", root.Get("output.0.name").String())
	}
}

func TestConvertOpenAIRequestToMintlify_InjectsTools(t *testing.T) {
	raw := []byte(`{
		"messages":[{"role":"user","content":"上海天气"}],
		"tools":[{"type":"function","function":{"name":"get_weather","description":"Get weather","parameters":{"type":"object"}}}]
	}`)
	out := convertOpenAIRequestToMintlify("claude-docs", raw, false)
	root := gjson.ParseBytes(out)
	msgs := root.Get("messages")
	if !msgs.IsArray() || len(msgs.Array()) < 2 {
		t.Fatalf("expected system+user messages: %s", out)
	}
	sys := msgs.Array()[0].Get("content").String()
	if !strings.Contains(sys, "<available_tools>") || !strings.Contains(sys, "get_weather") {
		t.Fatalf("missing tools prompt: %s", sys)
	}
	if !strings.Contains(sys, "<tool_call>") {
		t.Fatalf("missing protocol instructions: %s", sys)
	}
}

func TestMessagesFromOpenAIChat_ToolRoundTrip(t *testing.T) {
	raw := []byte(`{
		"messages":[
			{"role":"user","content":"hi"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"search","arguments":"{\"q\":\"x\"}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"result"}
		]
	}`)
	msgs := messagesFromOpenAIChat(raw)
	if len(msgs) != 3 {
		t.Fatalf("len=%d", len(msgs))
	}
	if !strings.Contains(msgs[1].Content, "<tool_call>") || !strings.Contains(msgs[1].Content, "search") {
		t.Fatalf("assistant=%s", msgs[1].Content)
	}
	if !strings.Contains(msgs[2].Content, "tool_result") {
		t.Fatalf("tool=%s", msgs[2].Content)
	}
}

func TestParsePromptToolCalls(t *testing.T) {
	clean, tools := parsePromptToolCalls(`ok
<tool_call>
<name>Bash</name>
<input>{"command":"ls"}</input>
</tool_call>`)
	if clean != "ok" {
		t.Fatalf("clean=%q", clean)
	}
	if len(tools) != 1 || tools[0].Name != "Bash" {
		t.Fatalf("tools=%v", tools)
	}
	if !strings.Contains(tools[0].Args, "ls") {
		t.Fatalf("args=%q", tools[0].Args)
	}
}
