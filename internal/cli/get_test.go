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
	})
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
	if err == nil {
		t.Fatal("expected ambiguous")
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
