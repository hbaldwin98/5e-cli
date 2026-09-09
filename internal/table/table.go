package table

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/hbaldwin98/5e-cli/internal/parse"
	"github.com/hbaldwin98/5e-cli/internal/store"
)

// Report is the result of rolling an indexed random table.
type Report struct {
	Kind    string   `json:"kind"`
	Name    string   `json:"name"`
	Source  string   `json:"source"`
	Headers []string `json:"headers,omitempty"`
	Rolls   []Roll   `json:"rolls"`
}

// Roll is one selected table row.
type Roll struct {
	Roll   int      `json:"roll"`
	Values []string `json:"values"`
}

// Query controls how many rows are selected and makes runs reproducible when
// Seed is provided.
type Query struct {
	Count int
	Seed  *int64
	// Level selects which level-banded sub-table to roll on for an
	// `encounter` kind entity (data/encounters.json), whose tables are
	// split by character level range (e.g. 1-4, 5-10) rather than being one
	// flat table the way data/tables.json entries are. Zero picks the
	// first sub-table. Ignored for every other table shape.
	Level int
}

// RollTable selects rows from an indexed table entity.
func RollTable(entity store.Entity, q Query) (Report, error) {
	var obj map[string]any
	dec := json.NewDecoder(bytes.NewReader(entity.JSON))
	dec.UseNumber()
	if err := dec.Decode(&obj); err != nil {
		return Report{}, fmt.Errorf("decode table %q: %w", entity.Name, err)
	}
	headers, rows := tableRows(obj, q.Level)
	if len(rows) == 0 {
		return Report{}, fmt.Errorf("table %q has no rows", entity.Name)
	}
	count := q.Count
	if count == 0 {
		count = 1
	}
	if count < 1 {
		return Report{}, fmt.Errorf("roll count must be positive")
	}
	seed := time.Now().UnixNano()
	if q.Seed != nil {
		seed = *q.Seed
	}
	rng := rand.New(rand.NewSource(seed))

	indexed := indexRows(rows)
	rolls := make([]Roll, 0, count)
	for range count {
		var roll int
		var row row
		if indexed == nil {
			index := rng.Intn(len(rows))
			roll, row = index+1, rows[index]
		} else {
			roll, row = selectRow(rng, indexed)
		}
		rolls = append(rolls, Roll{Roll: roll, Values: row.values})
	}
	return Report{
		Kind:    entity.Kind,
		Name:    entity.Name,
		Source:  entity.Source,
		Headers: headers,
		Rolls:   rolls,
	}, nil
}

type row struct {
	values []string
	min    int
	max    int
	valid  bool
}

func tableRows(obj map[string]any, level int) ([]string, []row) {
	headers := values(obj["colLabels"])
	rowsValue := obj["rows"]
	if rowsValue == nil {
		if nested, ok := obj["table"].([]any); ok && len(nested) > 0 {
			if nestedObj, ok := nested[0].(map[string]any); ok {
				if len(headers) == 0 {
					headers = values(nestedObj["colLabels"])
				}
				rowsValue = nestedObj["rows"]
			}
		}
	}
	if rowsValue == nil {
		if h, r, ok := encounterRows(obj, level); ok {
			return h, r
		}
	}
	var rows []row
	if values, ok := rowsValue.([]any); ok {
		for _, value := range values {
			cells, ok := value.([]any)
			if !ok || len(cells) == 0 {
				continue
			}
			text := make([]string, 0, len(cells))
			for _, cell := range cells {
				text = append(text, cellText(cell))
			}
			min, max, valid := rowRange(text[0])
			rows = append(rows, row{values: text, min: min, max: max, valid: valid})
		}
	}
	return headers, rows
}

// encounterRows reads the `encounter` kind's shape (data/encounters.json):
// {"tables":[{"minlvl":1,"maxlvl":4,"diceExpression":"d100","table":[{"min":1,"max":5,"result":"..."}]}]}
// rather than the row/colLabels shape data/tables.json and embedded prose
// tables use. Several sub-tables banded by character level are common
// (low-level vs. high-level encounters for the same region); level selects
// the band that contains it, defaulting to the first sub-table when level
// is 0 or matches none of the bands (most encounter tables have only one).
func encounterRows(obj map[string]any, level int) ([]string, []row, bool) {
	tables, ok := obj["tables"].([]any)
	if !ok || len(tables) == 0 {
		return nil, nil, false
	}
	sub := tables[0]
	if level > 0 {
		for _, t := range tables {
			tm, ok := t.(map[string]any)
			if !ok {
				continue
			}
			min, max := numberValue(tm["minlvl"]), numberValue(tm["maxlvl"])
			if min == 0 && max == 0 {
				continue
			}
			if level >= min && level <= max {
				sub = t
				break
			}
		}
	}
	tm, ok := sub.(map[string]any)
	if !ok {
		return nil, nil, false
	}
	rowsAny, ok := tm["table"].([]any)
	if !ok {
		return nil, nil, false
	}
	var rows []row
	for _, r := range rowsAny {
		rm, ok := r.(map[string]any)
		if !ok {
			continue
		}
		min := numberValue(rm["min"])
		max := numberValue(rm["max"])
		if max == 0 {
			max = min
		}
		rows = append(rows, row{values: []string{cellText(rm["result"])}, min: min, max: max, valid: min > 0})
	}
	return []string{"Result"}, rows, len(rows) > 0
}

func numberValue(v any) int {
	switch n := v.(type) {
	case json.Number:
		i, _ := strconv.Atoi(n.String())
		return i
	case float64:
		return int(n)
	}
	return 0
}

func indexRows(rows []row) []row {
	for _, item := range rows {
		if !item.valid {
			return nil
		}
	}
	return rows
}

func selectRow(rng *rand.Rand, rows []row) (int, row) {
	min, max := rows[0].min, rows[0].max
	for _, item := range rows[1:] {
		if item.min < min {
			min = item.min
		}
		if item.max > max {
			max = item.max
		}
	}
	roll := min + rng.Intn(max-min+1)
	for _, item := range rows {
		if roll >= item.min && roll <= item.max {
			return roll, item
		}
	}
	index := rng.Intn(len(rows))
	return index + 1, rows[index]
}

func rowRange(value string) (int, int, bool) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "–", "-"))
	parts := strings.Split(value, "-")
	if len(parts) > 2 {
		return 0, 0, false
	}
	min, ok := rollValue(parts[0])
	if !ok {
		return 0, 0, false
	}
	max := min
	if len(parts) == 2 {
		if max, ok = rollValue(parts[1]); !ok || max < min {
			return 0, 0, false
		}
	}
	return min, max, true
}

// rollValue parses one end of a table range. Percentile tables write 100 as
// "00", so a d100 row reads "97-00"; without this every d100 table would fail
// range parsing and fall back to picking rows by ordinal.
func rollValue(part string) (int, bool) {
	part = strings.TrimSpace(part)
	if part == "00" {
		return 100, true
	}
	n, err := strconv.Atoi(part)
	if err != nil {
		return 0, false
	}
	return n, true
}

func values(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, cellText(item))
	}
	return out
}

func cellText(value any) string {
	switch value := value.(type) {
	case string:
		text, _ := parse.RenderString(value)
		return text
	case json.Number:
		return value.String()
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(value)
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			parts = append(parts, cellText(item))
		}
		return strings.Join(parts, "; ")
	case map[string]any:
		if entry, ok := value["entry"]; ok {
			return cellText(entry)
		}
		if text, ok := value["text"]; ok {
			return cellText(text)
		}
	}
	return ""
}
