package ingest

import (
	"path/filepath"
	"strings"
)

// kindByArray maps 5etools JSON array keys to entity kinds.
var kindByArray = map[string]string{
	"action":          "action",
	"background":      "background",
	"facility":        "bastion",
	"charoption":      "charoption",
	"condition":       "condition",
	"disease":         "disease",
	"status":          "status",
	"cult":            "cult",
	"boon":            "boon",
	"deck":            "deck",
	"deity":           "deity",
	"feat":            "feat",
	"crochetPattern":  "homecraft",
	"item":            "item",
	"itemGroup":       "item",
	"baseitem":        "itemBase",
	"language":        "language",
	"magicvariant":    "magicvariant",
	"monsterfeatures": "monsterfeature",
	"object":          "object",
	"optionalfeature": "optionalfeature",
	"psionic":         "psionic",
	"race":            "race",
	"subrace":         "subrace",
	"recipe":          "recipe",
	"reward":          "reward",
	"sense":           "sense",
	"skill":           "skill",
	"table":           "table",
	"trap":            "trap",
	"hazard":          "hazard",
	"variantrule":     "variantrule",
	"vehicle":         "vehicle",
	"vehicleUpgrade":  "vehicle",
	"spell":           "spell",
	"monster":         "monster",
	"class":           "class",
	"subclass":        "subclass",
	"classFeature":    "classFeature",
	"subclassFeature": "subclassFeature",
}

var skipFiles = map[string]bool{
	"changelog.json":         true,
	"converter.json":         true,
	"encounterbuilder.json":  true,
	"encounters.json":        true,
	"life.json":              true,
	"loot.json":              true,
	"makebrew-creature.json": true,
	"makecards.json":         true,
	"msbcr.json":             true,
	"names.json":             true,
	"renderdemo.json":        true,
}

func classifyKey(key string) (kind string, fluff bool) {
	if key == "_meta" || strings.HasSuffix(key, "FluffMeta") {
		return "", false
	}
	if strings.HasSuffix(key, "Fluff") {
		base := strings.TrimSuffix(key, "Fluff")
		k, _ := classifyKey(base)
		return k, true
	}
	return kindByArray[key], false
}

func skipFile(name string) bool {
	base := filepath.Base(name)
	if skipFiles[base] {
		return true
	}
	if strings.HasPrefix(base, "foundry-") {
		return true
	}
	return false
}
