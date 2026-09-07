package search

import (
	"sort"
	"strings"

	"github.com/hbaldwin98/5e-cli/internal/edition"
	"github.com/hbaldwin98/5e-cli/internal/store"
)

// Hit is a ranked search result. IDs match `5e get`.
type Hit struct {
	Kind    string  `json:"kind"`
	Name    string  `json:"name"`
	Source  string  `json:"source"`
	Score   float64 `json:"score"`
	Snippet string  `json:"snippet,omitempty"`
}

// Query is a search request.
type Query struct {
	Text      string
	Kind      string
	Sources   []string
	Limit     int
	Edition   edition.Pref
	SRD       bool
	Adventure string
}

// Search merges fuzzy name hits with FTS5 body hits.
func Search(st *store.Store, q Query) ([]Hit, error) {
	if q.Limit <= 0 {
		q.Limit = 10
	}
	merged := map[string]*Hit{}
	if err := addNameHits(st, q, merged); err != nil {
		return nil, err
	}
	if err := addFTSHits(st, q, merged); err != nil {
		return nil, err
	}
	return rankHits(merged, q), nil
}

func addNameHits(st *store.Store, q Query, merged map[string]*Hit) error {
	names, err := st.Names()
	if err != nil {
		return err
	}
	seen, err := appearanceAllow(st, q)
	if err != nil {
		return err
	}
	prose := Prose(q.Text)
	srcOK := sourceSet(q.Sources)
	for _, e := range names {
		if !keepNameHit(q, e, srcOK, seen) {
			continue
		}
		ns := NameScore(q.Text, e.Name)
		if ns == 0 {
			continue
		}
		score := ns
		if !prose {
			score += 1
		}
		put(merged, Hit{Kind: e.Kind, Name: e.Name, Source: e.Source, Score: score, Snippet: snippet(e.Text, 120)})
	}
	return nil
}

func addFTSHits(st *store.Store, q Query, merged map[string]*Hit) error {
	fts := FTSQuery(q.Text)
	if fts == "" {
		return nil
	}
	limit := q.Limit * 5
	entHits, err := st.FTSEntities(fts, q.Kind, entitySources(q), q.SRD, limit)
	if err != nil {
		return err
	}
	var docHits []store.Hit
	if !q.SRD {
		docKind, docSources := documentFilter(q)
		if docKind != "-" {
			docHits, err = st.FTSDocuments(fts, docKind, docSources, limit)
			if err != nil {
				return err
			}
		}
	}
	for _, h := range append(entHits, docHits...) {
		put(merged, Hit{
			Kind:    h.Kind,
			Name:    h.Name,
			Source:  h.Source,
			Score:   ftsScore(h.Rank),
			Snippet: h.Snippet,
		})
	}
	return nil
}

func rankHits(merged map[string]*Hit, q Query) []Hit {
	out := make([]Hit, 0, len(merged))
	for _, h := range merged {
		out = append(out, *h)
	}
	sort.Slice(out, func(i, j int) bool {
		return lessHit(out[i], out[j], q.Edition)
	})
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out
}

func lessHit(a, b Hit, ed edition.Pref) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	aEd, bEd := edition.Match(a.Source, ed), edition.Match(b.Source, ed)
	if aEd != bEd {
		return aEd
	}
	if len(a.Name) != len(b.Name) {
		return len(a.Name) < len(b.Name)
	}
	if a.Name != b.Name {
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	}
	return a.Source < b.Source
}

func put(m map[string]*Hit, h Hit) {
	k := h.Kind + "\x00" + strings.ToLower(h.Name) + "\x00" + strings.ToLower(h.Source)
	if prev, ok := m[k]; ok {
		if h.Score > prev.Score {
			prev.Score = h.Score
		}
		if prev.Snippet == "" {
			prev.Snippet = h.Snippet
		}
		return
	}
	cp := h
	m[k] = &cp
}

func ftsScore(rank float64) float64 {
	if rank < 0 {
		rank = -rank
	}
	return 0.25 + 1.0/(1.0+rank)
}

func entitySources(q Query) []string {
	if q.Adventure != "" {
		return []string{q.Adventure}
	}
	return q.Sources
}

func documentFilter(q Query) (kind string, sources []string) {
	if q.Adventure != "" {
		switch q.Kind {
		case "location":
			return "", []string{q.Adventure}
		case "monster", "item":
			return "-", nil
		default:
			return q.Kind, []string{q.Adventure}
		}
	}
	if q.Kind == "" {
		return "bookSection", q.Sources
	}
	if store.AdventureDoc(q.Kind) || q.Kind == "bookSection" {
		return q.Kind, q.Sources
	}
	return "-", nil
}

func appearanceAllow(st *store.Store, q Query) (map[string]bool, error) {
	if q.Adventure == "" {
		return nil, nil
	}
	role := ""
	switch q.Kind {
	case "monster":
		role = "npc"
	case "item":
		role = "item"
	case "location", "adventureSection", "adventureLocation":
		return map[string]bool{}, nil
	}
	return st.AppearanceSet(q.Adventure, role)
}

func keepNameHit(q Query, e store.Entity, srcOK func(string) bool, seen map[string]bool) bool {
	if q.Kind != "" && e.Kind != q.Kind {
		return false
	}
	if q.SRD && !e.SRD {
		return false
	}
	if !srcOK(e.Source) {
		return false
	}
	if q.Adventure == "" {
		return true
	}
	if strings.EqualFold(e.Source, q.Adventure) {
		return true
	}
	return seen[store.AppearanceKey(e.Kind, e.Name, e.Source)]
}

func sourceSet(sources []string) func(string) bool {
	if len(sources) == 0 {
		return func(string) bool { return true }
	}
	s := map[string]bool{}
	for _, src := range sources {
		s[strings.ToLower(src)] = true
	}
	return func(src string) bool { return s[strings.ToLower(src)] }
}

func snippet(text string, n int) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
	if len(text) <= n {
		return text
	}
	return strings.TrimSpace(text[:n]) + "…"
}
