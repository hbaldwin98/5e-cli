package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hbaldwin98/5e-cli/internal/compare"
	"github.com/hbaldwin98/5e-cli/internal/parse"
	"github.com/hbaldwin98/5e-cli/internal/store"
)

func TestCompareJSON_allSourcesAndDifferences(t *testing.T) {
	index := comparisonIndex(t)
	data := filepath.Join(t.TempDir(), "missing-data")

	out, err := runCLI("--index", index, "--data", data, "--json", "compare", "spell", "Fireball")
	if err != nil {
		t.Fatal(err)
	}
	var result compare.Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("invalid comparison JSON: %v\n%s", err, out)
	}
	if result.Kind != "spell" || result.Name != "Fireball" {
		t.Fatalf("identity: %#v", result)
	}
	if len(result.Records) != 3 || result.Records[0].Source != "DMG" || result.Records[1].Source != "PHB" || result.Records[2].Source != "XPHB" {
		t.Fatalf("records: %#v", result.Records)
	}
	if len(result.Differences) == 0 {
		t.Fatalf("expected differences: %#v", result)
	}
	if result.Records[1].Text != "a bright explosion" || !result.Records[1].SRD {
		t.Fatalf("record metadata: %#v", result.Records[1])
	}
}

func TestCompare_filtersSourcesCaseInsensitively(t *testing.T) {
	index := comparisonIndex(t)
	data := filepath.Join(t.TempDir(), "missing-data")

	out, err := runCLI("--index", index, "--data", data, "--json", "compare", "spell", "Fireball", "--source", "xphb,phb")
	if err != nil {
		t.Fatal(err)
	}
	var result compare.Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Records) != 2 || result.Records[0].Source != "PHB" || result.Records[1].Source != "XPHB" {
		t.Fatalf("filtered records: %#v", result.Records)
	}
}

func TestCompare_editionAndSRDFilters(t *testing.T) {
	index := comparisonIndex(t)
	data := filepath.Join(t.TempDir(), "missing-data")

	out, err := runCLI("--edition", "2014", "--index", index, "--data", data, "--json", "compare", "spell", "Fireball")
	if err != nil {
		t.Fatal(err)
	}
	var result compare.Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Records) != 2 || result.Records[0].Source != "DMG" || result.Records[1].Source != "PHB" {
		t.Fatalf("classic records: %#v", result.Records)
	}

	_, err = runCLI("--edition", "2024", "--index", index, "--data", data, "compare", "spell", "Fireball")
	if err == nil || !strings.Contains(err.Error(), "at least two") {
		t.Fatalf("modern comparison error: %v", err)
	}

	out, err = runCLI("--srd", "--index", index, "--data", data, "--json", "compare", "spell", "Fireball")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Records) != 2 || result.Records[0].Source != "PHB" || result.Records[1].Source != "XPHB" {
		t.Fatalf("srd records: %#v", result.Records)
	}
}

func TestCompare_humanOutputAndNoDifferences(t *testing.T) {
	index := comparisonIndex(t)
	data := filepath.Join(t.TempDir(), "missing-data")

	out, err := runCLI("--index", index, "--data", data, "compare", "spell", "Fireball")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Compare spell Fireball", "level", "DMG", "PHB", "XPHB"} {
		if !strings.Contains(out, want) {
			t.Fatalf("human output missing %q:\n%s", want, out)
		}
	}

	out, err = runCLI("--index", index, "--data", data, "compare", "spell", "Light")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(out), "no top-level differences.") {
		t.Fatalf("no-difference output: %s", out)
	}
}

func TestCompare_requiresTwoSources(t *testing.T) {
	index := comparisonIndex(t)
	data := filepath.Join(t.TempDir(), "missing-data")

	_, err := runCLI("--index", index, "--data", data, "compare", "spell", "Fireball", "--source", "phb")
	if err == nil || !strings.Contains(err.Error(), "at least two") {
		t.Fatalf("expected minimum-source error, got %v", err)
	}
}

func comparisonIndex(t *testing.T) string {
	t.Helper()
	index := filepath.Join(t.TempDir(), "index.sqlite")
	entities := []parse.Entity{
		{Kind: "spell", Name: "Fireball", Source: "PHB", Page: 241, SRD: true, Text: "a bright explosion", JSON: json.RawMessage(`{"name":"Fireball","source":"PHB","level":3,"entries":["A bright streak explodes."]}`), Edges: []parse.Edge{{Tag: "condition", ToKind: "condition", ToName: "Blinded"}}},
		{Kind: "spell", Name: "Fireball", Source: "XPHB", Page: 209, SRD: true, Text: "a brighter explosion", JSON: json.RawMessage(`{"name":"Fireball","source":"XPHB","level":4,"entries":["A brighter streak explodes."]}`)},
		{Kind: "spell", Name: "Fireball", Source: "DMG", Page: 12, Text: "an explosion", JSON: json.RawMessage(`{"name":"Fireball","source":"DMG","level":3,"entries":["An explosion."]}`)},
		{Kind: "spell", Name: "Light", Source: "PHB", JSON: json.RawMessage(`{"name":"Light","source":"PHB","level":0,"entries":["A light."]}`)},
		{Kind: "spell", Name: "Light", Source: "XPHB", JSON: json.RawMessage(`{"name":"Light","source":"XPHB","level":0,"entries":["A light."]}`)},
	}
	if err := store.Create(index, store.Meta{SHA: "comparison", DataRoot: t.TempDir(), IngestedAt: store.Now()}, entities, nil, nil); err != nil {
		t.Fatal(err)
	}
	return index
}
