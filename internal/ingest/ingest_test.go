package ingest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hbaldwin98/5e-cli/internal/parse"
	"github.com/hbaldwin98/5e-cli/internal/search"
	"github.com/hbaldwin98/5e-cli/internal/store"
)

func TestRun_syntheticCorpus(t *testing.T) {
	dir := filepath.Join("testdata", "data")
	index := filepath.Join(t.TempDir(), "index.sqlite")
	res, err := Run(Options{DataDir: dir, Index: index, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Entities < 4 || res.Documents < 1 {
		t.Fatalf("got %+v", res)
	}

	st, err := store.Open(index)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ents, err := st.Get("spell", "testbolt", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 {
		t.Fatalf("spell matches %d", len(ents))
	}
	e := ents[0]
	if !strings.Contains(e.Text, "Scholars debate") {
		t.Fatalf("fluff missing: %q", e.Text)
	}
	foundBeast := false
	for _, edge := range e.Edges {
		if edge.ToName == "Test Beast" && edge.ToKind == "monster" {
			foundBeast = true
		}
	}
	if !foundBeast {
		t.Fatalf("missing creature edge: %+v", e.Edges)
	}

	skills, err := st.Get("skill", "Testing", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 2 {
		t.Fatalf("want 2 skills, got %d", len(skills))
	}
	one, err := st.Get("skill", "Testing", "XPHB")
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 1 || one[0].Source != "XPHB" {
		t.Fatalf("source filter %+v", one)
	}

	hits, err := search.Search(st, search.Query{Text: "testbol", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || !strings.EqualFold(hits[0].Name, "Testbolt") {
		t.Fatalf("fuzzy hits %+v", hits)
	}

	ruleHits, err := search.Search(st, search.Query{Text: "hold breath", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	foundSection := false
	for _, h := range ruleHits {
		if h.Kind == "bookSection" && h.Name == "Holding Breath" && h.Source == "PHB" {
			foundSection = true
		}
	}
	if !foundSection {
		t.Fatalf("rule search %+v", ruleHits)
	}

	advs, err := st.Get("adventure", "Lost Mine of Testing", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(advs) != 1 || advs[0].Source != "LMoP" {
		t.Fatalf("catalog %+v", advs)
	}
	byID, err := st.Lookup("adventure", "LMoP", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(byID) != 1 || byID[0].Name != "Lost Mine of Testing" {
		t.Fatalf("lookup id %+v", byID)
	}

	hide, err := search.Search(st, search.Query{Text: "hideout", Limit: 5, Adventure: "LMoP"})
	if err != nil {
		t.Fatal(err)
	}
	foundHide := false
	for _, h := range hide {
		if h.Kind == "adventureSection" && h.Name == "Cragmaw Hideout" {
			foundHide = true
		}
	}
	if !foundHide {
		t.Fatalf("adventure section %+v", hide)
	}

	locs, err := search.Search(st, search.Query{Text: "cave mouth", Limit: 5, Adventure: "LMoP", Kind: "location"})
	if err != nil {
		t.Fatal(err)
	}
	foundLoc := false
	for _, h := range locs {
		if h.Name == "Cave Mouth" && h.Kind == "adventureLocation" {
			foundLoc = true
		}
	}
	if !foundLoc {
		t.Fatalf("location %+v", locs)
	}

	npcs, err := search.Search(st, search.Query{Text: "beast", Limit: 5, Adventure: "LMoP", Kind: "monster"})
	if err != nil {
		t.Fatal(err)
	}
	foundNPC := false
	for _, h := range npcs {
		if h.Name == "Test Beast" && h.Source == "MM" {
			foundNPC = true
		}
	}
	if !foundNPC {
		t.Fatalf("appeared npc %+v", npcs)
	}

	res2, err := Run(Options{DataDir: dir, Index: index, Force: false})
	if err != nil {
		t.Fatal(err)
	}
	if !res2.Skipped {
		t.Fatal("expected idempotent skip")
	}
}

func TestIngest_discoversIdentityArraysAndSkipsSupportFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name, contents string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("encounters.json", `{"encounter":[{"name":"Forest Ambush","source":"DMG","entries":["A patrol appears."]}]}`)
	write("future.json", `{"futureRecord":[{"name":"Future Record","source":"UA","entries":["Official content."]}],"futureRecordFluff":[{"name":"Future Record","source":"UA","entries":["Lore."]}]}`)
	write("foundry-actions.json", `{"action":[{"name":"Foundry Action","source":"PHB"}]}`)
	write("makebrew-custom.json", `{"custom":[{"name":"Builder Action","source":"PHB"}]}`)
	write("loot.json", `{"individual":[{"name":"Loot Entry","source":"DMG"}]}`)

	entities := map[string]parse.Entity{}
	fluff := map[string]map[string]any{}
	if err := ingestDirJSON(dir, entities, fluff); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"encounter\x00forest ambush\x00dmg",
		"futureRecord\x00future record\x00ua",
	} {
		if _, ok := entities[key]; !ok {
			t.Fatalf("missing discovered entity %q: %v", key, entities)
		}
	}
	if _, ok := fluff["futureRecord\x00future record\x00ua"]; !ok {
		t.Fatalf("missing generic fluff: %v", fluff)
	}
	for _, key := range []string{
		"action\x00foundry action\x00phb",
		"custom\x00builder action\x00phb",
		"individual\x00loot entry\x00dmg",
	} {
		if _, ok := entities[key]; ok {
			t.Fatalf("support file leaked entity %q", key)
		}
	}
}
