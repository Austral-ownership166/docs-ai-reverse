package mintlify

import "testing"

func TestParseToolCall(t *testing.T) {
	tc, ok := ParseToolCall(`{"toolCallId":"toolu_01Ab","toolName":"search","args":{"query":"对话"}}`)
	if !ok {
		t.Fatal("expected parse ok")
	}
	if tc.ID != "toolu_01Ab" || tc.Name != "search" {
		t.Fatalf("unexpected tool call: %+v", tc)
	}
	if tc.ArgsJSONString() != `{"query":"对话"}` {
		t.Fatalf("args=%s", tc.ArgsJSONString())
	}
}

func TestParseToolResult(t *testing.T) {
	tr, ok := ParseToolResult(`{"toolCallId":"toolu_01Ab","result":{"type":"search","results":[]}}`)
	if !ok {
		t.Fatal("expected parse ok")
	}
	if tr.ID != "toolu_01Ab" {
		t.Fatalf("id=%s", tr.ID)
	}
	if tr.ResultJSONString() != `{"type":"search","results":[]}` {
		t.Fatalf("result=%s", tr.ResultJSONString())
	}
}

func TestParseFinish(t *testing.T) {
	fi, ok := ParseFinish(`{"finishReason":"tool-calls","usage":{"promptTokens":10,"completionTokens":5},"isContinued":false}`)
	if !ok {
		t.Fatal("expected parse ok")
	}
	if fi.Reason != "tool-calls" || fi.PromptTokens != 10 || fi.CompletionTokens != 5 {
		t.Fatalf("unexpected finish: %+v", fi)
	}
}
