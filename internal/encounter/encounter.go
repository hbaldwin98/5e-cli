package encounter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/hbaldwin98/5e-cli/internal/edition"
	"github.com/hbaldwin98/5e-cli/internal/search"
	"github.com/hbaldwin98/5e-cli/internal/statblock"
	"github.com/hbaldwin98/5e-cli/internal/store"
)

// Query describes a filtered monster lookup.
type Query struct {
	Text        string
	CR          string
	Type        string
	Size        string
	Environment string
	Sources     []string
	Edition     edition.Pref
	SRD         bool
	Limit       int
}

// Hit is a compact encounter result suitable for humans and agents. AC, HP,
// XP, and Speed are the numbers a DM actually needs to place a monster in an
// encounter without a second `get` round trip for the full stat block; they
// are omitted (zero/empty) rather than guessed when the source JSON does not
// carry them in a shape this package understands.
type Hit struct {
	Kind        string  `json:"kind"`
	Name        string  `json:"name"`
	Source      string  `json:"source"`
	Page        int     `json:"page"`
	Score       float64 `json:"score"`
	CR          string  `json:"cr,omitempty"`
	Type        string  `json:"type,omitempty"`
	Size        string  `json:"size,omitempty"`
	Environment string  `json:"environment,omitempty"`
	AC          int     `json:"ac,omitempty"`
	HP          int     `json:"hp,omitempty"`
	XP          int     `json:"xp,omitempty"`
	Speed       string  `json:"speed,omitempty"`
	SRD         bool    `json:"srd"`
	Snippet     string  `json:"snippet,omitempty"`
}

// Search returns monsters matching all metadata filters before applying Limit.
func Search(st *store.Store, q Query) ([]Hit, error) {
	// GetBare skips the per-row edge lookup: this scans every monster in the
	// index and never reads their edges.
	entities, err := st.GetBare("monster", "", "")
	if err != nil {
		return nil, err
	}
	candidates := filterEntities(entities, q)
	if q.Limit <= 0 {
		q.Limit = 10
	}

	ranked := make([]rankedHit, 0, len(candidates))
	for _, candidate := range candidates {
		hit, ok := makeHit(candidate, q.Text)
		if ok {
			ranked = append(ranked, rankedHit{hit: hit, exactEnv: candidate.exactEnv})
		}
	}
	sort.Slice(ranked, func(i, j int) bool {
		// A creature actually tagged for the requested environment outranks
		// one tagged "any". Both belong in the answer — a bandit really can
		// turn up in a swamp — but 65 wildcard NPCs sorting above the fey
		// would bury what the question was about.
		if ranked[i].exactEnv != ranked[j].exactEnv {
			return ranked[i].exactEnv
		}
		if ranked[i].hit.Score != ranked[j].hit.Score {
			return ranked[i].hit.Score > ranked[j].hit.Score
		}
		if ranked[i].hit.Name != ranked[j].hit.Name {
			return strings.ToLower(ranked[i].hit.Name) < strings.ToLower(ranked[j].hit.Name)
		}
		return strings.ToLower(ranked[i].hit.Source) < strings.ToLower(ranked[j].hit.Source)
	})
	if len(ranked) > q.Limit {
		ranked = ranked[:q.Limit]
	}
	hits := make([]Hit, len(ranked))
	for i, r := range ranked {
		hits[i] = r.hit
	}
	return hits, nil
}

// candidate pairs a stored monster with its decoded JSON so the filter pass
// and the hit pass share one unmarshal per row. exactEnv records whether it
// matched the environment filter by its own tag rather than by the "any"
// wildcard, which only affects ordering.
type candidate struct {
	entity   store.Entity
	obj      map[string]any
	exactEnv bool
}

// rankedHit carries a hit alongside the ordering key that does not belong in
// the hit itself, since exactEnv is meaningless without the query.
type rankedHit struct {
	hit      Hit
	exactEnv bool
}

func filterEntities(entities []store.Entity, q Query) []candidate {
	sources := sourceSet(q.Sources)
	filtered := make([]candidate, 0, len(entities))
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
		if q.Text != "" && !matchesText(entity, q.Text) {
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
		exactEnv := false
		if q.Environment != "" {
			var matched bool
			matched, exactEnv = matchEnvironment(obj, q.Environment)
			if !matched {
				continue
			}
		}
		filtered = append(filtered, candidate{entity: entity, obj: obj, exactEnv: exactEnv})
	}
	return filtered
}

// matchesText is the cheap string-only half of scoring, applied before the
// JSON decode so non-matching monsters are never unmarshalled.
func matchesText(entity store.Entity, query string) bool {
	return search.NameScore(query, entity.Name) > 0 || containsAll(entity.Text, query)
}

