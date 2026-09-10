package statblock

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderItem_attunementWithoutMetadata(t *testing.T) {
	var out strings.Builder
	renderItem(&out, map[string]any{"reqAttune": true})
	if got, want := out.String(), "requires attunement\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestSpeed_conditionalMovement(t *testing.T) {
	got := speed(map[string]any{
		"walk": json.Number("30"),
		"fly":  map[string]any{"number": json.Number("60"), "condition": " ({@condition prone})"},
	})
	if want := "30 ft., fly 60 ft. (prone)"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRenderMonster_fullStatBlockFields(t *testing.T) {
	obj := map[string]any{
		"name": "Aboleth", "size": []any{"L"}, "type": "aberration", "alignment": []any{"L", "E"},
		"ac":    []any{map[string]any{"ac": json.Number("17"), "from": []any{"natural armor"}}},
		"hp":    map[string]any{"average": json.Number("135"), "formula": "18d10 + 36"},
		"speed": map[string]any{"walk": json.Number("10"), "swim": json.Number("40")},
		"str":   json.Number("21"), "dex": json.Number("9"), "con": json.Number("15"),
		"int": json.Number("18"), "wis": json.Number("15"), "cha": json.Number("18"),
		"save":            map[string]any{"con": "+6", "int": "+8", "wis": "+6"},
		"skill":           map[string]any{"history": "+12", "perception": "+10"},
		"senses":          []any{"darkvision 120 ft."},
		"passive":         json.Number("20"),
		"languages":       []any{"Deep Speech", "telepathy 120 ft."},
		"cr":              "10",
		"immune":          []any{"poison"},
		"conditionImmune": []any{"poisoned", "charmed"},
	}
	got := RenderString("monster", obj)
	for _, want := range []string{
		"Large, aberration, lawful evil",
		"**Armor Class:** 17 (natural armor)",
		"**Hit Points:** 135 (18d10 + 36)",
		"**Speed:** 10 ft., swim 40 ft.",
		"**Saving Throws:** Con +6, Int +8, Wis +6",
		"**Skills:** History +12, Perception +10",
		"**Damage Immunities:** poison",
		"**Condition Immunities:** poisoned, charmed",
		"**Senses:** darkvision 120 ft., passive Perception 20",
		"**Languages:** Deep Speech, telepathy 120 ft.",
		"**Challenge:** 10 (5900 XP)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestRenderMonster_conditionalDamageResistance(t *testing.T) {
	obj := map[string]any{
		"resist": []any{
			"lightning",
			map[string]any{"resist": []any{"bludgeoning", "piercing", "slashing"}, "note": "from nonmagical attacks"},
		},
	}
	got := RenderString("monster", obj)
	if !strings.Contains(got, "**Damage Resistances:** lightning; bludgeoning; piercing; slashing (from nonmagical attacks)") {
		t.Fatalf("got:\n%s", got)
	}
}

func TestMergeSubrace_inheritsUnoverriddenRaceTraitsAndCombinesAbility(t *testing.T) {
	race := map[string]any{
		"name":  "Elf",
		"size":  []any{"M"},
		"speed": json.Number("30"),
		"ability": []any{
			map[string]any{"dex": json.Number("2")},
		},
		"entries": []any{
			map[string]any{"name": "Fey Ancestry", "entries": []any{"advantage against charm"}},
			map[string]any{"name": "Darkvision", "entries": []any{"60 feet"}},
		},
	}
	subrace := map[string]any{
		"name":     "Drow",
		"raceName": "Elf",
		"ability": []any{
			map[string]any{"cha": json.Number("1")},
		},
		"entries": []any{
			map[string]any{"name": "Superior Darkvision", "entries": []any{"120 feet"}, "data": map[string]any{"overwrite": "Darkvision"}},
			map[string]any{"name": "Sunlight Sensitivity", "entries": []any{"disadvantage in sunlight"}},
		},
	}

	merged := MergeSubrace(subrace, race)
	got := RenderString("subrace", merged)

	if !strings.Contains(got, "DEX +2, CHA +1") {
		t.Fatalf("expected combined ability scores, got:\n%s", got)
	}
	if !strings.Contains(got, "Fey Ancestry") {
		t.Fatalf("expected inherited race trait to survive, got:\n%s", got)
	}
	if strings.Contains(got, "60 feet") {
		t.Fatalf("expected the race's Darkvision entry to be overwritten, got:\n%s", got)
	}
	if !strings.Contains(got, "Superior Darkvision") || !strings.Contains(got, "Sunlight Sensitivity") {
		t.Fatalf("expected subrace's own traits, got:\n%s", got)
	}
}

func TestRenderCard_tableEntryRendersAsLipglossTableNotMarkdownPipes(t *testing.T) {
	obj := map[string]any{
		"name": "Sage",
		"entries": []any{
			map[string]any{
				"name": "Specialty",
				"type": "entries",
				"entries": []any{
					"Roll a d8.",
					map[string]any{
						"type":      "table",
						"colLabels": []any{"d8", "Field of Study"},
						"rows":      []any{[]any{"1", "Alchemist"}, []any{"2", "Astronomer"}},
					},
				},
			},
		},
	}
	got := RenderCard("background", "Sage", "PHB", obj, 80)
	if strings.Contains(got, "| d8 |") || strings.Contains(got, "| --- |") {
		t.Fatalf("expected a lipgloss table, got literal Markdown pipe syntax:\n%s", got)
	}
	if !strings.Contains(got, "Alchemist") || !strings.Contains(got, "Astronomer") {
		t.Fatalf("expected table contents to still be present, got:\n%s", got)
	}
}

func TestXPForCR(t *testing.T) {
	cases := map[string]int{"0": 10, "1/4": 50, "5": 1800, "20": 25000}
	for cr, want := range cases {
		got, ok := XPForCR(cr)
		if !ok || got != want {
			t.Errorf("XPForCR(%q) = %d, %v; want %d", cr, got, ok, want)
		}
	}
	if _, ok := XPForCR("bogus"); ok {
		t.Fatal("expected an unrecognized CR to report ok=false")
	}
}

func TestLore_rendersOnlyWhenPresentAndStaysOutOfTheStatBlock(t *testing.T) {
	obj := map[string]any{
		"name":  "Aboleth",
		"trait": []any{map[string]any{"name": "Amphibious", "entries": []any{"It breathes air and water."}}},
		"_fluff": []any{
			map[string]any{"type": "entries", "entries": []any{"Aboleths lurked in primordial oceans."}},
		},
	}
	if !HasLore(obj) {
		t.Fatal("expected HasLore to see the merged fluff")
	}
	lore := Lore(obj)
	if !strings.Contains(lore, "primordial oceans") {
		t.Fatalf("lore not rendered: %q", lore)
	}

	// The stat block must not carry it: lore is opt-in so it cannot bury the
	// numbers a DM opened the entry to read.
	block := RenderString("monster", obj)
	if strings.Contains(block, "primordial oceans") {
		t.Fatalf("lore leaked into the stat block:\n%s", block)
	}
	if !strings.Contains(block, "Amphibious") {
		t.Fatalf("stat block should still render its traits:\n%s", block)
	}

	bare := map[string]any{"name": "Goblin"}
	if HasLore(bare) || Lore(bare) != "" {
		t.Fatal("an entity with no fluff should report none")
	}
	if RenderLoreCard("monster", "Goblin", "MM", bare, 60) != "" {
		t.Fatal("a lore card for an entity with no lore should be empty")
	}
}
