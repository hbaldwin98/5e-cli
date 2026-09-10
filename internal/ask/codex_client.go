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

type codexInputItem struct {
	Type      string             `json:"type"`
	Role      string             `json:"role,omitempty"`
	Content   []codexContentPart `json:"content,omitempty"`
	Name      string             `json:"name,omitempty"`
	Arguments string             `json:"arguments,omitempty"`
	CallID    string             `json:"call_id,omitempty"`
	Output    string             `json:"output,omitempty"`
}

type codexContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func codexRequest(msgs []Message, tools []Tool, model string) map[string]any {
	var instructions []string
	input := make([]codexInputItem, 0, len(msgs))
	for _, msg := range msgs {
		switch msg.Role {
		case "system", "developer":
			instructions = append(instructions, msg.Content)
		case "tool":
			input = append(input, codexInputItem{Type: "function_call_output", CallID: msg.ToolCallID, Output: msg.Content})
		default:
			partType := "input_text"
			if msg.Role == "assistant" {
				partType = "output_text"
			}
			if msg.Content != "" {
				input = append(input, codexInputItem{Type: "message", Role: msg.Role, Content: []codexContentPart{{Type: partType, Text: msg.Content}}})
			}
			for _, call := range msg.ToolCalls {
				input = append(input, codexInputItem{Type: "function_call", CallID: call.ID, Name: call.Name, Arguments: string(call.Arguments)})
			}
		}
	}
	body := map[string]any{
		"model": model, "instructions": strings.Join(instructions, "\n\n"), "input": input,
		"tool_choice": "auto", "parallel_tool_calls": false, "store": false, "stream": true, "include": []string{},
	}
	if len(tools) > 0 {
		wire := make([]map[string]any, len(tools))
		for i, tool := range tools {
			params := tool.Parameters
			if len(params) == 0 {
				params = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			// Existing tool schemas intentionally allow optional properties and do
			// not declare additionalProperties:false, so they do not satisfy the
			// Responses API's strict-schema requirements.
			wire[i] = map[string]any{"type": "function", "name": tool.Name, "description": tool.Description, "strict": false, "parameters": json.RawMessage(params)}
		}
		body["tools"] = wire
	}
	return body
}

func (c *client) codexCompletion(ctx context.Context, msgs []Message, tools []Tool, onDelta func(string) error) (completionResult, error) {
	token, accountID := c.cfg.ChatAPIKey, c.cfg.ChatAccountID
	if c.cfg.ChatToken != nil {
		var err error
		token, accountID, err = c.cfg.ChatToken(ctx)
		if err != nil {
			return completionResult{}, err
		}
	}
	if token == "" {
		return completionResult{}, fmt.Errorf("codex is not authenticated; run `5e auth login codex`")
	}
	raw, err := json.Marshal(codexRequest(msgs, tools, c.cfg.AskModel))
	if err != nil {
		return completionResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.ChatBaseURL+"/responses", bytes.NewReader(raw))
	if err != nil {
		return completionResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("ChatGPT-Account-ID", accountID)
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("version", "0.75.0")
	req.Header.Set("User-Agent", "5e-cli")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return completionResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		return completionResult{}, fmt.Errorf("responses: %s", errorBodyMessage(payload, resp.Status))
	}

	var result completionResult
	completed := false
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var event struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
			Item  struct {
				Type, CallID, Name, Arguments string
			} `json:"item"`
			Response struct {
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
			} `json:"response"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return result, fmt.Errorf("responses: stream decode: %w", err)
		}
		switch event.Type {
		case "response.output_text.delta":
			result.Content += event.Delta
			if onDelta != nil && event.Delta != "" {
				if err := onDelta(event.Delta); err != nil {
					return result, err
				}
			}
		case "response.output_item.done":
			if event.Item.Type == "function_call" {
				result.ToolCalls = append(result.ToolCalls, ToolCall{ID: event.Item.CallID, Name: event.Item.Name, Arguments: json.RawMessage(event.Item.Arguments)})
			}
		case "response.completed":
			completed = true
		case "response.failed":
			message := "request failed"
			if event.Response.Error != nil && event.Response.Error.Message != "" {
				message = event.Response.Error.Message
			}
			return result, fmt.Errorf("responses: %s", message)
		}
	}
	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("responses: stream: %w", err)
	}
	if !completed {
		return result, fmt.Errorf("responses: stream ended before completion")
	}
	if result.Content == "" && len(result.ToolCalls) == 0 {
		return result, fmt.Errorf("chat: empty message")
	}
	return result, nil
}
