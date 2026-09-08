package store

import (
	"encoding/json"
	"path/filepath"
	"strings"
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

func TestGetDocument_optionalSectionAndParentFilters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	err := Create(path, Meta{SHA: "t", DataRoot: t.TempDir(), IngestedAt: Now()}, nil, []parse.Document{
		{Kind: "bookSection", ParentID: "PHB", Section: "Holding Breath", JSON: json.RawMessage(`{"name":"Holding Breath"}`), Text: "hold"},
		{Kind: "bookSection", ParentID: "PHB", Section: "Running", JSON: json.RawMessage(`{"name":"Running"}`), Text: "run"},
		{Kind: "bookSection", ParentID: "XPHB", Section: "Holding Breath", JSON: json.RawMessage(`{"name":"Holding Breath"}`), Text: "hold"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	tests := []struct {
		name            string
		section, parent string
		want            int
	}{
		{name: "all", want: 3},
		{name: "parent", parent: "phb", want: 2},
		{name: "section", section: "running", want: 1},
		{name: "both", section: "holding breath", parent: "PHB", want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			docs, err := st.GetDocument("bookSection", tt.section, tt.parent)
			if err != nil {
				t.Fatal(err)
			}
			if len(docs) != tt.want {
				t.Fatalf("got %d documents: %+v", len(docs), docs)
			}
		})
	}
}

func TestLookup_mergesDuplicateDocumentNamesWithinParent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	err := Create(path, Meta{SHA: "t", DataRoot: t.TempDir(), IngestedAt: Now()}, nil, []parse.Document{
		{Kind: "adventureLocation", ParentID: "LMoP", Section: "Redbrand Ruffians", JSON: json.RawMessage(`{"page":15,"entries":["The town encounter."]}`), Text: "The town encounter."},
		{Kind: "adventureLocation", ParentID: "LMoP", Section: "Redbrand Ruffians", JSON: json.RawMessage(`{"page":19,"entries":["The full encounter."]}`), Text: "The full encounter."},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ents, err := st.Lookup("adventureLocation", "Redbrand Ruffians", "LMoP")
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 {
		t.Fatalf("duplicate document lookup %+v", ents)
	}
	if !strings.Contains(ents[0].Text, "The town encounter.") || !strings.Contains(ents[0].Text, "The full encounter.") {
		t.Fatalf("merged text %q", ents[0].Text)
	}
	if !strings.Contains(string(ents[0].JSON), `"page":19`) {
		t.Fatalf("merged JSON %s", ents[0].JSON)
	}
	if !strings.Contains(string(ents[0].JSON), "The town encounter.") {
		t.Fatalf("merged JSON lost first document %s", ents[0].JSON)
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
		{Adventure: "LMoP", Role: "npc", Kind: "monster", Name: "Orc", Source: "MM", Chapter: "Chapter One", Location: "Armory"},
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
	tests := []struct {
		name              string
		adventure, role   string
		chapter, location string
		want              []string
	}{
		{name: "all", adventure: "lmop", want: []string{"Potion", "Goblin", "Orc"}},
		{name: "role", adventure: "LMoP", role: "npc", want: []string{"Goblin", "Orc"}},
		{name: "chapter", adventure: "LMoP", chapter: "chapter one", want: []string{"Goblin", "Orc"}},
		{name: "location", adventure: "LMoP", location: "ARMORY", want: []string{"Potion", "Orc"}},
		{name: "all filters", adventure: "LMoP", role: "npc", chapter: "Chapter One", location: "Cave Mouth", want: []string{"Goblin"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apps, err := st.AdventureAppearances(tt.adventure, tt.role, tt.chapter, tt.location)
			if err != nil {
				t.Fatal(err)
			}
			if len(apps) != len(tt.want) {
				t.Fatalf("got %d appearances: %+v", len(apps), apps)
			}
			for i, want := range tt.want {
				if apps[i].Name != want {
					t.Fatalf("appearance %d = %q, want %q", i, apps[i].Name, want)
				}
			}
		})
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

func TestGetBare_skipsEdges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	err := Create(path, Meta{SHA: "t", DataRoot: t.TempDir(), IngestedAt: Now()}, []parse.Entity{
		{Kind: "monster", Name: "Testgoblin", Source: "MM", JSON: json.RawMessage(`{"name":"Testgoblin"}`), Text: "a goblin",
			Edges: []parse.Edge{{Tag: "spell", ToKind: "spell", ToName: "Fireball", ToSource: "PHB"}}},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	full, err := st.Get("monster", "Testgoblin", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(full) != 1 || len(full[0].Edges) != 1 {
		t.Fatalf("Get should load edges: %+v", full)
	}

	bare, err := st.GetBare("monster", "Testgoblin", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(bare) != 1 || bare[0].Name != "Testgoblin" {
		t.Fatalf("GetBare row %+v", bare)
	}
	if bare[0].Edges != nil {
		t.Fatalf("GetBare should not load edges: %+v", bare[0].Edges)
	}
	if string(bare[0].JSON) != `{"name":"Testgoblin"}` {
		t.Fatalf("GetBare json %s", bare[0].JSON)
	}
}

func TestFilteredNames_narrowsInSQL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.sqlite")
	err := Create(path, Meta{SHA: "t", DataRoot: t.TempDir(), IngestedAt: Now()}, []parse.Entity{
		{Kind: "spell", Name: "Testbolt", Source: "PHB", SRD: true, JSON: json.RawMessage(`{}`), Text: "a"},
		{Kind: "spell", Name: "Testbolt", Source: "XPHB", JSON: json.RawMessage(`{}`), Text: "b"},
		{Kind: "monster", Name: "Testgoblin", Source: "MM", JSON: json.RawMessage(`{}`), Text: "c"},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	all, err := st.Names()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("want 3 rows unfiltered, got %d", len(all))
	}
	for _, tc := range []struct {
		name   string
		filter NameFilter
		want   int
	}{
		{"kind", NameFilter{Kind: "spell"}, 2},
		{"source", NameFilter{Sources: []string{"xphb"}}, 1},
		{"srd", NameFilter{SRDOnly: true}, 1},
		{"combined", NameFilter{Kind: "spell", Sources: []string{"PHB"}, SRDOnly: true}, 1},
		{"no match", NameFilter{Kind: "item"}, 0},
	} {
		got, err := st.FilteredNames(tc.filter)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(got) != tc.want {
			t.Fatalf("%s: want %d rows, got %d (%+v)", tc.name, tc.want, len(got), got)
		}
	}
}
