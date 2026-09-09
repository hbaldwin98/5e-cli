package cli

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hbaldwin98/5e-cli/internal/ask"
	randomtable "github.com/hbaldwin98/5e-cli/internal/table"
)

func TestWrapToolExecutorWithDisplays_capturesEachToolKind(t *testing.T) {
	base := func(_ context.Context, call ask.ToolCall) (string, error) {
		switch call.Name {
		case "get":
			return `{"kind":"monster","name":"Goblin","source":"MM","json":{"name":"Goblin","hp":{"average":7}}}`, nil
		case "roll":
			report := randomtable.Report{Kind: "table", Name: "Weather", Source: "PHB", Rolls: []randomtable.Roll{{Roll: 1, Values: []string{"Sunny"}}}}
			raw, _ := json.Marshal(report)
			return string(raw), nil
		case "dice":
			return `{"rolls":[{"expression":"2d6+3","total":9,"terms":[]}]}`, nil
		case "encounter":
			return `{"hits":[{"kind":"monster","name":"Goblin","source":"MM","cr":"1/4"}]}`, nil
		}
		return "", nil
	}
	wrapped, drain := wrapToolExecutorWithDisplays(base)

	for _, name := range []string{"get", "roll", "dice", "encounter"} {
		if _, err := wrapped(context.Background(), ask.ToolCall{Name: name}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}

	d := drain()
	if len(d.Entities) != 1 || d.Entities[0].Name != "Goblin" {
		t.Fatalf("entities: %+v", d.Entities)
	}
	if len(d.Rolls) != 1 || d.Rolls[0].Name != "Weather" {
		t.Fatalf("rolls: %+v", d.Rolls)
	}
	if len(d.DiceRolls) != 1 || d.DiceRolls[0].Total != 9 {
		t.Fatalf("dice rolls: %+v", d.DiceRolls)
	}
	if len(d.Encounters) != 1 || len(d.Encounters[0]) != 1 || d.Encounters[0][0].Name != "Goblin" {
		t.Fatalf("encounters: %+v", d.Encounters)
	}

	// drain clears state for the next turn.
	empty := drain()
	if !empty.empty() {
		t.Fatalf("expected drain to clear state, got %+v", empty)
	}
}

func TestWrapToolExecutorWithDisplays_ignoresErroredCalls(t *testing.T) {
	base := func(_ context.Context, call ask.ToolCall) (string, error) {
		return "", errNotFound
	}
	wrapped, drain := wrapToolExecutorWithDisplays(base)
	if _, err := wrapped(context.Background(), ask.ToolCall{Name: "get"}); err == nil {
		t.Fatal("expected the base executor's error to pass through")
	}
	if d := drain(); !d.empty() {
		t.Fatalf("an errored call should record nothing, got %+v", d)
	}
}

func TestDedupShown_keepsFirstOccurrence(t *testing.T) {
	in := []shownEntity{
		{Kind: "monster", Name: "Goblin", Source: "MM", Obj: map[string]any{"n": 1}},
		{Kind: "monster", Name: "Goblin", Source: "MM", Obj: map[string]any{"n": 2}},
		{Kind: "monster", Name: "Hobgoblin", Source: "MM"},
	}
	out := dedupShown(in)
	if len(out) != 2 || out[0].Obj["n"] != 1 {
		t.Fatalf("dedup: %+v", out)
	}
}

func TestMergeCitations_addsShownEntitiesNotAlreadyCited(t *testing.T) {
	citations := []ask.Hit{{Kind: "spell", Name: "Fireball", Source: "PHB"}}
	shown := []shownEntity{
		{Kind: "spell", Name: "Fireball", Source: "PHB"}, // already cited
		{Kind: "monster", Name: "Goblin", Source: "MM"},
	}
	out := mergeCitations(citations, shown)
	if len(out) != 2 {
		t.Fatalf("merged citations: %+v", out)
	}
	if out[1].Kind != "monster" || out[1].Score != 1 {
		t.Fatalf("appended citation: %+v", out[1])
	}
}

// errNotFound is a stand-in error for a failed tool call.
type notFoundErr struct{}

func (notFoundErr) Error() string { return "not found" }

var errNotFound = notFoundErr{}
