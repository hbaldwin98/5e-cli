package ask

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hbaldwin98/5e-cli/internal/parse"
	"github.com/hbaldwin98/5e-cli/internal/store"
)

func TestRetrieve_ranksKeywordNeighbors(t *testing.T) {
	st, cfg, api := harness(t)
	defer st.Close()

	hits, err := Retrieve(context.Background(), st, cfg, Query{Text: "fire explosion", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Name != "Fireball" {
		t.Fatalf("want Fireball first, got %+v", hits)
	}
	if api.embedCalls.Load() < 2 {
		t.Fatalf("expected corpus + query embeds, got %d", api.embedCalls.Load())
	}

	before := api.embedCalls.Load()
	hits, err = Retrieve(context.Background(), st, cfg, Query{Text: "hold breath underwater", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Name != "Holding Breath" {
		t.Fatalf("want Holding Breath first, got %+v", hits)
	}
	if api.embedCalls.Load()-before != 1 {
		t.Fatalf("cache should embed only the query; delta %d", api.embedCalls.Load()-before)
	}
	if api.chatCalls.Load() != 0 {
		t.Fatalf("retrieve must not call chat, got %d", api.chatCalls.Load())
	}
}

func TestRetrieve_srdDropsNonSRDAndDocuments(t *testing.T) {
	st, cfg, _ := harness(t)
	defer st.Close()

	hits, err := Retrieve(context.Background(), st, cfg, Query{Text: "fire explosion", Limit: 8, SRD: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Name != "Fireball" {
		t.Fatalf("want Fireball, got %+v", hits)
	}
	for _, h := range hits {
		if h.Name == "Longsword" || h.Name == "Holding Breath" {
			t.Fatalf("srd leaked %+v", hits)
		}
	}

	hits, err = Retrieve(context.Background(), st, cfg, Query{Text: "steel blade slashing", Limit: 8, SRD: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if h.Name == "Longsword" || h.Name == "Holding Breath" {
			t.Fatalf("srd leaked %+v", hits)
		}
	}
}

func TestRetrieve_rebuildsWhenModelChanges(t *testing.T) {
	st, cfg, api := harness(t)
	defer st.Close()

	if _, err := Retrieve(context.Background(), st, cfg, Query{Text: "fire", Limit: 1}); err != nil {
		t.Fatal(err)
	}
	first := api.embedCalls.Load()
	cfg.EmbedModel = "other-embed"
	if _, err := Retrieve(context.Background(), st, cfg, Query{Text: "fire", Limit: 1}); err != nil {
		t.Fatal(err)
	}
	if api.embedCalls.Load() <= first+1 {
		t.Fatalf("model change should rebuild corpus, calls %d → %d", first, api.embedCalls.Load())
	}
}

func TestAsk_groundsInRetrievedSources(t *testing.T) {
	st, cfg, api := harness(t)
	defer st.Close()

	res, err := Ask(context.Background(), st, cfg, Query{Text: "what does fireball do", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Answer, "Fireball") {
		t.Fatalf("answer %q", res.Answer)
	}
	if api.chatCalls.Load() != 1 {
		t.Fatalf("chat calls %d", api.chatCalls.Load())
	}
	if !strings.Contains(api.lastUser.Load().(string), "Fireball") {
		t.Fatalf("prompt missing citation:\n%s", api.lastUser.Load())
	}
	if len(res.Citations) == 0 || res.Citations[0].Name != "Fireball" {
		t.Fatalf("citations %+v", res.Citations)
	}
}

func TestClient_requiresAPIKey(t *testing.T) {
	cfg := Config{BaseURL: "http://127.0.0.1:1", CachePath: filepath.Join(t.TempDir(), "e.sqlite")}
	cli := newClient(cfg)
	_, err := cli.Embed(context.Background(), []string{"hi"})
	if err == nil || !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Fatalf("got %v", err)
	}
}

type fakeAPI struct {
	embedCalls atomic.Int32
	chatCalls  atomic.Int32
	lastUser   atomic.Value
}

func harness(t *testing.T) (*store.Store, Config, *fakeAPI) {
	t.Helper()
	api := &fakeAPI{}
	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)

	index := filepath.Join(t.TempDir(), "index.sqlite")
	err := store.Create(index, store.Meta{SHA: "testha", DataRoot: t.TempDir(), IngestedAt: store.Now()}, []parse.Entity{
		{Kind: "spell", Name: "Fireball", Source: "PHB", SRD: true, JSON: json.RawMessage(`{"name":"Fireball"}`), Text: "A bright streak flashes and explodes in a bloom of fire and flame."},
		{Kind: "item", Name: "Longsword", Source: "PHB", JSON: json.RawMessage(`{"name":"Longsword"}`), Text: "A martial melee weapon with a steel blade that deals slashing damage."},
	}, []parse.Document{
		{Kind: "bookSection", ParentID: "PHB", Section: "Holding Breath", JSON: json.RawMessage(`{}`), Text: "A creature can hold its breath underwater before it starts suffocating."},
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(index)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		APIKey:     "test-key",
		BaseURL:    srv.URL,
		EmbedModel: "fake-embed",
		AskModel:   "fake-ask",
		CachePath:  filepath.Join(t.TempDir(), "embeddings.sqlite"),
		HTTPClient: srv.Client(),
		Progress:   io.Discard,
	}
	return st, cfg, api
}

func (f *fakeAPI) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/embeddings", func(w http.ResponseWriter, r *http.Request) {
		f.embedCalls.Add(1)
		var req struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		type row struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		var data []row
		for i, s := range req.Input {
			data = append(data, row{Index: i, Embedding: keywordEmbed(s)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
	mux.HandleFunc("/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		f.chatCalls.Add(1)
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		user := ""
		for _, m := range req.Messages {
			if m.Role == "user" {
				user = m.Content
			}
		}
		f.lastUser.Store(user)
		answer := "I don't know."
		if strings.Contains(user, "Fireball") {
			answer = "Fireball explodes in fire (spell, Fireball, PHB)."
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": answer}},
			},
		})
	})
	return mux
}

func keywordEmbed(text string) []float32 {
	t := strings.ToLower(text)
	v := []float32{0.05, 0.05, 0.05, 0.05}
	add := func(i int, words ...string) {
		for _, w := range words {
			if strings.Contains(t, w) {
				v[i] += 1
			}
		}
	}
	add(0, "fire", "flame", "burn", "explode", "bloom")
	add(1, "sword", "blade", "slash", "steel")
	add(2, "breath", "suffocat", "underwater")
	return v
}
