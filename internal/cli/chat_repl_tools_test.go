package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hbaldwin98/5e-cli/internal/parse"
	"github.com/hbaldwin98/5e-cli/internal/store"
)

// toolCallingChatFixtureServer answers the first chat/completions request
// (no role:"tool" message yet) with a get tool call for the given kind and
// name, and every later request with a short reply that carries no
// "(kind, name, source)" citation of its own — the shape a model produces
// after the tool-calling prompt tells it not to restate a shown stat block.
func toolCallingChatFixtureServer(t *testing.T, kind, name string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/embeddings") {
			var req struct {
				Input []string `json:"input"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			type row struct {
				Index     int       `json:"index"`
				Embedding []float32 `json:"embedding"`
			}
			var data []row
			for i := range req.Input {
				data = append(data, row{Index: i, Embedding: []float32{0.1, 0.2, 0.3}})
			}
			json.NewEncoder(w).Encode(map[string]any{"data": data})
			return
		}
		var req struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		ranTool := false
		for _, m := range req.Messages {
			if m.Role == "tool" {
				ranTool = true
			}
		}
		if !ranTool {
			args, _ := json.Marshal(map[string]string{"kind": kind, "name": name})
			json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]any{
				"tool_calls": []map[string]any{{"id": "1", "function": map[string]any{"name": "get", "arguments": string(args)}}},
			}}}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]any{"content": "Here you go."}}}})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestChat_replToolCallAddsCitationEvenWithoutModelCitationText(t *testing.T) {
	index := filepath.Join(t.TempDir(), "index.sqlite")
	err := store.Create(index, store.Meta{SHA: "repltools", DataRoot: t.TempDir(), IngestedAt: store.Now()}, []parse.Entity{
		{Kind: "monster", Name: "Goblin", Source: "MM", JSON: json.RawMessage(`{"name":"Goblin","source":"MM","cr":"1/4"}`), Text: "A small humanoid goblin."},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	base := toolCallingChatFixtureServer(t, "monster", "Goblin")
	t.Setenv("OPENAI_API_KEY", "test")
	t.Setenv("OPENAI_BASE_URL", base)
	t.Setenv("FIVE_E_EMBED_MODEL", "fake-embed")
	t.Setenv("FIVE_E_ASK_MODEL", "fake-ask")

	dir := filepath.Join(t.TempDir(), "chats")
	data := filepath.Join(t.TempDir(), "missing-data")
	script := "show me a goblin\n/exit\n"

	out, err := runCLIStdin(script, "--index", index, "--data", data, "--json", "chat", "--chat-dir", dir)
	if err != nil {
		t.Fatal(err)
	}

	dec := json.NewDecoder(strings.NewReader(out))
	var found bool
	for {
		var event struct {
			Type      string `json:"type"`
			Answer    string `json:"answer"`
			Citations []struct {
				Kind, Name, Source string
			} `json:"citations"`
		}
		if err := dec.Decode(&event); err != nil {
			break
		}
		if event.Type != "turn" {
			continue
		}
		if event.Answer != "Here you go." {
			t.Fatalf("expected the model's minimal reply, got %q", event.Answer)
		}
		for _, c := range event.Citations {
			if c.Kind == "monster" && c.Name == "Goblin" && c.Source == "MM" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("expected a citation for the get-fetched entity even though the model's reply had none:\n%s", out)
	}
}
