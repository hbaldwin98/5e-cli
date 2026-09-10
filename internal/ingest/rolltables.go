package ingest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/hbaldwin98/5e-cli/internal/parse"
)

// Three data files are shaped as roll tables but written in their own
// bespoke JSON rather than the colLabels/rows shape data/tables.json uses,
// so classifyKey's catch-all could not index them and they sat in skipFiles:
//
//	names.json  name generators by race and gender
//	life.json   the DMG lifepath tables, including the d100 trinkets
//	loot.json   gemstones, art objects, and the DMG magic item tables A-I
//
// Each is real content a DM rolls on at the table, so convert them into the
// standard table shape at ingest and let `5e roll` and the roll tool reach
// them with no changes of their own.
//
// loot.json's individual, hoard, and dragon entries are deliberately left
// out: those are procedural generators (roll coins by CR, then a gem type,
// then a quantity, then cross-reference a magic item table), not tables you
// roll once and read. Indexing them as flat tables would produce something
// that answers to `5e roll` while giving a meaningless result, which is
// worse than not having them.
func loadRollTables(dataDir string) ([]parse.Entity, error) {
	var out []parse.Entity
	for _, load := range []func(string) ([]parse.Entity, error){
		loadNameTables, loadLifeTables, loadLootTables,
	} {
		entities, err := load(dataDir)
		if err != nil {
			return nil, err
		}
		out = append(out, entities...)
	}
	return out, nil
}

// readDataFile decodes one optional data file. A tree without it — a test
// fixture, an older 5etools checkout — simply contributes no tables, the
// same way the spell/class lookup and legendary groups are optional.
func readDataFile(dataDir, name string, v any) (bool, error) {
	raw, err := os.ReadFile(filepath.Join(dataDir, name))
	if err != nil {
		return false, nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return false, fmt.Errorf("parse %s: %w", name, err)
	}
	return true, nil
}

// rangeLabel renders a row's roll range the way tableRows' rowRange parses
// it: "7" for a single result, "7-16" for a span.
func rangeLabel(min, max int) string {
	if max > min {
		return strconv.Itoa(min) + "-" + strconv.Itoa(max)
	}
	return strconv.Itoa(min)
}

// tableEntity builds one standard table entity. Rows are [][]string rather
// than the raw shapes above so every caller here converges on the one shape
// internal/table already understands.
func tableEntity(name, source string, page int, headers []string, rows [][]string) (parse.Entity, bool) {
	if name == "" || source == "" || len(rows) == 0 {
		return parse.Entity{}, false
	}
	labels := make([]any, len(headers))
	for i, h := range headers {
		labels[i] = h
	}
	jsonRows := make([]any, len(rows))
	for i, r := range rows {
		cells := make([]any, len(r))
		for j, c := range r {
			cells[j] = c
		}
		jsonRows[i] = cells
	}
	obj := map[string]any{
		"name":      name,
		"source":    source,
		"colLabels": labels,
		"rows":      jsonRows,
	}
	if page > 0 {
		obj["page"] = page
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		return parse.Entity{}, false
	}
	return parse.FromObject("table", obj, raw)
}

// loadNameTables turns names.json into one table per race and option, e.g.
// "Dragonborn Female Names" — the option has to be in the name because a
// single race contributes several tables (Female, Male, Clan) and a table is
// addressed by name.
func loadNameTables(dataDir string) ([]parse.Entity, error) {
	var file struct {
		Name []struct {
			Name   string `json:"name"`
			Source string `json:"source"`
			Page   int    `json:"page"`
			Tables []struct {
				Option         string `json:"option"`
				DiceExpression string `json:"diceExpression"`
				Table          []struct {
					Min    int    `json:"min"`
					Max    int    `json:"max"`
					Result string `json:"result"`
				} `json:"table"`
			} `json:"tables"`
		} `json:"name"`
	}
	if ok, err := readDataFile(dataDir, "names.json", &file); err != nil || !ok {
		return nil, err
	}
	var out []parse.Entity
	for _, race := range file.Name {
		for _, t := range race.Tables {
			rows := make([][]string, 0, len(t.Table))
			for _, r := range t.Table {
				rows = append(rows, []string{rangeLabel(r.Min, r.Max), r.Result})
			}
			dice := t.DiceExpression
			if dice == "" {
				dice = "Roll"
			}
			name := race.Name + " " + t.Option + " Names"
			if e, ok := tableEntity(name, race.Source, race.Page, []string{dice, "Name"}, rows); ok {
				out = append(out, e)
			}
		}
	}
	return out, nil
}

// loadLifeTables turns life.json's trinket list into a d100 table. Its
// lifeClass and lifeBackground entries are left alone: they are prose
// prompts attached to a class or background rather than tables addressed by
// their own name.
func loadLifeTables(dataDir string) ([]parse.Entity, error) {
	var file struct {
		LifeTrinket []string `json:"lifeTrinket"`
	}
	if ok, err := readDataFile(dataDir, "life.json", &file); err != nil || !ok {
		return nil, err
	}
	rows := make([][]string, 0, len(file.LifeTrinket))
	for i, trinket := range file.LifeTrinket {
		rows = append(rows, []string{strconv.Itoa(i + 1), trinket})
	}
	e, ok := tableEntity("Trinkets", "PHB", 0, []string{"d100", "Trinket"}, rows)
	if !ok {
		return nil, nil
	}
	return []parse.Entity{e}, nil
}

// loadLootTables turns loot.json's gemstone, art object, and magic item
// tables into rollable tables. The gem and art-object lists carry no roll
// ranges of their own — 5etools expects a d12 or d10 over the list — so
// their rows are numbered by position.
func loadLootTables(dataDir string) ([]parse.Entity, error) {
	var file struct {
		Gems []struct {
			Name   string   `json:"name"`
			Source string   `json:"source"`
			Page   int      `json:"page"`
			Table  []string `json:"table"`
		} `json:"gems"`
		ArtObjects []struct {
			Name   string   `json:"name"`
			Source string   `json:"source"`
			Page   int      `json:"page"`
			Table  []string `json:"table"`
		} `json:"artObjects"`
		MagicItems []struct {
			Name   string `json:"name"`
			Source string `json:"source"`
			Page   int    `json:"page"`
			Table  []struct {
				Min  int    `json:"min"`
				Max  int    `json:"max"`
				Item string `json:"item"`
			} `json:"table"`
		} `json:"magicItems"`
	}
	if ok, err := readDataFile(dataDir, "loot.json", &file); err != nil || !ok {
		return nil, err
	}
	var out []parse.Entity
	positional := func(name, source string, page int, header string, items []string) {
		rows := make([][]string, 0, len(items))
		for i, item := range items {
			rows = append(rows, []string{strconv.Itoa(i + 1), item})
		}
		dice := "d" + strconv.Itoa(len(items))
		if e, ok := tableEntity(name, source, page, []string{dice, header}, rows); ok {
			out = append(out, e)
		}
	}
	for _, g := range file.Gems {
		positional(g.Name, g.Source, g.Page, "Gemstone", g.Table)
	}
	for _, a := range file.ArtObjects {
		positional(a.Name, a.Source, a.Page, "Art Object", a.Table)
	}
	for _, m := range file.MagicItems {
		rows := make([][]string, 0, len(m.Table))
		for _, r := range m.Table {
			rows = append(rows, []string{rangeLabel(r.Min, r.Max), r.Item})
		}
		if e, ok := tableEntity(m.Name, m.Source, m.Page, []string{"d100", "Item"}, rows); ok {
			out = append(out, e)
		}
	}
	return out, nil
}
