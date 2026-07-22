package provider

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"claude-code-chat/internal/mintlify"

	"github.com/tidwall/gjson"
)

const toolsBlockOpen = "<available_tools>"

// promptToolCall is one tool invocation parsed from model text.
type promptToolCall struct {
	ID   string
	Name string
	Args string
}

var (
	toolCallXMLRe = regexp.MustCompile(`(?s)<tool_call>\s*(?:<id>\s*(.*?)\s*</id>\s*)?<name>\s*(.*?)\s*</name>\s*<input>\s*(.*?)\s*</input>\s*</tool_call>`)
	legacyToolRe  = regexp.MustCompile(`(?s)\[tool_call id=([^\s\]]+)\s+name=([^\s\]]+)\]\s*(\{.*?\})(?:\n|$)`)
)

func requestHasTools(raw []byte) bool {
	tools := gjson.GetBytes(raw, "tools")
	return tools.Exists() && tools.IsArray() && len(tools.Array()) > 0
}

func buildToolsPromptFromRequest(raw []byte) string {
	tools := gjson.GetBytes(raw, "tools")
	if !tools.Exists() || !tools.IsArray() || len(tools.Array()) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("You have access to the following tools. Use them when they help complete the task.\n\n")
	b.WriteString(toolsBlockOpen)
	b.WriteByte('\n')

	tools.ForEach(func(_, tool gjson.Result) bool {
		name := tool.Get("function.name").String()
		desc := tool.Get("function.description").String()
		schema := tool.Get("function.parameters").Raw
		if name == "" {
			name = tool.Get("name").String()
			desc = tool.Get("description").String()
			schema = tool.Get("parameters").Raw
			if schema == "" {
				schema = tool.Get("input_schema").Raw
			}
		}
		if name == "" {
			return true
		}
		if schema == "" || schema == "null" {
			schema = "{}"
		}
		b.WriteString("<tool>\n")
		fmt.Fprintf(&b, "  <name>%s</name>\n", name)
		fmt.Fprintf(&b, "  <description>%s</description>\n", desc)
		fmt.Fprintf(&b, "  <input_schema>%s</input_schema>\n", compactJSON(schema))
		b.WriteString("</tool>\n")
		return true
	})

	b.WriteString("</available_tools>\n\n")
	b.WriteString("When you want to use a tool, respond with ONLY the following format and nothing else:\n")
	b.WriteString("<tool_call>\n<name>tool_name_here</name>\n<input>{\"key\": \"value\"}</input>\n</tool_call>\n\n")
	b.WriteString("You may include multiple <tool_call> blocks. When you have a final answer that does not require a tool, respond normally in prose.")
	return b.String()
}

func compactJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "{}"
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return raw
	}
	out, err := json.Marshal(v)
	if err != nil {
		return raw
	}
	return string(out)
}

func mergeToolsPrompt(msgs []mintlify.Message, prompt string) []mintlify.Message {
	if prompt == "" {
		return msgs
	}
	block := "[system]\n" + prompt
	if len(msgs) > 0 && strings.HasPrefix(msgs[0].Content, "[system]") {
		if strings.Contains(msgs[0].Content, toolsBlockOpen) {
			return msgs
		}
		merged := msgs[0].Content + "\n\n" + prompt
		out := make([]mintlify.Message, len(msgs))
		copy(out, msgs)
		out[0] = mintlify.NewUserMessage(merged)
		return out
	}
	return append([]mintlify.Message{mintlify.NewUserMessage(block)}, msgs...)
}

func formatPromptToolCall(id, name, args string) string {
	if args == "" {
		args = "{}"
	}
	var b strings.Builder
	b.WriteString("<tool_call>\n")
	if id != "" {
		fmt.Fprintf(&b, "<id>%s</id>\n", id)
	}
	fmt.Fprintf(&b, "<name>%s</name>\n", name)
	fmt.Fprintf(&b, "<input>%s</input>\n", args)
	b.WriteString("</tool_call>")
	return b.String()
}

// parsePromptToolCalls extracts tool calls from model text and returns cleaned prose.
func parsePromptToolCalls(text string) (clean string, tools []promptToolCall) {
	if text == "" {
		return "", nil
	}
	cleaned := text
	add := func(id, name, args string) {
		name = strings.TrimSpace(name)
		args = strings.TrimSpace(args)
		if name == "" {
			return
		}
		if args == "" {
			args = "{}"
		}
		var probe any
		if err := json.Unmarshal([]byte(args), &probe); err != nil {
			return
		}
		if id == "" {
			id = fmt.Sprintf("call_%s", mintlify.GenerateID()[:24])
		}
		tools = append(tools, promptToolCall{ID: id, Name: name, Args: args})
	}

	cleaned = toolCallXMLRe.ReplaceAllStringFunc(cleaned, func(m string) string {
		sub := toolCallXMLRe.FindStringSubmatch(m)
		if len(sub) == 4 {
			add(sub[1], sub[2], sub[3])
		}
		return ""
	})

	cleaned = legacyToolRe.ReplaceAllStringFunc(cleaned, func(m string) string {
		sub := legacyToolRe.FindStringSubmatch(m)
		if len(sub) == 4 {
			add(sub[1], sub[2], sub[3])
		}
		return ""
	})

	cleaned = strings.TrimSpace(cleaned)
	for strings.Contains(cleaned, "\n\n\n") {
		cleaned = strings.ReplaceAll(cleaned, "\n\n\n", "\n\n")
	}
	return cleaned, tools
}
