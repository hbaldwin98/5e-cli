package parse

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestApplyProperties_substitutesFieldsAndModifiers(t *testing.T) {
	obj := map[string]any{
		"bonusWeapon": "+1",
		"amount1":     json.Number("0.5"),
		"amount2":     json.Number("3"),
		"baseName":    "Longsword",
		"count":       json.Number("2.4"),
	}
	cases := map[string]string{
		"a {=bonusWeapon} bonus":  "a +1 bonus",
		"{=amount1/v} tablespoon": "½ tablespoon",
		"{=amount2} pounds":       "3 pounds",
		"{=amount2/x} pounds":     "three pounds",
		"{=baseName/l}":           "longsword",
		"{=baseName/a} longsword": "a longsword",
		"{=count/c}":              "3",
		"{=count/f}":              "2",
		// Modifiers apply in _OP_ORDER, not written order: ceil, then to
		// words, then title-case.
		"{=count/txc}": "Three",
	}
	for in, want := range cases {
		if got := ApplyProperties(in, obj); got != want {
			t.Errorf("ApplyProperties(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestApplyProperties_unknownFieldKeepsTheRawToken(t *testing.T) {
	// Blanking it would silently drop the token and leave the surrounding
	// words reading as though nothing were missing.
	got := ApplyProperties("a {=nosuchfield} bonus", map[string]any{})
	if want := "a {=nosuchfield} bonus"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolveInjectors_rewritesNestedStringsAndReportsNoChange(t *testing.T) {
	obj := map[string]any{
		"bonusAc": "+2",
		"entries": []any{
			"You have a {=bonusAc} bonus to AC.",
			map[string]any{"entries": []any{"Still {=bonusAc}."}},
		},
	}
	resolved, raw, ok := ResolveInjectors(obj)
	if !ok {
		t.Fatal("expected a rewrite")
	}
	if raw == nil {
		t.Fatal("expected re-encoded JSON")
	}
	entries, _ := resolved["entries"].([]any)
	if entries[0] != "You have a +2 bonus to AC." {
		t.Fatalf("top-level entry: %v", entries[0])
	}
	nested, _ := entries[1].(map[string]any)
	inner, _ := nested["entries"].([]any)
	if inner[0] != "Still +2." {
		t.Fatalf("nested entry: %v", inner[0])
	}

	if _, _, ok := ResolveInjectors(map[string]any{"entries": []any{"plain text"}}); ok {
		t.Fatal("a record with no injectors should report no change")
	}
}

func TestFlattenMagicVariant_liftsInheritsSoTheRecordCanIndex(t *testing.T) {
	obj := map[string]any{
		"name": "+1 Ammunition",
		"type": "GV|DMG",
		"inherits": map[string]any{
			"source":      "DMG",
			"rarity":      "uncommon",
			"bonusWeapon": "+1",
		},
	}
	merged, raw, ok := FlattenMagicVariant(obj)
	if !ok {
		t.Fatal("expected the merge to apply")
	}
	if merged["source"] != "DMG" || merged["rarity"] != "uncommon" {
		t.Fatalf("inherits was not lifted: %v", merged)
	}
	if _, still := merged["inherits"]; still {
		t.Fatal("inherits should not survive as its own key")
	}
	if merged["name"] != "+1 Ammunition" || merged["type"] != "GV|DMG" {
		t.Fatalf("top-level fields should survive: %v", merged)
	}
	var round map[string]any
	if err := json.Unmarshal(raw, &round); err != nil {
		t.Fatalf("re-encoded JSON: %v", err)
	}
	if round["source"] != "DMG" {
		t.Fatal("the stored JSON must carry the lifted fields, not just the object")
	}

	// Without inherits there is nothing to do and the original is kept.
	if _, _, ok := FlattenMagicVariant(map[string]any{"name": "x"}); ok {
		t.Fatal("expected no merge without an inherits key")
	}
}

func TestFlattenMagicVariant_inheritsWinsOnConflict(t *testing.T) {
	merged, _, _ := FlattenMagicVariant(map[string]any{
		"name":     "Thing",
		"type":     "GV|DMG",
		"inherits": map[string]any{"source": "DMG", "type": "S"},
	})
	if merged["type"] != "S" {
		t.Fatalf("inherits should win, got %v", merged["type"])
	}
}

func TestMergeLegendaryGroup_attachesSectionsAndSearchText(t *testing.T) {
	e := Entity{
		Kind: "monster", Name: "Aboleth", Source: "MM",
		JSON: json.RawMessage(`{"name":"Aboleth","legendaryGroup":{"name":"Aboleth","source":"MM"}}`),
		Text: "Aboleth",
	}
	merged := MergeLegendaryGroup(e, map[string]any{
		"name":            "Aboleth",
		"lairActions":     []any{"On initiative count 20, the aboleth takes a lair action."},
		"regionalEffects": []any{"Underground surfaces are slimy."},
	})

	var obj map[string]any
	if err := json.Unmarshal(merged.JSON, &obj); err != nil {
		t.Fatal(err)
	}
	if _, ok := obj["lairActions"].([]any); !ok {
		t.Fatalf("lairActions not merged: %v", obj)
	}
	if _, ok := obj["regionalEffects"].([]any); !ok {
		t.Fatalf("regionalEffects not merged: %v", obj)
	}
	// The merged content has to be searchable, so "lair action" finds the
	// monsters that have one.
	if !strings.Contains(merged.Text, "lair action") {
		t.Fatalf("merged text should carry the lair action: %q", merged.Text)
	}
	if !strings.Contains(merged.Text, "Aboleth") {
		t.Fatalf("original text should survive: %q", merged.Text)
	}
}

func TestMergeLegendaryGroup_groupWithNoContentLeavesTheEntityAlone(t *testing.T) {
	e := Entity{Kind: "monster", Name: "X", JSON: json.RawMessage(`{"name":"X"}`), Text: "X"}
	if got := MergeLegendaryGroup(e, map[string]any{"name": "X", "page": 1}); string(got.JSON) != string(e.JSON) || got.Text != e.Text {
		t.Fatalf("expected the entity untouched, got %+v", got)
	}
}
