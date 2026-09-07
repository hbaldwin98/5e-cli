package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hbaldwin98/5e-cli/internal/parse"

	_ "modernc.org/sqlite"
)

const schema = `
PRAGMA journal_mode=WAL;
PRAGMA foreign_keys=ON;

CREATE TABLE ingest_meta (
  submodule_sha TEXT NOT NULL,
  data_root     TEXT NOT NULL,
  ingested_at   TEXT NOT NULL
);

CREATE TABLE entities (
  id        INTEGER PRIMARY KEY,
  kind      TEXT NOT NULL,
  name      TEXT NOT NULL,
  source    TEXT NOT NULL,
  page      INTEGER,
  srd       INTEGER NOT NULL DEFAULT 0,
  json      TEXT NOT NULL,
  text      TEXT NOT NULL,
  UNIQUE (kind, name, source)
);

CREATE VIRTUAL TABLE entity_fts USING fts5(
  name,
  text,
  content='entities',
  content_rowid='id'
);

CREATE TABLE edges (
  from_id    INTEGER NOT NULL REFERENCES entities(id),
  tag        TEXT NOT NULL,
  to_kind    TEXT,
  to_name    TEXT NOT NULL,
  to_source  TEXT,
  display    TEXT
);

CREATE TABLE documents (
  id         INTEGER PRIMARY KEY,
  kind       TEXT NOT NULL,
  parent_id  TEXT NOT NULL,
  section    TEXT NOT NULL,
  json       TEXT NOT NULL,
  text       TEXT NOT NULL
);

CREATE VIRTUAL TABLE document_fts USING fts5(
  section,
  text,
  content='documents',
  content_rowid='id'
);
`

// Meta is the ingest fingerprint stored in the index.
type Meta struct {
	SHA        string
	DataRoot   string
	IngestedAt string
}

// Entity is a stored lookup row.
type Entity struct {
	ID     int64
	Kind   string
	Name   string
	Source string
	Page   int
	SRD    bool
	JSON   json.RawMessage
	Text   string
	Edges  []parse.Edge
}

// Document is a stored book/adventure section.
type Document struct {
	ID       int64
	Kind     string
	ParentID string
	Section  string
	JSON     json.RawMessage
	Text     string
}

// Store is an open sqlite index.
type Store struct {
	DB *sql.DB
}

// Open opens an existing index for reading.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{DB: db}, nil
}

