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
		{Adventure: "LMoP", Role: "npc", Kind: "monster", Name: "Goblin", Source: "MM", Location: "Cave Mouth"},
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
