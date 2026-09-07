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
	Text      string
	Kind      string
	Sources   []string
	Limit     int
	SRD       bool
	Adventure string
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
	srdOK, err := srdAllow(st, q.SRD)
	if err != nil {
		return nil, err
	}
	return rankVectors(vecs, query, q, chunkFilter(q, sourceSet(q.Sources), srdOK)), nil
}

func srdAllow(st *store.Store, only bool) (func(kind, name, source string) bool, error) {
	if !only {
		return func(string, string, string) bool { return true }, nil
	}
	names, err := st.Names()
	if err != nil {
		return nil, err
	}
	ok := make(map[string]bool, len(names))
	for _, e := range names {
		if e.SRD {
			ok[chunkKey(e.Kind, e.Name, e.Source)] = true
		}
	}
	return func(kind, name, source string) bool {
		return ok[chunkKey(kind, name, source)]
	}, nil
}

func chunkKey(kind, name, source string) string {
	return strings.ToLower(kind) + "\x00" + strings.ToLower(name) + "\x00" + strings.ToLower(source)
}

func chunkFilter(q Query, srcOK func(string) bool, srdOK func(kind, name, source string) bool) func(vector) bool {
	return func(v vector) bool {
		if q.Kind != "" && !strings.EqualFold(v.Kind, q.Kind) {
			return false
		}
		if !srcOK(v.Source) {
			return false
		}
		if store.AdventureDoc(v.Kind) {
			return q.Adventure != "" && strings.EqualFold(v.Source, q.Adventure) && srdOK(v.Kind, v.Name, v.Source)
		}
		if q.Adventure != "" && !strings.EqualFold(v.Source, q.Adventure) {
			return false
		}
		return srdOK(v.Kind, v.Name, v.Source)
	}
}

func rankVectors(vecs []vector, query []float32, q Query, keep func(vector) bool) []Hit {
	type scored struct {
		v     vector
		score float64
	}
	var ranked []scored
	for _, v := range vecs {
		if !keep(v) {
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
	return out
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
