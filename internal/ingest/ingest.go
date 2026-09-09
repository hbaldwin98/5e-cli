package ingest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hbaldwin98/5e-cli/internal/parse"
	"github.com/hbaldwin98/5e-cli/internal/store"
)

// Result is a completed ingest.
type Result struct {
	Skipped   bool
	Entities  int
	Documents int
	Index     string
	SHA       string
}

// Options control ingest.
type Options struct {
	DataDir string
	Index   string
	Force   bool
}

// Run parses 5etools JSON from dataDir into a sqlite index.
func Run(opt Options) (Result, error) {
	if opt.DataDir == "" {
		return Result{}, fmt.Errorf("data directory not set (use --data or FIVE_E_DATA, or clone the 5etools submodule)")
	}
	st, err := os.Stat(opt.DataDir)
	if err != nil || !st.IsDir() {
		return Result{}, fmt.Errorf("data directory %s not found", opt.DataDir)
	}
	abs, err := filepath.Abs(opt.DataDir)
	if err != nil {
		return Result{}, err
	}
	sha, err := Fingerprint(abs)
	if err != nil {
		return Result{}, err
	}
	if !opt.Force {
		if s, err := store.Open(opt.Index); err == nil {
			meta, merr := s.Meta()
			_ = s.Close()
			if merr == nil && meta.SHA == sha {
				return Result{Skipped: true, Index: opt.Index, SHA: sha}, nil
			}
		}
	}
	return writeIndex(abs, opt.Index, sha)
}

func writeIndex(dataDir, index, sha string) (Result, error) {
	col, err := loadEntities(dataDir)
	if err != nil {
		return Result{}, err
	}
	entities := col.entities
	for k, f := range col.fluff {
		if e, ok := entities[k]; ok {
			entities[k] = parse.MergeFluff(e, f)
		}
	}
	if err := mergeSpellClasses(dataDir, entities); err != nil {
		return Result{}, err
	}
	docs, apps, inline, err := loadDocuments(dataDir)
	if err != nil {
		return Result{}, err
	}
	// data/tables.json holds only a handful of tables; keep the standalone
	// records authoritative and fill in the rest from prose.
	for _, e := range col.tables {
		if _, ok := entities[e.Key()]; !ok {
			entities[e.Key()] = e
		}
	}
	for _, e := range inline {
		if _, ok := entities[e.Key()]; !ok {
			entities[e.Key()] = e
		}
	}
	list := make([]parse.Entity, 0, len(entities))
	for _, e := range entities {
		list = append(list, e)
	}
	apps = append(apps, sourcedAppearances(list)...)
	meta := store.Meta{SHA: sha, DataRoot: dataDir, IngestedAt: store.Now()}
	if err := store.Create(index, meta, list, docs, apps); err != nil {
		return Result{}, err
	}
	return Result{Entities: len(list), Documents: len(docs), Index: index, SHA: sha}, nil
}

// mergeSpellClasses attaches each spell's granted-to classes, read from
// 5etools' generated spell/class lookup (generated/gendata-spell-source-lookup.json,
// keyed by lowercased spell name), onto the matching spell entities. Newer
// 5etools spell records carry no "classes" field of their own — the
// association only exists in this separately generated file — so without
// this step "list spell --class" and a spell's Classes field would always
// be empty. The lookup file is optional: test fixtures and older data trees
// without it simply leave spells without a Classes field, same as before
// this feature existed.
func mergeSpellClasses(dataDir string, entities map[string]parse.Entity) error {
	raw, err := os.ReadFile(filepath.Join(dataDir, "generated", "gendata-spell-source-lookup.json"))
	if err != nil {
		return nil
	}
	// Keyed by lowercased source book id (e.g. "phb", "xphb"), then by
	// lowercased spell name, then a class field shaped source->className->bool
	// — a spell reprinted across books can grant different classes per book,
	// so every source group must be unioned to get the full class list.
	type spellEntry struct {
		Class map[string]map[string]any `json:"class"`
	}
	var bySource map[string]map[string]spellEntry
	if err := json.Unmarshal(raw, &bySource); err != nil {
		return fmt.Errorf("parse spell source lookup: %w", err)
	}
	classesByName := map[string]map[string]bool{}
	for _, spells := range bySource {
		for name, entry := range spells {
			set := classesByName[name]
			if set == nil {
				set = map[string]bool{}
				classesByName[name] = set
			}
			for _, classes := range entry.Class {
				for className := range classes {
					set[className] = true
				}
			}
		}
	}
	for key, e := range entities {
		if e.Kind != "spell" {
			continue
		}
		set, ok := classesByName[strings.ToLower(e.Name)]
		if !ok {
			continue
		}
		names := make([]string, 0, len(set))
		for name := range set {
			names = append(names, name)
		}
		sort.Strings(names)
		entities[key] = parse.MergeSpellClasses(e, names)
	}
	return nil
}

