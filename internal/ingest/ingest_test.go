package ingest

import (
	"path/filepath"
	"strings"
	"testing"

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

	res2, err := Run(Options{DataDir: dir, Index: index, Force: false})
	if err != nil {
		t.Fatal(err)
	}
	if !res2.Skipped {
		t.Fatal("expected idempotent skip")
	}
}
