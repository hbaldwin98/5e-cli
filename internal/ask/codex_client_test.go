package ask

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hbaldwin98/5e-cli/internal/provider"
)

func TestCodexCompletionUsesResponsesAPIAndTranslatesTools(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer oauth-token" {
			t.Errorf("authorization = %q", got)
		}
		if got := r.Header.Get("ChatGPT-Account-ID"); got != "account-1" {
			t.Errorf("account = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = strings.NewReader("").WriteTo(w)
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"call_id\":\"call-1\",\"name\":\"lookup\",\"arguments\":\"{\\\"name\\\":\\\"goblin\\\"}\"}}\n\ndata: {\"type\":\"response.completed\"}\n\n"))
	}))
	defer srv.Close()

	c := newClient(Config{ChatProvider: provider.Codex, ChatBaseURL: srv.URL, ChatAPIKey: "oauth-token", ChatAccountID: "account-1", AskModel: "codex-model", HTTPClient: srv.Client()})
	result, err := c.ChatCompletion(context.Background(), []Message{{Role: "system", Content: "be concise"}, {Role: "user", Content: "find it"}}, []Tool{{Name: "lookup", Description: "look up", Parameters: json.RawMessage(`{"type":"object"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].Name != "lookup" {
		t.Fatalf("tool calls = %#v", result.ToolCalls)
	}
	if body["instructions"] != "be concise" || body["model"] != "codex-model" || body["stream"] != true {
		t.Fatalf("request body = %#v", body)
	}
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 1 || tools[0].(map[string]any)["name"] != "lookup" {
		t.Fatalf("tools = %#v", body["tools"])
	}
	if strict := tools[0].(map[string]any)["strict"]; strict != false {
		t.Fatalf("tool strict = %#v, want false for non-strict schemas", strict)
	}
}

func TestCodexCompletionStreamsTextThroughRefreshedToken(t *testing.T) {
	var deltas []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer refreshed-token" {
			t.Errorf("authorization = %q", got)
		}
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\ndata: {\"type\":\"response.completed\"}\n\n"))
	}))
	defer srv.Close()

	c := newClient(Config{
		ChatProvider: provider.Codex,
		ChatBaseURL:  srv.URL,
		ChatToken: func(context.Context) (string, string, error) {
			return "refreshed-token", "account-1", nil
		},
		HTTPClient: srv.Client(),
	})
	got, err := c.ChatMessagesStream(context.Background(), []Message{{Role: "user", Content: "hi"}}, func(delta string) error {
		deltas = append(deltas, delta)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello" || len(deltas) != 1 || deltas[0] != "hello" {
		t.Fatalf("content = %q, deltas = %#v", got, deltas)
	}
}

func TestCodexCompletionReportsResponsesFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"model unavailable\"}}}\n\n"))
	}))
	defer srv.Close()

	c := newClient(Config{ChatProvider: provider.Codex, ChatBaseURL: srv.URL, ChatAPIKey: "token", HTTPClient: srv.Client()})
	_, err := c.ChatCompletion(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "model unavailable") {
		t.Fatalf("error = %v", err)
	}
}

func TestCodexCompletionRequiresAuthentication(t *testing.T) {
	c := newClient(Config{ChatProvider: provider.Codex})
	_, err := c.ChatCompletion(context.Background(), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "auth login codex") {
		t.Fatalf("error = %v", err)
	}
}

func TestCodexChatDoesNotSendOAuthTokenToEmbeddings(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": []float32{1}}}})
	}))
	defer srv.Close()

	c := newClient(Config{APIKey: "embedding-key", BaseURL: srv.URL, ChatProvider: provider.Codex, ChatAPIKey: "oauth-token", ChatBaseURL: "https://example.invalid", HTTPClient: srv.Client()})
	if _, err := c.Embed(context.Background(), []string{"hello"}); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer embedding-key" {
		t.Fatalf("embedding authorization = %q", auth)
	}
}