// collector accumulates everything one pass over the data tree produces.
// Tables are kept apart from entities because harvested tables must never
// displace a standalone record with the same kind/name/source.
type collector struct {
	entities map[string]parse.Entity
	fluff    map[string]map[string]any
	tables   map[string]parse.Entity
}

func newCollector() *collector {
	return &collector{
		entities: map[string]parse.Entity{},
		fluff:    map[string]map[string]any{},
		tables:   map[string]parse.Entity{},
	}
}

func (c *collector) addTables(source string, obj map[string]any) {
	for _, t := range parse.Tables(source, obj) {
		if _, ok := c.tables[t.Key()]; !ok {
			c.tables[t.Key()] = t
		}
	}
}

func loadEntities(dataDir string) (*collector, error) {
	col := newCollector()
	if err := ingestDirJSON(dataDir, col); err != nil {
		return nil, err
	}
	for _, dir := range []string{"spells", "bestiary", "class"} {
		if err := ingestIndexed(filepath.Join(dataDir, dir), col); err != nil {
			return nil, err
		}
	}
	if err := loadAdventureCatalog(dataDir, col.entities); err != nil {
		return nil, err
	}
	return col, nil
}

func ingestDirJSON(dir string, col *collector) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".json") || skipFile(ent.Name()) {
			continue
		}
		if err := ingestFile(filepath.Join(dir, ent.Name()), col); err != nil {
			return fmt.Errorf("%s: %w", ent.Name(), err)
		}
	}
	return nil
}

func ingestIndexed(dir string, col *collector) error {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil
	}
	for _, indexName := range []string{"index.json", "fluff-index.json"} {
		p := filepath.Join(dir, indexName)
		raw, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		var idx map[string]string
		if err := json.Unmarshal(raw, &idx); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		for _, file := range idx {
			fp := filepath.Join(dir, file)
			if err := ingestFile(fp, col); err != nil {
				return fmt.Errorf("%s: %w", fp, err)
			}
		}
	}
	return nil
}

func ingestFile(path string, col *collector) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return err
	}
	for key, payload := range root {
		kind, isFluff := classifyKey(key)
		if kind == "" {
			continue
		}
		var items []json.RawMessage
		if err := json.Unmarshal(payload, &items); err != nil {
			continue
		}
		for _, item := range items {
			var obj map[string]any
			if err := json.Unmarshal(item, &obj); err != nil {
				continue
			}
			if isFluff {
				name, _ := obj["name"].(string)
				source, _ := obj["source"].(string)
				if name == "" || source == "" {
					continue
				}
				k := parse.Entity{Kind: kind, Name: name, Source: source}.Key()
				col.fluff[k] = obj
				continue
			}
			e, ok := parse.FromObject(kind, obj, item)
			if !ok {
				continue
			}
			col.entities[e.Key()] = e
			col.addTables(e.Source, obj)
		}
	}
	return nil
}

func loadDocuments(dataDir string) ([]parse.Document, []parse.Appearance, []parse.Entity, error) {
	bookIDs, err := loadIDMap(filepath.Join(dataDir, "books.json"), "book")
	if err != nil {
		return nil, nil, nil, err
	}
	advIDs, err := loadIDMap(filepath.Join(dataDir, "adventures.json"), "adventure")
	if err != nil {
		return nil, nil, nil, err
	}
	books, err := ingestDocumentDir(filepath.Join(dataDir, "book"), "book-", "bookSection", bookIDs)
	if err != nil {
		return nil, nil, nil, err
	}
	advs, err := ingestDocumentDir(filepath.Join(dataDir, "adventure"), "adventure-", "adventureSection", advIDs)
	if err != nil {
		return nil, nil, nil, err
	}
	docs := append(books.docs, advs.docs...)
	apps := append(books.apps, advs.apps...)
	tables := append(books.tables, advs.tables...)
	return docs, apps, tables, nil
}

