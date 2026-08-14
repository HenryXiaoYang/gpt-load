package channel

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/models"
	"gpt-load/internal/utils"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	mistralConsoleEndpoint          = "/api-ui/bora/v1/conversations"
	mistralConsoleMaxTokens         = uint64(1_000_000)
	mistralConsoleSessionCookieName = "ory_session_coolcurranf83m3srkfl"
)

func init() {
	Register("mistral-console", newMistralConsoleChannel)
}

type MistralConsoleChannel struct {
	*OpenAIChannel
}

func newMistralConsoleChannel(f *Factory, group *models.Group) (ChannelProxy, error) {
	base, err := f.newBaseChannel("mistral-console", group)
	if err != nil {
		return nil, err
	}
	return &MistralConsoleChannel{OpenAIChannel: &OpenAIChannel{BaseChannel: base}}, nil
}

func (ch *MistralConsoleChannel) BuildUpstreamURL(_ *url.URL, _ string) (string, error) {
	base := ch.getUpstreamURL()
	if base == nil {
		return "", fmt.Errorf("no upstream URL configured for channel %s", ch.Name)
	}
	final := *base
	final.Path = strings.TrimRight(final.Path, "/") + mistralConsoleEndpoint
	final.RawQuery = ""
	return final.String(), nil
}

