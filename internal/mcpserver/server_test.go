package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hbaldwin98/5e-cli/internal/ask"
	"github.com/hbaldwin98/5e-cli/internal/parse"
	"github.com/hbaldwin98/5e-cli/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestGet_returnsEntityJSON(t *testing.T) {
	st := testStore(t)
	defer st.Close()
	session := connect(t, New(st, Options{}))
	defer session.Close()

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "get",
		Arguments: map[string]any{
			"kind": "spell",
			"name": "Testbolt",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
	out := toolJSON(t, res)
	if out["name"] != "Testbolt" || out["kind"] != "spell" {
		t.Fatalf("got %#v", out)
	}
}

func TestSearch_ranksFuzzyName(t *testing.T) {
	st := testStore(t)
	defer st.Close()
	session := connect(t, New(st, Options{}))
	defer session.Close()

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "search",
		Arguments: map[string]any{
			"query": "testbol",
			"limit": 5,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
	out := toolJSON(t, res)
	hits, _ := out["hits"].([]any)
	if len(hits) == 0 {
		t.Fatalf("no hits: %#v", out)
	}
	first, _ := hits[0].(map[string]any)
	if first["name"] != "Testbolt" {
		t.Fatalf("first hit %#v", first)
	}
}

func TestAdventureSearch_npcAppearance(t *testing.T) {
	st := testStore(t)
	defer st.Close()
	session := connect(t, New(st, Options{}))
	defer session.Close()

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "adventure_search",
		Arguments: map[string]any{
			"adventure": "LMoP",
			"query":     "goblin",
			"kind":      "npc",
			"limit":     5,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
	hits, _ := toolJSON(t, res)["hits"].([]any)
	if len(hits) == 0 {
		t.Fatalf("no hits")
	}
	first, _ := hits[0].(map[string]any)
	if first["name"] != "Goblin" {
		t.Fatalf("first hit %#v", first)
	}
}

func TestSemanticSearch_doesNotCallChat(t *testing.T) {
	st := testStore(t)
	defer st.Close()

	var chat int
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			low := strings.ToLower(s)
			if strings.Contains(low, "bolt") || strings.Contains(low, "testing") {
				v = []float32{1, 0}
			}
			if strings.Contains(low, "skill") || strings.Contains(low, "phb") {
				v = []float32{0, 1}
			}
			data = append(data, row{Index: i, Embedding: v})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(api.Close)

	session := connect(t, New(st, Options{Ask: ask.Config{
		APIKey:     "test",
		BaseURL:    api.URL,
		EmbedModel: "fake-embed",
		CachePath:  filepath.Join(t.TempDir(), "embeddings.sqlite"),
		HTTPClient: api.Client(),
		Progress:   io.Discard,
	}}))
	defer session.Close()

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "semantic_search",
		Arguments: map[string]any{
			"query": "testing bolt",
			"limit": 3,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
	if chat != 0 {
		t.Fatalf("chat called %d times", chat)
	}
	out := toolJSON(t, res)
	hits, _ := out["hits"].([]any)
	if len(hits) == 0 {
		t.Fatalf("no hits %#v", out)
	}
	first, _ := hits[0].(map[string]any)
	if first["name"] != "Testbolt" {
		t.Fatalf("first hit %#v", first)
	}
}

func TestGet_ambiguousRequiresSource(t *testing.T) {
	st := testStore(t)
	defer st.Close()
	session := connect(t, New(st, Options{Edition: "all"}))
	defer session.Close()

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "get",
		Arguments: map[string]any{
			"kind": "skill",
			"name": "Testing",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected ambiguous error")
	}
}

func TestGet_srdDropsNonSRD(t *testing.T) {
	st := testStore(t)
	defer st.Close()
	session := connect(t, New(st, Options{SRD: true}))
	defer session.Close()

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "get",
		Arguments: map[string]any{
			"kind": "spell",
			"name": "Testbolt",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}
	out := toolJSON(t, res)
	if out["name"] != "Testbolt" {
		t.Fatalf("got %#v", out)
	}

	res, err = session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "get",
		Arguments: map[string]any{
			"kind": "skill",
			"name": "Testing",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("expected missing non-srd skill")
	}

	res, err = session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "search",
		Arguments: map[string]any{
			"query": "testing",
			"limit": 10,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("search error: %+v", res.Content)
	}
	hits, _ := toolJSON(t, res)["hits"].([]any)
	if len(hits) != 1 {
		t.Fatalf("srd search %#v", hits)
	}
	first, _ := hits[0].(map[string]any)
	if first["name"] != "Testbolt" {
		t.Fatalf("first hit %#v", first)
	}
}

func testStore(t *testing.T) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "index.sqlite")
	err := store.Create(path, store.Meta{SHA: "mcp", DataRoot: t.TempDir(), IngestedAt: store.Now()}, []parse.Entity{
		{Kind: "spell", Name: "Testbolt", Source: "PHB", SRD: true, JSON: json.RawMessage(`{"name":"Testbolt"}`), Text: "a bolt of testing"},
		{Kind: "skill", Name: "Testing", Source: "PHB", JSON: json.RawMessage(`{}`), Text: "phb"},
		{Kind: "skill", Name: "Testing", Source: "XPHB", JSON: json.RawMessage(`{}`), Text: "xphb"},
		{Kind: "adventure", Name: "Lost Mine of Testing", Source: "LMoP", JSON: json.RawMessage(`{}`), Text: "phandelver"},
		{Kind: "monster", Name: "Goblin", Source: "MM", JSON: json.RawMessage(`{}`), Text: "a small humanoid"},
	}, []parse.Document{
		{Kind: "bookSection", ParentID: "PHB", Section: "Holding Breath", JSON: json.RawMessage(`{}`), Text: "hold breath"},
		{Kind: "adventureSection", ParentID: "LMoP", Section: "Cragmaw Hideout", JSON: json.RawMessage(`{}`), Text: "goblins nest in the hideout"},
	}, []parse.Appearance{
		{Adventure: "LMoP", Role: "npc", Kind: "monster", Name: "Goblin", Source: "MM", Location: "Cragmaw Hideout"},
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func connect(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := server.Connect(context.Background(), t1, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
	session, err := client.Connect(context.Background(), t2, nil)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func toolJSON(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	if res.StructuredContent != nil {
		raw, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if len(res.Content) == 0 {
		t.Fatal("no content")
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content %T", res.Content[0])
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text.Text), &out); err != nil {
		t.Fatalf("json %v: %s", err, text.Text)
	}
	return out
}
