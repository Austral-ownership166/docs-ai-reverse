package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

const sampleMintlifyToolStream = `f:{"messageId":"msg-1"}
9:{"toolCallId":"toolu_01Q9fK9d885ZpZD8G4ExQArk","toolName":"search","args":{"query":"对话"}}
a:{"toolCallId":"toolu_01Q9fK9d885ZpZD8G4ExQArk","result":{"type":"search","results":[{"path":"en/x","content":"hello"}]}}
e:{"finishReason":"tool-calls","usage":{"promptTokens":100,"completionTokens":10},"isContinued":false}
f:{"messageId":"msg-2"}
0:"找到了"
0:"文档"
e:{"finishReason":"stop","usage":{"promptTokens":200,"completionTokens":20},"isContinued":false}
d:{"finishReason":"stop","usage":{"promptTokens":200,"completionTokens":20}}
`

func feedStream(t *testing.T, fn func(ctx context.Context, model string, a, b, raw []byte, param *any) [][]byte, body string) [][]byte {
	t.Helper()
	var param any
	var out [][]byte
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		chunks := fn(context.Background(), "claude-docs", nil, nil, []byte(line), &param)
		out = append(out, chunks...)
	}
	chunks := fn(context.Background(), "claude-docs", nil, nil, []byte(streamDoneSentinel), &param)
	out = append(out, chunks...)
	return out
}

func TestConvertMintlifyStreamToOpenAI_ToolCalls(t *testing.T) {
	chunks := feedStream(t, convertMintlifyStreamToOpenAI, sampleMintlifyToolStream)

	var sawTool, sawText, finish string
	for _, c := range chunks {
		root := gjson.ParseBytes(c)
		if root.Get("choices.0.delta.tool_calls.0.function.name").String() == "search" {
			sawTool = root.Get("choices.0.delta.tool_calls.0.id").String()
			args := root.Get("choices.0.delta.tool_calls.0.function.arguments").String()
			if !strings.Contains(args, "对话") {
				t.Fatalf("tool args missing query: %s", args)
			}
		}
		if txt := root.Get("choices.0.delta.content").String(); txt != "" {
			sawText += txt
		}
		if fr := root.Get("choices.0.finish_reason").String(); fr != "" && fr != "null" {
			finish = fr
		}
	}
	if sawTool != "toolu_01Q9fK9d885ZpZD8G4ExQArk" {
		t.Fatalf("missing tool call id, got %q", sawTool)
	}
	if sawText != "找到了文档" {
		t.Fatalf("text=%q", sawText)
	}
	if finish != "stop" {
		t.Fatalf("finish_reason=%q want stop (server-resolved tools)", finish)
	}
}

func TestConvertMintlifyNonStreamToOpenAI_ToolCalls(t *testing.T) {
	out := convertMintlifyNonStreamToOpenAI(context.Background(), "claude-docs", nil, nil, []byte(sampleMintlifyToolStream), nil)
	root := gjson.ParseBytes(out)
	if root.Get("choices.0.message.tool_calls.0.function.name").String() != "search" {
		t.Fatalf("missing tool_calls: %s", out)
	}
	if root.Get("choices.0.message.content").String() != "找到了文档" {
		t.Fatalf("content=%s", root.Get("choices.0.message.content").String())
	}
	if root.Get("choices.0.finish_reason").String() != "stop" {
		t.Fatalf("finish=%s", root.Get("choices.0.finish_reason").String())
	}
	if root.Get("usage.prompt_tokens").Int() != 200 {
		t.Fatalf("usage prompt=%d", root.Get("usage.prompt_tokens").Int())
	}
}

func TestConvertMintlifyStreamToOpenAIResponse_FunctionCall(t *testing.T) {
	chunks := feedStream(t, convertMintlifyStreamToOpenAIResponse, sampleMintlifyToolStream)

	var events []string
	var text string
	var fcName, fcArgs string
	var status string
	for _, c := range chunks {
		s := string(c)
		for _, line := range strings.Split(s, "\n") {
			if strings.HasPrefix(line, "event: ") {
				events = append(events, strings.TrimPrefix(line, "event: "))
			}
			if strings.HasPrefix(line, "data: ") {
				data := strings.TrimPrefix(line, "data: ")
				root := gjson.Parse(data)
				switch root.Get("type").String() {
				case "response.output_item.added":
					if root.Get("item.type").String() == "function_call" {
						fcName = root.Get("item.name").String()
					}
				case "response.function_call_arguments.done":
					fcArgs = root.Get("arguments").String()
				case "response.output_text.delta":
					text += root.Get("delta").String()
				case "response.completed":
					status = root.Get("response.status").String()
					if root.Get("response.output.0.type").String() != "function_call" {
						t.Fatalf("output[0] should be function_call: %s", data)
					}
					if root.Get("response.output.1.type").String() != "message" {
						t.Fatalf("output[1] should be message: %s", data)
					}
				}
			}
		}
	}
	if fcName != "search" {
		t.Fatalf("fc name=%q events=%v", fcName, events)
	}
	if !strings.Contains(fcArgs, "对话") {
		t.Fatalf("fc args=%q", fcArgs)
	}
	if text != "找到了文档" {
		t.Fatalf("text=%q", text)
	}
	if status != "completed" {
		t.Fatalf("status=%q", status)
	}
}

func TestConvertMintlifyNonStreamToOpenAIResponse_FunctionCall(t *testing.T) {
	out := convertMintlifyNonStreamToOpenAIResponse(context.Background(), "claude-docs", nil, nil, []byte(sampleMintlifyToolStream), nil)
	root := gjson.ParseBytes(out)
	if root.Get("output.0.type").String() != "function_call" {
		t.Fatalf("output[0]=%s body=%s", root.Get("output.0.type").String(), out)
	}
	if root.Get("output.0.name").String() != "search" {
		t.Fatalf("name=%s", root.Get("output.0.name").String())
	}
	if root.Get("output.1.type").String() != "message" {
		t.Fatalf("output[1]=%s", root.Get("output.1.type").String())
	}
	if root.Get("output.1.content.0.text").String() != "找到了文档" {
		t.Fatalf("text=%s", root.Get("output.1.content.0.text").String())
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
	if !strings.Contains(msgs[1].Content, "tool_call") || !strings.Contains(msgs[1].Content, "search") {
		t.Fatalf("assistant=%s", msgs[1].Content)
	}
	if !strings.Contains(msgs[2].Content, "tool_result") {
		t.Fatalf("tool=%s", msgs[2].Content)
	}
}

func TestPendingToolCallsFinishReason(t *testing.T) {
	body := `9:{"toolCallId":"toolu_x","toolName":"search","args":{"query":"x"}}
e:{"finishReason":"tool-calls","usage":{"promptTokens":1,"completionTokens":1},"isContinued":false}`
	chunks := feedStream(t, convertMintlifyStreamToOpenAI, body)
	var finish string
	for _, c := range chunks {
		if fr := gjson.GetBytes(c, "choices.0.finish_reason").String(); fr != "" && fr != "null" {
			finish = fr
		}
	}
	if finish != "tool_calls" {
		t.Fatalf("finish=%q want tool_calls", finish)
	}
}
