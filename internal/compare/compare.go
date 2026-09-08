// Package compare compares the source-specific records for one entity.
package compare

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/hbaldwin98/5e-cli/internal/parse"
	"github.com/hbaldwin98/5e-cli/internal/store"
)

// Result contains the full records and the top-level JSON fields that differ.
type Result struct {
	Kind        string       `json:"kind"`
	Name        string       `json:"name"`
	Records     []Record     `json:"records"`
	Differences []Difference `json:"differences"`
}

// Record is the source-specific normalized entity returned by a comparison.
type Record struct {
	Kind   string          `json:"kind"`
	Name   string          `json:"name"`
	Source string          `json:"source"`
	Page   int             `json:"page"`
	SRD    bool            `json:"srd"`
	Text   string          `json:"text"`
	JSON   json.RawMessage `json:"json"`
	Edges  []parse.Edge    `json:"edges"`
}

// Difference is one changed top-level JSON field.
type Difference struct {
	Field  string  `json:"field"`
	Values []Value `json:"values"`
}

// Value is a field value for one source. Present distinguishes a missing field
// from a field whose value is null, false, or zero.
type Value struct {
	Source  string `json:"source"`
	Present bool   `json:"present"`
	Value   any    `json:"value"`
}

// FilterSources keeps only the records whose source is in sources.
func FilterSources(entities []store.Entity, sources []string) []store.Entity {
	wanted := make(map[string]bool, len(sources))
	for _, source := range sources {
		wanted[strings.ToLower(source)] = true
	}
	out := make([]store.Entity, 0, len(entities))
	for _, entity := range entities {
		if wanted[strings.ToLower(entity.Source)] {
			out = append(out, entity)
		}
	}
	return out
}

// Compare compares at least two source-specific records for one entity.
func Compare(entities []store.Entity) (Result, error) {
	if len(entities) < 2 {
		return Result{}, fmt.Errorf("compare requires at least two matching sources")
	}
	ordered := append([]store.Entity(nil), entities...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return strings.ToUpper(ordered[i].Source) < strings.ToUpper(ordered[j].Source)
	})
	kind, name := ordered[0].Kind, ordered[0].Name
	seenSources := make(map[string]bool, len(ordered))
	fields := make([]map[string]any, len(ordered))
	for i, entity := range ordered {
		if !strings.EqualFold(entity.Kind, kind) || !strings.EqualFold(entity.Name, name) {
			return Result{}, fmt.Errorf("cannot compare different entities")
		}
		sourceKey := strings.ToLower(entity.Source)
		if seenSources[sourceKey] {
			return Result{}, fmt.Errorf("cannot compare duplicate source %q", entity.Source)
		}
		seenSources[sourceKey] = true
		var err error
		fields[i], err = jsonFields(entity)
		if err != nil {
			return Result{}, err
		}
	}

	result := Result{
		Kind:    kind,
		Name:    name,
		Records: make([]Record, 0, len(ordered)),
	}
	for _, entity := range ordered {
		result.Records = append(result.Records, record(entity))
	}
	result.Differences = differences(ordered, fields)
	if result.Differences == nil {
		result.Differences = []Difference{}
	}
	return result, nil
}

func record(entity store.Entity) Record {
	return Record{
		Kind:   entity.Kind,
		Name:   entity.Name,
		Source: entity.Source,
		Page:   entity.Page,
		SRD:    entity.SRD,
		Text:   entity.Text,
		JSON:   entity.JSON,
		Edges:  entity.Edges,
	}
}

func jsonFields(entity store.Entity) (map[string]any, error) {
	fields := map[string]any{}
	if len(bytes.TrimSpace(entity.JSON)) == 0 {
		return fields, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(entity.JSON))
	decoder.UseNumber()
	if err := decoder.Decode(&fields); err != nil {
		return nil, fmt.Errorf("decode %s %q (%s): %w", entity.Kind, entity.Name, entity.Source, err)
	}
	if fields == nil {
		return map[string]any{}, nil
	}
	return fields, nil
}

func differences(entities []store.Entity, fields []map[string]any) []Difference {
	keys := map[string]bool{}
	for _, values := range fields {
		for key := range values {
			if key != "name" && key != "source" {
				keys[key] = true
			}
		}
	}
	orderedKeys := make([]string, 0, len(keys))
	for key := range keys {
		orderedKeys = append(orderedKeys, key)
	}
	sort.Strings(orderedKeys)

	var out []Difference
	for _, key := range orderedKeys {
		values := make([]Value, 0, len(entities))
		var first any
		firstPresent := false
		same := true
		for i, entity := range entities {
			value, present := fields[i][key]
			values = append(values, Value{Source: entity.Source, Present: present, Value: value})
			if i == 0 {
				first, firstPresent = value, present
				continue
			}
			if present != firstPresent || !reflect.DeepEqual(value, first) {
				same = false
			}
		}
		if !same {
			out = append(out, Difference{Field: key, Values: values})
		}
	}
	return out
}
