package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/hbaldwin98/5e-cli/internal/search"
	"github.com/hbaldwin98/5e-cli/internal/store"
)

func TestWriteHumanEntity_spell(t *testing.T) {
	e := entity("spell", "Fireball", "PHB", `{
		"name":"Fireball","source":"PHB","level":3,"school":"V",
		"time":[{"number":1,"unit":"action"}],
		"range":{"type":"point","distance":{"type":"feet","amount":150}},
		"components":{"v":true,"s":true,"m":"a tiny ball of bat guano and sulfur"},
		"duration":[{"type":"instant"}],
		"entries":["Each creature makes a {@dc 15} save and takes {@damage 8d6} fire damage.",{"type":"list","items":["The fire spreads.",{"type":"item","name":"Cover","entry":"Objects behind cover are safe."}]}],
		"entriesHigherLevel":[{"type":"entries","name":"Using a Higher-Level Slot","entries":["Damage increases by {@damage 1d6}."]}]
	}`)

	var out bytes.Buffer
	if err := writeHumanEntity(&out, nil, e, false); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"# Fireball",
		"*spell | PHB*",
		"3rd-level evocation",
		"**Casting Time:** 1 action",
		"**Range:** 150 feet",
		"**Components:** V, S, M (a tiny ball of bat guano and sulfur)",
		"**Duration:** Instant",
		"Each creature makes a DC 15 save and takes 8d6 fire damage.",
		"- The fire spreads.",
		"- Cover. Objects behind cover are safe.",
		"## At Higher Levels",
		"Using a Higher-Level Slot. Damage increases by 1d6.",
	}
	for _, text := range want {
		if !strings.Contains(out.String(), text) {
			t.Errorf("output missing %q:\n%s", text, out.String())
		}
	}
	if strings.Contains(out.String(), "{@") {
		t.Fatalf("unexpanded tag:\n%s", out.String())
	}
}

func TestWriteHumanEntity_monster(t *testing.T) {
	e := entity("monster", "Goblin", "MM", `{
		"name":"Goblin","source":"MM","size":["S"],"type":"humanoid","alignment":["N","E"],
		"ac":[{"ac":15,"from":["leather armor","shield"]}],
		"hp":{"average":7,"formula":"2d6"},"speed":{"walk":30},
		"str":8,"dex":15,"con":10,"int":10,"wis":8,"cha":8,
		"trait":[{"name":"Nimble Escape","entries":["The goblin can take the Disengage action."]}],
		"action":[{"name":"Scimitar","entries":["{@atk mw} {@hit 4} to hit. {@h} 5 slashing damage."]}]
	}`)

	var out bytes.Buffer
	if err := writeHumanEntity(&out, nil, e, false); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"Small, humanoid, neutral evil",
		"**Armor Class:** 15 (leather armor; shield)",
		"**Hit Points:** 7 (2d6)",
		"**Speed:** 30 ft.",
		"| 8 (-1) | 15 (+2)",
		"## Traits",
		"Nimble Escape. The goblin can take the Disengage action.",
		"## Actions",
		"Scimitar. Melee Weapon Attack: +4 to hit. Hit: 5 slashing damage.",
	}
	for _, text := range want {
		if !strings.Contains(out.String(), text) {
			t.Errorf("output missing %q:\n%s", text, out.String())
		}
	}
}

func TestWriteHumanEntity_itemAndTable(t *testing.T) {
	e := entity("item", "Test Wand", "DMG", `{
		"name":"Test Wand","source":"DMG","type":"WD","rarity":"rare","reqAttune":true,
		"entries":["This wand casts {@spell fireball}.",{"type":"table","caption":"Charges","colLabels":["Roll","Effect"],"rows":[["1", "One charge"],["2", "Two charges"]]}]
	}`)

	var out bytes.Buffer
	if err := writeHumanEntity(&out, nil, e, false); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"wand, rare (requires attunement)",
		"This wand casts fireball.",
		"Charges",
		"| Roll | Effect |",
		"| 1 | One charge |",
	}
	for _, text := range want {
		if !strings.Contains(out.String(), text) {
			t.Errorf("output missing %q:\n%s", text, out.String())
		}
	}
}

