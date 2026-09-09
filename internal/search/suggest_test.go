package search

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hbaldwin98/5e-cli/internal/parse"
	"github.com/hbaldwin98/5e-cli/internal/store"
)

func openTestStore(t *testing.T, entities []parse.Entity) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "index.sqlite")
	if err := store.Create(path, store.Meta{SHA: "s", DataRoot: t.TempDir(), IngestedAt: store.Now()}, entities, nil, nil); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestSuggest_typoResolvesToNearestName(t *testing.T) {
	st := openTestStore(t, []parse.Entity{
		{Kind: "spell", Name: "Fireball", Source: "XPHB", JSON: json.RawMessage(`{}`)},
		{Kind: "spell", Name: "Fire Bolt", Source: "XPHB", JSON: json.RawMessage(`{}`)},
		{Kind: "monster", Name: "Fireball", Source: "MM", JSON: json.RawMessage(`{}`)},
	})

	suggestions, err := Suggest(st, "spell", "Firebal", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(suggestions) == 0 || suggestions[0].Entity.Name != "Fireball" {
		t.Fatalf("want Fireball first, got %+v", suggestions)
	}
	for _, s := range suggestions {
		if s.Entity.Kind != "spell" {
			t.Fatalf("suggestion leaked another kind: %+v", s)
		}
	}
}

func TestNotFoundError_includesSuggestionsAndFallsBackWithout(t *testing.T) {
	st := openTestStore(t, []parse.Entity{
		{Kind: "spell", Name: "Fireball", Source: "XPHB", JSON: json.RawMessage(`{}`)},
	})

	err := NotFoundError(st, "spell", "Firebal")
	if err == nil || !strings.Contains(err.Error(), "did you mean") || !strings.Contains(err.Error(), "Fireball") {
		t.Fatalf("want a suggestion, got %v", err)
	}

	err = NotFoundError(st, "spell", "Something Totally Unrelated Xyzzy")
	if err == nil || strings.Contains(err.Error(), "did you mean") {
		t.Fatalf("want a bare not-found with no plausible match, got %v", err)
	}

	if err := NotFoundError(nil, "spell", "Fireball"); err == nil || strings.Contains(err.Error(), "did you mean") {
		t.Fatalf("nil store should skip suggestions, got %v", err)
	}
}

func TestFilterByQuery_fallsBackToFuzzyOnlyWhenSubstringMisses(t *testing.T) {
	ents := []store.Entity{
		{Kind: "spell", Name: "Fireball"},
		{Kind: "spell", Name: "Fire Bolt"},
		{Kind: "spell", Name: "Magic Missile"},
	}

	exact := FilterByQuery(ents, "fire")
	if len(exact) != 2 {
		t.Fatalf("substring match: %+v", exact)
	}

	fuzzy := FilterByQuery(ents, "Firebal")
	if len(fuzzy) == 0 || fuzzy[0].Name != "Fireball" {
		t.Fatalf("fuzzy fallback: %+v", fuzzy)
	}

	if got := FilterByQuery(ents, ""); len(got) != len(ents) {
		t.Fatalf("empty query should return everything unchanged: %+v", got)
	}
}
