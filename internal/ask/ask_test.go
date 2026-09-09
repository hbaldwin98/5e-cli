package ask

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

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

func TestRetrieve_skipsAdventureDocs(t *testing.T) {
	st, cfg, _ := harness(t)
	defer st.Close()

	hits, err := Retrieve(context.Background(), st, cfg, Query{Text: "goblin hideout cragmaw", Limit: 8})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if h.Kind == "adventureSection" {
			t.Fatalf("default retrieve mixed adventure %+v", hits)
		}
	}

	hits, err = Retrieve(context.Background(), st, cfg, Query{Text: "goblin hideout cragmaw", Limit: 8, Adventure: "LMoP"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Name != "Cragmaw Hideout" {
		t.Fatalf("adventure retrieve %+v", hits)
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
		{Kind: "adventureSection", ParentID: "LMoP", Section: "Cragmaw Hideout", JSON: json.RawMessage(`{}`), Text: "Goblins nest in the Cragmaw hideout in the hills."},
	}, nil)
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
	add(3, "goblin", "hideout", "cragmaw")
	return v
}

func TestSplitText_windowsStayUnderLimit(t *testing.T) {
	const window = 100
	// Unique words: repetitive text would make the overlap in reassemble
	// ambiguous and the coverage check meaningless.
	var b strings.Builder
	for i := range 1200 {
		fmt.Fprintf(&b, "w%05d ", i)
	}
	body := strings.TrimSpace(b.String())
	parts := splitText(body, window)
	if len(parts) < 2 {
		t.Fatalf("want several windows, got %d", len(parts))
	}
	for i, p := range parts {
		if n := utf8.RuneCountInString(p); n > window {
			t.Fatalf("part %d is %d runes, over the %d window", i, n, window)
		}
	}
	if got := reassemble(parts); got != body {
		t.Fatalf("windows do not cover the text: rebuilt %d of %d runes", len(got), len(body))
	}
}

// reassemble stitches overlapping windows back together, so the test proves
// the split covers every rune rather than just that it produced windows.
func reassemble(parts []string) string {
	out := parts[0]
	for _, p := range parts[1:] {
		overlap := 0
		for n := min(len(out), len(p)); n > 0; n-- {
			if strings.HasSuffix(out, p[:n]) {
				overlap = n
				break
			}
		}
		out += p[overlap:]
	}
	return out
}

func TestSplitText_shortTextIsOneWindow(t *testing.T) {
	if got := splitText("a bolt of testing", 100); len(got) != 1 || got[0] != "a bolt of testing" {
		t.Fatalf("got %q", got)
	}
}

func TestWindowRunes_fitsTheModelLimit(t *testing.T) {
	// The window must stay under the limit even if real text tokenizes at the
	// pessimistic ratio the sizing assumes.
	for _, limit := range []int{512, 8192} {
		w := windowRunes(limit)
		if got := estimateTokens(strings.Repeat("x", w)); got > limit {
			t.Fatalf("limit %d: a full window estimates %d tokens", limit, got)
		}
	}
}

func TestBatchEnd_respectsCountAndTokenBudget(t *testing.T) {
	small := make([]chunk, 200)
	for i := range small {
		small[i] = chunk{Text: "short"}
	}
	if got := batchEnd(small, 0); got != embedBatch {
		t.Fatalf("small chunks should fill the batch, got %d", got)
	}

	big := make([]chunk, 10)
	for i := range big {
		big[i] = chunk{Text: strings.Repeat("x", maxBatchTokens)}
	}
	got := batchEnd(big, 0)
	if got < 1 {
		t.Fatal("batchEnd must always take at least one chunk")
	}
	if got >= len(big) {
		t.Fatalf("oversized chunks should split the batch, got %d", got)
	}
}

func TestChunkID_distinguishesParts(t *testing.T) {
	a := chunk{Kind: "bookSection", Name: "Combat", Source: "PHB", Part: 0}
	b := chunk{Kind: "bookSection", Name: "Combat", Source: "PHB", Part: 1}
	if a.id() == b.id() {
		t.Fatalf("parts share id %q, which would collide on the vectors primary key", a.id())
	}
}

