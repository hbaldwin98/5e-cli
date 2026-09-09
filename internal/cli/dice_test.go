package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDiceJSON_isSeededAndReturnsTotal(t *testing.T) {
	out, err := runCLI("--json", "dice", "2d6+3", "--seed", "1")
	if err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if report["expression"] != "2d6+3" {
		t.Fatalf("expression: %s", out)
	}
	if _, ok := report["total"].(float64); !ok {
		t.Fatalf("total: %s", out)
	}
}

func TestDiceHumanOutput(t *testing.T) {
	out, err := runCLI("dice", "2d6+3", "--seed", "1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "2d6+3 = ") {
		t.Fatalf("output: %s", out)
	}
}

func TestDiceCount(t *testing.T) {
	out, err := runCLI("--json", "dice", "1d20", "--count", "3", "--seed", "1")
	if err != nil {
		t.Fatal(err)
	}
	var reports []map[string]any
	if err := json.Unmarshal([]byte(out), &reports); err != nil {
		t.Fatal(err)
	}
	if len(reports) != 3 {
		t.Fatalf("expected 3 rolls: %s", out)
	}
}

func TestDiceInvalidExpression(t *testing.T) {
	if _, err := runCLI("dice", "notdice"); err == nil {
		t.Fatal("expected error")
	}
}
