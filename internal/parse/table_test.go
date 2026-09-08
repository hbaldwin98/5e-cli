package parse

import "testing"

func TestTables_harvestsCaptionedTablesFromProse(t *testing.T) {
	root := map[string]any{
		"data": []any{map[string]any{
			"type": "section",
			"name": "Chapter 1",
			"entries": []any{
				map[string]any{
					"type":      "table",
					"caption":   "{@i Weather}",
					"colLabels": []any{"1d20", "Effect"},
					"rows":      []any{[]any{"1-14", "Normal"}},
				},
				map[string]any{"type": "table", "rows": []any{[]any{"1", "Unnamed"}}},
			},
		}},
	}
	got := Tables("XDMG", root)
	if len(got) != 1 {
		t.Fatalf("want 1 captioned table, got %d: %+v", len(got), got)
	}
	if got[0].Kind != "table" || got[0].Name != "Weather" || got[0].Source != "XDMG" {
		t.Fatalf("unexpected entity: %+v", got[0])
	}
	if got[0].Text == "" || len(got[0].JSON) == 0 {
		t.Fatalf("table entity missing body: %+v", got[0])
	}
}

func TestTables_requireSource(t *testing.T) {
	root := map[string]any{"type": "table", "caption": "Weather"}
	if got := Tables("", root); len(got) != 0 {
		t.Fatalf("want no tables without a source, got %+v", got)
	}
}
