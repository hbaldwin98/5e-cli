package ask

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/hbaldwin98/5e-cli/internal/edition"
	"github.com/hbaldwin98/5e-cli/internal/store"
)

// Query is a semantic retrieve / ask request.
type Query struct {
	Text    string
	Kind    string
	Sources []string
	Limit   int
	Edition edition.Pref
	SRD     bool
	// Adventure adds one module's prose to the corpus. AdventureOnly narrows
	// the query to that module instead, which answers "what does this module
	// say" but cannot answer a rules question.
	Adventure     string
	AdventureOnly bool
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
	ranked, err := retrieveChunks(ctx, st, cfg, q)
	if err != nil {
		return nil, err
	}
	out := make([]Hit, len(ranked))
	for i, r := range ranked {
		out[i] = r.hit()
	}
	return out, nil
}

// scoredChunk is a ranked chunk with its text still attached. Hit carries only
// a short snippet for display, which is not enough to ground an answer.
type scoredChunk struct {
	chunk
	id    string
	score float64
}

func (s scoredChunk) hit() Hit {
	return Hit{
		Kind:    s.Kind,
		Name:    s.Name,
		Source:  s.Source,
		Score:   s.score,
		Snippet: snippet(s.Text, 160),
	}
}

func retrieveChunks(ctx context.Context, st *store.Store, cfg Config, q Query) ([]scoredChunk, error) {
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
	adventures, restrict, err := adventureScope(st, q)
	if err != nil {
		return nil, err
	}
	vecs, err := loadVectors(cfg.CachePath, vectorScope{
		Kind:       q.Kind,
		Sources:    q.Sources,
		Adventures: adventures,
		Restrict:   restrict,
	})
	if err != nil {
		return nil, err
	}
	cli := newClient(cfg)
	qv, err := cli.Embed(ctx, []string{clipText(q.Text, windowRunes(cfg.EmbedMaxTokens))})
	if err != nil {
		return nil, err
	}
	if len(qv) != 1 {
		return nil, fmt.Errorf("embeddings: expected 1 query vector")
	}
	if err := validateVector(qv[0], "the query"); err != nil {
		return nil, err
	}
	if len(vecs) > 0 && len(qv[0]) != len(vecs[0].vec) {
		return nil, fmt.Errorf("embeddings: query dimension %d does not match the cached corpus dimension %d; rebuild the index or clear the embedding cache", len(qv[0]), len(vecs[0].vec))
	}
	query := l2norm(qv[0])
	srdOK, err := srdAllow(st, q.SRD)
	if err != nil {
		return nil, err
	}
	keep := chunkFilter(q, sourceSet(q.Sources), srdOK, adventures, restrict)
	lex, err := lexicalRank(st, q, keep)
	if err != nil {
		return nil, err
	}
	ranked := rankVectors(vecs, query, q, adventures, keep, cfg.MinScore, lex)
	if err := attachText(cfg.CachePath, ranked); err != nil {
		return nil, err
	}
	return ranked, nil
}

// lexicalRank runs the query through FTS5 across entities and documents and
// returns each matching chunk's position in the combined relevance order,
// best match first. It lets an exact term match rank a chunk the embedding
// scored weakly, and lets rankVectors admit a lexically-justified chunk past
// the score gate that a pure vector match would otherwise need to clear.
func lexicalRank(st *store.Store, q Query, keep func(vector) bool) (map[string]int, error) {
	match := lexicalQuery(q.Text)
	if match == "" {
		return nil, nil
	}
	limit := q.Limit * 8
	entHits, err := st.FTSEntities(match, "", nil, false, limit)
	if err != nil {
		return nil, err
	}
	docHits, err := st.FTSDocuments(match, "", nil, limit)
	if err != nil {
		return nil, err
	}
	hits := append(entHits, docHits...)
	sort.Slice(hits, func(i, j int) bool { return hits[i].Rank < hits[j].Rank })

	rank := make(map[string]int, len(hits))
	for _, h := range hits {
		if !keep((vector{chunk: chunk{Kind: h.Kind, Name: h.Name, Source: h.Source}})) {
			continue
		}
		key := chunkKey(h.Kind, h.Name, h.Source)
		if _, ok := rank[key]; ok {
			continue
		}
		rank[key] = len(rank)
	}
	return rank, nil
}

