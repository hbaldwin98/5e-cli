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
	}, nil)
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

func TestNames_includesSRD(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	err := Create(path, Meta{SHA: "t", DataRoot: t.TempDir(), IngestedAt: Now()}, []parse.Entity{
		{Kind: "spell", Name: "Testbolt", Source: "PHB", SRD: true, JSON: json.RawMessage(`{}`), Text: "srd spell"},
		{Kind: "item", Name: "Secret Blade", Source: "PHB", SRD: false, JSON: json.RawMessage(`{}`), Text: "not srd"},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	names, err := st.Names()
	if err != nil {
		t.Fatal(err)
	}
	srd := map[string]bool{}
	for _, e := range names {
		srd[e.Name] = e.SRD
	}
	if !srd["Testbolt"] || srd["Secret Blade"] {
		t.Fatalf("%v", srd)
	}
	only := SRDOnly(names)
	if len(only) != 1 || only[0].Name != "Testbolt" {
		t.Fatalf("SRDOnly %+v", only)
	}
}

func TestFTSEntities_srdOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	err := Create(path, Meta{SHA: "t", DataRoot: t.TempDir(), IngestedAt: Now()}, []parse.Entity{
		{Kind: "spell", Name: "Testbolt", Source: "PHB", SRD: true, JSON: json.RawMessage(`{}`), Text: "a testing bolt"},
		{Kind: "item", Name: "Secret Blade", Source: "PHB", SRD: false, JSON: json.RawMessage(`{}`), Text: "a testing blade"},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	all, err := st.FTSEntities("testing", "", nil, false, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("unfiltered %+v", all)
	}
	srd, err := st.FTSEntities("testing", "", nil, true, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(srd) != 1 || srd[0].Name != "Testbolt" {
		t.Fatalf("srdOnly %+v", srd)
	}
}

func TestAppearanceSet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	err := Create(path, Meta{SHA: "t", DataRoot: t.TempDir(), IngestedAt: Now()}, []parse.Entity{
		{Kind: "monster", Name: "Goblin", Source: "MM", JSON: json.RawMessage(`{}`), Text: "goblin"},
	}, nil, []parse.Appearance{
		{Adventure: "LMoP", Role: "npc", Kind: "monster", Name: "Goblin", Source: "MM", Location: "Cave Mouth"},
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	seen, err := st.AppearanceSet("LMoP", "npc")
	if err != nil {
		t.Fatal(err)
	}
	if !seen[AppearanceKey("monster", "Goblin", "MM")] {
		t.Fatalf("%v", seen)
	}
}

func TestAdventureAppearances_filtersChapterAndLocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	err := Create(path, Meta{SHA: "t", DataRoot: t.TempDir(), IngestedAt: Now()}, nil, nil, []parse.Appearance{
		{Adventure: "LMoP", Role: "npc", Kind: "monster", Name: "Goblin", Source: "MM", Chapter: "Chapter One", Location: "Cave Mouth"},
		{Adventure: "LMoP", Role: "item", Kind: "item", Name: "Potion", Source: "DMG", Chapter: "Chapter Two", Location: "Armory"},
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	apps, err := st.AdventureAppearances("LMoP", "npc", "Chapter One", "Cave Mouth")
	if err != nil || len(apps) != 1 || apps[0].Name != "Goblin" {
		t.Fatalf("filtered appearances: %v %+v", err, apps)
	}
}

func TestReferences_incomingAndOutgoing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	err := Create(path, Meta{SHA: "t", DataRoot: t.TempDir(), IngestedAt: Now()}, []parse.Entity{
		{Kind: "item", Name: "Test Wand", Source: "DMG", JSON: json.RawMessage(`{}`), Text: "wand", Edges: []parse.Edge{{Tag: "spell", ToKind: "spell", ToName: "Testbolt", ToSource: "PHB"}}},
		{Kind: "spell", Name: "Testbolt", Source: "PHB", JSON: json.RawMessage(`{}`), Text: "bolt"},
		{Kind: "spell", Name: "Otherbolt", Source: "PHB", JSON: json.RawMessage(`{}`), Text: "bolt", Edges: []parse.Edge{{Tag: "spell", ToKind: "spell", ToName: "Testbolt", ToSource: "PHB"}}},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	out, err := st.References("item", "Test Wand", "DMG", "outgoing", "")
	if err != nil || len(out) != 1 || out[0].To.Name != "Testbolt" || out[0].Direction != "outgoing" {
		t.Fatalf("outgoing: %v %+v", err, out)
	}
	in, err := st.References("spell", "Testbolt", "PHB", "incoming", "spell")
	if err != nil || len(in) != 2 || in[0].Direction != "incoming" {
		t.Fatalf("incoming: %v %+v", err, in)
	}
}
