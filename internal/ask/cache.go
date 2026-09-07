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
	embedBatch = 64
	maxRunes   = 24000
)

type chunk struct {
	Kind   string
	Name   string
	Source string
	Text   string
}

func (c chunk) id() string {
	return c.Kind + "\x1f" + c.Name + "\x1f" + c.Source
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
	if ok, err := cacheFresh(cfg.CachePath, meta.SHA, cfg.BaseURL, cfg.EmbedModel); err != nil {
		return err
	} else if ok {
		return nil
	}
	chunks, err := corpus(st)
	if err != nil {
		return err
	}
	if len(chunks) == 0 {
		return fmt.Errorf("index has no embeddable text; run `5e ingest`")
	}
	cli := newClient(cfg)
	return writeCache(ctx, cli, cfg, meta.SHA, chunks)
}

func cacheFresh(path, sha, baseURL, model string) (bool, error) {
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
	var n int
	err = db.QueryRow(`SELECT corpus_sha, base_url, embed_model FROM meta LIMIT 1`).Scan(&gotSHA, &gotURL, &gotModel)
	if err != nil {
		return false, nil
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM vectors`).Scan(&n); err != nil || n == 0 {
		return false, nil
	}
	return gotSHA == sha && gotURL == baseURL && gotModel == model, nil
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

	for i := 0; i < len(chunks); i += embedBatch {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := i + embedBatch
		if end > len(chunks) {
			end = len(chunks)
		}
		batch := chunks[i:end]
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
		`INSERT INTO meta (corpus_sha, base_url, embed_model, dim, built_at) VALUES (?, ?, ?, ?, ?)`,
		sha, cfg.BaseURL, cfg.EmbedModel, dim, store.Now(),
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

func corpus(st *store.Store) ([]chunk, error) {
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
		text := clipText(e.Text)
		if strings.TrimSpace(text) == "" {
			continue
		}
		out = append(out, chunk{Kind: e.Kind, Name: e.Name, Source: e.Source, Text: text})
	}
	for _, d := range docs {
		text := clipText(d.Text)
		if strings.TrimSpace(text) == "" {
			continue
		}
		out = append(out, chunk{Kind: d.Kind, Name: d.Section, Source: d.ParentID, Text: text})
	}
	return out, nil
}

func clipText(s string) string {
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	r := []rune(s)
	return string(r[:maxRunes])
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