// documentHarvest is everything one book/adventure directory contributes.
type documentHarvest struct {
	docs   []parse.Document
	apps   []parse.Appearance
	tables []parse.Entity
}

func loadIDMap(path, arrayKey string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	var items []map[string]any
	if err := json.Unmarshal(root[arrayKey], &items); err != nil {
		return map[string]string{}, nil
	}
	out := map[string]string{}
	for _, it := range items {
		id, _ := it["id"].(string)
		if id == "" {
			continue
		}
		out[strings.ToLower(id)] = id
	}
	return out, nil
}

func ingestDocumentDir(dir, prefix, kind string, ids map[string]string) (documentHarvest, error) {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return documentHarvest{}, nil
	}
	var harvest documentHarvest
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") || !strings.HasPrefix(d.Name(), prefix) {
			return nil
		}
		stem := strings.TrimSuffix(strings.TrimPrefix(d.Name(), prefix), ".json")
		parent := ids[strings.ToLower(stem)]
		if parent == "" {
			parent = stem
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var root any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&root); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		harvest.docs = append(harvest.docs, parse.Sections(kind, parent, root)...)
		harvest.tables = append(harvest.tables, parse.Tables(parent, root)...)
		if kind == "adventureSection" {
			harvest.apps = append(harvest.apps, parse.Appearances(parent, root)...)
		}
		return nil
	})
	return harvest, err
}

func sourcedAppearances(entities []parse.Entity) []parse.Appearance {
	adv := map[string]bool{}
	for _, e := range entities {
		if e.Kind == "adventure" {
			adv[strings.ToLower(e.Source)] = true
		}
	}
	var out []parse.Appearance
	for _, e := range entities {
		if !adv[strings.ToLower(e.Source)] {
			continue
		}
		role := ""
		switch e.Kind {
		case "monster":
			role = "npc"
		case "item", "itemBase":
			role = "item"
		default:
			continue
		}
		out = append(out, parse.Appearance{
			Adventure: e.Source,
			Role:      role,
			Kind:      e.Kind,
			Name:      e.Name,
			Source:    e.Source,
		})
	}
	return out
}

func loadAdventureCatalog(dataDir string, entities map[string]parse.Entity) error {
	path := filepath.Join(dataDir, "adventures.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return err
	}
	var items []map[string]any
	if err := json.Unmarshal(root["adventure"], &items); err != nil {
		return nil
	}
	for _, obj := range items {
		id, _ := obj["id"].(string)
		if src, _ := obj["source"].(string); src == "" && id != "" {
			obj["source"] = id
		}
		item, err := json.Marshal(obj)
		if err != nil {
			return err
		}
		e, ok := parse.FromObject("adventure", obj, item)
		if !ok {
			continue
		}
		entities[e.Key()] = e
	}
	return nil
}

// Fingerprint identifies the data tree: git HEAD if possible, else a size hash.
func Fingerprint(dataDir string) (string, error) {
	root, err := filepath.Abs(filepath.Dir(dataDir))
	if err != nil {
		root = filepath.Dir(dataDir)
	}
	if gitRoot, gerr := exec.Command("git", "-C", root, "rev-parse", "--show-toplevel").Output(); gerr == nil {
		gitAbs, _ := filepath.Abs(strings.TrimSpace(string(gitRoot)))
		if gitAbs == root {
			if out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output(); err == nil {
				if sha := strings.TrimSpace(string(out)); sha != "" {
					return sha, nil
				}
			}
		}
	}
	h := sha256.New()
	err = filepath.WalkDir(dataDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dataDir, path)
		fmt.Fprintf(h, "%s %d\n", filepath.ToSlash(rel), info.Size())
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
