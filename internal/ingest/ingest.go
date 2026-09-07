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
	entities, fluff, err := loadEntities(dataDir)
	if err != nil {
		return Result{}, err
	}
	for k, f := range fluff {
		if e, ok := entities[k]; ok {
			entities[k] = parse.MergeFluff(e, f)
		}
	}
	docs, apps, err := loadDocuments(dataDir)
	if err != nil {
		return Result{}, err
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

func loadEntities(dataDir string) (map[string]parse.Entity, map[string]map[string]any, error) {
	entities := map[string]parse.Entity{}
	fluff := map[string]map[string]any{}

	if err := ingestDirJSON(dataDir, entities, fluff); err != nil {
		return nil, nil, err
	}
	for _, dir := range []string{"spells", "bestiary", "class"} {
		if err := ingestIndexed(filepath.Join(dataDir, dir), entities, fluff); err != nil {
			return nil, nil, err
		}
	}
	if err := loadAdventureCatalog(dataDir, entities); err != nil {
		return nil, nil, err
	}
	return entities, fluff, nil
}

func ingestDirJSON(dir string, entities map[string]parse.Entity, fluff map[string]map[string]any) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, ent := range entries {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".json") || skipFile(ent.Name()) {
			continue
		}
		if err := ingestFile(filepath.Join(dir, ent.Name()), entities, fluff); err != nil {
			return fmt.Errorf("%s: %w", ent.Name(), err)
		}
	}
	return nil
}

func ingestIndexed(dir string, entities map[string]parse.Entity, fluff map[string]map[string]any) error {
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
			if err := ingestFile(fp, entities, fluff); err != nil {
				return fmt.Errorf("%s: %w", fp, err)
			}
		}
	}
	return nil
}

func ingestFile(path string, entities map[string]parse.Entity, fluff map[string]map[string]any) error {
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
				fluff[k] = obj
				continue
			}
			e, ok := parse.FromObject(kind, obj, item)
			if !ok {
				continue
			}
			entities[e.Key()] = e
		}
	}
	return nil
}

func loadDocuments(dataDir string) ([]parse.Document, []parse.Appearance, error) {
	bookIDs, err := loadIDMap(filepath.Join(dataDir, "books.json"), "book")
	if err != nil {
		return nil, nil, err
	}
	advIDs, err := loadIDMap(filepath.Join(dataDir, "adventures.json"), "adventure")
	if err != nil {
		return nil, nil, err
	}
	var docs []parse.Document
	var apps []parse.Appearance
	books, bookApps, err := ingestDocumentDir(filepath.Join(dataDir, "book"), "book-", "bookSection", bookIDs)
	if err != nil {
		return nil, nil, err
	}
	advs, advApps, err := ingestDocumentDir(filepath.Join(dataDir, "adventure"), "adventure-", "adventureSection", advIDs)
	if err != nil {
		return nil, nil, err
	}
	docs = append(docs, books...)
	docs = append(docs, advs...)
	apps = append(apps, bookApps...)
	apps = append(apps, advApps...)
	return docs, apps, nil
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

func ingestDocumentDir(dir, prefix, kind string, ids map[string]string) ([]parse.Document, []parse.Appearance, error) {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil, nil, nil
	}
	var docs []parse.Document
	var apps []parse.Appearance
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
		docs = append(docs, parse.Sections(kind, parent, root)...)
		if kind == "adventureSection" {
			apps = append(apps, parse.Appearances(parent, root)...)
		}
		return nil
	})
	return docs, apps, err
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
