package ask

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/hbaldwin98/5e-cli/internal/store"

	_ "modernc.org/sqlite"
)

const (
	cacheSchema = `
PRAGMA journal_mode=WAL;

CREATE TABLE meta (
  corpus_sha  TEXT NOT NULL,
  base_url    TEXT NOT NULL,
  embed_model TEXT NOT NULL,
  dim         INTEGER NOT NULL,
  max_tokens  INTEGER NOT NULL,
  built_at    TEXT NOT NULL
);

CREATE TABLE vectors (
  id        TEXT PRIMARY KEY,
  kind      TEXT NOT NULL,
  name      TEXT NOT NULL,
  source    TEXT NOT NULL,
  text      TEXT NOT NULL,
  embedding BLOB NOT NULL
);
`
	// embedBatch bounds inputs per request; maxBatchTokens bounds their
	// combined size, since the API caps a request's total tokens too.
	embedBatch     = 64
	maxBatchTokens = 250000

	// minCharsPerToken is a deliberately pessimistic tokenizer ratio. Ordinary
	// English runs near 4 characters per token; rules text with dice notation
	// and proper nouns runs denser. Sizing windows at 1.9 keeps a window under
	// the model's limit without shipping a tokenizer.
	minCharsPerToken = 1.9

	// chunkOverlapRunes repeats a little text across window boundaries so a
	// passage split down the middle is still retrievable from either side.
	// splitText clamps it to a fraction of the window.
	chunkOverlapRunes = 400
)

type chunk struct {
	Kind   string
	Name   string
	Source string
	Part   int
	Text   string
}

func (c chunk) id() string {
	return fmt.Sprintf("%s\x1f%s\x1f%s\x1f%d", c.Kind, c.Name, c.Source, c.Part)
}

// windowRunes is the largest chunk that fits the model's per-input token
// limit under the pessimistic ratio above.
func windowRunes(maxTokens int) int {
	return max(int(float64(maxTokens)*minCharsPerToken), 64)
}

// estimateTokens is the same pessimistic ratio read in the other direction,
// used to keep a batch under the per-request total.
func estimateTokens(s string) int {
	return int(float64(utf8.RuneCountInString(s))/minCharsPerToken) + 1
}

type vector struct {
	chunk
	vec []float32
}

func ensureCache(ctx context.Context, st *store.Store, cfg Config) error {
	cfg = cfg.withDefaults()
	if cfg.CachePath == "" {
		return fmt.Errorf("embedding cache path is empty")
	}
	meta, err := st.Meta()
	if err != nil {
		return err
	}
	if ok, err := cacheFresh(cfg.CachePath, meta.SHA, cfg.BaseURL, cfg.EmbedModel, cfg.EmbedMaxTokens); err != nil {
		return err
	} else if ok {
		return nil
	}
	chunks, err := corpus(st, windowRunes(cfg.EmbedMaxTokens))
	if err != nil {
		return err
	}
	if len(chunks) == 0 {
		return fmt.Errorf("index has no embeddable text; run `5e ingest`")
	}
	cli := newClient(cfg)
	return writeCache(ctx, cli, cfg, meta.SHA, chunks)
}

func cacheFresh(path, sha, baseURL, model string, maxTokens int) (bool, error) {
	st, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if st.Size() == 0 {
		return false, nil
	}
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return false, err
	}
	defer db.Close()
	var gotSHA, gotURL, gotModel string
	var gotMaxTokens, n int
	// A cache written before max_tokens existed fails this scan, which is the
	// wanted answer: it was chunked under different limits and must be rebuilt.
	err = db.QueryRow(`SELECT corpus_sha, base_url, embed_model, max_tokens FROM meta LIMIT 1`).
		Scan(&gotSHA, &gotURL, &gotModel, &gotMaxTokens)
	if err != nil {
		return false, nil
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM vectors`).Scan(&n); err != nil || n == 0 {
		return false, nil
	}
	return gotSHA == sha && gotURL == baseURL && gotModel == model && gotMaxTokens == maxTokens, nil
}

func writeCache(ctx context.Context, cli *client, cfg Config, sha string, chunks []chunk) error {
	if err := os.MkdirAll(filepath.Dir(cfg.CachePath), 0o755); err != nil {
		return err
	}
	tmp := cfg.CachePath + ".tmp"
	_ = os.Remove(tmp)
	db, err := sql.Open("sqlite", dsn(tmp))
	if err != nil {
		return err
	}
	defer func() {
		_ = db.Close()
		_ = os.Remove(tmp)
	}()
	if _, err := db.Exec(cacheSchema); err != nil {
		return fmt.Errorf("embedding schema: %w", err)
	}

	var dim int
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	ins, err := tx.Prepare(`INSERT INTO vectors (id, kind, name, source, text, embedding) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer ins.Close()

	for i := 0; i < len(chunks); {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := batchEnd(chunks, i)
		batch := chunks[i:end]
		i = end
		inputs := make([]string, len(batch))
		for j, ch := range batch {
			inputs[j] = ch.Text
		}
		if cfg.Progress != nil {
			fmt.Fprintf(cfg.Progress, "embedding %d/%d\n", end, len(chunks))
		}
		vecs, err := cli.Embed(ctx, inputs)
		if err != nil {
			return err
		}
		for j, ch := range batch {
			v := l2norm(vecs[j])
			if dim == 0 {
				dim = len(v)
			} else if len(v) != dim {
				return fmt.Errorf("embeddings: dimension %d then %d", dim, len(v))
			}
			if _, err := ins.Exec(ch.id(), ch.Kind, ch.Name, ch.Source, ch.Text, encodeVec(v)); err != nil {
				return err
			}
		}
	}
	if dim == 0 {
		return fmt.Errorf("embeddings: no vectors returned")
	}
	if _, err := tx.Exec(
		`INSERT INTO meta (corpus_sha, base_url, embed_model, dim, max_tokens, built_at) VALUES (?, ?, ?, ?, ?, ?)`,
		sha, cfg.BaseURL, cfg.EmbedModel, dim, cfg.EmbedMaxTokens, store.Now(),
	); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := db.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, cfg.CachePath)
}

