package adventure

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/hbaldwin98/5e-cli/internal/parse"
	"github.com/hbaldwin98/5e-cli/internal/store"
)

func TestResolve_nameOrID(t *testing.T) {
	st := testAdvStore(t)
	defer st.Close()

	e, err := Resolve(st, "Lost Mine of Testing")
	if err != nil || e.Source != "LMoP" {
		t.Fatalf("name %v %+v", err, e)
	}
	e, err = Resolve(st, "LMoP")
	if err != nil || e.Name != "Lost Mine of Testing" {
		t.Fatalf("id %v %+v", err, e)
	}
	if _, err := Resolve(st, "missing"); err == nil {
		t.Fatal("expected missing")
	}
}

func TestSearchKind(t *testing.T) {
	if SearchKind("npc") != "monster" {
		t.Fatal(SearchKind("npc"))
	}
	if SearchKind("location") != "location" {
		t.Fatal(SearchKind("location"))
	}
	if SearchKind("") != "" {
		t.Fatal("empty")
	}
}

func TestLookup_appearedNPCAndLocation(t *testing.T) {
	st := testAdvStore(t)
	defer st.Close()

	kind, err := GetKind("npc")
	if err != nil || kind != "monster" {
		t.Fatalf("GetKind npc %q %v", kind, err)
	}
	kind, err = GetKind("location")
	if err != nil || kind != "adventureLocation" {
		t.Fatalf("GetKind location %q %v", kind, err)
	}

	ents, err := Lookup(st, "npc", "Goblin", "LMoP")
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 || ents[0].Source != "MM" {
		t.Fatalf("npc %+v", ents)
	}

	ents, err = Lookup(st, "location", "Cave Mouth", "LMoP")
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 || ents[0].Kind != "adventureLocation" {
		t.Fatalf("location %+v", ents)
	}

	ents, err = Lookup(st, "location", "Cragmaw Hideout", "LMoP")
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 || ents[0].Kind != "adventureSection" {
		t.Fatalf("section %+v", ents)
	}
}

func TestList_filtersAdventureContents(t *testing.T) {
	st := testAdvStore(t)
	defer st.Close()

	report, err := List(st, "LMoP", "npc", "Cragmaw Hideout", "Cave Mouth")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Chapters) != 0 || len(report.Locations) != 0 {
		t.Fatalf("npc report included sections: %+v", report)
	}
	if len(report.Appearances) != 1 || report.Appearances[0].Name != "Goblin" {
		t.Fatalf("npc report: %+v", report)
	}
	if report.Appearances[0].Chapter != "Cragmaw Hideout" {
		t.Fatalf("appearance chapter: %+v", report.Appearances[0])
	}

	report, err = List(st, "LMoP", "location", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Chapters) != 1 || len(report.Locations) != 1 || len(report.Appearances) != 0 {
		t.Fatalf("location report: %+v", report)
	}
}

func TestList_allAndItemRoles(t *testing.T) {
	st := testAdvStore(t)
	defer st.Close()

	report, err := List(st, "lmop", " ALL ", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Chapters) != 1 || len(report.Locations) != 1 || len(report.Appearances) != 2 {
		t.Fatalf("all report: %+v", report)
	}
	if report.Appearances[0].Role != "item" || report.Appearances[1].Role != "npc" {
		t.Fatalf("appearance order: %+v", report.Appearances)
	}

	report, err = List(st, "LMoP", "item", "cragmaw hideout", "cave mouth")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Chapters) != 0 || len(report.Locations) != 0 || len(report.Appearances) != 1 {
		t.Fatalf("item report: %+v", report)
	}
	if report.Appearances[0].Name != "Potion of Healing" {
		t.Fatalf("item appearance: %+v", report.Appearances[0])
	}

	if _, err := List(st, "LMoP", "monster", "", ""); err == nil {
		t.Fatal("expected unknown role")
	}
}

func testAdvStore(t *testing.T) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "index.sqlite")
	err := store.Create(path, store.Meta{SHA: "a", DataRoot: t.TempDir(), IngestedAt: store.Now()}, []parse.Entity{
		{Kind: "adventure", Name: "Lost Mine of Testing", Source: "LMoP", JSON: json.RawMessage(`{}`), Text: "phandelver"},
		{Kind: "monster", Name: "Goblin", Source: "MM", JSON: json.RawMessage(`{}`), Text: "a small humanoid"},
	}, []parse.Document{
		{Kind: "adventureSection", ParentID: "LMoP", Section: "Cragmaw Hideout", JSON: json.RawMessage(`{}`), Text: "hideout"},
		{Kind: "adventureLocation", ParentID: "LMoP", Section: "Cave Mouth", JSON: json.RawMessage(`{}`), Text: "mouth"},
	}, []parse.Appearance{
		{Adventure: "LMoP", Role: "npc", Kind: "monster", Name: "Goblin", Source: "MM", Chapter: "Cragmaw Hideout", Location: "Cave Mouth"},
		{Adventure: "LMoP", Role: "item", Kind: "item", Name: "Potion of Healing", Source: "DMG", Chapter: "Cragmaw Hideout", Location: "Cave Mouth"},
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
