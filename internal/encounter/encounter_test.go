package encounter

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/hbaldwin98/5e-cli/internal/edition"
	"github.com/hbaldwin98/5e-cli/internal/parse"
	"github.com/hbaldwin98/5e-cli/internal/store"
)

func TestSearch_filtersBeforeLimitAndReturnsMetadata(t *testing.T) {
	st := encounterStore(t)
	defer st.Close()

	hits, err := Search(st, Query{
		Text:    "goblin",
		CR:      "1/4",
		Type:    "humanoid",
		Size:    "small",
		Edition: edition.All,
		Limit:   2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || hits[0].Name != "Goblin" || hits[1].Source != "XPHB" {
		t.Fatalf("hits: %+v", hits)
	}
	if hits[0].CR != "1/4" || hits[0].Type != "humanoid" || hits[0].Size != "Small" {
		t.Fatalf("metadata: %+v", hits[0])
	}
	if hits[0].Page != 42 || !hits[0].SRD || hits[0].Snippet == "" {
		t.Fatalf("record metadata: %+v", hits[0])
	}
}

func TestSearch_filtersSourceEditionAndSRD(t *testing.T) {
	st := encounterStore(t)
	defer st.Close()

	hits, err := Search(st, Query{Text: "goblin", Sources: []string{"phb"}, Edition: edition.All, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Source != "PHB" {
		t.Fatalf("source filter: %+v", hits)
	}

	hits, err = Search(st, Query{Text: "goblin", Edition: edition.Classic, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Source != "PHB" {
		t.Fatalf("edition filter: %+v", hits)
	}

	hits, err = Search(st, Query{Text: "ogre", Edition: edition.All, SRD: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("srd filter: %+v", hits)
	}
}

func TestSearch_matchesBodyTextAndStructuredMetadata(t *testing.T) {
	st := encounterStore(t)
	defer st.Close()

	hits, err := Search(st, Query{Text: "watchtower", Edition: edition.All, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Name != "Ogre" || hits[0].CR != "2" {
		t.Fatalf("body search: %+v", hits)
	}
}

func encounterStore(t *testing.T) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "index.sqlite")
	entities := []parse.Entity{
		{
			Kind: "monster", Name: "Goblin", Source: "PHB", Page: 42, SRD: true,
			JSON: json.RawMessage(`{"name":"Goblin","source":"PHB","cr":"1/4","type":"humanoid","size":["S"]}`),
			Text: "small humanoid goblin",
		},
		{
			Kind: "monster", Name: "Goblin", Source: "XPHB", SRD: true,
			JSON: json.RawMessage(`{"name":"Goblin","source":"XPHB","cr":"1/4","type":{"type":"humanoid"},"size":["S"]}`),
			Text: "small humanoid goblin",
		},
		{
			Kind: "monster", Name: "Ogre", Source: "MM",
			JSON: json.RawMessage(`{"name":"Ogre","source":"MM","cr":2,"type":{"type":"giant"},"size":"L"}`),
			Text: "A giant watches the watchtower.",
		},
	}
	if err := store.Create(path, store.Meta{SHA: "encounter", DataRoot: t.TempDir(), IngestedAt: store.Now()}, entities, nil, nil); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestSizeMatches_normalizesBothSides(t *testing.T) {
	for _, tc := range []struct {
		value, wanted string
		want          bool
	}{
		{"S", "small", true},
		{"S", "s", true},
		{"Small", "s", true},
		{"Small", "small", true},
		{"small", "S", true},
		{"S", "large", false},
		{"Small", "l", false},
	} {
		if got := sizeMatches(tc.value, tc.wanted); got != tc.want {
			t.Fatalf("sizeMatches(%q, %q) = %v, want %v", tc.value, tc.wanted, got, tc.want)
		}
	}
}

func TestSizes_rendersDisplayNames(t *testing.T) {
	if got := sizes(map[string]any{"size": []any{"S", "Medium"}}); got != "Small, Medium" {
		t.Fatalf("got %q", got)
	}
}
