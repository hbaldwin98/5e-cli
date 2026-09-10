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

// MergeLegendaryGroup attaches a legendary group's lair actions, regional
// effects, and mythic encounter onto a monster entity, under the keys the
// monster renderer already walks.
//
// A monster's own record carries only a {"name","source"} pointer to its
// group; the content lives in bestiary/legendarygroups.json. So a boss
// monster's stat block looks complete while its lair actions — the whole
// reason a DM pulls the stat block up to run the set-piece fight — are
// absent. Merging here, rather than looking the group up at render time,
// follows MergeSpellClasses and keeps internal/statblock free of any store.
func MergeLegendaryGroup(e Entity, group map[string]any) Entity {
	dec := json.NewDecoder(strings.NewReader(string(e.JSON)))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return e
	}
	var merged bool
	var text []string
	for _, key := range []string{"lairActions", "regionalEffects", "mythicEncounter"} {
		entries, ok := group[key].([]any)
		if !ok || len(entries) == 0 {
			continue
		}
		obj[key] = entries
		merged = true
		if t, _ := Flatten(map[string]any{key: entries}); t != "" {
			text = append(text, t)
		}
	}
	if !merged {
		return e
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		return e
	}
	e.JSON = raw
	// Keep the merged content searchable too, so "lair action" finds the
	// monsters that have one rather than nothing at all.
	if joined := strings.Join(text, "\n\n"); joined != "" {
		if e.Text != "" {
			e.Text = e.Text + "\n\n" + joined
		} else {
			e.Text = joined
		}
	}
	return e
}

// FlattenMagicVariant lifts a magicvariant's "inherits" object up to the top
// level, returning the merged object and its re-encoded JSON.
//
// A magicvariant record describes a template ("+1 Ammunition") whose own top
// level carries only how it combines with a base item (type, requires,
// excludes). Everything that makes it an item a DM can read — source, page,
// srd, rarity, entries, reqAttune, the bonus fields — lives under inherits,
// because those are the fields the generated item inherits. Only 5 of 230
// records carry a top-level source, so without this the other 225 (every
// +1/+2/+3 weapon, armor, shield and ammunition, Adamantine and Mithral
// armor) fail FromObjectWithName's name/source check and never reach the
// index at all.
//
// inherits wins on conflict, since it is the generated item's own value, and
// the merged JSON is what gets stored so the item renderer sees those fields
// too rather than only the search text seeing them.
func FlattenMagicVariant(obj map[string]any) (map[string]any, json.RawMessage, bool) {
	inherits, ok := obj["inherits"].(map[string]any)
	if !ok {
		return obj, nil, false
	}
	merged := make(map[string]any, len(obj)+len(inherits))
	for k, v := range obj {
		if k == "inherits" {
			continue
		}
		merged[k] = v
	}
	for k, v := range inherits {
		merged[k] = v
	}
	raw, err := json.Marshal(merged)
	if err != nil {
		return obj, nil, false
	}
	return merged, raw, true
}

// FromObject builds an Entity from a 5etools object and its array kind,
// disambiguating its name up front (see DisambiguateName). classFeature and
// subclassFeature are the exception: a plain feature name only actually
// collides with another feature of the same kind and source some of the
// time (e.g. every class's own "Fighting Style" feature, all from the same
// book), so eagerly disambiguating every one of them would force a lookup
// like "Action Surge (Fighter 2)" even when the plain name "Action Surge" is
// perfectly unique. The ingest pipeline instead collects those two kinds
// with their plain name via FromObjectWithName and disambiguates only the
// ones that actually collide once every class has been loaded — see
// internal/ingest's resolveFeatureNames.
func FromObject(kind string, obj map[string]any, raw json.RawMessage) (Entity, bool) {
	name, _ := obj["name"].(string)
	if kind != "classFeature" && kind != "subclassFeature" {
		name = DisambiguateName(kind, name, obj)
	}
	return FromObjectWithName(kind, obj, raw, name)
}

// FromObjectWithName is FromObject with the name already resolved — used
// directly by classFeature/subclassFeature ingest once collision detection
// across every class has picked the final name for each feature.
func FromObjectWithName(kind string, obj map[string]any, raw json.RawMessage, name string) (Entity, bool) {
	source, _ := obj["source"].(string)
	if name == "" || source == "" {
		return Entity{}, false
	}
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

// DisambiguateName appends a distinguishing suffix to name for kinds whose
// plain name is expected to collide (classFeature/subclassFeature, when
// called for a colliding group, and subrace, which reuses names like "High"
// or "Wood" across many different races).
func DisambiguateName(kind, name string, obj map[string]any) string {
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
	// Keep the structured entries too, not just the flattened search text,
	// so the lore can actually be displayed rather than only matched. It
	// goes under an underscore-prefixed key that no 5etools record uses, and
	// statblock renders it only when a caller asks for lore — see
	// statblock.Lore, and the decision not to put it in every stat block.
	if entries, ok := fluff["entries"].([]any); ok && len(entries) > 0 {
		if raw, ok := withFluffEntries(e.JSON, entries); ok {
			e.JSON = raw
		}
	}
	return e
}

// FluffKey is where MergeFluff stores an entity's lore entries on its JSON.
const FluffKey = "_fluff"

func withFluffEntries(raw json.RawMessage, entries []any) (json.RawMessage, bool) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return nil, false
	}
	obj[FluffKey] = entries
	out, err := json.Marshal(obj)
	if err != nil {
		return nil, false
	}
	return out, true
}
