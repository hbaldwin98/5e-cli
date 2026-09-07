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
		if entries, ok := t["entries"]; ok {
			collectSections(kind, parentID, entries, out)
		}
		if data, ok := t["data"]; ok {
			collectSections(kind, parentID, data, out)
		}
	}
}

// SourceOf uses a document's parent id as the "source" in search hits.
func (d Document) SourceOf() string {
	return d.ParentID
}
