package search

import (
	"sort"

	"github.com/hbaldwin98/5e-cli/internal/statblock"
	"github.com/hbaldwin98/5e-cli/internal/store"
)

// SpellRow is one entry of a class or level spell list: the level a caster
// prepares the spell at, plus enough to look it up again.
type SpellRow struct {
	Level  int    `json:"level"`
	Name   string `json:"name"`
	Source string `json:"source"`
}

// SpellList narrows already-listed spell entities to the ones a class can
// cast and/or of a given level, returning them ordered by level then name.
// class is matched against each spell's own classes field — populated at
// ingest from 5etools' generated spell/class lookup, since individual spell
// records no longer embed it — so "which spells does a Wizard get" is a data
// question, not something to infer from retrieved prose. A nil level means
// every level; level 0 means cantrips.
//
// Each row needs its own Lookup because FilteredNames deliberately leaves the
// JSON blob in sqlite rather than pulling every spell's text into Go.
func SpellList(st *store.Store, ents []store.Entity, class string, level *int) []SpellRow {
	var rows []SpellRow
	for _, e := range ents {
		found, err := st.Lookup("spell", e.Name, e.Source)
		if err != nil || len(found) == 0 {
			continue
		}
		obj, err := statblock.Decode(found[0].JSON)
		if err != nil {
			continue
		}
		if class != "" && !statblock.SpellGrantedToClass(obj, class) {
			continue
		}
		lvl := statblock.SpellLevel(obj)
		if level != nil && lvl != *level {
			continue
		}
		rows = append(rows, SpellRow{Level: lvl, Name: e.Name, Source: e.Source})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Level != rows[j].Level {
			return rows[i].Level < rows[j].Level
		}
		return rows[i].Name < rows[j].Name
	})
	return rows
}