func makeHit(c candidate, query string) (Hit, bool) {
	entity, obj := c.entity, c.obj
	score := search.NameScore(query, entity.Name)
	if strings.TrimSpace(query) == "" {
		score = 0
	} else if score == 0 {
		if !containsAll(entity.Text, query) {
			return Hit{}, false
		}
		score = 0.25
	}
	cr := challenge(obj)
	xp, _ := statblock.XPForCR(cr)
	return Hit{
		Kind:        entity.Kind,
		Name:        entity.Name,
		Source:      entity.Source,
		Page:        entity.Page,
		Score:       score,
		CR:          cr,
		Type:        creatureType(obj),
		Size:        sizes(obj),
		Environment: strings.Join(environments(obj), ", "),
		AC:          armorClassValue(obj),
		HP:          hitPointsValue(obj),
		XP:          xp,
		Speed:       speedSummary(obj),
		SRD:         entity.SRD,
		Snippet:     snippet(entity.Text),
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

// hasEnvironment reports whether a monster is tagged for wanted. 5etools
// writes these as lowercase tags ("forest", "underdark") with planes as a
// compound "planar, feywild", so a query of "feywild" matches the plane
// without the caller having to know the prefix.
//
// A monster tagged "any" is at home everywhere and matches every query — a
// swarm of rats belongs in the swamp list as much as the urban one.
func matchEnvironment(obj map[string]any, wanted string) (matched, exact bool) {
	wanted = strings.ToLower(strings.TrimSpace(wanted))
	if wanted == "" {
		return true, false
	}
	for _, tag := range environments(obj) {
		tag = strings.ToLower(tag)
		if tag == wanted {
			return true, true
		}
		// "planar, feywild" answers to "feywild" and to "planar".
		for _, part := range strings.Split(tag, ",") {
			if strings.TrimSpace(part) == wanted {
				return true, true
			}
		}
		if tag == "any" {
			matched = true
		}
	}
	return matched, false
}

func environments(obj map[string]any) []string {
	list, _ := obj["environment"].([]any)
	out := make([]string, 0, len(list))
	for _, item := range list {
		if s := stringValue(item); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// sizeMatches normalizes both sides, so data written as "S" or "Small" both
// answer to a --size of "s" or "small".
func sizeMatches(value, wanted string) bool {
	return sizeName(value) == sizeName(wanted)
}

var sizeNames = map[string]string{
	"t": "tiny", "s": "small", "m": "medium", "l": "large", "h": "huge", "g": "gargantuan",
}

func sizeName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if name := sizeNames[value]; name != "" {
		return name
	}
	return value
}

func sizes(obj map[string]any) string {
	var values []string
	appendSize := func(value string) {
		if name := sizeName(value); name != "" {
			values = append(values, strings.ToUpper(name[:1])+name[1:])
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

// snippet bounds text to 160 runes for display. Clipping by byte index would
// risk cutting a multi-byte rune in half and producing invalid UTF-8 in the
// middle of the preview.
func snippet(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) > 160 {
		return string([]rune(text)[:160]) + "..."
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

// armorClassValue reads a monster's AC as an int for Hit: either a bare
// number, or the first entry of the array 5etools uses when armor sources
// (natural armor, a shield) are listed alongside the number.
func armorClassValue(obj map[string]any) int {
	switch value := obj["ac"].(type) {
	case json.Number:
		n, _ := strconv.Atoi(value.String())
		return n
	case []any:
		if len(value) == 0 {
			return 0
		}
		switch first := value[0].(type) {
		case json.Number:
			n, _ := strconv.Atoi(first.String())
			return n
		case map[string]any:
			if ac, ok := first["ac"].(json.Number); ok {
				n, _ := strconv.Atoi(ac.String())
				return n
			}
		}
	}
	return 0
}

func hitPointsValue(obj map[string]any) int {
	hp, _ := obj["hp"].(map[string]any)
	if average, ok := hp["average"].(json.Number); ok {
		n, _ := strconv.Atoi(average.String())
		return n
	}
	return 0
}

// speedSummary reads a monster's speed the same way `5e get` does, since a
// DM placing a monster needs to know it flies or burrows, not only that it
// walks.
func speedSummary(obj map[string]any) string {
	switch value := obj["speed"].(type) {
	case json.Number:
		return value.String() + " ft."
	case map[string]any:
		order := []string{"walk", "burrow", "climb", "fly", "swim"}
		var out []string
		for _, kind := range order {
			label := ""
			if kind != "walk" {
				label = kind + " "
			}
			switch speedValue := value[kind].(type) {
			case json.Number:
				out = append(out, label+speedValue.String()+" ft.")
			case map[string]any:
				if number, ok := speedValue["number"].(json.Number); ok {
					out = append(out, label+number.String()+" ft.")
				}
			}
		}
		return strings.Join(out, ", ")
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
