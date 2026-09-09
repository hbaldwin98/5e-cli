package search

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/hbaldwin98/5e-cli/internal/edition"
	"github.com/hbaldwin98/5e-cli/internal/parse"
	"github.com/hbaldwin98/5e-cli/internal/store"
)

func TestSearch_prefersConfiguredEdition(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	err := store.Create(path, store.Meta{SHA: "s", DataRoot: t.TempDir(), IngestedAt: store.Now()}, []parse.Entity{
		{Kind: "skill", Name: "Testing", Source: "PHB", JSON: json.RawMessage(`{}`), Text: "you test things"},
		{Kind: "skill", Name: "Testing", Source: "XPHB", JSON: json.RawMessage(`{}`), Text: "you test things"},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	hits, err := Search(st, Query{Text: "Testing", Limit: 5, Edition: edition.Modern})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) < 2 || hits[0].Source != "XPHB" {
		t.Fatalf("2024 first: %+v", hits)
	}

	hits, err = Search(st, Query{Text: "Testing", Limit: 5, Edition: edition.Classic})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) < 2 || hits[0].Source != "PHB" {
		t.Fatalf("2014 first: %+v", hits)
	}
}

func TestSearch_shorterNameBreaksTies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	err := store.Create(path, store.Meta{SHA: "s", DataRoot: t.TempDir(), IngestedAt: store.Now()}, []parse.Entity{
		{Kind: "item", Name: "Staff of Endmagic", Source: "PHB", JSON: json.RawMessage(`{}`), Text: "staff"},
		{Kind: "item", Name: "Tiny magic", Source: "TCE", JSON: json.RawMessage(`{}`), Text: "tiny"},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	hits, err := Search(st, Query{Text: "magic", Limit: 5, Edition: edition.All})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) < 2 || hits[0].Name != "Tiny magic" {
		t.Fatalf("shorter first: %+v", hits)
	}
}

func TestSearch_srdDropsNonSRDAndDocuments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	err := store.Create(path, store.Meta{SHA: "s", DataRoot: t.TempDir(), IngestedAt: store.Now()}, []parse.Entity{
		{Kind: "spell", Name: "Testbolt", Source: "PHB", SRD: true, JSON: json.RawMessage(`{}`), Text: "a testing bolt"},
		{Kind: "item", Name: "Secret Blade", Source: "PHB", SRD: false, JSON: json.RawMessage(`{}`), Text: "a testing blade"},
	}, []parse.Document{
		{Kind: "bookSection", ParentID: "PHB", Section: "Testing Breath", JSON: json.RawMessage(`{}`), Text: "testing breath rules"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	all, err := Search(st, Query{Text: "testing", Limit: 10, Edition: edition.All})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) < 3 {
		t.Fatalf("unfiltered %+v", all)
	}

	hits, err := Search(st, Query{Text: "testing", Limit: 10, Edition: edition.All, SRD: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Name != "Testbolt" {
		t.Fatalf("srd %+v", hits)
	}
}

func TestSearch_defaultSkipsAdventureSections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	err := store.Create(path, store.Meta{SHA: "s", DataRoot: t.TempDir(), IngestedAt: store.Now()}, []parse.Entity{
		{Kind: "spell", Name: "Testbolt", Source: "PHB", JSON: json.RawMessage(`{}`), Text: "a testing bolt"},
	}, []parse.Document{
		{Kind: "bookSection", ParentID: "PHB", Section: "Testing Breath", JSON: json.RawMessage(`{}`), Text: "testing breath rules"},
		{Kind: "adventureSection", ParentID: "LMoP", Section: "Cragmaw Hideout", JSON: json.RawMessage(`{}`), Text: "testing goblins in the hideout"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	hits, err := Search(st, Query{Text: "testing", Limit: 10, Edition: edition.All})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if h.Kind == "adventureSection" {
			t.Fatalf("default search mixed adventure: %+v", hits)
		}
	}

	hits, err = Search(st, Query{Text: "hideout", Limit: 10, Edition: edition.All, Adventure: "LMoP"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Name != "Cragmaw Hideout" {
		t.Fatalf("adventure search %+v", hits)
	}
}

func TestSearch_adventureNPCFromAppearance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	err := store.Create(path, store.Meta{SHA: "s", DataRoot: t.TempDir(), IngestedAt: store.Now()}, []parse.Entity{
		{Kind: "adventure", Name: "Lost Mine of Testing", Source: "LMoP", JSON: json.RawMessage(`{}`), Text: "phandelver"},
		{Kind: "monster", Name: "Goblin", Source: "MM", JSON: json.RawMessage(`{}`), Text: "a small humanoid"},
	}, []parse.Document{
		{Kind: "adventureLocation", ParentID: "LMoP", Section: "Cave Mouth", JSON: json.RawMessage(`{}`), Text: "a goblin watches"},
	}, []parse.Appearance{
		{Adventure: "LMoP", Role: "npc", Kind: "monster", Name: "Goblin", Source: "MM", Location: "Cave Mouth"},
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	hits, err := Search(st, Query{Text: "goblin", Limit: 10, Edition: edition.All, Adventure: "LMoP", Kind: "monster"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Name != "Goblin" || hits[0].Source != "MM" {
		t.Fatalf("npc %+v", hits)
	}

	hits, err = Search(st, Query{Text: "cave", Limit: 10, Edition: edition.All, Adventure: "LMoP", Kind: "location"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Name != "Cave Mouth" {
		t.Fatalf("location %+v", hits)
	}
}

func TestSnippet_clipsOnARuneBoundary(t *testing.T) {
	text := strings.Repeat("鬼", 20) // each rune is 3 bytes; clipping by byte index would corrupt it
	got := snippet(text, 10)
	if !utf8.ValidString(got) {
		t.Fatalf("snippet produced invalid UTF-8: %q", got)
	}
	if want := strings.Repeat("鬼", 10) + "…"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
