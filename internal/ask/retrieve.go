package ask

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hbaldwin98/5e-cli/internal/store"
)

// Query is a semantic retrieve / ask request.
type Query struct {
	Text    string
	Kind    string
	Sources []string
	Limit   int
}

// Hit is a ranked chunk. IDs match `5e get` (documents use section as name).
type Hit struct {
	Kind    string  `json:"kind"`
	Name    string  `json:"name"`
	Source  string  `json:"source"`
	Score   float64 `json:"score"`
	Snippet string  `json:"snippet,omitempty"`
}

// Retrieve embeds the query and returns the nearest corpus chunks.
func Retrieve(ctx context.Context, st *store.Store, cfg Config, q Query) ([]Hit, error) {
	cfg = cfg.withDefaults()
	if strings.TrimSpace(q.Text) == "" {
		return nil, fmt.Errorf("empty query")
	}
	if q.Limit <= 0 {
		q.Limit = 8
	}
	if err := ensureCache(ctx, st, cfg); err != nil {
		return nil, err
	}
	vecs, err := loadVectors(cfg.CachePath)
	if err != nil {
		return nil, err
	}
	cli := newClient(cfg)
	qv, err := cli.Embed(ctx, []string{clipText(q.Text)})
	if err != nil {
		return nil, err
	}
	if len(qv) != 1 {
		return nil, fmt.Errorf("embeddings: expected 1 query vector")
	}
	query := l2norm(qv[0])
	srcOK := sourceSet(q.Sources)
	type scored struct {
		v     vector
		score float64
	}
	var ranked []scored
	for _, v := range vecs {
		if q.Kind != "" && !strings.EqualFold(v.Kind, q.Kind) {
			continue
		}
		if !srcOK(v.Source) {
			continue
		}
		ranked = append(ranked, scored{v: v, score: dot(query, v.vec)})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].v.Name < ranked[j].v.Name
	})
	if len(ranked) > q.Limit {
		ranked = ranked[:q.Limit]
	}
	out := make([]Hit, len(ranked))
	for i, r := range ranked {
		out[i] = Hit{
			Kind:    r.v.Kind,
			Name:    r.v.Name,
			Source:  r.v.Source,
			Score:   r.score,
			Snippet: snippet(r.v.Text, 160),
		}
	}
	return out, nil
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
