package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctor_JSONReportsMissingSetup(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	index := filepath.Join(t.TempDir(), "index.sqlite")
	out, err := runCLI("--data", data, "--index", index, "--json", "doctor")
	if err == nil {
		t.Fatal("expected doctor to fail for missing setup")
	}
	var report map[string]any
	if jsonErr := json.Unmarshal([]byte(out), &report); jsonErr != nil {
		t.Fatalf("invalid doctor JSON: %v\n%s", jsonErr, out)
	}
	if report["ready"] != false || report["issue"] == nil {
		t.Fatalf("unexpected doctor report: %s", out)
	}
}

func TestDoctor_humanReportsReadySetup(t *testing.T) {
	data := t.TempDir()
	for _, dir := range []string{"spells", "bestiary", "class"} {
		if err := os.Mkdir(filepath.Join(data, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(data, "content.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(t.TempDir(), "index.sqlite")

	_, err := runCLI("--data", data, "--index", index, "ingest")
	if err != nil {
		t.Fatal(err)
	}
	out, err := runCLI("--data", data, "--index", index, "doctor")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Status") || !strings.Contains(out, "ready") || !strings.Contains(out, "Data") || !strings.Contains(out, "Index") {
		t.Fatalf("unexpected doctor output: %s", out)
	}
}
