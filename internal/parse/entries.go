package parse

import (
	"encoding/json"
	"fmt"
	"strings"
)

var skipKeys = map[string]bool{
	"source":            true,
	"page":              true,
	"otherSources":      true,
	"additionalSources": true,
	"referenceSources":  true,
	"reprintedAs":       true,
	"srd":               true,
	"srd52":             true,
	"basicRules":        true,
	"hasFluff":          true,
	"hasFluffImages":    true,
	"hasToken":          true,
	"tokenUrl":          true,
	"soundClip":         true,
	"fluff":             true,
	"_meta":             true,
	"damageTags":        true,
	"miscTags":          true,
	"languageTags":      true,
	"areaTags":          true,
	"spellcastingTags":  true,
	"traitTags":         true,
	"actionTags":        true,
	"senseTags":         true,
	"conditionInflict":  true,
	"damageInflict":     true,
	"attachedItems":     true,
	"environment":       true,
}

// Flatten walks a 5etools JSON value and returns plaintext plus tag edges.
func Flatten(v any) (string, []Edge) {
	var b strings.Builder
	var edges []Edge
	walk(v, &b, &edges)
	return strings.TrimSpace(b.String()), edges
}

// FlattenEntity extracts searchable text from a full entity object.
func FlattenEntity(obj map[string]any) (string, []Edge) {
	var b strings.Builder
	var edges []Edge
	walkMap(obj, &b, &edges)
	return strings.TrimSpace(b.String()), edges
}

func walk(v any, b *strings.Builder, edges *[]Edge) {
	switch t := v.(type) {
	case nil:
		return
	case string:
		text, more := RenderString(t)
		writeSep(b)
		b.WriteString(text)
		*edges = append(*edges, more...)
	case json.Number:
		writeSep(b)
		b.WriteString(t.String())
	case float64:
		writeSep(b)
		if t == float64(int64(t)) {
			fmt.Fprintf(b, "%d", int64(t))
		} else {
			fmt.Fprintf(b, "%v", t)
		}
	case bool:
		return
	case []any:
		for _, item := range t {
			walk(item, b, edges)
		}
	case map[string]any:
		walkMap(t, b, edges)
	}
}

func walkMap(obj map[string]any, b *strings.Builder, edges *[]Edge) {
	if typ, _ := obj["type"].(string); typ == "image" || typ == "gallery" {
		return
	}
	if name, _ := obj["name"].(string); name != "" {
		text, more := RenderString(name)
		writeSep(b)
		b.WriteString(text)
		*edges = append(*edges, more...)
	}
	if cap, _ := obj["caption"].(string); cap != "" {
		text, more := RenderString(cap)
		writeSep(b)
		b.WriteString(text)
		*edges = append(*edges, more...)
	}
	for k, v := range obj {
		if k == "name" || k == "caption" || k == "type" || skipKeys[k] {
			continue
		}
		walk(v, b, edges)
	}
}

func writeSep(b *strings.Builder) {
	if b.Len() == 0 {
		return
	}
	s := b.String()
	if s[len(s)-1] == '\n' {
		return
	}
	b.WriteByte('\n')
}
