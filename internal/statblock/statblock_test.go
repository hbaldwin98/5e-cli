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