func loadVectors(path string) ([]vector, error) {
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT kind, name, source, text, embedding FROM vectors`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []vector
	for rows.Next() {
		var v vector
		var blob []byte
		if err := rows.Scan(&v.Kind, &v.Name, &v.Source, &v.Text, &blob); err != nil {
			return nil, err
		}
		v.vec, err = decodeVec(blob)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// batchEnd grows a batch until it hits the request's input count or its
// combined token budget, always taking at least one chunk.
func batchEnd(chunks []chunk, start int) int {
	tokens := 0
	for i := start; i < len(chunks) && i-start < embedBatch; i++ {
		next := tokens + estimateTokens(chunks[i].Text)
		if i > start && next > maxBatchTokens {
			return i
		}
		tokens = next
	}
	end := start + embedBatch
	if end > len(chunks) {
		end = len(chunks)
	}
	return end
}

func corpus(st *store.Store, window int) ([]chunk, error) {
	ents, err := st.Names()
	if err != nil {
		return nil, err
	}
	docs, err := st.Documents()
	if err != nil {
		return nil, err
	}
	out := make([]chunk, 0, len(ents)+len(docs))
	for _, e := range ents {
		out = appendChunks(out, e.Kind, e.Name, e.Source, e.Text, window)
	}
	for _, d := range docs {
		out = appendChunks(out, d.Kind, d.Section, d.ParentID, d.Text, window)
	}
	return out, nil
}

// appendChunks splits one record's text across as many windows as it needs.
// Long book and adventure sections used to be truncated to a single window,
// which both risked the model's input limit and dropped most of the section.
func appendChunks(out []chunk, kind, name, source, text string, window int) []chunk {
	for i, part := range splitText(text, window) {
		if strings.TrimSpace(part) == "" {
			continue
		}
		out = append(out, chunk{Kind: kind, Name: name, Source: source, Part: i, Text: part})
	}
	return out
}

// splitText cuts s into overlapping windows of at most window runes, breaking
// on a line or space near the edge so a window rarely ends mid-word.
func splitText(s string, window int) []string {
	if window <= 0 || utf8.RuneCountInString(s) <= window {
		return []string{s}
	}
	runes := []rune(s)
	// Keep the overlap a fraction of the window. A small window configured
	// through FIVE_E_EMBED_MAX_TOKENS would otherwise overlap by more than it
	// advances and never reach the end of the text.
	overlap := min(chunkOverlapRunes, window/4)
	var out []string
	for start := 0; start < len(runes); {
		end := start + window
		if end >= len(runes) {
			out = append(out, string(runes[start:]))
			break
		}
		end = breakPoint(runes, start, end)
		out = append(out, string(runes[start:end]))
		next := end - overlap
		if next <= start {
			next = end // never lose forward progress
		}
		start = next
	}
	return out
}

// breakPoint backs up to the last newline or space in the final tenth of the
// window, falling back to a hard cut when the text has no break there.
func breakPoint(runes []rune, start, end int) int {
	limit := end - (end-start)/10
	for i := end - 1; i > limit; i-- {
		if runes[i] == '\n' || runes[i] == ' ' {
			return i + 1
		}
	}
	return end
}

// clipText bounds a single ad-hoc input, such as the user's query.
func clipText(s string, window int) string {
	if window <= 0 || utf8.RuneCountInString(s) <= window {
		return s
	}
	return string([]rune(s)[:window])
}

func l2norm(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return v
	}
	n := float32(math.Sqrt(sum))
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = x / n
	}
	return out
}

func encodeVec(v []float32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(x))
	}
	return b
}

func decodeVec(b []byte) ([]float32, error) {
	if len(b)%4 != 0 || len(b) == 0 {
		return nil, fmt.Errorf("invalid embedding blob")
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v, nil
}

func dsn(path string) string {
	return "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)"
}

func dot(a, b []float32) float64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	var sum float64
	for i := 0; i < n; i++ {
		sum += float64(a[i]) * float64(b[i])
	}
	return sum
}
