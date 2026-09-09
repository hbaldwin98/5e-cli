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
	messages := make([]map[string]string, len(msgs))
	for i, m := range msgs {
		messages[i] = map[string]string{"role": m.Role, "content": m.Content}
	}
	body := map[string]any{
		"model":       c.cfg.AskModel,
		"messages":    messages,
		"temperature": 0,
		"max_tokens":  c.cfg.AnswerMaxTokens,
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *apiError `json:"error"`
	}
	if err := c.post(ctx, "chat/completions", body, &resp); err != nil {
		return "", err
	}
	if resp.Error != nil && resp.Error.Message != "" {
		return "", fmt.Errorf("chat: %s", resp.Error.Message)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("chat: empty choices")
	}
	return messageText(resp.Choices[0].Message.Content)
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
	messages := make([]map[string]string, len(msgs))
	for i, m := range msgs {
		messages[i] = map[string]string{"role": m.Role, "content": m.Content}
	}
	body := map[string]any{
		"model":       c.cfg.AskModel,
		"messages":    messages,
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
		msg := strings.TrimSpace(string(payload))
		if len(msg) > 512 {
			msg = msg[:512] + "…"
		}
		if msg == "" {
			msg = resp.Status
		}
		return "", fmt.Errorf("chat/completions: %s", msg)
	}

	var full strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
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
	if full.Len() == 0 {
		return "", fmt.Errorf("chat: empty message")
	}
	return full.String(), nil
}

type apiError struct {
	Message string `json:"message"`
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
		msg := strings.TrimSpace(string(payload))
		if len(msg) > 512 {
			msg = msg[:512] + "…"
		}
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("%s: %s", path, msg)
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
