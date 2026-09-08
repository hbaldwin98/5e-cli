package adventure

import (
	"fmt"
	"strings"

	"github.com/hbaldwin98/5e-cli/internal/parse"
	"github.com/hbaldwin98/5e-cli/internal/store"
)

// Section is the lightweight identity of an adventure chapter or location.
type Section struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Source string `json:"source"`
}

// Report is the structured contents of one adventure.
type Report struct {
	Chapters    []Section          `json:"chapters,omitempty"`
	Locations   []Section          `json:"locations,omitempty"`
	Appearances []parse.Appearance `json:"appearances,omitempty"`
}

// List returns adventure contents filtered by role, chapter, and location.
func List(st *store.Store, adventureID, role, chapter, location string) (Report, error) {
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "all" {
		role = ""
	}
	if role != "" && role != "npc" && role != "item" && role != "location" {
		return Report{}, fmt.Errorf("unknown adventure role %q", role)
	}
	report := Report{}
	if role == "" || role == "location" {
		chapters, err := st.GetDocument("adventureSection", chapter, adventureID)
		if err != nil {
			return Report{}, err
		}
		locations, err := st.GetDocument("adventureLocation", location, adventureID)
		if err != nil {
			return Report{}, err
		}
		for _, section := range chapters {
			report.Chapters = append(report.Chapters, Section{Kind: section.Kind, Name: section.Section, Source: section.ParentID})
		}
		for _, section := range locations {
			report.Locations = append(report.Locations, Section{Kind: section.Kind, Name: section.Section, Source: section.ParentID})
		}
	}
	if role == "" || role == "npc" || role == "item" {
		appearances, err := st.AdventureAppearances(adventureID, role, chapter, location)
		if err != nil {
			return Report{}, err
		}
		report.Appearances = appearances
	}
	return report, nil
}

// Resolve finds a catalog adventure by title or 5etools id.
func Resolve(st *store.Store, nameOrID string) (store.Entity, error) {
	ents, err := st.Lookup("adventure", nameOrID, "")
	if err != nil {
		return store.Entity{}, err
	}
	if len(ents) == 0 {
		return store.Entity{}, fmt.Errorf("no adventure named %q", nameOrID)
	}
	if len(ents) > 1 {
		return store.Entity{}, fmt.Errorf("ambiguous adventure %q", nameOrID)
	}
	return ents[0], nil
}

// SearchKind maps an adventure role to a store kind. Empty role searches the whole module.
func SearchKind(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "", "all":
		return ""
	case "npc":
		return "monster"
	case "location":
		return "location"
	case "item":
		return "item"
	default:
		return role
	}
}

// GetKind maps get <role> to a lookup kind.
func GetKind(role string) (string, error) {
	if strings.TrimSpace(role) == "" {
		return "", fmt.Errorf("adventure get requires a role (npc, location, item)")
	}
	k := SearchKind(role)
	switch k {
	case "":
		return "", fmt.Errorf("unknown adventure role %q", role)
	case "location":
		return "adventureLocation", nil
	default:
		return k, nil
	}
}

// Lookup finds one role hit inside an adventure, including MM creatures that appear there.
func Lookup(st *store.Store, role, name, adventureID string) ([]store.Entity, error) {
	kind, err := GetKind(role)
	if err != nil {
		return nil, err
	}
	ents, err := st.Lookup(kind, name, adventureID)
	if err != nil || len(ents) > 0 {
		return ents, err
	}
	if kind == "adventureLocation" {
		return st.Lookup("adventureSection", name, adventureID)
	}
	return lookupAppeared(st, adventureID, role, kind, name)
}

func lookupAppeared(st *store.Store, adventureID, role, kind, name string) ([]store.Entity, error) {
	if role != "npc" && role != "item" {
		return nil, nil
	}
	seen, err := st.AppearanceSet(adventureID, role)
	if err != nil {
		return nil, err
	}
	ents, err := st.Get(kind, name, "")
	if err != nil {
		return nil, err
	}
	var out []store.Entity
	for _, e := range ents {
		if seen[store.AppearanceKey(e.Kind, e.Name, e.Source)] {
			out = append(out, e)
		}
	}
	return out, nil
}
