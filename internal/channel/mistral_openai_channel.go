package channel

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"gpt-load/internal/models"
)

func init() {
	Register("mistral-openai", newMistralOpenAIChannel)
}

type MistralOpenAIChannel struct {
	*OpenAIChannel
}

func newMistralOpenAIChannel(f *Factory, group *models.Group) (ChannelProxy, error) {
	base, err := f.newBaseChannel("mistral-openai", group)
	if err != nil {
		return nil, err
	}
	return &MistralOpenAIChannel{OpenAIChannel: &OpenAIChannel{BaseChannel: base}}, nil
}

func (ch *MistralOpenAIChannel) ModifyRequest(req *http.Request, apiKey *models.APIKey, group *models.Group) {
	ch.OpenAIChannel.ModifyRequest(req, apiKey, group)
	// Let net/http negotiate decompression so the response remains transformable.
	req.Header.Del("Accept-Encoding")
}

func (ch *MistralOpenAIChannel) ApplyModelRedirect(req *http.Request, body []byte, group *models.Group) ([]byte, error) {
	body, err := ch.BaseChannel.ApplyModelRedirect(req, body, group)
	if err != nil || !isChatCompletionsPath(req.URL.Path) {
		return body, err
	}
	return openAIRequestToMistral(body), nil
}

func (ch *MistralOpenAIChannel) TransformResponse(req *http.Request, resp *http.Response, stream bool) {
	if !isChatCompletionsPath(req.URL.Path) || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return
	}

	resp.Header.Del("Content-Length")
	if stream {
		resp.Body = newMistralSSEReader(resp.Body)
		resp.ContentLength = -1
		return
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return
	}
	_ = resp.Body.Close()
	body = mistralResponseToOpenAI(body, false)
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
}

func isChatCompletionsPath(path string) bool {
	return strings.HasSuffix(strings.TrimRight(path, "/"), "/chat/completions")
}

func openAIRequestToMistral(body []byte) []byte {
	var request map[string]json.RawMessage
	if json.Unmarshal(body, &request) != nil {
		return body
	}

	var messages []json.RawMessage
	if json.Unmarshal(request["messages"], &messages) != nil {
		return body
	}

	changed := false
	for i, raw := range messages {
		var message map[string]json.RawMessage
		var role string
		if json.Unmarshal(raw, &message) != nil || json.Unmarshal(message["role"], &role) != nil || role != "assistant" {
			continue
		}

		reasoningRaw, exists := message["reasoning_content"]
		if !exists {
			continue
		}
		delete(message, "reasoning_content")
		changed = true

		var reasoning string
		if json.Unmarshal(reasoningRaw, &reasoning) != nil || reasoning == "" || hasThinkingChunk(message["content"]) {
			messages[i], _ = json.Marshal(message)
			continue
		}

		thinking, _ := json.Marshal(map[string]any{
			"type":     "thinking",
			"thinking": []map[string]string{{"type": "text", "text": reasoning}},
			"closed":   true,
		})
		parts := []json.RawMessage{thinking}

		var text string
		if json.Unmarshal(message["content"], &text) == nil {
			if text != "" {
				part, _ := json.Marshal(map[string]string{"type": "text", "text": text})
				parts = append(parts, part)
			}
		} else {
			var existing []json.RawMessage
			if json.Unmarshal(message["content"], &existing) == nil {
				parts = append(parts, existing...)
			}
		}

		message["content"], _ = json.Marshal(parts)
		messages[i], _ = json.Marshal(message)
	}

	if !changed {
		return body
	}
	request["messages"], _ = json.Marshal(messages)
	converted, err := json.Marshal(request)
	if err != nil {
		return body
	}
	return converted
}

func hasThinkingChunk(content json.RawMessage) bool {
	var parts []map[string]json.RawMessage
	if json.Unmarshal(content, &parts) != nil {
		return false
	}
	for _, part := range parts {
		var typ string
		if json.Unmarshal(part["type"], &typ) == nil && typ == "thinking" {
			return true
		}
	}
	return false
}

