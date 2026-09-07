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

func TestAsk_retrieveOnlyAndAnswer(t *testing.T) {
	index := filepath.Join(t.TempDir(), "index.sqlite")
	err := store.Create(index, store.Meta{SHA: "ask", DataRoot: t.TempDir(), IngestedAt: store.Now()}, []parse.Entity{
		{Kind: "spell", Name: "Fireball", Source: "PHB", SRD: true, JSON: json.RawMessage(`{}`), Text: "A bright streak flashes and explodes in fire and flame."},
		{Kind: "item", Name: "Longsword", Source: "PHB", JSON: json.RawMessage(`{}`), Text: "A martial melee weapon with a steel blade."},
	}, []parse.Document{
		{Kind: "bookSection", ParentID: "PHB", Section: "Holding Breath", JSON: json.RawMessage(`{}`), Text: "A creature can hold its breath underwater."},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{
					{"message": map[string]any{"content": "Fireball explodes in fire (spell, Fireball, PHB)."}},
				},
			})
			return
		}
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		type row struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		var data []row
		for i, s := range req.Input {
			v := []float32{0.05, 0.05}
			low := strings.ToLower(s)
			if strings.Contains(low, "fire") || strings.Contains(low, "flame") {
				v = []float32{1, 0}
			}
			if strings.Contains(low, "sword") || strings.Contains(low, "blade") {
				v = []float32{0, 1}
			}
			data = append(data, row{Index: i, Embedding: v})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(api.Close)
	t.Setenv("OPENAI_API_KEY", "test")
	t.Setenv("OPENAI_BASE_URL", api.URL)
	t.Setenv("FIVE_E_EMBED_MODEL", "fake-embed")
	t.Setenv("FIVE_E_ASK_MODEL", "fake-ask")
	data := filepath.Join(t.TempDir(), "missing-data")

	out, err := runCLI("--srd", "--index", index, "--data", data, "--json", "ask", "--retrieve-only", "fire explosion")
	if err != nil {
		t.Fatal(err)
	}
	var hits []map[string]any
	if err := json.Unmarshal([]byte(out), &hits); err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0]["name"] != "Fireball" {
		t.Fatalf("retrieve: %s", out)
	}
	for _, h := range hits {
		if h["name"] == "Longsword" || h["name"] == "Holding Breath" {
			t.Fatalf("srd leaked %s", out)
		}
	}

	out, err = runCLI("--index", index, "--data", data, "ask", "what does fireball do")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Fireball") {
		t.Fatalf("ask: %s", out)
	}
}