// lexicalQuery turns a natural-language question into an FTS5 MATCH
// expression. Unlike search.FTSQuery, terms are ORed rather than ANDed: a
// full question rarely reuses every one of its own words verbatim in a
// matching passage, and this query only widens recall for reranking, so a
// partial term overlap is exactly the signal it should catch.
func lexicalQuery(q string) string {
	var parts []string
	for _, f := range strings.Fields(q) {
		var b strings.Builder
		for _, r := range f {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				b.WriteRune(r)
			}
		}
		if b.Len() == 0 {
			continue
		}
		parts = append(parts, b.String()+"*")
	}
	return strings.Join(parts, " OR ")
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

func chunkFilter(q Query, srcOK func(string) bool, srdOK func(kind, name, source string) bool, adventures []string, restrict bool) func(vector) bool {
	advOK := sourceSet(adventures)
	return func(v vector) bool {
		if q.Kind != "" && !strings.EqualFold(v.Kind, q.Kind) {
			return false
		}
		if !srcOK(v.Source) {
			return false
		}
		if store.AdventureDoc(v.Kind) {
			return len(adventures) > 0 && advOK(v.Source) && srdOK(v.Kind, v.Name, v.Source)
		}
		if restrict && !advOK(v.Source) {
			return false
		}
		return srdOK(v.Kind, v.Name, v.Source)
	}
}

// adventureScope decides which adventures the query may see. Naming a module
// adds its prose to the corpus rather than replacing it: a question asked
// inside an adventure is usually still a rules question, and the party's
// spells and the monsters they are fighting live in the rulebooks.
// AdventureOnly is the narrower reading, for "what does this module say".
//
// With no module named, the question is checked for names that occur only
// inside adventures, which lets "who is Gundren Rockseeker" reach LMoP
// without the caller knowing it is an LMoP NPC. Detection is skipped when a
// module is named: that scope is the caller's answer to the same question.
func adventureScope(st *store.Store, q Query) (adventures []string, restrict bool, err error) {
	if q.Adventure != "" {
		return []string{q.Adventure}, q.AdventureOnly, nil
	}
	named, err := namedAdventures(st, q.Text)
	return named, false, err
}

func namedAdventures(st *store.Store, text string) ([]string, error) {
	refs, err := st.AdventureNames()
	if err != nil {
		return nil, err
	}
	haystack := normalizeWords(text)
	if strings.TrimSpace(haystack) == "" {
		return nil, nil
	}
	seen := map[string]bool{}
	var out []string
	for _, ref := range refs {
		// normalizeWords pads both sides, so plain containment is already the
		// whole-word test.
		needle := normalizeWords(ref.Name)
		if strings.TrimSpace(needle) == "" || !strings.Contains(haystack, needle) {
			continue
		}
		if key := strings.ToLower(ref.Source); !seen[key] {
			seen[key] = true
			out = append(out, ref.Source)
		}
	}
	sort.Strings(out)
	return out, nil
}

// normalizeWords lowercases and space-pads a phrase so containment is a
// whole-word test: "sun" must not match "Sunless Citadel".
func normalizeWords(s string) string {
	var b strings.Builder
	b.WriteByte(' ')
	prevSpace := true
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'':
			b.WriteRune(r)
			prevSpace = false
		case !prevSpace:
			b.WriteByte(' ')
			prevSpace = true
		}
	}
	if !prevSpace {
		b.WriteByte(' ')
	}
	return b.String()
}

// rrfK is the Reciprocal Rank Fusion constant: it flattens the influence of
// rank 1 versus rank 2 so one retrieval method cannot dominate just because
// its top hit is a hair better than its second.
// maxPartsPerRecord bounds how many windows of one long record can appear in
// a single result list. A long section is embedded as several overlapping
// parts, and a broad question can genuinely be answered by more than one of
// them (e.g. a class feature described early in a section and expanded on
// several paragraphs later); collapsing to a single best-scoring window would
// silently drop the second passage. The cap keeps one long record from
// filling the whole result list on its own.
const maxPartsPerRecord = 2

