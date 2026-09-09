package ask

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type client struct {
	cfg Config
}

func newClient(cfg Config) *client {
	return &client{cfg: cfg.withDefaults()}
}

func (c *client) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	body := map[string]any{
		"model":           c.cfg.EmbedModel,
		"input":           inputs,
		"encoding_format": "float",
	}
	var resp struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Error *apiError `json:"error"`
	}
	if err := c.post(ctx, "embeddings", body, &resp); err != nil {
		return nil, err
	}
	if resp.Error != nil && resp.Error.Message != "" {
		return nil, fmt.Errorf("embeddings: %s", resp.Error.Message)
	}
	out := make([][]float32, len(inputs))
	for _, row := range resp.Data {
		if row.Index < 0 || row.Index >= len(out) {
			return nil, fmt.Errorf("embeddings: unexpected index %d", row.Index)
		}
		if len(row.Embedding) == 0 {
			return nil, fmt.Errorf("embeddings: empty vector at index %d", row.Index)
		}
		out[row.Index] = row.Embedding
	}
	for i, v := range out {
		if len(v) == 0 {
			return nil, fmt.Errorf("embeddings: missing vector at index %d", i)
		}
	}
	return out, nil
}

func (c *client) Chat(ctx context.Context, system, user string) (string, error) {
	return c.ChatMessages(ctx, []Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	})
}

func (c *client) ChatMessages(ctx context.Context, msgs []Message) (string, error) {
	result, err := c.ChatCompletion(ctx, msgs, nil)
	if err != nil {
		return "", err
	}
	return result.Content, nil
}

// completionResult is one chat/completions response: either a final answer
// (Content set, ToolCalls empty) or a request to run tools before the model
// continues (ToolCalls set; Content is typically empty in that case).
type completionResult struct {
	Content   string
	ToolCalls []ToolCall
}

// wireMessage is one internal Message translated to the OpenAI-compatible
// wire shape. Only an assistant message that is itself requesting tool calls
// carries ToolCalls; only a role:"tool" message carries ToolCallID.
type wireMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function wireToolCallFunc `json:"function"`
}

type wireToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func toWireMessages(msgs []Message) []wireMessage {
	out := make([]wireMessage, len(msgs))
	for i, m := range msgs {
		w := wireMessage{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID}
		for _, tc := range m.ToolCalls {
			w.ToolCalls = append(w.ToolCalls, wireToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: wireToolCallFunc{
					Name:      tc.Name,
					Arguments: string(tc.Arguments),
				},
			})
		}
		out[i] = w
	}
	return out
}

func toWireTools(tools []Tool) []map[string]any {
	if len(tools) == 0 {
		return nil
	}
	out := make([]map[string]any, len(tools))
	for i, t := range tools {
		params := t.Parameters
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out[i] = map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  json.RawMessage(params),
			},
		}
	}
	return out
}

