package parse

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestFlatten_listAndItem(t *testing.T) {
	var v any
	raw := `{
		"name": "Probe",
		"entries": [
			"Try a {@skill Testing} check.",
			{"type": "list", "items": ["one", "two"]}
		]
	}`
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatal(err)
	}
	text, edges := Flatten(v)
	if !strings.Contains(text, "Probe") || !strings.Contains(text, "Testing") || !strings.Contains(text, "one") {
		t.Fatalf("text=%q", text)
	}
	if len(edges) != 1 || edges[0].ToName != "Testing" {
		t.Fatalf("edges %+v", edges)
	}
}

func TestFromObject_srdAndFluff(t *testing.T) {
	obj := map[string]any{
		"name":    "Testbolt",
		"source":  "PHB",
		"page":    float64(12),
		"srd":     true,
		"entries": []any{"A bolt. {@creature Test Beast|MM}"},
	}
	raw, _ := json.Marshal(obj)
	e, ok := FromObject("spell", obj, raw)
	if !ok || !e.SRD || e.Page != 12 || e.Name != "Testbolt" {
		t.Fatalf("entity %+v ok=%v", e, ok)
	}
	if len(e.Edges) != 1 || e.Edges[0].ToName != "Test Beast" {
		t.Fatalf("edges %+v", e.Edges)
	}
	e = MergeFluff(e, map[string]any{"entries": []any{"Lore about Testbolt."}})
	if !strings.Contains(e.Text, "Lore about Testbolt.") {
		t.Fatalf("fluff not merged: %q", e.Text)
	}

	feat, ok := FromObject("classFeature", map[string]any{
		"name":      "Extra Attack",
		"source":    "PHB",
		"className": "Fighter",
		"level":     float64(5),
		"entries":   []any{"You can attack twice."},
	}, nil)
	if !ok || feat.Name != "Extra Attack (Fighter 5)" {
		t.Fatalf("disambiguate %q ok=%v", feat.Name, ok)
	}
}

func TestSections_namedChunks(t *testing.T) {
	root := map[string]any{
		"data": []any{
			map[string]any{
				"type":    "section",
				"name":    "Holding Breath",
				"entries": []any{"Hold breath for minutes."},
			},
		},
	}
	docs := Sections("bookSection", "PHB", root)
	if len(docs) != 1 || docs[0].Section != "Holding Breath" || docs[0].ParentID != "PHB" {
		t.Fatalf("docs %+v", docs)
	}
	if !strings.Contains(docs[0].Text, "Hold breath") {
		t.Fatalf("text %q", docs[0].Text)
	}
}

func TestSections_adventureLocations(t *testing.T) {
	root := map[string]any{
		"data": []any{
			map[string]any{
				"type": "section",
				"name": "Cragmaw Hideout",
				"entries": []any{
					"Goblins nest here.",
					map[string]any{
						"type":    "entries",
						"name":    "Cave Mouth",
						"entries": []any{"A {@creature Goblin|MM} watches."},
					},
				},
			},
		},
	}
	docs := Sections("adventureSection", "LMoP", root)
	if len(docs) != 2 {
		t.Fatalf("docs %+v", docs)
	}
	kinds := map[string]string{}
	for _, d := range docs {
		kinds[d.Section] = d.Kind
	}
	if kinds["Cragmaw Hideout"] != "adventureSection" || kinds["Cave Mouth"] != "adventureLocation" {
		t.Fatalf("kinds %v", kinds)
	}
	apps := Appearances("LMoP", root)
	if len(apps) != 1 || apps[0].Name != "Goblin" || apps[0].Source != "MM" || apps[0].Role != "npc" {
		t.Fatalf("appearances %+v", apps)
	}
	if apps[0].Chapter != "Cragmaw Hideout" || apps[0].Location != "Cave Mouth" {
		t.Fatalf("chapter/location %q/%q", apps[0].Chapter, apps[0].Location)
	}
}

