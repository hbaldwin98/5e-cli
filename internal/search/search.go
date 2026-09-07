package search

import (
	"sort"
	"strings"

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
	Text    string
	Kind    string
	Sources []string
	Limit   int
}

// Search merges fuzzy name hits with FTS5 body hits.
func Search(st *store.Store, q Query) ([]Hit, error) {
	if q.Limit <= 0 {
		q.Limit = 10
	}
	merged := map[string]*Hit{}
	prose := Prose(q.Text)

	names, err := st.Names()
	if err != nil {
		return nil, err
	}
	srcOK := sourceSet(q.Sources)
	for _, e := range names {
		if q.Kind != "" && e.Kind != q.Kind {
			continue
		}
		if !srcOK(e.Source) {
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

	fts := FTSQuery(q.Text)
	if fts != "" {
		limit := q.Limit * 5
		entHits, err := st.FTSEntities(fts, q.Kind, q.Sources, limit)
		if err != nil {
			return nil, err
		}
		docHits, err := st.FTSDocuments(fts, q.Kind, q.Sources, limit)
		if err != nil {
			return nil, err
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
	}

	out := make([]Hit, 0, len(merged))
	for _, h := range merged {
		out = append(out, *h)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].Name != out[j].Name {
			return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
		}
		return out[i].Source < out[j].Source
	})
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
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
