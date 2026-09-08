package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hbaldwin98/5e-cli/internal/parse"
	"github.com/hbaldwin98/5e-cli/internal/store"
)

func TestRollJSON_isSeededAndReturnsRows(t *testing.T) {
	index := rollIndex(t)
	data := filepath.Join(t.TempDir(), "missing-data")
	out, err := runCLI("--index", index, "--data", data, "--json", "roll", "Weather", "--count", "3", "--seed", "11")
	if err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if report["name"] != "Weather" || report["source"] != "PHB" {
		t.Fatalf("metadata: %s", out)
	}
	rolls, ok := report["rolls"].([]any)
	if !ok || len(rolls) != 3 {
		t.Fatalf("rolls: %s", out)
	}
}

func TestRollHumanOutput(t *testing.T) {
	index := rollIndex(t)
	data := filepath.Join(t.TempDir(), "missing-data")
	out, err := runCLI("--index", index, "--data", data, "roll", "Weather", "--seed", "2")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# Weather", "*table | PHB*", "| d6 | Result |", "| 5 | 4-6 | Rain |"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func rollIndex(t *testing.T) string {
	t.Helper()
	index := filepath.Join(t.TempDir(), "index.sqlite")
	err := store.Create(index, store.Meta{SHA: "roll", DataRoot: t.TempDir(), IngestedAt: store.Now()}, []parse.Entity{
		{Kind: "table", Name: "Weather", Source: "PHB", JSON: json.RawMessage(`{"name":"Weather","source":"PHB","colLabels":["d6","Result"],"rows":[["1-3","Sunny"],["4-6","Rain"]]}`), Text: "Weather Sunny Rain"},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return index
}
