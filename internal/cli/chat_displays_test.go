package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hbaldwin98/5e-cli/internal/ask"
	"github.com/hbaldwin98/5e-cli/internal/dice"
	"github.com/hbaldwin98/5e-cli/internal/encounter"
	"github.com/hbaldwin98/5e-cli/internal/search"
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

// TestChatModel_rollDisplayIsRenderedNotLiteralMarkdown guards against a
// roll (or encounter, or dice) tool result reaching the workspace
// transcript as raw, unrendered Markdown. finishTurn used to build these
// with writeTurnDisplays writing into a bytes.Buffer, but writeRandomTable
// (and encounterResultsMarkdown's caller) decide whether to render via
// isTTY(w) — false for a buffer — so the "| Roll |" table syntax showed up
// literally instead of as a rendered table.
func TestChatModel_rollDisplayIsRenderedNotLiteralMarkdown(t *testing.T) {
	m, _ := newTestChatModel(t)
	report := randomtable.Report{
		Kind: "table", Name: "Weather", Source: "PHB",
		Headers: []string{"Result"},
		Rolls:   []randomtable.Roll{{Roll: 1, Values: []string{"Sunny"}}},
	}
	m.drainShown = func() turnDisplays {
		return turnDisplays{Rolls: []randomtable.Report{report}}
	}
	m.streaming = true
	m.finishTurn(turnDoneMsg{res: ask.Result{Answer: "Rolled the weather table."}})

	transcript := m.transcriptText()
	if strings.Contains(transcript, "| Roll |") || strings.Contains(transcript, "| ---: |") {
		t.Fatalf("roll table reached the transcript as literal Markdown:\n%s", transcript)
	}
	if !strings.Contains(transcript, "Weather") || !strings.Contains(transcript, "Sunny") {
		t.Fatalf("roll table content missing from the transcript:\n%s", transcript)
	}
}

// TestChatModel_encounterDisplayIsRenderedNotLiteralMarkdown is the same
// regression guard as the roll test above, for an encounter (monster
// search) tool result's Markdown table.
func TestChatModel_encounterDisplayIsRenderedNotLiteralMarkdown(t *testing.T) {
	m, _ := newTestChatModel(t)
	m.drainShown = func() turnDisplays {
		return turnDisplays{Encounters: [][]encounter.Hit{{{Kind: "monster", Name: "Goblin", Source: "MM", CR: "1/4", Type: "humanoid", Size: "Small", Score: 1}}}}
	}
	m.streaming = true
	m.finishTurn(turnDoneMsg{res: ask.Result{Answer: "Found a goblin."}})

	transcript := m.transcriptText()
	if strings.Contains(transcript, "| Name | CR |") || strings.Contains(transcript, "| --- | --- |") {
		t.Fatalf("encounter table reached the transcript as literal Markdown:\n%s", transcript)
	}
	if !strings.Contains(transcript, "Goblin") {
		t.Fatalf("encounter hit missing from the transcript:\n%s", transcript)
	}
}

// TestChatModel_diceDisplayReachesTheTranscript checks the dice tool's
// result (plain text, no Markdown involved) still shows up — the
// regression above was specific to Markdown-rendered displays, but this
// path changed too and deserves its own coverage.
func TestChatModel_diceDisplayReachesTheTranscript(t *testing.T) {
	m, _ := newTestChatModel(t)
	m.drainShown = func() turnDisplays {
		return turnDisplays{DiceRolls: []dice.Report{{Expression: "2d6+3", Total: 9}}}
	}
	m.streaming = true
	m.finishTurn(turnDoneMsg{res: ask.Result{Answer: "Rolled it."}})

	transcript := m.transcriptText()
	if !strings.Contains(transcript, "2d6+3 = 9") {
		t.Fatalf("dice result missing from the transcript:\n%s", transcript)
	}
}

func TestWrapToolExecutorWithDisplays_spellListCardVsPlainNameList(t *testing.T) {
	base := func(_ context.Context, call ask.ToolCall) (string, error) {
		if call.Arguments != nil && strings.Contains(string(call.Arguments), "class") {
			return `{"kind":"spell","class":"Wizard","total":2,"spells":[{"level":0,"name":"Fire Bolt","source":"PHB"},{"level":3,"name":"Fireball","source":"PHB"}]}`, nil
		}
		return `{"kind":"spell","total":1,"names":[{"name":"Fireball","source":"PHB"}]}`, nil
	}
	wrapped, drain := wrapToolExecutorWithDisplays(base)

	if _, err := wrapped(context.Background(), ask.ToolCall{Name: "list", Arguments: json.RawMessage(`{"kind":"spell","class":"Wizard"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := wrapped(context.Background(), ask.ToolCall{Name: "list", Arguments: json.RawMessage(`{"kind":"spell"}`)}); err != nil {
		t.Fatal(err)
	}

	d := drain()
	// Only the class list is card-worthy; a plain name dump stays prose.
	if len(d.SpellLists) != 1 || d.SpellLists[0].Class != "Wizard" || len(d.SpellLists[0].Spells) != 2 {
		t.Fatalf("spell lists: %+v", d.SpellLists)
	}
}

func TestRenderSpellListCard_isALipglossTableNotMarkdown(t *testing.T) {
	got := renderSpellListCard(shownSpellList{
		Class: "Wizard",
		Total: 2,
		Spells: []search.SpellRow{
			{Level: 0, Name: "Fire Bolt", Source: "PHB"},
			{Level: 3, Name: "Fireball", Source: "PHB"},
		},
	}, 60)

	if strings.Contains(got, "| --- |") {
		t.Fatalf("expected a lipgloss table, got Markdown pipes:\n%s", got)
	}
	for _, want := range []string{"Wizard Spells", "Cantrip", "Fire Bolt", "Fireball", "Level"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if !strings.Contains(got, "╭") {
		t.Fatalf("expected a bordered card:\n%s", got)
	}
}
