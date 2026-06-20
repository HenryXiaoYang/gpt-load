package channel

import (
	"gpt-load/internal/models"
	"net/http"
	"net/url"
	"testing"
)

func TestRegisteredChannelsIncludesOpenAIImageGeneration(t *testing.T) {
	channels := GetChannels()
	for _, channelType := range channels {
		if channelType == "openai-image-generation" {
			return
		}
	}

	t.Fatalf("expected openai-image-generation to be registered, got %#v", channels)
}

func TestBuildOpenAIValidationPayloadUsesResponsesFormat(t *testing.T) {
	payload := buildOpenAIValidationPayload("/v1/responses", "gpt-5.5")

	if payload["model"] != "gpt-5.5" {
		t.Fatalf("unexpected model: %v", payload["model"])
	}
	if payload["input"] != "hi" {
		t.Fatalf("expected Responses API input payload, got %v", payload["input"])
	}
	if _, exists := payload["messages"]; exists {
		t.Fatal("Responses API validation payload must not include messages")
	}
}

func TestBuildOpenAIValidationPayloadUsesChatCompletionsFormat(t *testing.T) {
	payload := buildOpenAIValidationPayload("/v1/chat/completions", "gpt-4.1-nano")

	if payload["model"] != "gpt-4.1-nano" {
		t.Fatalf("unexpected model: %v", payload["model"])
	}
	if _, exists := payload["input"]; exists {
		t.Fatal("Chat Completions validation payload must not include input")
	}
	messages, ok := payload["messages"].([]map[string]string)
	if !ok || len(messages) != 1 {
		t.Fatalf("expected one chat message, got %#v", payload["messages"])
	}
	if messages[0]["role"] != "user" || messages[0]["content"] != "hi" {
		t.Fatalf("unexpected message payload: %#v", messages[0])
	}
}

func TestBuildOpenAIImageGenerationValidationPayloadUsesPrompt(t *testing.T) {
	payload := buildOpenAIImageGenerationValidationPayload("gpt-image-2")

	if payload["model"] != "gpt-image-2" {
		t.Fatalf("unexpected model: %v", payload["model"])
	}
	if payload["prompt"] != "cat" {
		t.Fatalf("expected image generation prompt cat, got %v", payload["prompt"])
	}
	if _, exists := payload["messages"]; exists {
		t.Fatal("Image generation validation payload must not include messages")
	}
	if _, exists := payload["input"]; exists {
		t.Fatal("Image generation validation payload must not include input")
	}
}

func TestInferOpenAITierFromHeaders(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		requests string
		tokens   string
		want     string
	}{
		{name: "gpt 4.1 tier 1", model: "gpt-4.1", requests: "500", tokens: "30000", want: "T1"},
		{name: "gpt 4.1 tier 2", model: "gpt-4.1-2025-04-14", requests: "5,000", tokens: "450,000", want: "T2"},
		{name: "gpt 4.1 nano tier 5", model: "gpt-4.1-nano", requests: "30000", tokens: "150000000", want: "T5"},
		{name: "gpt 5 tier 3 conflicting pair", model: "gpt-5", requests: "5000", tokens: "2000000", want: "T3"},
		{name: "gpt 5.5 tier 3", model: "gpt-5.5", requests: "5000", tokens: "2000000", want: "T3"},
		{name: "gpt 5.5 tier fallback", model: "gpt-5.5", requests: "6000", tokens: "2500000", want: "T3"},
		{name: "gpt 5.5 pro tier 3", model: "gpt-5.5-pro", requests: "500", tokens: "500000", want: "T3"},
		{name: "gpt 5 mini tier 5", model: "gpt-5-mini", requests: "30000", tokens: "180000000", want: "T5"},
		{name: "unknown model unambiguous pair", model: "custom-model", requests: "500", tokens: "30000", want: "T1"},
		{name: "unknown model conflicting pair", model: "custom-model", requests: "5000", tokens: "2000000", want: ""},
		{name: "unknown pair", model: "gpt-4.1", requests: "123", tokens: "456", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := http.Header{}
			headers.Set("x-ratelimit-limit-requests", tt.requests)
			headers.Set("x-ratelimit-limit-tokens", tt.tokens)

			if got := inferOpenAITierFromHeaders(tt.model, headers); got != tt.want {
				t.Fatalf("unexpected tier: got %q want %q", got, tt.want)
			}
		})
	}
}

