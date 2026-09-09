package search

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hbaldwin98/5e-cli/internal/store"
)

// Suggestion is one near-miss candidate for a failed name lookup: the
// entity it points at plus how well it scored against the query, so a
// caller can decide its own cutoff or phrasing.
type Suggestion struct {
	Entity store.Entity
	Score  float64
}

// Suggest finds the best NameScore matches for name within kind (every kind
// if kind is ""), for a "no such entity — did you mean...?" prompt after an
// exact lookup misses. It reuses the same fuzzy/prefix scoring the search
// command already uses for names, so a typo or a slightly-off spelling
// ("Fireballl", "acid arow") surfaces the entity the user actually meant
// instead of a bare "not found".
func Suggest(st *store.Store, kind, name string, limit int) ([]Suggestion, error) {
	if limit <= 0 {
		limit = 5
	}
	names, err := st.FilteredNames(store.NameFilter{Kind: kind})
	if err != nil {
		return nil, err
	}
	out := make([]Suggestion, 0, len(names))
	for _, e := range names {
		score := NameScore(name, e.Name)
		if score <= 0 {
			continue
		}
		out = append(out, Suggestion{Entity: e, Score: score})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Entity.Name < out[j].Entity.Name
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// SuggestionNames is Suggest's entity names deduplicated (several sources
// can share a name), in rank order — the shape a "did you mean" message
// actually wants to print, since repeating "Fireball" once per source
// reprint would just be noise there.
func SuggestionNames(suggestions []Suggestion) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range suggestions {
		if seen[s.Entity.Name] {
			continue
		}
		seen[s.Entity.Name] = true
		out = append(out, s.Entity.Name)
	}
	return out
}

// FilterByQuery narrows ents to those matching query, the way `list`'s
// --query and the chat list tool's query argument do: a plain
// case-insensitive substring match first, and — only when that finds
// nothing — a fuzzy fallback (NameScore, best match first) so a slightly
// wrong query ("Firebal", "acid arow") still returns something instead of
// an empty list. An empty query returns ents unchanged.
func FilterByQuery(ents []store.Entity, query string) []store.Entity {
	if query == "" {
		return ents
	}
	q := strings.ToLower(query)
	var exact []store.Entity
	for _, e := range ents {
		if strings.Contains(strings.ToLower(e.Name), q) {
			exact = append(exact, e)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	type scored struct {
		e     store.Entity
		score float64
	}
	var fuzzy []scored
	for _, e := range ents {
		if s := NameScore(query, e.Name); s > 0 {
			fuzzy = append(fuzzy, scored{e, s})
		}
	}
	sort.Slice(fuzzy, func(i, j int) bool {
		if fuzzy[i].score != fuzzy[j].score {
			return fuzzy[i].score > fuzzy[j].score
		}
		return fuzzy[i].e.Name < fuzzy[j].e.Name
	})
	out := make([]store.Entity, len(fuzzy))
	for i, s := range fuzzy {
		out[i] = s.e
	}
	return out
}

// NotFoundError builds the "no <kind> named <name>" error every `get`-style
// lookup (CLI, chat tool, MCP tool) raises on a miss, appending a "did you
// mean" suggestion list from Suggest when it finds anything worth
// mentioning — a typo'd or slightly-off name (a missing letter, a plural,
// close-but-wrong wording) resolves to the entity the caller meant instead
// of a bare "not found" that gives no path forward. st may be nil (a lookup
// path with no store handy for suggestions), in which case this is just the
// bare message.
func NotFoundError(st *store.Store, kind, name string) error {
	base := fmt.Errorf("no %s named %q", kind, name)
	if st == nil {
		return base
	}
	suggestions, err := Suggest(st, kind, name, 5)
	if err != nil || len(suggestions) == 0 {
		return base
	}
	names := SuggestionNames(suggestions)
	if len(names) == 1 {
		return fmt.Errorf("%w — did you mean %q?", base, names[0])
	}
	return fmt.Errorf("%w — did you mean one of: %s?", base, strings.Join(names, ", "))
}
