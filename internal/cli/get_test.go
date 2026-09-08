package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hbaldwin98/5e-cli/internal/parse"
	"github.com/hbaldwin98/5e-cli/internal/store"
)

func TestGetJSON_entityAndSection(t *testing.T) {
	index := filepath.Join(t.TempDir(), "index.sqlite")
	err := store.Create(index, store.Meta{SHA: "cli", DataRoot: t.TempDir(), IngestedAt: store.Now()}, []parse.Entity{
		{Kind: "spell", Name: "Testbolt", Source: "PHB", JSON: json.RawMessage(`{"name":"Testbolt"}`), Text: "a bolt"},
		{Kind: "skill", Name: "Testing", Source: "PHB", JSON: json.RawMessage(`{}`), Text: "phb skill"},
		{Kind: "skill", Name: "Testing", Source: "XPHB", JSON: json.RawMessage(`{}`), Text: "xphb skill"},
	}, []parse.Document{
		{Kind: "bookSection", ParentID: "PHB", Section: "Holding Breath", JSON: json.RawMessage(`{"name":"Holding Breath"}`), Text: "hold breath"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(t.TempDir(), "missing-data")

	out, err := runCLI("--index", index, "--data", data, "--json", "get", "spell", "Testbolt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"name":"Testbolt"`) {
		t.Fatalf("entity get: %s", out)
	}

	out, err = runCLI("--index", index, "--data", data, "--json", "get", "bookSection", "Holding Breath", "--source", "PHB")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"name":"Holding Breath"`) {
		t.Fatalf("section get: %s", out)
	}

	_, err = runCLI("--index", index, "--data", data, "get", "spell", "Missing")
	if err == nil || !strings.Contains(err.Error(), "no spell") {
		t.Fatalf("missing: %v", err)
	}

	_, err = runCLI("--index", index, "--data", data, "--json", "get", "skill", "Testing")
	if err != nil {
		t.Fatal(err)
	}
}

func TestGet_editionDisambiguates(t *testing.T) {
	index := filepath.Join(t.TempDir(), "index.sqlite")
	err := store.Create(index, store.Meta{SHA: "cli", DataRoot: t.TempDir(), IngestedAt: store.Now()}, []parse.Entity{
		{Kind: "skill", Name: "Testing", Source: "PHB", JSON: json.RawMessage(`{}`), Text: "phb skill"},
		{Kind: "skill", Name: "Testing", Source: "XPHB", JSON: json.RawMessage(`{}`), Text: "xphb skill"},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(t.TempDir(), "missing-data")

	out, err := runCLI("--index", index, "--data", data, "--json", "get", "skill", "Testing")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"source":"XPHB"`) {
		t.Fatalf("default 2024: %s", out)
	}

	out, err = runCLI("--edition", "2014", "--index", index, "--data", data, "--json", "get", "skill", "Testing")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"source":"PHB"`) {
		t.Fatalf("2014: %s", out)
	}

	_, err = runCLI("--edition", "all", "--index", index, "--data", data, "--json", "get", "skill", "Testing")
	if err == nil {
		t.Fatal("expected ambiguous")
	}

	out, err = runCLI("--index", index, "--data", data, "--json", "search", "Testing")
	if err != nil {
		t.Fatal(err)
	}
	var hits []map[string]any
	if err := json.Unmarshal([]byte(out), &hits); err != nil {
		t.Fatal(err)
	}
	if len(hits) < 2 || hits[0]["source"] != "XPHB" {
		t.Fatalf("search 2024 first: %s", out)
	}
}

func TestGet_srdFilters(t *testing.T) {
	index := filepath.Join(t.TempDir(), "index.sqlite")
	err := store.Create(index, store.Meta{SHA: "cli", DataRoot: t.TempDir(), IngestedAt: store.Now()}, []parse.Entity{
		{Kind: "spell", Name: "Testbolt", Source: "PHB", SRD: true, JSON: json.RawMessage(`{"name":"Testbolt"}`), Text: "a testing bolt"},
		{Kind: "item", Name: "Secret Blade", Source: "PHB", JSON: json.RawMessage(`{}`), Text: "a testing blade"},
	}, []parse.Document{
		{Kind: "bookSection", ParentID: "PHB", Section: "Holding Breath", JSON: json.RawMessage(`{}`), Text: "testing breath"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(t.TempDir(), "missing-data")

	out, err := runCLI("--srd", "--index", index, "--data", data, "--json", "get", "spell", "Testbolt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"name":"Testbolt"`) {
		t.Fatalf("srd get: %s", out)
	}

	_, err = runCLI("--srd", "--index", index, "--data", data, "get", "item", "Secret Blade")
	if err == nil || !strings.Contains(err.Error(), "no item") {
		t.Fatalf("non-srd get: %v", err)
	}

	_, err = runCLI("--srd", "--index", index, "--data", data, "get", "bookSection", "Holding Breath")
	if err == nil || !strings.Contains(err.Error(), "no bookSection") {
		t.Fatalf("section get: %v", err)
	}

	out, err = runCLI("--srd", "--index", index, "--data", data, "--json", "search", "testing")
	if err != nil {
		t.Fatal(err)
	}
	var hits []map[string]any
	if err := json.Unmarshal([]byte(out), &hits); err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0]["name"] != "Testbolt" {
		t.Fatalf("srd search: %s", out)
	}
}