func TestBuildOpenAIValidationResultOnlyUpdatesOfficialOpenAIAPI(t *testing.T) {
	headers := http.Header{}
	headers.Set("x-ratelimit-limit-requests", "500")
	headers.Set("x-ratelimit-limit-tokens", "200000")

	official := buildOpenAIValidationResult(mustParseURL(t, "https://api.openai.com/v1/chat/completions"), "gpt-4.1-nano", headers)
	if !official.IsValid || !official.OpenAITierUpdated || official.OpenAITier != "T1" || official.OpenAITierReason != "inferred" {
		t.Fatalf("unexpected official result: %#v", official)
	}

	compatible := buildOpenAIValidationResult(mustParseURL(t, "https://example.com/v1/chat/completions"), "gpt-4.1-nano", headers)
	if !compatible.IsValid || compatible.OpenAITierUpdated || compatible.OpenAITier != "" || compatible.OpenAITierReason != "not_official_openai" {
		t.Fatalf("unexpected compatible result: %#v", compatible)
	}
}

func TestBuildOpenAIValidationResultReturnsTierDiagnostics(t *testing.T) {
	headers := http.Header{}
	headers.Set("x-ratelimit-limit-requests", "5000")

	result := buildOpenAIValidationResult(mustParseURL(t, "https://api.openai.com/v1/chat/completions"), "gpt-5.5", headers)
	if !result.IsValid || !result.OpenAITierUpdated || result.OpenAITier != "" {
		t.Fatalf("unexpected validation result: %#v", result)
	}
	if result.OpenAIModel != "gpt-5.5" || result.OpenAIHost != "api.openai.com" {
		t.Fatalf("unexpected model or host diagnostics: %#v", result)
	}
	if result.OpenAIRequestsLimit != "5000" || result.OpenAITokensLimit != "" {
		t.Fatalf("unexpected rate limit diagnostics: %#v", result)
	}
	if result.OpenAITierReason != "missing_tokens_header" {
		t.Fatalf("unexpected tier reason: %#v", result)
	}
}

func TestBuildAnthropicValidationPayloadUsesMessagesFormat(t *testing.T) {
	payload := buildAnthropicValidationPayload("claude-sonnet-4-20250514")

	if payload["model"] != "claude-sonnet-4-20250514" {
		t.Fatalf("unexpected model: %v", payload["model"])
	}
	if payload["max_tokens"] != 100 {
		t.Fatalf("expected max_tokens, got %v", payload["max_tokens"])
	}
	if _, exists := payload["input"]; exists {
		t.Fatal("Anthropic validation payload must not include Responses API input")
	}
	messages, ok := payload["messages"].([]map[string]string)
	if !ok || len(messages) != 1 {
		t.Fatalf("expected one Anthropic message, got %#v", payload["messages"])
	}
	if messages[0]["role"] != "user" || messages[0]["content"] != "hi" {
		t.Fatalf("unexpected Anthropic message payload: %#v", messages[0])
	}
}

func TestGeminiBuildValidationRequestNativeFormat(t *testing.T) {
	ch := &GeminiChannel{
		BaseChannel: &BaseChannel{
			Name:               "gemini",
			Upstreams:          []UpstreamInfo{{URL: mustParseURL(t, "https://generativelanguage.googleapis.com")}},
			TestModel:          "gemini-2.0-flash-lite",
			ValidationEndpoint: "",
		},
	}

	reqURL, payload, authMode, err := ch.buildValidationRequest(&models.APIKey{KeyValue: "sk-test"})
	if err != nil {
		t.Fatalf("buildValidationRequest returned error: %v", err)
	}
	if authMode != "query" {
		t.Fatalf("expected query auth mode, got %q", authMode)
	}
	if reqURL != "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.0-flash-lite:generateContent?key=sk-test" {
		t.Fatalf("unexpected request URL: %s", reqURL)
	}
	if _, exists := payload["contents"]; !exists {
		t.Fatalf("expected native Gemini contents payload, got %#v", payload)
	}
}

func TestGeminiBuildValidationRequestOpenAICompatibleFormat(t *testing.T) {
	ch := &GeminiChannel{
		BaseChannel: &BaseChannel{
			Name:               "gemini",
			Upstreams:          []UpstreamInfo{{URL: mustParseURL(t, "https://generativelanguage.googleapis.com")}},
			TestModel:          "gemini-2.0-flash-lite",
			ValidationEndpoint: "/v1beta/openai/chat/completions",
		},
	}

	reqURL, payload, authMode, err := ch.buildValidationRequest(&models.APIKey{KeyValue: "sk-test"})
	if err != nil {
		t.Fatalf("buildValidationRequest returned error: %v", err)
	}
	if authMode != "bearer" {
		t.Fatalf("expected bearer auth mode, got %q", authMode)
	}
	if reqURL != "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions" {
		t.Fatalf("unexpected request URL: %s", reqURL)
	}
	if _, exists := payload["messages"]; !exists {
		t.Fatalf("expected OpenAI-compatible messages payload, got %#v", payload)
	}
	if _, exists := payload["contents"]; exists {
		t.Fatal("OpenAI-compatible validation payload must not include Gemini contents")
	}
}

func mustParseURL(t *testing.T, rawURL string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("failed to parse test URL: %v", err)
	}
	return parsed
}