// Create writes a new index atomically at path.
func Create(path string, meta Meta, entities []parse.Entity, docs []parse.Document) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	_ = os.Remove(tmp)
	db, err := sql.Open("sqlite", dsn(tmp))
	if err != nil {
		return err
	}
	defer func() {
		_ = db.Close()
		_ = os.Remove(tmp)
	}()
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(
		`INSERT INTO ingest_meta (submodule_sha, data_root, ingested_at) VALUES (?, ?, ?)`,
		meta.SHA, meta.DataRoot, meta.IngestedAt,
	); err != nil {
		return err
	}

	insEnt, err := tx.Prepare(`INSERT OR REPLACE INTO entities (kind, name, source, page, srd, json, text) VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer insEnt.Close()
	insEdge, err := tx.Prepare(`INSERT INTO edges (from_id, tag, to_kind, to_name, to_source, display) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer insEdge.Close()

	for _, e := range entities {
		srd := 0
		if e.SRD {
			srd = 1
		}
		res, err := insEnt.Exec(e.Kind, e.Name, e.Source, e.Page, srd, string(e.JSON), e.Text)
		if err != nil {
			return fmt.Errorf("entity %s %s (%s): %w", e.Kind, e.Name, e.Source, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		for _, edge := range e.Edges {
			if _, err := insEdge.Exec(id, edge.Tag, edge.ToKind, edge.ToName, edge.ToSource, edge.Display); err != nil {
				return err
			}
		}
	}

	insDoc, err := tx.Prepare(`INSERT INTO documents (kind, parent_id, section, json, text) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer insDoc.Close()
	for _, d := range docs {
		if _, err := insDoc.Exec(d.Kind, d.ParentID, d.Section, string(d.JSON), d.Text); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(`INSERT INTO entity_fts(entity_fts) VALUES('rebuild')`); err != nil {
		return fmt.Errorf("entity fts: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO document_fts(document_fts) VALUES('rebuild')`); err != nil {
		return fmt.Errorf("document fts: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if err := db.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func dsn(path string) string {
	return "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(1)"
}

// Meta reads ingest_meta (single row).
func (s *Store) Meta() (Meta, error) {
	var m Meta
	err := s.DB.QueryRow(`SELECT submodule_sha, data_root, ingested_at FROM ingest_meta LIMIT 1`).
		Scan(&m.SHA, &m.DataRoot, &m.IngestedAt)
	return m, err
}

// Close closes the database.
func (s *Store) Close() error {
	if s == nil || s.DB == nil {
		return nil
	}
	return s.DB.Close()
}

// Get returns entities matching kind+name, optionally filtered by source.
func (s *Store) Get(kind, name, source string) ([]Entity, error) {
	q := `SELECT id, kind, name, source, page, srd, json, text FROM entities WHERE kind = ? AND name = ? COLLATE NOCASE`
	args := []any{kind, name}
	if source != "" {
		q += ` AND source = ? COLLATE NOCASE`
		args = append(args, source)
	}
	q += ` ORDER BY source`
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entity
	for rows.Next() {
		var e Entity
		var srd int
		var raw string
		if err := rows.Scan(&e.ID, &e.Kind, &e.Name, &e.Source, &e.Page, &srd, &raw, &e.Text); err != nil {
			return nil, err
		}
		e.SRD = srd != 0
		e.JSON = json.RawMessage(raw)
		e.Edges, err = s.edges(e.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Lookup returns entities, or book/adventure sections when kind+name match a document.
func (s *Store) Lookup(kind, name, source string) ([]Entity, error) {
	ents, err := s.Get(kind, name, source)
	if err != nil || len(ents) > 0 {
		return ents, err
	}
	docs, err := s.GetDocument(kind, name, source)
	if err != nil {
		return nil, err
	}
	out := make([]Entity, 0, len(docs))
	for _, d := range docs {
		out = append(out, Entity{
			ID:     d.ID,
			Kind:   d.Kind,
			Name:   d.Section,
			Source: d.ParentID,
			JSON:   d.JSON,
			Text:   d.Text,
		})
	}
	return out, nil
}

// GetDocument returns book/adventure sections matching kind+section, optionally parent/source.
func (s *Store) GetDocument(kind, section, parent string) ([]Document, error) {
	q := `SELECT id, kind, parent_id, section, json, text FROM documents WHERE kind = ? AND section = ? COLLATE NOCASE`
	args := []any{kind, section}
	if parent != "" {
		q += ` AND parent_id = ? COLLATE NOCASE`
		args = append(args, parent)
	}
	q += ` ORDER BY parent_id`
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Document
	for rows.Next() {
		var d Document
		var raw string
		if err := rows.Scan(&d.ID, &d.Kind, &d.ParentID, &d.Section, &raw, &d.Text); err != nil {
			return nil, err
		}
		d.JSON = json.RawMessage(raw)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) edges(id int64) ([]parse.Edge, error) {
	rows, err := s.DB.Query(`SELECT tag, to_kind, to_name, to_source, display FROM edges WHERE from_id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []parse.Edge
	for rows.Next() {
		var e parse.Edge
		if err := rows.Scan(&e.Tag, &e.ToKind, &e.ToName, &e.ToSource, &e.Display); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Names returns kind, name, source, text, and SRD for fuzzy ranking.
func (s *Store) Names() ([]Entity, error) {
	rows, err := s.DB.Query(`SELECT kind, name, source, text, srd FROM entities`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entity
	for rows.Next() {
		var e Entity
		var srd int
		if err := rows.Scan(&e.Kind, &e.Name, &e.Source, &e.Text, &srd); err != nil {
			return nil, err
		}
		e.SRD = srd != 0
		out = append(out, e)
	}
	return out, rows.Err()
}

// SRDOnly keeps entities marked SRD / basic rules at ingest.
func SRDOnly(ents []Entity) []Entity {
	var out []Entity
	for _, e := range ents {
		if e.SRD {
			out = append(out, e)
		}
	}
	return out
}

// Documents returns book/adventure sections for embedding and search.
func (s *Store) Documents() ([]Document, error) {
	rows, err := s.DB.Query(`SELECT kind, parent_id, section, text FROM documents`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Document
	for rows.Next() {
		var d Document
		if err := rows.Scan(&d.Kind, &d.ParentID, &d.Section, &d.Text); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// FTSEntities runs FTS5 against entity text.
func (s *Store) FTSEntities(match string, kind string, sources []string, srdOnly bool, limit int) ([]Hit, error) {
	q := `
SELECT e.kind, e.name, e.source, snippet(entity_fts, 1, '', '', '…', 16), rank
FROM entity_fts
JOIN entities e ON e.id = entity_fts.rowid
WHERE entity_fts MATCH ?
`
	args := []any{match}
	if kind != "" {
		q += ` AND e.kind = ?`
		args = append(args, kind)
	}
	if len(sources) > 0 {
		q += ` AND e.source COLLATE NOCASE IN (` + placeholders(len(sources)) + `)`
		for _, src := range sources {
			args = append(args, src)
		}
	}
	if srdOnly {
		q += ` AND e.srd = 1`
	}
	q += ` ORDER BY rank LIMIT ?`
	args = append(args, limit)
	return s.queryHits(q, args...)
}

// FTSDocuments runs FTS5 against book/adventure sections.
func (s *Store) FTSDocuments(match string, kind string, sources []string, limit int) ([]Hit, error) {
	q := `
SELECT d.kind, d.section, d.parent_id, snippet(document_fts, 1, '', '', '…', 16), rank
FROM document_fts
JOIN documents d ON d.id = document_fts.rowid
WHERE document_fts MATCH ?
`
	args := []any{match}
	if kind != "" {
		q += ` AND d.kind = ?`
		args = append(args, kind)
	}
	if len(sources) > 0 {
		q += ` AND d.parent_id COLLATE NOCASE IN (` + placeholders(len(sources)) + `)`
		for _, src := range sources {
			args = append(args, src)
		}
	}
	q += ` ORDER BY rank LIMIT ?`
	args = append(args, limit)
	return s.queryHits(q, args...)
}

// Hit is a ranked search row.
type Hit struct {
	Kind    string
	Name    string
	Source  string
	Snippet string
	Rank    float64
}

func (s *Store) queryHits(q string, args ...any) ([]Hit, error) {
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.Kind, &h.Name, &h.Source, &h.Snippet, &h.Rank); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	s := "?"
	for i := 1; i < n; i++ {
		s += ",?"
	}
	return s
}

// Now formats ingest time in UTC.
func Now() string {
	return time.Now().UTC().Format(time.RFC3339)
}
