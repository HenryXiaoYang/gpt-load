package channel

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestOpenAIRequestToMistralPreservesReasoningHistory(t *testing.T) {
	input := []byte(`{
		"model":"mistral-small-latest",
		"reasoning_effort":"max",
		"messages":[
			{"role":"user","content":"question"},
			{"role":"assistant","reasoning_content":"think","content":"answer","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},
			{"role":"assistant","reasoning_content":"duplicate","content":[{"type":"thinking","thinking":[{"type":"text","text":"native"}],"closed":true},{"type":"text","text":"kept"}]}
		]
	}`)

	var got struct {
		ReasoningEffort string `json:"reasoning_effort"`
		Messages        []struct {
			ReasoningContent json.RawMessage  `json:"reasoning_content"`
			Content          json.RawMessage  `json:"content"`
			ToolCalls        []map[string]any `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(openAIRequestToMistral(input), &got); err != nil {
		t.Fatal(err)
	}
	if got.ReasoningEffort != "max" {
		t.Fatalf("reasoning_effort changed: %q", got.ReasoningEffort)
	}
	if got.Messages[1].ReasoningContent != nil || len(got.Messages[1].ToolCalls) != 1 {
		t.Fatalf("assistant extensions not converted cleanly: %#v", got.Messages[1])
	}
	var converted, native []map[string]any
	if json.Unmarshal(got.Messages[1].Content, &converted) != nil || json.Unmarshal(got.Messages[2].Content, &native) != nil {
		t.Fatalf("assistant content was not converted to chunks: %s", got.Messages[1].Content)
	}
	assertThinkingText(t, converted, "think")
	if converted[1]["text"] != "answer" {
		t.Fatalf("final answer not preserved: %#v", converted)
	}
	if len(native) != 2 {
		t.Fatalf("native thinking duplicated: %#v", native)
	}
	assertThinkingText(t, native, "native")
}

func TestMistralResponseToOpenAI(t *testing.T) {
	input := []byte(`{
		"id":"chat_1",
		"choices":[
			{"index":0,"message":{"role":"assistant","content":[{"type":"thinking","thinking":[{"type":"text","text":"one"},{"type":"text","text":" two"}],"closed":true},{"type":"text","text":"four"}],"tool_calls":[{"id":"call_1"}]},"finish_reason":"stop"},
			{"index":1,"message":{"role":"assistant","content":"plain"},"finish_reason":"stop"}
		],
		"usage":{"completion_tokens":3}
	}`)

	var got struct {
		Choices []struct {
			Message struct {
				Content          json.RawMessage  `json:"content"`
				ReasoningContent string           `json:"reasoning_content"`
				ToolCalls        []map[string]any `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage map[string]int `json:"usage"`
	}
	if err := json.Unmarshal(mistralResponseToOpenAI(input, false), &got); err != nil {
		t.Fatal(err)
	}
	if got.Choices[0].Message.ReasoningContent != "one two" || string(got.Choices[0].Message.Content) != `"four"` {
		t.Fatalf("unexpected converted message: %#v", got.Choices[0].Message)
	}
	if len(got.Choices[0].Message.ToolCalls) != 1 || string(got.Choices[1].Message.Content) != `"plain"` || got.Usage["completion_tokens"] != 3 {
		t.Fatalf("unrelated response fields changed: %#v", got)
	}
}

func TestMistralResponseLeavesUnknownChunksUntouched(t *testing.T) {
	input := []byte(`{"choices":[{"message":{"content":[{"type":"reference","reference_ids":[1]}]}}]}`)
	if got := mistralResponseToOpenAI(input, false); string(got) != string(input) {
		t.Fatalf("unknown content should pass through: %s", got)
	}
}

func TestMistralStreamingResponseToOpenAI(t *testing.T) {
	input := strings.Join([]string{
		`: heartbeat`,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":"think"}],"closed":false}]}}]}`,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":"ing"}],"closed":true},{"type":"text","text":"an"}]}}]}`,
		`data: {"id":"chat_1","choices":[{"index":0,"delta":{"content":"swer"},"finish_reason":"stop"}]}`,
		`data: [DONE]`,
		``,
	}, "\n")

	got, err := io.ReadAll(newMistralSSEReader(io.NopCloser(strings.NewReader(input))))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(got), "\n")
	if lines[0] != ": heartbeat" || lines[4] != "data: [DONE]" {
		t.Fatalf("SSE framing changed: %s", got)
	}
	first := streamDelta(t, lines[1])
	if first["reasoning_content"] != "think" {
		t.Fatalf("thinking delta not converted: %#v", first)
	}
	if _, exists := first["content"]; exists {
		t.Fatalf("thinking delta retained Mistral content: %#v", first)
	}
	transition := streamDelta(t, lines[2])
	if transition["reasoning_content"] != "ing" || transition["content"] != "an" {
		t.Fatalf("transition delta not converted: %#v", transition)
	}
	answer := streamDelta(t, lines[3])
	if answer["content"] != "swer" {
		t.Fatalf("answer delta changed: %#v", answer)
	}
}

func assertThinkingText(t *testing.T, parts []map[string]any, want string) {
	t.Helper()
	thinking, ok := parts[0]["thinking"].([]any)
	if !ok || len(thinking) != 1 {
		t.Fatalf("invalid thinking chunk: %#v", parts)
	}
	text, ok := thinking[0].(map[string]any)["text"].(string)
	if !ok || text != want || parts[0]["closed"] != true {
		t.Fatalf("unexpected thinking chunk: %#v", parts[0])
	}
}

func streamDelta(t *testing.T, line string) map[string]any {
	t.Helper()
	var event struct {
		Choices []struct {
			Delta map[string]any `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event); err != nil {
		t.Fatal(err)
	}
	return event.Choices[0].Delta
}
