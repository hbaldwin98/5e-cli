package compare

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hbaldwin98/5e-cli/internal/parse"
	"github.com/hbaldwin98/5e-cli/internal/store"
)

func TestCompare_reportsChangedFieldsAndPreservesRecords(t *testing.T) {
	result, err := Compare([]store.Entity{
		{
			Kind: "spell", Name: "Fireball", Source: "XPHB", Page: 234, SRD: true,
			Text: "new fire", JSON: json.RawMessage(`{"name":"Fireball","source":"XPHB","level":3,"entries":["new"],"ritual":null}`),
			Edges: []parse.Edge{{Tag: "condition", ToKind: "condition", ToName: "Burning"}},
		},
		{
			Kind: "spell", Name: "Fireball", Source: "PHB", Page: 241,
			Text: "old fire", JSON: json.RawMessage(`{"name":"Fireball","source":"PHB","level":3,"entries":["old"]}`),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != "spell" || result.Name != "Fireball" {
		t.Fatalf("identity: %+v", result)
	}
	if len(result.Records) != 2 || result.Records[0].Source != "PHB" || result.Records[1].Source != "XPHB" {
		t.Fatalf("record order: %+v", result.Records)
	}
	if len(result.Records[1].Edges) != 1 || result.Records[1].Page != 234 || !result.Records[1].SRD {
		t.Fatalf("record data: %+v", result.Records[1])
	}
	if len(result.Differences) != 2 {
		t.Fatalf("differences: %+v", result.Differences)
	}
	if result.Differences[0].Field != "entries" || result.Differences[1].Field != "ritual" {
		t.Fatalf("difference order: %+v", result.Differences)
	}
	if result.Differences[1].Values[0].Present || !result.Differences[1].Values[1].Present {
		t.Fatalf("presence: %+v", result.Differences[1])
	}
	if result.Differences[1].Values[1].Value != nil {
		t.Fatalf("null value: %#v", result.Differences[1].Values[1].Value)
	}
}

func TestCompare_ignoresIdentityFieldsAndReportsNoDifferences(t *testing.T) {
	result, err := Compare([]store.Entity{
		{Kind: "skill", Name: "Athletics", Source: "PHB", JSON: json.RawMessage(`{"name":"Athletics","source":"PHB","entries":["same"]}`)},
		{Kind: "skill", Name: "Athletics", Source: "XPHB", JSON: json.RawMessage(`{"name":"Athletics","source":"XPHB","entries":["same"]}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Differences == nil || len(result.Differences) != 0 {
		t.Fatalf("differences: %#v", result.Differences)
	}
}

func TestCompare_rejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name     string
		entities []store.Entity
		want     string
	}{
		{name: "one source", entities: []store.Entity{{Kind: "spell", Name: "Light", Source: "PHB"}}, want: "at least two"},
		{name: "different entities", entities: []store.Entity{{Kind: "spell", Name: "Light", Source: "PHB", JSON: json.RawMessage(`{}`)}, {Kind: "spell", Name: "Darkness", Source: "XPHB", JSON: json.RawMessage(`{}`)}}, want: "different entities"},
		{name: "duplicate source", entities: []store.Entity{{Kind: "spell", Name: "Light", Source: "PHB", JSON: json.RawMessage(`{}`)}, {Kind: "spell", Name: "Light", Source: "phb", JSON: json.RawMessage(`{}`)}}, want: "duplicate source"},
		{name: "invalid json", entities: []store.Entity{{Kind: "spell", Name: "Light", Source: "PHB", JSON: json.RawMessage(`{`)}, {Kind: "spell", Name: "Light", Source: "XPHB", JSON: json.RawMessage(`{}`)}}, want: "decode"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Compare(test.entities)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error %v, want %q", err, test.want)
			}
		})
	}
}