func TestGet_adventureByNameOrID(t *testing.T) {
	index := filepath.Join(t.TempDir(), "index.sqlite")
	err := store.Create(index, store.Meta{SHA: "cli", DataRoot: t.TempDir(), IngestedAt: store.Now()}, []parse.Entity{
		{Kind: "adventure", Name: "Lost Mine of Testing", Source: "LMoP", JSON: json.RawMessage(`{"name":"Lost Mine of Testing"}`), Text: "phandelver"},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(t.TempDir(), "missing-data")

	out, err := runCLI("--index", index, "--data", data, "--json", "get", "adventure", "Lost Mine of Testing")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"source":"LMoP"`) {
		t.Fatalf("by name: %s", out)
	}

	out, err = runCLI("--index", index, "--data", data, "--json", "get", "adventure", "LMoP")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"name":"Lost Mine of Testing"`) {
		t.Fatalf("by id: %s", out)
	}
}

func TestAdventure_searchAndGet(t *testing.T) {
	index := filepath.Join(t.TempDir(), "index.sqlite")
	err := store.Create(index, store.Meta{SHA: "cli", DataRoot: t.TempDir(), IngestedAt: store.Now()}, []parse.Entity{
		{Kind: "adventure", Name: "Lost Mine of Testing", Source: "LMoP", JSON: json.RawMessage(`{}`), Text: "phandelver"},
		{Kind: "monster", Name: "Ash Zombie", Source: "LMoP", JSON: json.RawMessage(`{"name":"Ash Zombie"}`), Text: "a burned undead"},
		{Kind: "monster", Name: "Goblin", Source: "MM", JSON: json.RawMessage(`{}`), Text: "a small humanoid"},
	}, []parse.Document{
		{Kind: "adventureSection", ParentID: "LMoP", Section: "Cragmaw Hideout", JSON: json.RawMessage(`{}`), Text: "goblins nest in the cragmaw hideout"},
		{Kind: "adventureLocation", ParentID: "LMoP", Section: "Cave Mouth", JSON: json.RawMessage(`{}`), Text: "a goblin watches the trail"},
		{Kind: "bookSection", ParentID: "PHB", Section: "Holding Breath", JSON: json.RawMessage(`{}`), Text: "goblins can hold breath too"},
	}, []parse.Appearance{
		{Adventure: "LMoP", Role: "npc", Kind: "monster", Name: "Goblin", Source: "MM", Location: "Cave Mouth"},
	})
	if err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(t.TempDir(), "missing-data")

	out, err := runCLI("--index", index, "--data", data, "--json", "search", "hideout")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Cragmaw") {
		t.Fatalf("default search mixed adventure: %s", out)
	}

	out, err = runCLI("--index", index, "--data", data, "--json", "adventure", "LMoP", "search", "hideout")
	if err != nil {
		t.Fatal(err)
	}
	var hits []map[string]any
	if err := json.Unmarshal([]byte(out), &hits); err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0]["name"] != "Cragmaw Hideout" {
		t.Fatalf("adventure search: %s", out)
	}

	out, err = runCLI("--index", index, "--data", data, "--json", "adventure", "LMoP", "search", "--kind", "npc", "zombie")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &hits); err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0]["name"] != "Ash Zombie" {
		t.Fatalf("npc search: %s", out)
	}

	out, err = runCLI("--index", index, "--data", data, "--json", "adventure", "LMoP", "get", "location", "Cragmaw Hideout")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"name":"Cragmaw Hideout"`) {
		t.Fatalf("get location: %s", out)
	}

	out, err = runCLI("--index", index, "--data", data, "--json", "adventure", "LMoP", "get", "npc", "Ash Zombie")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"name":"Ash Zombie"`) {
		t.Fatalf("get npc: %s", out)
	}

	out, err = runCLI("--index", index, "--data", data, "--json", "adventure", "LMoP", "search", "--kind", "npc", "goblin")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &hits); err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0]["name"] != "Goblin" || hits[0]["source"] != "MM" {
		t.Fatalf("appeared npc: %s", out)
	}

	out, err = runCLI("--index", index, "--data", data, "--json", "adventure", "LMoP", "get", "npc", "Goblin")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"source":"MM"`) {
		t.Fatalf("get appeared npc: %s", out)
	}

	out, err = runCLI("--index", index, "--data", data, "--json", "adventure", "LMoP", "get", "location", "Cave Mouth")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"name":"Cave Mouth"`) {
		t.Fatalf("get location: %s", out)
	}
}

