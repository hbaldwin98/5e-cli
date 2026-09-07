package ask

import (
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
	body := map[string]any{
		"model": c.cfg.AskModel,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"temperature": 0,
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
