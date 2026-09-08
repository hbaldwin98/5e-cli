package parse

import (
	"encoding/json"
)

// Tables extracts rollable table nodes embedded in a book or adventure body.
// Most 5e random tables live inline in prose rather than in data/tables.json,
// so they only become lookup rows if they are harvested during ingest.
func Tables(source string, root any) []Entity {
	var out []Entity
	collectTables(source, root, &out)
	return out
}

func collectTables(source string, v any, out *[]Entity) {
	switch t := v.(type) {
	case []any:
		for _, item := range t {
			collectTables(source, item, out)
		}
	case map[string]any:
		if typ, _ := t["type"].(string); typ == "table" {
			if e, ok := tableEntity(source, t); ok {
				*out = append(*out, e)
			}
		}
		for _, value := range t {
			collectTables(source, value, out)
		}
	}
}

func tableEntity(source string, obj map[string]any) (Entity, bool) {
	name := tableName(obj)
	if name == "" || source == "" {
		return Entity{}, false
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		return Entity{}, false
	}
	text, edges := FlattenEntity(obj)
	return Entity{
		Kind:   "table",
		Name:   name,
		Source: source,
		Page:   intPage(obj["page"]),
		JSON:   raw,
		Text:   text,
		Edges:  edges,
	}, true
}

// tableName prefers the rendered caption; inline tables rarely carry a name.
func tableName(obj map[string]any) string {
	for _, key := range []string{"caption", "name"} {
		if raw, _ := obj[key].(string); raw != "" {
			text, _ := RenderString(raw)
			if text != "" {
				return text
			}
		}
	}
	return ""
}
