package search

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/hbaldwin98/5e-cli/internal/edition"
	"github.com/hbaldwin98/5e-cli/internal/parse"
	"github.com/hbaldwin98/5e-cli/internal/store"
)

func TestSearch_prefersConfiguredEdition(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	err := store.Create(path, store.Meta{SHA: "s", DataRoot: t.TempDir(), IngestedAt: store.Now()}, []parse.Entity{
		{Kind: "skill", Name: "Testing", Source: "PHB", JSON: json.RawMessage(`{}`), Text: "you test things"},
		{Kind: "skill", Name: "Testing", Source: "XPHB", JSON: json.RawMessage(`{}`), Text: "you test things"},
	}, nil)
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
	}, nil)
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
