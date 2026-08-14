package channel

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestMistralConsoleRequestUsesBoraEndpointShape(t *testing.T) {
	input := []byte(`{"model":"glm-5-2","messages":[{"role":"system","content":"Be concise"},{"role":"user","content":"Hello"}],"max_tokens":12,"stream":false}`)
	output, err := openAIRequestToMistralConsole(input, "fallback")
	if err != nil {
		t.Fatal(err)
	}

	var request map[string]any
	if err := json.Unmarshal(output, &request); err != nil {
		t.Fatal(err)
	}
	if request["model"] != "glm-5-2" || request["stream"] != true {
		t.Fatalf("unexpected Bora request: %#v", request)
	}
	if request["instructions"] != "[system]\nBe concise" {
		t.Fatalf("system message was not converted: %#v", request["instructions"])
	}
	args := request["completion_args"].(map[string]any)
	if args["max_tokens"] != float64(12) || args["reasoning_effort"] != "high" {
		t.Fatalf("completion args were not converted: %#v", args)
	}
}

func TestMistralConsoleResponseConvertsBoraSSE(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"conversation.response.started","conversation_id":"conv-1"}`,
		``,
		`data: {"type":"message.output.delta","content":{"type":"thinking","thinking":[{"type":"text","text":"think"}]}}`,
		``,
		`data: {"type":"message.output.delta","content":"answer"}`,
		``,
		`data: {"type":"conversation.response.done","usage":{"input_tokens":2,"output_tokens":3}}`,
		``,
	}, "\n")
	output, err := mistralConsoleResponseToOpenAI([]byte(sse), "glm-5-2")
	if err != nil {
		t.Fatal(err)
	}

	var response struct {
		ID      string `json:"id"`
		Choices []struct {
			Message struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
		} `json:"choices"`
		Usage map[string]any `json:"usage"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatal(err)
	}
	if response.ID != "chatcmpl-conv-1" || response.Choices[0].Message.Content != "answer" || response.Choices[0].Message.ReasoningContent != "think" {
		t.Fatalf("unexpected OpenAI response: %s", output)
	}
	if response.Usage["prompt_tokens"] != float64(2) || response.Usage["completion_tokens"] != float64(3) {
		t.Fatalf("usage aliases were not normalized: %#v", response.Usage)
	}
}

func TestMistralConsoleStreamingResponseConvertsIncrementally(t *testing.T) {
	sse := "data: {\"type\":\"message.output.delta\",\"content\":\"ok\"}\n\ndata: {\"type\":\"conversation.response.done\"}\n\n"
	reader := newMistralConsoleSSEReader(io.NopCloser(strings.NewReader(sse)), "glm-5-2")
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), `"content":"ok"`) || !strings.Contains(string(output), "data: [DONE]") {
		t.Fatalf("unexpected stream: %s", output)
	}
}

func TestMistralConsoleCookieFormat(t *testing.T) {
	if _, err := validateMistralConsoleCookie("Cookie: session=x"); err == nil {
		t.Fatal("expected Cookie prefix to be rejected")
	}
	if _, err := validateMistralConsoleCookie("session=x; cf_clearance=y"); err == nil {
		t.Fatal("expected a cookie without ory_session_* to be rejected")
	}
	if got, err := validateMistralConsoleCookie("ory_session_test=session; cf_clearance=y"); err != nil || got != "ory_session_test=session; cf_clearance=y" {
		t.Fatalf("valid cookie rejected: %q, %v", got, err)
	}
	bare := strings.Repeat("a", 100)
	if got, err := validateMistralConsoleCookie(bare); err != nil || got != mistralConsoleSessionCookieName+"=\""+bare+"\"" {
		t.Fatalf("bare session value was not normalized: %q, %v", got, err)
	}
}
