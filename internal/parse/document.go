package parse

import (
	"encoding/json"
)

// Document is a book or adventure section chunk.
type Document struct {
	Kind     string
	ParentID string
	Section  string
	JSON     json.RawMessage
	Text     string
}

// Sections extracts named section chunks from a book/adventure file.
func Sections(kind, parentID string, root any) []Document {
	var out []Document
	collectSections(kind, parentID, root, &out)
	return out
}

func collectSections(kind, parentID string, v any, out *[]Document) {
	switch t := v.(type) {
	case []any:
		for _, item := range t {
			collectSections(kind, parentID, item, out)
		}
	case map[string]any:
		typ, _ := t["type"].(string)
		name, _ := t["name"].(string)
		if typ == "section" && name != "" {
			raw, _ := json.Marshal(t)
			text, _ := Flatten(t)
			*out = append(*out, Document{
				Kind:     kind,
				ParentID: parentID,
				Section:  name,
				JSON:     raw,
				Text:     text,
			})
		}
		if kind == "adventureSection" && typ == "entries" && name != "" {
			raw, _ := json.Marshal(t)
			text, _ := Flatten(t)
			*out = append(*out, Document{
				Kind:     "adventureLocation",
				ParentID: parentID,
				Section:  name,
				JSON:     raw,
				Text:     text,
			})
		}
		if entries, ok := t["entries"]; ok {
			collectSections(kind, parentID, entries, out)
		}
		if data, ok := t["data"]; ok {
			collectSections(kind, parentID, data, out)
		}
	}
}

// Appearance is a creature or item mentioned in an adventure.
type Appearance struct {
	Adventure string `json:"adventure"`
	Role      string `json:"role"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Source    string `json:"source,omitempty"`
	Chapter   string `json:"chapter,omitempty"`
	Location  string `json:"location,omitempty"`
}

// Appearances extracts npc/item mentions from an adventure file.
func Appearances(adventure string, root any) []Appearance {
	var out []Appearance
	collectAppearances(adventure, "", "", root, &out)
	return out
}

func collectAppearances(adv, chapter, loc string, v any, out *[]Appearance) {
	switch t := v.(type) {
	case string:
		_, edges := RenderString(t)
		for _, e := range edges {
			if a, ok := appearanceFromEdge(adv, chapter, loc, e); ok {
				*out = append(*out, a)
			}
		}
	case []any:
		for _, item := range t {
			collectAppearances(adv, chapter, loc, item, out)
		}
	case map[string]any:
		typ, _ := t["type"].(string)
		name, _ := t["name"].(string)
		nextChapter, nextLoc := chapter, loc
		if typ == "section" && name != "" {
			nextChapter, nextLoc = name, ""
		} else if typ == "entries" && name != "" {
			nextLoc = name
		}
		if typ == "statblock" {
			if a, ok := appearanceFromStatblock(adv, nextChapter, nextLoc, t); ok {
				*out = append(*out, a)
			}
		}
		if entries, ok := t["entries"]; ok {
			collectAppearances(adv, nextChapter, nextLoc, entries, out)
		}
		if data, ok := t["data"]; ok {
			collectAppearances(adv, nextChapter, nextLoc, data, out)
		}
	}
}

func appearanceFromEdge(adv, chapter, loc string, e Edge) (Appearance, bool) {
	role := appearanceRole(e.Tag)
	if role == "" || e.ToName == "" {
		return Appearance{}, false
	}
	return Appearance{
		Adventure: adv,
		Role:      role,
		Kind:      e.ToKind,
		Name:      e.ToName,
		Source:    e.ToSource,
		Chapter:   chapter,
		Location:  loc,
	}, true
}

func appearanceFromStatblock(adv, chapter, loc string, t map[string]any) (Appearance, bool) {
	tag, _ := t["tag"].(string)
	name, _ := t["name"].(string)
	source, _ := t["source"].(string)
	role := appearanceRole(tag)
	if role == "" || name == "" {
		return Appearance{}, false
	}
	kind := "monster"
	if role == "item" {
		kind = "item"
	}
	return Appearance{
		Adventure: adv,
		Role:      role,
		Kind:      kind,
		Name:      name,
		Source:    source,
		Chapter:   chapter,
		Location:  loc,
	}, true
}

func appearanceRole(tag string) string {
	switch tag {
	case "creature":
		return "npc"
	case "item":
		return "item"
	default:
		return ""
	}
}

// SourceOf uses a document's parent id as the "source" in search hits.
func (d Document) SourceOf() string {
	return d.ParentID
}
