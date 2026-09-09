package parse

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Entity is a normalized 5etools record ready for the index.
type Entity struct {
	Kind   string
	Name   string
	Source string
	Page   int
	SRD    bool
	JSON   json.RawMessage
	Text   string
	Edges  []Edge
}

// Key is the unique identity: kind + name + source (case-folded).
func (e Entity) Key() string {
	return e.Kind + "\x00" + strings.ToLower(e.Name) + "\x00" + strings.ToLower(e.Source)
}

// FromObject builds an Entity from a 5etools object and its array kind.
func FromObject(kind string, obj map[string]any, raw json.RawMessage) (Entity, bool) {
	name, _ := obj["name"].(string)
	source, _ := obj["source"].(string)
	if name == "" || source == "" {
		return Entity{}, false
	}
	name = disambiguateName(kind, name, obj)
	text, edges := FlattenEntity(obj)
	return Entity{
		Kind:   kind,
		Name:   name,
		Source: source,
		Page:   intPage(obj["page"]),
		SRD:    isSRD(obj),
		JSON:   raw,
		Text:   text,
		Edges:  edges,
	}, true
}

func disambiguateName(kind, name string, obj map[string]any) string {
	switch kind {
	case "classFeature":
		if cn, _ := obj["className"].(string); cn != "" {
			if lvl := intPage(obj["level"]); lvl > 0 {
				return name + " (" + cn + " " + strconv.Itoa(lvl) + ")"
			}
			return name + " (" + cn + ")"
		}
	case "subclassFeature":
		cn, _ := obj["className"].(string)
		sc, _ := obj["subclassShortName"].(string)
		lvl := intPage(obj["level"])
		if cn != "" && sc != "" && lvl > 0 {
			return name + " (" + cn + " " + sc + " " + strconv.Itoa(lvl) + ")"
		}
		if cn != "" && sc != "" {
			return name + " (" + cn + " " + sc + ")"
		}
	case "subrace":
		if rn, _ := obj["raceName"].(string); rn != "" {
			return name + " (" + rn + ")"
		}
	}
	return name
}

func intPage(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case json.Number:
		n, _ := t.Int64()
		return int(n)
	default:
		return 0
	}
}

func isSRD(obj map[string]any) bool {
	return truthy(obj["srd"]) || truthy(obj["srd52"]) || truthy(obj["basicRules"])
}

func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t != ""
	default:
		return false
	}
}

// MergeSpellClasses injects a "classes" field into a spell entity's JSON,
// shaped like 5etools' own (now-retired) per-spell classes field —
// {"fromClassList": [{"class": {"name": ...}}, ...]} — so
// internal/statblock's spellClasses/SpellGrantedToClass can read it exactly
// as it would that older format. Newer 5etools data dropped per-spell class
// tagging in favor of a separately generated lookup keyed by spell name
// (gendata-spell-source-lookup.json), so ingest must reattach it.
func MergeSpellClasses(e Entity, classNames []string) Entity {
	if len(classNames) == 0 {
		return e
	}
	dec := json.NewDecoder(strings.NewReader(string(e.JSON)))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return e
	}
	list := make([]map[string]any, len(classNames))
	for i, name := range classNames {
		list[i] = map[string]any{"class": map[string]any{"name": name}}
	}
	obj["classes"] = map[string]any{"fromClassList": list}
	raw, err := json.Marshal(obj)
	if err != nil {
		return e
	}
	e.JSON = raw
	if e.Text != "" {
		e.Text = e.Text + "\n\n" + strings.Join(classNames, ", ")
	} else {
		e.Text = strings.Join(classNames, ", ")
	}
	return e
}

// MergeFluff appends fluff plaintext and edges onto an entity.
func MergeFluff(e Entity, fluff map[string]any) Entity {
	ft, fe := Flatten(fluff)
	if ft == "" {
		return e
	}
	if e.Text != "" {
		e.Text = e.Text + "\n\n" + ft
	} else {
		e.Text = ft
	}
	e.Edges = append(e.Edges, fe...)
	return e
}