func TestWriteHumanEntity_raceMechanics(t *testing.T) {
	e := entity("race", "Human", "PHB", `{
		"name":"Human","source":"PHB","size":["S","M"],"speed":30,
		"ability":[{"str":1,"dex":1,"con":1,"int":1,"wis":1,"cha":1}],
		"entries":[{"name":"Languages","entries":["You can speak Common."]}]
	}`)

	var out bytes.Buffer
	if err := writeHumanEntity(&out, nil, e, false); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{
		"**Size:** Small or Medium",
		"**Speed:** 30 ft.",
		"**Ability Scores:** STR +1, DEX +1, CON +1, INT +1, WIS +1, CHA +1",
		"Languages. You can speak Common.",
	} {
		if !strings.Contains(out.String(), text) {
			t.Errorf("output missing %q:\n%s", text, out.String())
		}
	}
}

func TestWriteSearchResults_singleLineAligned(t *testing.T) {
	hits := []search.Hit{
		{Kind: "spell\nritual", Name: "Fire|ball", Source: "PHB\n2024", Snippet: "A bright\nflash | of light"},
		{Kind: "item", Name: "Fire", Source: "DMG", Snippet: "A flame"},
	}
	var out bytes.Buffer
	if err := writeSearchResults(&out, hits); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("want a header and two rows, got:\n%s", out.String())
	}
	if !strings.Contains(lines[0], "Kind") || !strings.Contains(lines[0], "Match") {
		t.Fatalf("missing columns: %q", lines[0])
	}
	if !strings.Contains(lines[1], "A bright flash | of light") {
		t.Fatalf("snippet was not collapsed: %q", lines[1])
	}
	if !strings.Contains(lines[1], "spell ritual") || !strings.Contains(lines[1], "Fire|ball") {
		t.Fatalf("table values were not rendered: %q", lines[1])
	}
}

func TestWriteHumanEntity_rawMarkdownForNonTTY(t *testing.T) {
	e := entity("spell", "Light", "PHB", `{"level":0,"school":"V","entries":["Bright light."]}`)
	var out bytes.Buffer
	if err := writeHumanEntity(&out, nil, e, false); err != nil {
		t.Fatal(err)
	}
	want := "# Light\n\n*spell | PHB*\n\nEvocation cantrip\nBright light.\n"
	if out.String() != want {
		t.Fatalf("raw Markdown changed:\nwant %q\n got %q", want, out.String())
	}
}

func TestRenderMarkdown_regularFileRemainsRaw(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	file, err := os.CreateTemp(t.TempDir(), "render-*.md")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	const markdown = "# Heading\n\n**value**\n"
	if err := renderMarkdown(file, markdown); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != markdown {
		t.Fatalf("regular file should receive raw Markdown: %q", got)
	}
}

func TestTTYAnswerRenderer_passesThroughForANonTTYWriter(t *testing.T) {
	var out bytes.Buffer
	render := ttyAnswerRenderer(&out)
	const markdown = "**bold**"
	if got := render(markdown); got != markdown {
		t.Fatalf("want a non-TTY writer to leave the text unrendered, got %q", got)
	}
}

func TestTTYAnswerRenderer_rendersForARealTerminal(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	file, err := os.CreateTemp(t.TempDir(), "render-*.md")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	// A regular file isn't itself a TTY (isTTY checks os.ModeCharDevice),
	// so exercise the renderer directly against renderMarkdownToString's
	// unconditional path the same way ttyAnswerRenderer's TTY branch does,
	// rather than trying to fake a character device in a test.
	rendered, err := renderMarkdownToString("**bold**", 80)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rendered, "**") {
		t.Fatalf("want markdown syntax rendered away, got %q", rendered)
	}
}

func TestWriteEntity_JSONRemainsMachineReadable(t *testing.T) {
	e := entity("spell", "Light", "PHB", `{"level":0,"school":"V"}`)
	var out bytes.Buffer
	cmd := rootCmd()
	cmd.SetOut(&out)
	if err := writeEntity(cmd, nil, true, e, false); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, out.String())
	}
	if got["name"] != "Light" || got["kind"] != "spell" {
		t.Fatalf("unexpected envelope: %#v", got)
	}
	obj, ok := got["json"].(map[string]any)
	if !ok || obj["school"] != "V" {
		t.Fatalf("embedded entity JSON changed: %#v", got["json"])
	}
	if strings.Contains(out.String(), "# Light") {
		t.Fatalf("JSON output contains Markdown: %s", out.String())
	}
}

func entity(kind, name, source, raw string) store.Entity {
	return store.Entity{Kind: kind, Name: name, Source: source, JSON: json.RawMessage(raw)}
}