func (ch *MistralConsoleChannel) ModifyRequest(req *http.Request, apiKey *models.APIKey, _ *models.Group) {
	req.Header.Del("Authorization")
	if cookie, err := validateMistralConsoleCookie(apiKey.KeyValue); err == nil {
		req.Header.Set("Cookie", cookie)
	} else {
		req.Header.Del("Cookie")
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Del("Accept-Encoding")
}

func (ch *MistralConsoleChannel) ApplyModelRedirect(req *http.Request, body []byte, group *models.Group) ([]byte, error) {
	body, err := ch.BaseChannel.ApplyModelRedirect(req, body, group)
	if err != nil {
		return body, err
	}
	return openAIRequestToMistralConsole(body, ch.TestModel)
}

func (ch *MistralConsoleChannel) ValidateKey(ctx context.Context, apiKey *models.APIKey, group *models.Group) (KeyValidationResult, error) {
	cookie, err := validateMistralConsoleCookie(apiKey.KeyValue)
	if err != nil {
		return KeyValidationResult{}, err
	}
	upstream := ch.getUpstreamURL()
	if upstream == nil {
		return KeyValidationResult{}, fmt.Errorf("no upstream URL configured for channel %s", ch.Name)
	}
	endpoint := *upstream
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + mistralConsoleEndpoint
	endpoint.RawQuery = ""
	body, err := openAIRequestToMistralConsole([]byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"Say OK"}]}`, ch.TestModel)), ch.TestModel)
	if err != nil {
		return KeyValidationResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return KeyValidationResult{}, err
	}
	req.Header.Set("Cookie", cookie)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	if len(group.HeaderRuleList) > 0 {
		utils.ApplyHeaderRules(req, group.HeaderRuleList, utils.NewHeaderVariableContext(group, apiKey))
	}
	resp, err := ch.HTTPClient.Do(req)
	if err != nil {
		return KeyValidationResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return KeyValidationResult{IsValid: true}, nil
	}
	errorBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return KeyValidationResult{}, fmt.Errorf("key is invalid (status %d): %w", resp.StatusCode, readErr)
	}
	return KeyValidationResult{}, fmt.Errorf("[status %d] %s", resp.StatusCode, app_errors.ParseUpstreamError(errorBody))
}

func (ch *MistralConsoleChannel) TransformResponse(req *http.Request, resp *http.Response, stream bool) {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return
	}
	resp.Header.Del("Content-Length")
	if stream {
		resp.Body = newMistralConsoleSSEReader(resp.Body, ch.TestModel)
		resp.ContentLength = -1
		return
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
	converted, err := mistralConsoleResponseToOpenAI(body, ch.TestModel)
	if err != nil {
		return
	}
	resp.Body = io.NopCloser(bytes.NewReader(converted))
	resp.ContentLength = int64(len(converted))
	resp.Header.Set("Content-Type", "application/json")
	resp.Header.Set("Content-Length", fmt.Sprint(len(converted)))
}

func validateMistralConsoleCookie(value string) (string, error) {
	cookie := strings.TrimSpace(value)
	if cookie == "" {
		return "", fmt.Errorf("Mistral Console Cookie is empty")
	}
	if strings.ContainsAny(cookie, "\r\n") {
		return "", fmt.Errorf("Mistral Console Cookie must not contain CR or LF characters")
	}
	if strings.HasPrefix(strings.ToLower(cookie), "cookie:") {
		return "", fmt.Errorf("enter only the Cookie header value, without the Cookie: prefix")
	}
	if mistralConsoleHasSessionCookie(cookie) {
		return cookie, nil
	}

	session := cookie
	if len(session) >= 2 && strings.HasPrefix(session, "\"") && strings.HasSuffix(session, "\"") {
		session = session[1 : len(session)-1]
	}
	if !mistralConsoleIsBareSessionValue(session) {
		return "", fmt.Errorf("channel credential must contain a valid Mistral Console session cookie or session value")
	}
	return mistralConsoleSessionCookieName + "=\"" + session + "\"", nil
}

func mistralConsoleHasSessionCookie(cookie string) bool {
	for _, part := range strings.Split(cookie, ";") {
		pair := strings.TrimSpace(part)
		equals := strings.IndexByte(pair, '=')
		if equals <= len("ory_session_") || !strings.HasPrefix(pair, "ory_session_") {
			continue
		}
		if strings.TrimSpace(pair[equals+1:]) != "" {
			return true
		}
	}
	return false
}

func mistralConsoleIsBareSessionValue(value string) bool {
	if len(value) < 100 || strings.ContainsAny(value, " \t;\"") {
		return false
	}
	unpadded := strings.TrimRight(value, "=")
	if unpadded == "" || strings.Contains(unpadded, "=") {
		return false
	}
	for _, char := range unpadded {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}

func openAIRequestToMistralConsole(body []byte, fallbackModel string) ([]byte, error) {
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, err
	}
	model, _ := request["model"].(string)
	if model == "" {
		model = fallbackModel
	}
	messages, ok := request["messages"].([]any)
	if !ok || len(messages) == 0 {
		return nil, fmt.Errorf("Mistral Console requires at least one message")
	}

	instructions := make([]string, 0)
	inputs := make([]map[string]any, 0, len(messages))
	for index, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("message %d must be an object", index)
		}
		role, _ := message["role"].(string)
		text, err := consoleMessageText(message["content"])
		if err != nil {
			return nil, fmt.Errorf("message %d: %w", index, err)
		}
		switch role {
		case "system", "developer":
			if text != "" {
				instructions = append(instructions, "["+role+"]\n"+text)
			}
		case "user", "assistant":
			if text != "" {
				entryType := "message.input"
				if role == "assistant" {
					entryType = "message.output"
				}
				entry := map[string]any{"object": "entry", "type": entryType, "role": role, "content": text}
				if role == "user" {
					entry["prefix"] = false
				}
				inputs = append(inputs, entry)
			}
			if role == "assistant" {
				calls, err := consoleToolCallInputs(message["tool_calls"])
				if err != nil {
					return nil, fmt.Errorf("message %d: %w", index, err)
				}
				inputs = append(inputs, calls...)
			}
		case "tool":
			toolCallID, _ := message["tool_call_id"].(string)
			if strings.TrimSpace(toolCallID) == "" {
				return nil, fmt.Errorf("message %d: tool_call_id is required", index)
			}
			inputs = append(inputs, map[string]any{"object": "entry", "type": "function.result", "tool_call_id": toolCallID, "result": text})
		default:
			return nil, fmt.Errorf("Mistral Console does not support message role %q", role)
		}
	}
	if len(inputs) == 0 {
		return nil, fmt.Errorf("Mistral Console requires at least one input message")
	}

	args := map[string]any{"reasoning_effort": "high", "max_tokens": consoleMaxTokens(request)}
	for _, key := range []string{"temperature", "top_p"} {
		if value, exists := request[key]; exists && value != nil {
			args[key] = value
		}
	}
	tools, toolInstruction, err := consoleTools(request["tools"], request["tool_choice"])
	if err != nil {
		return nil, err
	}
	if toolInstruction != "" {
		instructions = append(instructions, toolInstruction)
	}

	payload := map[string]any{
		"model":           model,
		"instructions":    strings.Join(instructions, "\n\n"),
		"completion_args": args,
		"stream":          true,
		"inputs":          inputs,
	}
	if len(tools) > 0 {
		payload["tools"] = tools
	}
	return json.Marshal(payload)
}

func consoleMaxTokens(request map[string]any) uint64 {
	value, exists := jsonNumber(request["max_completion_tokens"])
	if !exists {
		value, exists = jsonNumber(request["max_tokens"])
	}
	if !exists {
		return mistralConsoleMaxTokens
	}
	if value > mistralConsoleMaxTokens {
		return mistralConsoleMaxTokens
	}
	return value
}

func jsonNumber(value any) (uint64, bool) {
	switch value := value.(type) {
	case float64:
		if value >= 0 {
			return uint64(value), true
		}
	case json.Number:
		if parsed, err := value.Int64(); err == nil && parsed >= 0 {
			return uint64(parsed), true
		}
	}
	return 0, false
}

func consoleMessageText(value any) (string, error) {
	switch value := value.(type) {
	case nil:
		return "", nil
	case string:
		return value, nil
	case []any:
		var result strings.Builder
		for _, part := range value {
			item, ok := part.(map[string]any)
			if !ok || item["type"] != "text" {
				return "", fmt.Errorf("Mistral Console only supports text content")
			}
			text, ok := item["text"].(string)
			if !ok {
				return "", fmt.Errorf("text content must be a string")
			}
			result.WriteString(text)
		}
		return result.String(), nil
	default:
		return "", fmt.Errorf("unsupported content type %T", value)
	}
}

func consoleToolCallInputs(value any) ([]map[string]any, error) {
	if value == nil {
		return nil, nil
	}
	calls, ok := value.([]any)
	if !ok || len(calls) == 0 {
		return nil, fmt.Errorf("tool_calls must be an array")
	}
	result := make([]map[string]any, 0, len(calls))
	for index, raw := range calls {
		call, ok := raw.(map[string]any)
		if !ok || call["type"] != "function" {
			return nil, fmt.Errorf("tool call %d is unsupported", index)
		}
		id, _ := call["id"].(string)
		function, _ := call["function"].(map[string]any)
		name, _ := function["name"].(string)
		arguments, _ := function["arguments"].(string)
		if id == "" || name == "" {
			return nil, fmt.Errorf("tool call %d requires id and function.name", index)
		}
		result = append(result, map[string]any{"object": "entry", "type": "function.call", "name": name, "tool_call_id": id, "arguments": arguments})
	}
	return result, nil
}

func consoleTools(value, choice any) ([]map[string]any, string, error) {
	tools := make([]map[string]any, 0)
	if value != nil {
		rawTools, ok := value.([]any)
		if !ok {
			return nil, "", fmt.Errorf("tools must be an array")
		}
		for index, raw := range rawTools {
			tool, ok := raw.(map[string]any)
			if !ok {
				return nil, "", fmt.Errorf("tool %d is invalid", index)
			}
			typ, _ := tool["type"].(string)
			switch typ {
			case "function", "code_interpreter", "image_generation", "web_search_premium":
				tools = append(tools, tool)
			case "web_search", "web_search_preview":
				tools = append(tools, map[string]any{"type": "web_search_premium"})
			default:
				return nil, "", fmt.Errorf("tool %d has unsupported type %q", index, typ)
			}
		}
	}
	if choice == nil || choice == "auto" {
		return tools, "", nil
	}
	if choice == "none" {
		return nil, "", nil
	}
	if choice == "required" {
		if len(tools) == 0 {
			return nil, "", fmt.Errorf("tool_choice required needs at least one tool")
		}
		return tools, "You must call at least one provided tool before answering.", nil
	}
	selected, ok := choice.(map[string]any)
	if !ok {
		return nil, "", fmt.Errorf("unsupported tool_choice")
	}
	function, _ := selected["function"].(map[string]any)
	name, _ := function["name"].(string)
	if name == "" {
		return nil, "", fmt.Errorf("tool_choice function.name is required")
	}
	for _, tool := range tools {
		fn, _ := tool["function"].(map[string]any)
		if fnName, _ := fn["name"].(string); fnName == name {
			return []map[string]any{tool}, "You must call the function " + name + " before answering.", nil
		}
	}
	return nil, "", fmt.Errorf("tool_choice references unknown function %q", name)
}

type mistralConsoleState struct {
	id        string
	model     string
	created   int64
	text      strings.Builder
	reasoning strings.Builder
	toolCalls []mistralConsoleToolCall
	toolIndex map[string]int
	usage     map[string]any
	completed bool
}

type mistralConsoleToolCall struct {
	id        string
	name      string
	arguments string
}

func newMistralConsoleState(model string) *mistralConsoleState {
	return &mistralConsoleState{id: "chatcmpl-" + fmt.Sprint(time.Now().UnixNano()), model: model, created: time.Now().Unix(), toolIndex: map[string]int{}}
}

func mistralConsoleResponseToOpenAI(body []byte, fallbackModel string) ([]byte, error) {
	state, err := consumeMistralConsoleSSE(bytes.NewReader(body), fallbackModel, nil)
	if err != nil {
		return nil, err
	}
	if !state.completed {
		return nil, fmt.Errorf("Mistral Console stream ended before conversation.response.done")
	}
	message := map[string]any{"role": "assistant", "content": state.text.String()}
	if state.reasoning.Len() > 0 {
		message["reasoning_content"] = state.reasoning.String()
	}
	if len(state.toolCalls) > 0 {
		calls := make([]map[string]any, 0, len(state.toolCalls))
		for _, call := range state.toolCalls {
			calls = append(calls, map[string]any{"id": call.id, "type": "function", "function": map[string]any{"name": call.name, "arguments": call.arguments}})
		}
		message["tool_calls"] = calls
	}
	usage := consoleUsage(state.usage)
	finish := "stop"
	if len(state.toolCalls) > 0 {
		finish = "tool_calls"
	}
	return json.Marshal(map[string]any{"id": state.id, "object": "chat.completion", "created": state.created, "model": state.model, "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}}, "usage": usage})
}

func consoleUsage(raw map[string]any) map[string]any {
	usage := make(map[string]any, len(raw)+3)
	for key, value := range raw {
		usage[key] = value
	}
	if prompt, ok := usageNumber(usage["prompt_tokens"]); !ok || prompt == 0 {
		prompt, _ := usageNumber(usage["input_tokens"])
		usage["prompt_tokens"] = prompt
	}
	if completion, ok := usageNumber(usage["completion_tokens"]); !ok || completion == 0 {
		completion, _ := usageNumber(usage["output_tokens"])
		usage["completion_tokens"] = completion
	}
	if total, ok := usageNumber(usage["total_tokens"]); !ok || total == 0 {
		prompt, _ := usageNumber(usage["prompt_tokens"])
		completion, _ := usageNumber(usage["completion_tokens"])
		usage["total_tokens"] = prompt + completion
	}
	return usage
}

func usageNumber(value any) (int64, bool) {
	switch value := value.(type) {
	case float64:
		return int64(value), true
	case int:
		return int64(value), true
	case json.Number:
		parsed, _ := value.Int64()
		return parsed, true
	default:
		return 0, false
	}
}

func consumeMistralConsoleSSE(reader io.Reader, model string, emit func([]byte) error) (*mistralConsoleState, error) {
	state := newMistralConsoleState(model)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 64<<20)
	var eventName string
	var dataLines []string
	dispatch := func() error {
		if len(dataLines) == 0 {
			eventName = ""
			return nil
		}
		data := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		current := eventName
		eventName = ""
		var event map[string]any
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return err
		}
		outputs, err := state.handleEvent(current, event)
		if err != nil {
			return err
		}
		for _, output := range outputs {
			if emit != nil {
				if err := emit(output); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return nil, err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if err := dispatch(); err != nil {
		return nil, err
	}
	return state, nil
}

func (state *mistralConsoleState) handleEvent(eventName string, event map[string]any) ([][]byte, error) {
	typ, _ := event["type"].(string)
	if typ == "" {
		typ = eventName
	}
	if model, _ := event["model"].(string); model != "" {
		state.model = model
	}
	if strings.HasSuffix(strings.ToLower(typ), ".error") || strings.HasSuffix(strings.ToLower(typ), ".failed") {
		message, _ := event["message"].(string)
		if message == "" {
			message = "unknown upstream error"
		}
		return nil, fmt.Errorf("Mistral Console error event: %s", message)
	}
	switch typ {
	case "conversation.response.started":
		if id, _ := event["conversation_id"].(string); id != "" {
			state.id = "chatcmpl-" + strings.TrimPrefix(id, "chatcmpl-")
		}
		return [][]byte{consoleSSE(map[string]any{"id": state.id, "object": "chat.completion.chunk", "created": state.created, "model": state.model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant"}, "finish_reason": nil}}})}, nil
	case "message.output.delta":
		content, reasoning, err := consoleResponseContent(event["content"])
		if err != nil {
			return nil, err
		}
		state.text.WriteString(content)
		state.reasoning.WriteString(reasoning)
		return consoleStreamChunks(state, content, reasoning), nil
	case "function.call.delta":
		chunk, err := state.handleToolCall(event)
		if err != nil {
			return nil, err
		}
		return [][]byte{chunk}, nil
	case "tool.execution.done":
		if event["name"] == "image_generation" {
			info, _ := event["info"].(map[string]any)
			result, _ := info["result"].(string)
			var image struct {
				URL string `json:"url"`
			}
			if result != "" && json.Unmarshal([]byte(result), &image) == nil && image.URL != "" {
				text := "![Generated image](" + strings.ReplaceAll(image.URL, ")", "%29") + ")\n\n"
				state.text.WriteString(text)
				return consoleStreamChunks(state, text, ""), nil
			}
		}
	case "conversation.response.done":
		state.completed = true
		state.usage, _ = event["usage"].(map[string]any)
		return [][]byte{consoleSSE(map[string]any{"id": state.id, "object": "chat.completion.chunk", "created": state.created, "model": state.model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": consoleFinishReason(state)}}}), []byte("data: [DONE]\n\n")}, nil
	}
	return nil, nil
}

func consoleResponseContent(value any) (string, string, error) {
	if value == nil {
		return "", "", nil
	}
	if text, ok := value.(string); ok {
		return text, "", nil
	}
	part, ok := value.(map[string]any)
	if !ok || part["type"] != "thinking" {
		return "", "", fmt.Errorf("unsupported Mistral Console response content")
	}
	var result strings.Builder
	switch thinking := part["thinking"].(type) {
	case string:
		result.WriteString(thinking)
	case []any:
		for _, raw := range thinking {
			item, _ := raw.(map[string]any)
			text, _ := item["text"].(string)
			result.WriteString(text)
		}
	default:
		return "", "", fmt.Errorf("unsupported Mistral Console thinking content")
	}
	return "", result.String(), nil
}

func consoleStreamChunks(state *mistralConsoleState, content, reasoning string) [][]byte {
	result := make([][]byte, 0, 2)
	if reasoning != "" {
		result = append(result, consoleSSE(map[string]any{"id": state.id, "object": "chat.completion.chunk", "created": state.created, "model": state.model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"reasoning_content": reasoning}, "finish_reason": nil}}}))
	}
	if content != "" {
		result = append(result, consoleSSE(map[string]any{"id": state.id, "object": "chat.completion.chunk", "created": state.created, "model": state.model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": content}, "finish_reason": nil}}}))
	}
	return result
}

func (state *mistralConsoleState) handleToolCall(event map[string]any) ([]byte, error) {
	id, _ := event["tool_call_id"].(string)
	if id == "" {
		id, _ = event["id"].(string)
	}
	if id == "" {
		return nil, fmt.Errorf("Mistral Console function.call.delta is missing tool_call_id")
	}
	index, exists := state.toolIndex[id]
	if !exists {
		index = len(state.toolCalls)
		state.toolIndex[id] = index
		name, _ := event["name"].(string)
		state.toolCalls = append(state.toolCalls, mistralConsoleToolCall{id: id, name: name})
	}
	name, _ := event["name"].(string)
	if name != "" {
		state.toolCalls[index].name = name
	}
	arguments, _ := event["arguments"].(string)
	state.toolCalls[index].arguments += arguments
	function := map[string]any{"arguments": arguments}
	if name != "" {
		function["name"] = name
	}
	return consoleSSE(map[string]any{"id": state.id, "object": "chat.completion.chunk", "created": state.created, "model": state.model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": index, "id": id, "type": "function", "function": function}}}, "finish_reason": nil}}}), nil
}

func consoleFinishReason(state *mistralConsoleState) string {
	if len(state.toolCalls) > 0 {
		return "tool_calls"
	}
	return "stop"
}

func consoleSSE(value map[string]any) []byte {
	body, _ := json.Marshal(value)
	return append([]byte("data: "), append(body, []byte("\n\n")...)...)
}

type mistralConsoleSSEReader struct {
	source    *bufio.Reader
	closer    io.Closer
	model     string
	state     *mistralConsoleState
	pending   []byte
	eventName string
	dataLines []string
	done      bool
	err       error
}

func newMistralConsoleSSEReader(source io.ReadCloser, model string) io.ReadCloser {
	return &mistralConsoleSSEReader{source: bufio.NewReader(source), closer: source, model: model, state: newMistralConsoleState(model)}
}

func (reader *mistralConsoleSSEReader) Read(p []byte) (int, error) {
	for len(reader.pending) == 0 && !reader.done {
		line, err := reader.source.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
			if line == "" {
				if dispatchErr := reader.dispatch(); dispatchErr != nil {
					reader.err = dispatchErr
					break
				}
			} else if strings.HasPrefix(line, ":") {
			} else if strings.HasPrefix(line, "event:") {
				reader.eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			} else if strings.HasPrefix(line, "data:") {
				reader.dataLines = append(reader.dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
		if err != nil {
			if err == io.EOF && len(reader.dataLines) > 0 {
				if dispatchErr := reader.dispatch(); dispatchErr != nil {
					reader.err = dispatchErr
				}
			}
			if reader.pending == nil && reader.err == nil {
				reader.done = true
			}
			break
		}
	}
	if len(reader.pending) > 0 {
		n := copy(p, reader.pending)
		reader.pending = reader.pending[n:]
		return n, nil
	}
	if reader.err != nil {
		return 0, reader.err
	}
	return 0, io.EOF
}

func (reader *mistralConsoleSSEReader) dispatch() error {
	if len(reader.dataLines) == 0 {
		reader.eventName = ""
		return nil
	}
	data := strings.Join(reader.dataLines, "\n")
	reader.dataLines = nil
	var event map[string]any
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return err
	}
	outputs, err := reader.state.handleEvent(reader.eventName, event)
	reader.eventName = ""
	if err != nil {
		return err
	}
	for _, output := range outputs {
		reader.pending = append(reader.pending, output...)
	}
	if reader.state.completed {
		reader.done = true
	}
	return nil
}

func (reader *mistralConsoleSSEReader) Close() error {
	return reader.closer.Close()
}
