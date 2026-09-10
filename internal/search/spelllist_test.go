package search

import (
	"encoding/json"
	"testing"

	"github.com/hbaldwin98/5e-cli/internal/parse"
	"github.com/hbaldwin98/5e-cli/internal/store"
)

func spellJSON(level int, classes ...string) json.RawMessage {
	from := make([]any, len(classes))
	for i, c := range classes {
		from[i] = map[string]any{"class": map[string]any{"name": c}}
	}
	raw, _ := json.Marshal(map[string]any{
		"level":   level,
		"classes": map[string]any{"fromClassList": from},
	})
	return raw
}

func spellListStore(t *testing.T) (*store.Store, []store.Entity) {
	t.Helper()
	st := openTestStore(t, []parse.Entity{
		{Kind: "spell", Name: "Fire Bolt", Source: "PHB", JSON: spellJSON(0, "Wizard", "Sorcerer")},
		{Kind: "spell", Name: "Sacred Flame", Source: "PHB", JSON: spellJSON(0, "Cleric")},
		{Kind: "spell", Name: "Magic Missile", Source: "PHB", JSON: spellJSON(1, "Wizard")},
		{Kind: "spell", Name: "Fireball", Source: "PHB", JSON: spellJSON(3, "Wizard", "Sorcerer")},
		{Kind: "spell", Name: "Cure Wounds", Source: "PHB", JSON: spellJSON(1, "Cleric")},
	})
	ents, err := st.FilteredNames(store.NameFilter{Kind: "spell"})
	if err != nil {
		t.Fatal(err)
	}
	return st, ents
}

func TestSpellList_byClassOrdersByLevelThenName(t *testing.T) {
	st, ents := spellListStore(t)

	rows := SpellList(st, ents, "Wizard", nil)

	var got []string
	for _, r := range rows {
		got = append(got, r.Name)
	}
	want := []string{"Fire Bolt", "Magic Missile", "Fireball"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if rows[0].Level != 0 || rows[2].Level != 3 {
		t.Fatalf("levels not carried through: %+v", rows)
	}
}

func TestSpellList_levelZeroIsCantripsOnly(t *testing.T) {
	st, ents := spellListStore(t)
	level := 0

	rows := SpellList(st, ents, "Cleric", &level)

	if len(rows) != 1 || rows[0].Name != "Sacred Flame" {
		t.Fatalf("want only the Cleric cantrip, got %+v", rows)
	}
}

func TestSpellList_levelWithoutClassSpansEveryClass(t *testing.T) {
	st, ents := spellListStore(t)
	level := 1

	rows := SpellList(st, ents, "", &level)

	if len(rows) != 2 {
		t.Fatalf("want both 1st-level spells, got %+v", rows)
	}
}