func TestSections_doesNotFlattenNamedChildrenIntoParent(t *testing.T) {
	root := map[string]any{
		"data": []any{
			map[string]any{
				"type": "section",
				"name": "Phandalin",
				"entries": []any{
					"The town stands on old ruins.",
					map[string]any{
						"type":    "entries",
						"name":    "Redbrand Ruffians",
						"entries": []any{"The ruffians control the town."},
					},
				},
			},
		},
	}

	docs := Sections("adventureSection", "LMoP", root)
	if len(docs) != 2 {
		t.Fatalf("docs %+v", docs)
	}
	if !strings.Contains(docs[0].Text, "The town stands") {
		t.Fatalf("parent text %q", docs[0].Text)
	}
	if strings.Contains(docs[0].Text, "control the town") {
		t.Fatalf("parent included child text %q", docs[0].Text)
	}
	if strings.Contains(string(docs[0].JSON), "control the town") {
		t.Fatalf("parent JSON included child text %s", docs[0].JSON)
	}
	if !strings.Contains(docs[1].Text, "control the town") {
		t.Fatalf("child text %q", docs[1].Text)
	}
}

func TestAppearances_statblock(t *testing.T) {
	root := map[string]any{
		"data": []any{
			map[string]any{
				"type":   "statblock",
				"tag":    "creature",
				"name":   "Ash Zombie",
				"source": "LMoP",
			},
			map[string]any{
				"type":   "statblock",
				"tag":    "item",
				"name":   "Potion of Healing",
				"source": "DMG",
			},
		},
	}
	apps := Appearances("LMoP", root)
	if len(apps) != 2 {
		t.Fatalf("%+v", apps)
	}
	if apps[0].Role != "npc" || apps[0].Kind != "monster" || apps[0].Name != "Ash Zombie" {
		t.Fatalf("creature %+v", apps[0])
	}
	if apps[1].Role != "item" || apps[1].Kind != "item" {
		t.Fatalf("item %+v", apps[1])
	}
}

func TestAppearances_tracksChapterAndLocationContext(t *testing.T) {
	root := map[string]any{
		"data": []any{
			map[string]any{
				"type": "section",
				"name": "Chapter One",
				"entries": []any{
					"A {@item Compass|DMG} is mounted here.",
					map[string]any{
						"type": "entries",
						"name": "Room A",
						"entries": []any{
							"A {@creature Goblin|MM} watches.",
							map[string]any{
								"type":   "statblock",
								"tag":    "creature",
								"name":   "Orc",
								"source": "MM",
							},
							map[string]any{
								"type":   "statblock",
								"tag":    "spell",
								"name":   "Ignored",
								"source": "PHB",
							},
						},
					},
					map[string]any{
						"type": "entries",
						"name": "Room B",
						"data": []any{"A {@item Potion|DMG} is stored here."},
					},
					map[string]any{
						"type":    "section",
						"name":    "Chapter Two",
						"entries": []any{"A {@creature Kobold|MM} appears."},
					},
				},
			},
		},
	}

	got := Appearances("LMoP", root)
	want := []Appearance{
		{Adventure: "LMoP", Role: "item", Kind: "item", Name: "Compass", Source: "DMG", Chapter: "Chapter One"},
		{Adventure: "LMoP", Role: "npc", Kind: "monster", Name: "Goblin", Source: "MM", Chapter: "Chapter One", Location: "Room A"},
		{Adventure: "LMoP", Role: "npc", Kind: "monster", Name: "Orc", Source: "MM", Chapter: "Chapter One", Location: "Room A"},
		{Adventure: "LMoP", Role: "item", Kind: "item", Name: "Potion", Source: "DMG", Chapter: "Chapter One", Location: "Room B"},
		{Adventure: "LMoP", Role: "npc", Kind: "monster", Name: "Kobold", Source: "MM", Chapter: "Chapter Two"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("appearances:\n got %#v\nwant %#v", got, want)
	}
}