func mistralResponseToOpenAI(body []byte, stream bool) []byte {
	var response map[string]json.RawMessage
	if json.Unmarshal(body, &response) != nil {
		return body
	}

	var choices []json.RawMessage
	if json.Unmarshal(response["choices"], &choices) != nil {
		return body
	}

	field := "message"
	if stream {
		field = "delta"
	}
	changed := false
	for i, raw := range choices {
		var choice map[string]json.RawMessage
		if json.Unmarshal(raw, &choice) != nil {
			continue
		}
		var message map[string]json.RawMessage
		if json.Unmarshal(choice[field], &message) != nil {
			continue
		}

		reasoning, text, ok := flattenMistralContent(message["content"])
		if !ok {
			continue
		}
		if reasoning == "" {
			delete(message, "reasoning_content")
		} else {
			message["reasoning_content"], _ = json.Marshal(reasoning)
		}
		if stream && text == "" {
			delete(message, "content")
		} else {
			message["content"], _ = json.Marshal(text)
		}
		choice[field], _ = json.Marshal(message)
		choices[i], _ = json.Marshal(choice)
		changed = true
	}

	if !changed {
		return body
	}
	response["choices"], _ = json.Marshal(choices)
	converted, err := json.Marshal(response)
	if err != nil {
		return body
	}
	return converted
}

func flattenMistralContent(content json.RawMessage) (string, string, bool) {
	var parts []map[string]json.RawMessage
	if json.Unmarshal(content, &parts) != nil {
		return "", "", false
	}

	var reasoning, text strings.Builder
	for _, part := range parts {
		var typ string
		if json.Unmarshal(part["type"], &typ) != nil {
			return "", "", false
		}
		switch typ {
		case "text":
			var value string
			if json.Unmarshal(part["text"], &value) != nil {
				return "", "", false
			}
			text.WriteString(value)
		case "thinking":
			var chunks []map[string]json.RawMessage
			if json.Unmarshal(part["thinking"], &chunks) != nil {
				return "", "", false
			}
			for _, chunk := range chunks {
				var chunkType, value string
				if json.Unmarshal(chunk["type"], &chunkType) != nil || chunkType != "text" || json.Unmarshal(chunk["text"], &value) != nil {
					return "", "", false
				}
				reasoning.WriteString(value)
			}
		default:
			return "", "", false
		}
	}
	return reasoning.String(), text.String(), true
}

type mistralSSEReader struct {
	source  io.ReadCloser
	reader  *bufio.Reader
	pending []byte
	err     error
}

func newMistralSSEReader(source io.ReadCloser) io.ReadCloser {
	return &mistralSSEReader{source: source, reader: bufio.NewReader(source)}
}

func (r *mistralSSEReader) Read(p []byte) (int, error) {
	for len(r.pending) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		line, err := r.reader.ReadBytes('\n')
		if len(line) > 0 {
			r.pending = transformMistralSSELine(line)
			r.err = err
			break
		}
		if err != nil {
			return 0, err
		}
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

func (r *mistralSSEReader) Close() error {
	return r.source.Close()
}

func transformMistralSSELine(line []byte) []byte {
	ending := []byte{}
	content := line
	if bytes.HasSuffix(content, []byte("\r\n")) {
		ending = []byte("\r\n")
		content = content[:len(content)-2]
	} else if bytes.HasSuffix(content, []byte("\n")) {
		ending = []byte("\n")
		content = content[:len(content)-1]
	}
	if !bytes.HasPrefix(content, []byte("data:")) {
		return line
	}
	payload := bytes.TrimSpace(content[len("data:"):])
	if bytes.Equal(payload, []byte("[DONE]")) {
		return line
	}
	converted := mistralResponseToOpenAI(payload, true)
	if bytes.Equal(converted, payload) {
		return line
	}
	return append(append([]byte("data: "), converted...), ending...)
}
