package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hbaldwin98/5e-cli/internal/parse"
	"github.com/hbaldwin98/5e-cli/internal/store"
)

func TestEncounterJSON_filtersAndReturnsStructuredHits(t *testing.T) {
	index := encounterCLIIndex(t)
	data := filepath.Join(t.TempDir(), "missing-data")

	out, err := runCLI("--index", index, "--data", data, "--edition", "all", "--json", "encounter", "goblin", "--type", "humanoid", "--size", "small", "--cr", "1/4", "--limit", "1")
	if err != nil {
		t.Fatal(err)
	}
	var hits []map[string]any
	if err := json.Unmarshal([]byte(out), &hits); err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0]["source"] != "PHB" {
		t.Fatalf("filtered encounter hits: %s", out)
	}
	if hits[0]["cr"] != "1/4" || hits[0]["type"] != "humanoid" || hits[0]["size"] != "Small" {
		t.Fatalf("structured metadata: %s", out)
	}
}

func TestEncounter_editionAndSRDFilters(t *testing.T) {
	index := encounterCLIIndex(t)
	data := filepath.Join(t.TempDir(), "missing-data")

	out, err := runCLI("--index", index, "--data", data, "--edition", "2014", "--json", "encounter", "goblin")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"source":"PHB"`) || strings.Contains(out, `"source":"XPHB"`) {
		t.Fatalf("classic edition: %s", out)
	}

	out, err = runCLI("--index", index, "--data", data, "--edition", "all", "--srd", "--json", "encounter", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Ogre") {
		t.Fatalf("non-SRD monster leaked: %s", out)
	}
}

func TestEncounter_humanOutput(t *testing.T) {
	index := encounterCLIIndex(t)
	data := filepath.Join(t.TempDir(), "missing-data")

	out, err := runCLI("--index", index, "--data", data, "--edition", "all", "encounter", "goblin")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"| Name | CR | Type | Size | Source | Match |", "Goblin", "Small", "PHB"} {
		if !strings.Contains(out, want) {
			t.Fatalf("human output missing %q:\n%s", want, out)
		}
	}
}

func encounterCLIIndex(t *testing.T) string {
	t.Helper()
	index := filepath.Join(t.TempDir(), "index.sqlite")
	err := store.Create(index, store.Meta{SHA: "encounter-cli", DataRoot: t.TempDir(), IngestedAt: store.Now()}, []parse.Entity{
		{Kind: "monster", Name: "Goblin", Source: "PHB", Page: 166, SRD: true, JSON: json.RawMessage(`{"name":"Goblin","source":"PHB","cr":"1/4","type":"humanoid","size":["S"]}`), Text: "A small humanoid that lives in caves."},
		{Kind: "monster", Name: "Goblin", Source: "XPHB", Page: 138, SRD: true, JSON: json.RawMessage(`{"name":"Goblin","source":"XPHB","cr":"1/4","type":"humanoid","size":["S"]}`), Text: "A small humanoid that lives in caves."},
		{Kind: "monster", Name: "Ogre", Source: "MM", Page: 237, JSON: json.RawMessage(`{"name":"Ogre","source":"MM","cr":2,"type":{"type":"giant"},"size":"L"}`), Text: "A large giant that smashes through defenses."},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return index
}