func TestRefs_JSONSupportsBothDirections(t *testing.T) {
	index := filepath.Join(t.TempDir(), "index.sqlite")
	err := store.Create(index, store.Meta{SHA: "cli", DataRoot: t.TempDir(), IngestedAt: store.Now()}, []parse.Entity{
		{Kind: "item", Name: "Test Wand", Source: "DMG", JSON: json.RawMessage(`{}`), Text: "wand", Edges: []parse.Edge{{Tag: "spell", ToKind: "spell", ToName: "Testbolt", ToSource: "PHB"}}},
		{Kind: "spell", Name: "Testbolt", Source: "PHB", JSON: json.RawMessage(`{}`), Text: "bolt"},
		{Kind: "spell", Name: "Otherbolt", Source: "PHB", JSON: json.RawMessage(`{}`), Text: "bolt", Edges: []parse.Edge{{Tag: "spell", ToKind: "spell", ToName: "Testbolt", ToSource: "PHB"}}},
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(t.TempDir(), "missing-data")
	out, err := runCLI("--index", index, "--data", data, "--json", "refs", "spell", "Testbolt", "--source", "PHB")
	if err != nil {
		t.Fatal(err)
	}
	var refs []map[string]any
	if err := json.Unmarshal([]byte(out), &refs); err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 || refs[0]["direction"] != "incoming" {
		t.Fatalf("refs: %s", out)
	}

	out, err = runCLI("--index", index, "--data", data, "--json", "refs", "item", "Test Wand", "--direction", "outgoing")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"to":{"kind":"spell","name":"Testbolt","source":"PHB"}`) {
		t.Fatalf("outgoing ref: %s", out)
	}
}

func TestMCP_requiresIndex(t *testing.T) {
	_, err := runCLI("--index", filepath.Join(t.TempDir(), "missing.sqlite"), "--data", filepath.Join(t.TempDir(), "missing-data"), "mcp")
	if err == nil || !strings.Contains(err.Error(), "no index") {
		t.Fatalf("got %v", err)
	}
}

func runCLI(args ...string) (string, error) {
	cmd := rootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}
