package store

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/hbaldwin98/5e-cli/internal/parse"
)

func TestLookup_entityAndBookSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	err := Create(path, Meta{SHA: "t", DataRoot: t.TempDir(), IngestedAt: Now()}, []parse.Entity{
		{Kind: "spell", Name: "Testbolt", Source: "PHB", JSON: json.RawMessage(`{"name":"Testbolt"}`), Text: "a bolt"},
	}, []parse.Document{
		{Kind: "bookSection", ParentID: "PHB", Section: "Holding Breath", JSON: json.RawMessage(`{"name":"Holding Breath"}`), Text: "hold breath"},
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ents, err := st.Lookup("spell", "Testbolt", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 || ents[0].Name != "Testbolt" {
		t.Fatalf("entity lookup %+v", ents)
	}

	secs, err := st.Lookup("bookSection", "Holding Breath", "PHB")
	if err != nil {
		t.Fatal(err)
	}
	if len(secs) != 1 || secs[0].Name != "Holding Breath" || secs[0].Source != "PHB" {
		t.Fatalf("section lookup %+v", secs)
	}
	if string(secs[0].JSON) != `{"name":"Holding Breath"}` {
		t.Fatalf("section json %s", secs[0].JSON)
	}

	anyPHB, err := st.Lookup("bookSection", "Holding Breath", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(anyPHB) != 1 {
		t.Fatalf("unfiltered section %+v", anyPHB)
	}

	miss, err := st.Lookup("spell", "Missing", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(miss) != 0 {
		t.Fatalf("want empty, got %+v", miss)
	}
}
