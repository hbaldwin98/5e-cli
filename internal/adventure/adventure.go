package adventure

import (
	"fmt"
	"strings"

	"github.com/hbaldwin98/5e-cli/internal/store"
)

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
