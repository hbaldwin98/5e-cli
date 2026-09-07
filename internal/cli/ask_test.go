package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hbaldwin98/5e-cli/internal/parse"
	"github.com/hbaldwin98/5e-cli/internal/store"
)

func TestAskRetrieveOnlyJSON(t *testing.T) {
	index := filepath.Join(t.TempDir(), "index.sqlite")
	err := store.Create(index, store.Meta{SHA: "cli", DataRoot: t.TempDir(), IngestedAt: store.Now()}, []parse.Entity{
		{Kind: "spell", Name: "Fireball", Source: "PHB", JSON: json.RawMessage(`{}`), Text: "Explodes in fire and flame."},
		{Kind: "item", Name: "Longsword", Source: "PHB", JSON: json.RawMessage(`{}`), Text: "A steel blade that slashes."},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	var chat int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			chat++
			http.Error(w, "chat should not run", 500)
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
			v := []float32{0.1, 0.1}
			if strings.Contains(strings.ToLower(s), "fire") || strings.Contains(strings.ToLower(s), "flame") {
				v = []float32{1, 0}
			}
			if strings.Contains(strings.ToLower(s), "sword") || strings.Contains(strings.ToLower(s), "blade") {
				v = []float32{0, 1}
			}
			data = append(data, row{Index: i, Embedding: v})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)

	t.Setenv("OPENAI_API_KEY", "test")
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("FIVE_E_EMBED_MODEL", "fake-embed")
	t.Setenv("FIVE_E_EMBEDDINGS", filepath.Join(t.TempDir(), "embeddings.sqlite"))

	cmd := rootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{
		"--index", index,
		"--data", filepath.Join(t.TempDir(), "missing-data"),
		"--json",
		"ask", "--retrieve-only", "fire explosion",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if chat != 0 {
		t.Fatalf("chat called %d times", chat)
	}
	var hits []map[string]any
	if err := json.Unmarshal(out.Bytes(), &hits); err != nil {
		t.Fatalf("json %v: %s", err, out.String())
	}
	if len(hits) == 0 || hits[0]["name"] != "Fireball" {
		t.Fatalf("hits %s", out.String())
	}
}