func rankVectors(vecs []vector, query []float32, q Query, adventures []string, keep func(vector) bool, minScore float64, lexRank map[string]int) []scoredChunk {
	groups := map[string][]scoredChunk{}
	for _, v := range vecs {
		if !keep(v) {
			continue
		}
		s := scoredChunk{chunk: v.chunk, id: v.id, score: dot(query, v.vec)}
		key := chunkKey(v.Kind, v.Name, v.Source)
		groups[key] = append(groups[key], s)
	}
	var scored []scoredChunk
	for _, g := range groups {
		sort.Slice(g, func(i, j int) bool {
			if g[i].score != g[j].score {
				return g[i].score > g[j].score
			}
			return g[i].Part < g[j].Part
		})
		if len(g) > maxPartsPerRecord {
			g = g[:maxPartsPerRecord]
		}
		scored = append(scored, g...)
	}

	// Two tiers, not one fused score: a chunk that clears the calibrated
	// score gate on its own is ranked purely by that score, so a strong
	// semantic match is never displaced by a coincidental keyword overlap
	// (e.g. a "damage" in a follow-up question matching an unrelated item's
	// flavor text). A chunk below the gate is dropped unless a lexical hit
	// justifies it, and those rescued chunks are appended after the
	// confident tier, ordered by how well they matched lexically.
	var confident, rescued []scoredChunk
	for _, s := range scored {
		key := chunkKey(s.Kind, s.Name, s.Source)
		if s.score >= minScore {
			confident = append(confident, s)
			continue
		}
		if _, lexical := lexRank[key]; lexical {
			rescued = append(rescued, s)
		}
	}
	confident = editionChunks(confident, q.Edition, adventures)
	rescued = editionChunks(rescued, q.Edition, adventures)

	sort.Slice(confident, func(i, j int) bool {
		if confident[i].score != confident[j].score {
			return confident[i].score > confident[j].score
		}
		if confident[i].Name != confident[j].Name {
			return confident[i].Name < confident[j].Name
		}
		return confident[i].Part < confident[j].Part
	})
	sort.Slice(rescued, func(i, j int) bool {
		ri := lexRank[chunkKey(rescued[i].Kind, rescued[i].Name, rescued[i].Source)]
		rj := lexRank[chunkKey(rescued[j].Kind, rescued[j].Name, rescued[j].Source)]
		if ri != rj {
			return ri < rj
		}
		if rescued[i].Name != rescued[j].Name {
			return rescued[i].Name < rescued[j].Name
		}
		return rescued[i].Part < rescued[j].Part
	})

	ranked := append(confident, rescued...)
	if len(ranked) > q.Limit {
		ranked = ranked[:q.Limit]
	}
	return ranked
}

// editionChunks keeps the preferred reprint for each logical record. A source
// without a preferred-edition counterpart stays available, matching get's
// fallback behavior. Adventure sources are content scope, not rules-edition
// reprints, so they are never removed by this preference.
func editionChunks(chunks []scoredChunk, pref edition.Pref, adventures []string) []scoredChunk {
	if pref == edition.All || len(chunks) == 0 {
		return chunks
	}
	advOK := sourceSet(adventures)
	isAdventure := func(c scoredChunk) bool {
		return store.AdventureDoc(c.Kind) || (len(adventures) > 0 && advOK(c.Source))
	}
	sources := make(map[string][]string)
	seen := make(map[string]map[string]bool)
	for _, c := range chunks {
		if isAdventure(c) {
			continue
		}
		key := recordKey(c.Kind, c.Name)
		if seen[key] == nil {
			seen[key] = make(map[string]bool)
		}
		source := strings.ToLower(c.Source)
		if !seen[key][source] {
			seen[key][source] = true
			sources[key] = append(sources[key], c.Source)
		}
	}

	allowed := make(map[string]map[string]bool, len(sources))
	for key, srcs := range sources {
		allowed[key] = make(map[string]bool)
		for _, src := range edition.Prefer(srcs, pref) {
			allowed[key][strings.ToLower(src)] = true
		}
	}

	out := make([]scoredChunk, 0, len(chunks))
	for _, c := range chunks {
		if isAdventure(c) {
			out = append(out, c)
			continue
		}
		if allowed[recordKey(c.Kind, c.Name)][strings.ToLower(c.Source)] {
			out = append(out, c)
		}
	}
	return out
}

func recordKey(kind, name string) string {
	return strings.ToLower(kind) + "\x00" + strings.ToLower(name)
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

// snippet bounds text to n runes for display. Clipping by byte index would
// risk cutting a multi-byte rune in half and producing invalid UTF-8 in the
// middle of the preview.
func snippet(text string, n int) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
	if utf8.RuneCountInString(text) <= n {
		return text
	}
	return strings.TrimSpace(string([]rune(text)[:n])) + "…"
}
