package table

import (
	"encoding/json"
	"reflect"
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
