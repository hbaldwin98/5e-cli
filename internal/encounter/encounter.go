package encounter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/hbaldwin98/5e-cli/internal/edition"
	"github.com/hbaldwin98/5e-cli/internal/search"
	"github.com/hbaldwin98/5e-cli/internal/store"
)

// Query describes a filtered monster lookup.
type Query struct {
	Text    string
	CR      string
	Type    string
	Size    string
	Sources []string
	Edition edition.Pref
	SRD     bool
	Limit   int
}

// Hit is a compact encounter result suitable for humans and agents.
type Hit struct {
	Kind    string  `json:"kind"`
	Name    string  `json:"name"`
	Source  string  `json:"source"`
	Page    int     `json:"page"`
	Score   float64 `json:"score"`
	CR      string  `json:"cr,omitempty"`
	Type    string  `json:"type,omitempty"`
	Size    string  `json:"size,omitempty"`
	SRD     bool    `json:"srd"`
	Snippet string  `json:"snippet,omitempty"`
}

// Search returns monsters matching all metadata filters before applying Limit.
func Search(st *store.Store, q Query) ([]Hit, error) {
	entities, err := st.Get("monster", "", "")
	if err != nil {
		return nil, err
	}
	entities = filterEntities(entities, q)
	if q.Limit <= 0 {
		q.Limit = 10
	}

	hits := make([]Hit, 0, len(entities))
	for _, entity := range entities {
		hit, ok := makeHit(entity, q.Text)
		if ok {
			hits = append(hits, hit)
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		if hits[i].Name != hits[j].Name {
			return strings.ToLower(hits[i].Name) < strings.ToLower(hits[j].Name)
		}
		return strings.ToLower(hits[i].Source) < strings.ToLower(hits[j].Source)
	})
	if len(hits) > q.Limit {
		hits = hits[:q.Limit]
	}
	return hits, nil
}

func filterEntities(entities []store.Entity, q Query) []store.Entity {
	sources := sourceSet(q.Sources)
	filtered := make([]store.Entity, 0, len(entities))
	for _, entity := range entities {
		if len(sources) > 0 && !sources[strings.ToLower(entity.Source)] {
			continue
		}
		if q.SRD && !entity.SRD {
			continue
		}
		if q.Edition != edition.All && !edition.Match(entity.Source, q.Edition) {
			continue
		}
		obj, err := decode(entity.JSON)
		if err != nil {
			continue
		}
		if q.CR != "" && !strings.EqualFold(challenge(obj), q.CR) {
			continue
		}
		if q.Type != "" && !strings.EqualFold(creatureType(obj), q.Type) {
			continue
		}
		if q.Size != "" && !hasSize(obj, q.Size) {
			continue
		}
		filtered = append(filtered, entity)
	}
	return filtered
}

func makeHit(entity store.Entity, query string) (Hit, bool) {
	obj, err := decode(entity.JSON)
	if err != nil {
		return Hit{}, false
	}
	score := search.NameScore(query, entity.Name)
	if strings.TrimSpace(query) == "" {
		score = 0
	} else if score == 0 {
		if !containsAll(entity.Text, query) {
			return Hit{}, false
		}
		score = 0.25
	}
	return Hit{
		Kind:    entity.Kind,
		Name:    entity.Name,
		Source:  entity.Source,
		Page:    entity.Page,
		Score:   score,
		CR:      challenge(obj),
		Type:    creatureType(obj),
		Size:    sizes(obj),
		SRD:     entity.SRD,
		Snippet: snippet(entity.Text),
	}, true
}

func decode(raw json.RawMessage) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return nil, err
	}
	return obj, nil
}

func challenge(obj map[string]any) string {
	switch value := obj["cr"].(type) {
	case string:
		return value
	case json.Number:
		return value.String()
	case map[string]any:
		return scalar(value["cr"])
	default:
		return ""
	}
}

func creatureType(obj map[string]any) string {
	switch value := obj["type"].(type) {
	case string:
		return value
	case map[string]any:
		return stringValue(value["type"])
	default:
		return ""
	}
}

func hasSize(obj map[string]any, wanted string) bool {
	wanted = strings.ToLower(strings.TrimSpace(wanted))
	switch value := obj["size"].(type) {
	case string:
		return sizeMatches(value, wanted)
	case []any:
		for _, item := range value {
			if sizeMatches(stringValue(item), wanted) {
				return true
			}
		}
	}
	return false
}

func sizeMatches(value, wanted string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	names := map[string]string{
		"t": "tiny", "s": "small", "m": "medium", "l": "large", "h": "huge", "g": "gargantuan",
	}
	if names[value] != "" {
		return value == wanted || names[value] == wanted
	}
	return value == wanted
}

func sizes(obj map[string]any) string {
	var values []string
	appendSize := func(value string) {
		value = strings.ToLower(strings.TrimSpace(value))
		names := map[string]string{"t": "Tiny", "s": "Small", "m": "Medium", "l": "Large", "h": "Huge", "g": "Gargantuan"}
		if name := names[value]; name != "" {
			value = name
		}
		if value != "" {
			values = append(values, value)
		}
	}
	switch value := obj["size"].(type) {
	case string:
		appendSize(value)
	case []any:
		for _, item := range value {
			appendSize(stringValue(item))
		}
	}
	return strings.Join(values, ", ")
}

func containsAll(text, query string) bool {
	text = strings.ToLower(text)
	for _, word := range strings.Fields(strings.ToLower(query)) {
		if !strings.Contains(text, word) {
			return false
		}
	}
	return true
}

func snippet(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 160 {
		return text[:160] + "..."
	}
	return text
}

func sourceSet(sources []string) map[string]bool {
	set := map[string]bool{}
	for _, source := range sources {
		for _, part := range strings.Split(source, ",") {
			if part = strings.TrimSpace(part); part != "" {
				set[strings.ToLower(part)] = true
			}
		}
	}
	return set
}

func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func scalar(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case json.Number:
		return value.String()
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	default:
		return fmt.Sprint(value)
	}
}