// ChatCompletion is ChatMessages with tool support: when tools is non-empty,
// the model may respond with tool calls instead of (or in addition to
// planning) a final answer, and the caller is expected to execute them and
// append role:"tool" messages before calling again. Non-streaming: a tool
// round has to be inspected for tool_calls before anything can be shown to
// the user, so there is nothing meaningful to stream until the loop that
// owns this call has a final answer in hand.
func (c *client) ChatCompletion(ctx context.Context, msgs []Message, tools []Tool) (completionResult, error) {
	body := map[string]any{
		"model":       c.cfg.AskModel,
		"messages":    toWireMessages(msgs),
		"temperature": 0,
		"max_tokens":  c.cfg.AnswerMaxTokens,
	}
	if wireTools := toWireTools(tools); wireTools != nil {
		body["tools"] = wireTools
		body["tool_choice"] = "auto"
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content   json.RawMessage `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Error *apiError `json:"error"`
	}
	if err := c.post(ctx, "chat/completions", body, &resp); err != nil {
		return completionResult{}, err
	}
	if resp.Error != nil && resp.Error.Message != "" {
		return completionResult{}, fmt.Errorf("chat: %s", resp.Error.Message)
	}
	if len(resp.Choices) == 0 {
		return completionResult{}, fmt.Errorf("chat: empty choices")
	}
	msg := resp.Choices[0].Message
	if len(msg.ToolCalls) > 0 {
		calls := make([]ToolCall, len(msg.ToolCalls))
		for i, tc := range msg.ToolCalls {
			calls[i] = ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: json.RawMessage(tc.Function.Arguments)}
		}
		return completionResult{ToolCalls: calls}, nil
	}
	content, err := messageText(msg.Content)
	if err != nil {
		return completionResult{}, err
	}
	return completionResult{Content: content}, nil
}

// ChatMessagesStream is ChatMessages, but calls onDelta with each token as
// the model generates it, so a TTY caller can render an answer as it
// arrives instead of waiting for the whole thing. It still returns the
// complete answer at the end, so a caller that needs the final text (to
// validate citations against, or to persist) does not have to reassemble it
// from the deltas itself.
func (c *client) ChatMessagesStream(ctx context.Context, msgs []Message, onDelta func(string) error) (string, error) {
	if c.cfg.APIKey == "" {
		return "", fmt.Errorf("OPENAI_API_KEY is not set")
	}
	body := map[string]any{
		"model":       c.cfg.AskModel,
		"messages":    toWireMessages(msgs),
		"temperature": 0,
		"max_tokens":  c.cfg.AnswerMaxTokens,
		"stream":      true,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	url := c.cfg.BaseURL + "/" + strings.TrimLeft("chat/completions", "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		return "", fmt.Errorf("chat/completions: %s", errorBodyMessage(payload, resp.Status))
	}

	var full strings.Builder
	done := false
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			done = true
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Error *apiError `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return full.String(), fmt.Errorf("chat/completions: stream decode: %w", err)
		}
		if chunk.Error != nil && chunk.Error.Message != "" {
			return full.String(), fmt.Errorf("chat: %s", chunk.Error.Message)
		}
		for _, c := range chunk.Choices {
			if c.Delta.Content == "" {
				continue
			}
			full.WriteString(c.Delta.Content)
			if onDelta != nil {
				if err := onDelta(c.Delta.Content); err != nil {
					return full.String(), err
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return full.String(), fmt.Errorf("chat/completions: stream: %w", err)
	}
	if !done {
		// The connection closed (or the server ended the response) before a
		// terminal "[DONE]" ever arrived. Without this check that reads as
		// total success — full.String() is whatever text happened to arrive
		// before the cutoff, and a caller has no way to tell it apart from a
		// genuinely complete answer, so a truncated mid-sentence response
		// silently gets treated (and saved) as the real one.
		return full.String(), fmt.Errorf("chat/completions: stream ended before completion (connection closed early)")
	}
	if full.Len() == 0 {
		return "", fmt.Errorf("chat: empty message")
	}
	return full.String(), nil
}

type apiError struct {
	Message string `json:"message"`
}

// errorBodyMessage extracts the human-readable message from an
// OpenAI-compatible error response body ({"error":{"message":"..."}}),
// falling back to the raw body (truncated) or the HTTP status line if it
// isn't that shape. Dumping the raw JSON body directly, as this used to do,
// shows the reader its literal escape sequences (backslash-escaped quotes
// and the like) instead of the message the API actually meant to convey.
func errorBodyMessage(payload []byte, status string) string {
	var parsed struct {
		Error *apiError `json:"error"`
	}
	if err := json.Unmarshal(payload, &parsed); err == nil && parsed.Error != nil && parsed.Error.Message != "" {
		return parsed.Error.Message
	}
	msg := strings.TrimSpace(string(payload))
	if len(msg) > 512 {
		msg = msg[:512] + "…"
	}
	if msg == "" {
		msg = status
	}
	return msg
}

func (c *client) post(ctx context.Context, path string, body any, dest any) error {
	if c.cfg.APIKey == "" {
		return fmt.Errorf("OPENAI_API_KEY is not set")
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	url := c.cfg.BaseURL + "/" + strings.TrimLeft(path, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s: %s", path, errorBodyMessage(payload, resp.Status))
	}
	if err := json.Unmarshal(payload, dest); err != nil {
		return fmt.Errorf("%s: decode: %w", path, err)
	}
	return nil
}

func messageText(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return "", fmt.Errorf("chat: empty message")
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", err
		}
		return s, nil
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", fmt.Errorf("chat: content: %w", err)
	}
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String(), nil
}