func TestAsk_promptCarriesFullSourceText(t *testing.T) {
	st, cfg, api := harness(t)
	defer st.Close()

	if _, err := Ask(context.Background(), st, cfg, Query{Text: "what does fireball do", Limit: 3}); err != nil {
		t.Fatal(err)
	}
	prompt, _ := api.lastUser.Load().(string)
	// The whole sentence must reach the model, not the leading fragment a
	// display snippet would carry.
	const full = "A bright streak flashes and explodes in a bloom of fire and flame."
	if !strings.Contains(prompt, full) {
		t.Fatalf("prompt is missing the full source text:\n%s", prompt)
	}
}

func TestUserPrompt_clipsOnlyWhatExceedsTheBudget(t *testing.T) {
	short := "a short condition entry"
	long := strings.Repeat("long rules text. ", 500) // 8500 runes
	ranked := []scoredChunk{
		{chunk: chunk{Kind: "spell", Name: "Long", Source: "PHB", Text: long}},
		{chunk: chunk{Kind: "condition", Name: "Short", Source: "PHB", Text: short}},
	}
	prompt := userPrompt("q", ranked, 4000)

	if !strings.Contains(prompt, short) {
		t.Fatal("a short source must never be clipped")
	}
	if utf8.RuneCountInString(prompt) > 4000+len(short)+200 {
		t.Fatalf("prompt overshot the budget: %d runes", utf8.RuneCountInString(prompt))
	}
	if !strings.Contains(prompt, "long rules text. long rules text.") {
		t.Fatal("the long source should still contribute a substantial body")
	}
}

func TestShareBudget_givesUnusedShareToLongSources(t *testing.T) {
	texts := []string{strings.Repeat("x", 1000), "tiny", strings.Repeat("y", 1000)}
	shares := shareBudget(texts, 900)

	if shares[1] != 4 {
		t.Fatalf("short text should get exactly what it needs, got %d", shares[1])
	}
	if total := shares[0] + shares[1] + shares[2]; total > 900 {
		t.Fatalf("shares exceed the budget: %d", total)
	}
	// An equal split would have handed each source 300; the long ones should
	// have absorbed what "tiny" did not use.
	if shares[0] <= 300 || shares[2] <= 300 {
		t.Fatalf("long sources did not absorb the leftover: %v", shares)
	}
}

func TestShareBudget_zeroBudgetIsSafe(t *testing.T) {
	for _, share := range shareBudget([]string{"a", "b"}, 0) {
		if share != 0 {
			t.Fatalf("want no text at a zero budget, got %d", share)
		}
	}
}

func TestVectorScope_excludesAdventureDocsByDefault(t *testing.T) {
	where, args := vectorScope{}.where()
	if !strings.Contains(where, "adventureSection") || !strings.Contains(where, "adventureLocation") {
		t.Fatalf("default scope must exclude module prose: %q", where)
	}
	if len(args) != 0 {
		t.Fatalf("unexpected args %v", args)
	}
}

func TestVectorScope_adventureNarrowsToThatSource(t *testing.T) {
	where, args := vectorScope{Adventure: "LMoP"}.where()
	if strings.Contains(where, "NOT IN") {
		t.Fatalf("an adventure query must not exclude module prose: %q", where)
	}
	if len(args) != 1 || args[0] != "LMoP" {
		t.Fatalf("args %v", args)
	}
}

func TestVectorScope_kindAndSources(t *testing.T) {
	where, args := vectorScope{Kind: "spell", Sources: []string{"PHB", "XPHB"}}.where()
	if !strings.Contains(where, "kind = ?") || !strings.Contains(where, "IN (?,?)") {
		t.Fatalf("where %q", where)
	}
	if len(args) != 3 {
		t.Fatalf("args %v", args)
	}
}

func TestRetrieve_adventureScopeReachesModuleProse(t *testing.T) {
	st, cfg, _ := harness(t)
	defer st.Close()

	// Without an adventure the module section is unreachable, which is what
	// made adventure-only NPCs unanswerable.
	hits, err := Retrieve(context.Background(), st, cfg, Query{Text: "goblin hideout cragmaw", Limit: 8})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if h.Name == "Cragmaw Hideout" {
			t.Fatal("default retrieve should not reach module prose")
		}
	}

	hits, err = Retrieve(context.Background(), st, cfg, Query{Text: "goblin hideout cragmaw", Limit: 8, Adventure: "LMoP"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Name != "Cragmaw Hideout" {
		t.Fatalf("adventure scope should reach module prose, got %+v", hits)
	}
	if hits[0].Snippet == "" {
		t.Fatal("snippet is empty; text was not attached after ranking")
	}
}
