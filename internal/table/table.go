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
}

// RollTable selects rows from an indexed table entity.
func RollTable(entity store.Entity, q Query) (Report, error) {
	var obj map[string]any
	dec := json.NewDecoder(bytes.NewReader(entity.JSON))
	dec.UseNumber()
	if err := dec.Decode(&obj); err != nil {
		return Report{}, fmt.Errorf("decode table %q: %w", entity.Name, err)
	}
	headers, rows := tableRows(obj)
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

func tableRows(obj map[string]any) ([]string, []row) {
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
	min, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, false
	}
	max := min
	if len(parts) == 2 {
		max, err = strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil || max < min {
			return 0, 0, false
		}
	}
	return min, max, true
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
