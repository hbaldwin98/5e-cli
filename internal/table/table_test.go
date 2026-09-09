package table

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/hbaldwin98/5e-cli/internal/store"
)

func TestRollTable_numericRangesAreSeededAndPreserveMetadata(t *testing.T) {
	e := store.Entity{
		Kind:   "table",
		Name:   "Weather",
		Source: "PHB",
		Page:   42,
		SRD:    true,
		JSON:   json.RawMessage(`{"name":"Weather","source":"PHB","colLabels":["d6","Result"],"rows":[["1-3","Sunny"],["4-6","Rain"]]}`),
	}
	seed := int64(7)
	first, err := RollTable(e, Query{Count: 4, Seed: &seed})
	if err != nil {
		t.Fatal(err)
	}
	second, err := RollTable(e, Query{Count: 4, Seed: &seed})
	if err != nil {
		t.Fatal(err)
	}
	if first.Kind != "table" || first.Name != "Weather" || first.Source != "PHB" || len(first.Headers) != 2 {
		t.Fatalf("metadata: %+v", first)
	}
	if len(first.Rolls) != 4 || len(second.Rolls) != 4 {
		t.Fatalf("roll count: %+v %+v", first.Rolls, second.Rolls)
	}
	for i := range first.Rolls {
		if !reflect.DeepEqual(first.Rolls[i], second.Rolls[i]) {
			t.Fatalf("seeded rolls differ: %+v %+v", first.Rolls, second.Rolls)
		}
		if first.Rolls[i].Roll < 1 || first.Rolls[i].Roll > 6 {
			t.Fatalf("roll out of range: %+v", first.Rolls[i])
		}
	}
}

func TestRollTable_encounterShapeSingleTable(t *testing.T) {
	e := store.Entity{
		Kind:   "encounter",
		Name:   "Airborne Encounters",
		Source: "EFA",
		JSON: json.RawMessage(`{"name":"Airborne Encounters","source":"EFA","tables":[
			{"diceExpression":"1d2","table":[
				{"min":1,"max":1,"result":"A pirate airship"},
				{"min":2,"max":2,"result":"A griffon patrol"}
			]}
		]}`),
	}
	seed := int64(3)
	report, err := RollTable(e, Query{Count: 2, Seed: &seed})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Rolls) != 2 {
		t.Fatalf("rolls: %+v", report.Rolls)
	}
	for _, roll := range report.Rolls {
		if roll.Roll < 1 || roll.Roll > 2 || len(roll.Values) != 1 || roll.Values[0] == "" {
			t.Fatalf("roll: %+v", roll)
		}
	}
}

func TestRollTable_encounterShapePicksLevelBand(t *testing.T) {
	e := store.Entity{
		Kind:   "encounter",
		Name:   "Arctic",
		Source: "XGE",
		JSON: json.RawMessage(`{"name":"Arctic","source":"XGE","tables":[
			{"minlvl":1,"maxlvl":4,"diceExpression":"d2","table":[
				{"min":1,"max":1,"result":"low tier A"},
				{"min":2,"max":2,"result":"low tier B"}
			]},
			{"minlvl":5,"maxlvl":10,"diceExpression":"d2","table":[
				{"min":1,"max":1,"result":"high tier A"},
				{"min":2,"max":2,"result":"high tier B"}
			]}
		]}`),
	}
	report, err := RollTable(e, Query{Count: 1, Level: 7})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Rolls) != 1 || !strings.Contains(report.Rolls[0].Values[0], "high tier") {
		t.Fatalf("expected the level-7 band, got: %+v", report.Rolls)
	}
}

func TestRollTable_nestedAndFallbackRows(t *testing.T) {
	e := store.Entity{
		Kind: "table", Name: "Mood", Source: "PHB",
		JSON: json.RawMessage(`{"table":[{"colLabels":["Mood"],"rows":[["odd"],["even"]]}]}`),
	}
	seed := int64(3)
	report, err := RollTable(e, Query{Seed: &seed})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Headers) != 1 || report.Headers[0] != "Mood" || len(report.Rolls) != 1 {
		t.Fatalf("nested table: %+v", report)
	}
	if len(report.Rolls[0].Values) != 1 || (report.Rolls[0].Values[0] != "odd" && report.Rolls[0].Values[0] != "even") {
		t.Fatalf("fallback row: %+v", report.Rolls[0])
	}
}

func TestRollTable_rendersTaggedCellsAndValidatesInput(t *testing.T) {
	e := store.Entity{
		Kind: "table", Name: "Effects", Source: "PHB",
		JSON: json.RawMessage(`{"rows":[["1","{@condition prone}"],["2",{"entry":"{@spell light}"}]]}`),
	}
	seed := int64(1)
	report, err := RollTable(e, Query{Seed: &seed})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Rolls[0].Values) < 2 || (report.Rolls[0].Values[1] != "prone" && report.Rolls[0].Values[1] != "light") {
		t.Fatalf("tagged cell: %+v", report.Rolls)
	}
	if _, err := RollTable(e, Query{Count: -1}); err == nil {
		t.Fatal("expected invalid count")
	}
	if _, err := RollTable(store.Entity{JSON: json.RawMessage(`{"rows":[]}`)}, Query{}); err == nil {
		t.Fatal("expected missing rows")
	}
}

func TestRowRange_percentileZeroZeroIsHundred(t *testing.T) {
	min, max, ok := rowRange("97–00")
	if !ok || min != 97 || max != 100 {
		t.Fatalf("want 97..100, got %d..%d ok=%v", min, max, ok)
	}
	if _, _, ok := rowRange("00"); !ok {
		t.Fatal("bare 00 should parse as 100")
	}
}
