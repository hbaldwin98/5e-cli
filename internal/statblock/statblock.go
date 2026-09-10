// Package statblock renders one entity's raw 5etools JSON as a readable
// Markdown stat block. It is shared by the CLI's human output and by the
// tool-calling layer in internal/ask: a chat model gets the same clean,
// complete block a person reading the terminal does, instead of parsing the
// raw tagged JSON itself.
package statblock

import (
	"encoding/json"
	"fmt"
	"image/color"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
	lgtable "charm.land/lipgloss/v2/table"
	"github.com/hbaldwin98/5e-cli/internal/parse"
)

// cardOpts carries card-only rendering context (content width, accent color)
// through the generic entries walk (renderEntriesOpt and friends), so a
// table entry renders as an actual lipgloss table sized to the card instead
// of literal Markdown pipe syntax. nil means "not rendering for a card" and
// keeps the original Markdown behavior every other caller relies on.
type cardOpts struct {
	width  int
	accent color.Color
}

// Render writes one entity's stat-block body (everything after the name and
// kind/source line, which the caller already knows) to w: the kind-specific
// summary fields it has one for (monster, spell, item, race), followed by
// its entries sections (traits, actions, spellcasting, and so on for a
// monster; entries and higher-level text for anything else). Every kind
// falls through to the entries sections even without a dedicated summary,
// so nothing indexed renders as a bare, unreadable JSON blob.
func Render(w io.Writer, kind string, obj map[string]any) {
	switch kind {
	case "spell":
		renderSpell(w, obj)
	case "monster":
		renderMonster(w, obj)
	case "item", "itemBase", "magicvariant":
		renderItem(w, obj)
	case "race", "subrace":
		renderRace(w, obj)
	case "class":
		renderClass(w, obj)
	case "background":
		renderBackground(w, obj)
	case "feat":
		renderFeat(w, obj)
	case "deity":
		renderDeity(w, obj)
	case "vehicle":
		renderVehicle(w, obj)
	case "trap", "hazard":
		renderTrapHazard(w, obj)
	case "object":
		renderObject(w, obj)
	case "optionalfeature":
		renderOptionalFeature(w, obj)
	case "reward", "boon", "cult":
		writeField(w, "Type", stringValue(obj["type"]))
	case "psionic":
		renderPsionic(w, obj)
	case "variantrule":
		writeField(w, "Rule Type", variantRuleTypeName(stringValue(obj["ruleType"])))
	}
	for _, section := range entrySections(kind) {
		if entries, ok := obj[section.key].([]any); ok && len(entries) > 0 {
			if section.heading != "" {
				fmt.Fprintf(w, "\n## %s\n\n", section.heading)
			}
			renderEntries(w, entries, 0)
		}
	}
}

// RenderString is Render collected into a string, for a caller (a tool
// result, a test) that wants the text rather than a writer.
func RenderString(kind string, obj map[string]any) string {
	var b strings.Builder
	Render(&b, kind, obj)
	return b.String()
}

// Decode parses a stored entity's raw JSON payload the way Render expects
// it: json.Number preserved rather than collapsed to float64, since a large
// monster HP total or spell level must round-trip exactly.
func Decode(raw json.RawMessage) (map[string]any, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return nil, err
	}
	return obj, nil
}

func renderSpell(w io.Writer, obj map[string]any) {
	level := integer(obj["level"])
	school := spellSchool(stringValue(obj["school"]))
	if level == 0 {
		fmt.Fprintf(w, "%s cantrip\n", title(school))
	} else {
		fmt.Fprintf(w, "%s-level %s\n", ordinal(level), school)
	}
	castingTime := spellTimes(obj["time"])
	if spellIsRitual(obj) {
		castingTime += " (ritual)"
	}
	writeField(w, "Casting Time", castingTime)
	writeField(w, "Range", spellRange(obj["range"]))
	writeField(w, "Components", spellComponents(obj["components"]))
	writeField(w, "Duration", spellDurations(obj["duration"]))
	writeField(w, "Classes", spellClasses(obj["classes"]))
}

// SpellLevel is a spell's level (0 for a cantrip), exported for a caller
// (the list command's --class grouping) that wants it without duplicating
// the integer() decode.
func SpellLevel(obj map[string]any) int {
	return integer(obj["level"])
}

// SpellGrantedToClass reports whether class appears among the classes a
// spell is granted to, case-insensitively — the same classes field
// spellClasses renders, populated at ingest time from 5etools' generated
// spell/class lookup since individual spell records no longer embed it.
func SpellGrantedToClass(obj map[string]any, class string) bool {
	for _, name := range strings.Split(spellClasses(obj["classes"]), ", ") {
		if strings.EqualFold(name, class) {
			return true
		}
	}
	return false
}

func spellIsRitual(obj map[string]any) bool {
	meta, _ := obj["meta"].(map[string]any)
	return meta["ritual"] == true
}

// spellClasses lists the classes a spell is granted to, from the classes
// field's fromClassList (classes with this spell on their own list) and
// fromSubclass (subclasses that grant it beyond their base class), since a
// DM checking who can cast a spell needs both.
func spellClasses(v any) string {
	obj, _ := v.(map[string]any)
	seen := map[string]bool{}
	var names []string
	addFrom := func(key string) {
		list, _ := obj[key].([]any)
		for _, item := range list {
			entry, _ := item.(map[string]any)
			cls, _ := entry["class"].(map[string]any)
			name := stringValue(cls["name"])
			if name == "" {
				name = stringValue(entry["name"])
			}
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
	}
	addFrom("fromClassList")
	addFrom("fromSubclass")
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func renderMonster(w io.Writer, obj map[string]any) {
	description := joinNonEmpty(", ", monsterSize(obj["size"]), monsterType(obj["type"]))
	if alignment := monsterAlignment(obj["alignment"]); alignment != "" {
		description = joinNonEmpty(", ", description, alignment)
	}
	if description != "" {
		fmt.Fprintln(w, description)
	}
	writeField(w, "Armor Class", armorClass(obj["ac"]))
	writeField(w, "Hit Points", hitPoints(obj["hp"]))
	writeField(w, "Speed", speed(obj["speed"]))

	abilities := []string{"str", "dex", "con", "int", "wis", "cha"}
	hasAbilities := false
	for _, ability := range abilities {
		hasAbilities = hasAbilities || obj[ability] != nil
	}
	if hasAbilities {
		fmt.Fprintln(w, "\n| STR | DEX | CON | INT | WIS | CHA |")
		fmt.Fprintln(w, "| ---: | ---: | ---: | ---: | ---: | ---: |")
		fmt.Fprint(w, "|")
		for _, ability := range abilities {
			score := integer(obj[ability])
			fmt.Fprintf(w, " %d (%+d) |", score, abilityModifier(score))
		}
		fmt.Fprintln(w)
		fmt.Fprintln(w)
	}

	writeField(w, "Saving Throws", abilityMap(obj["save"]))
	writeField(w, "Skills", abilityMap(obj["skill"]))
	writeField(w, "Damage Vulnerabilities", damageTags(obj["vulnerable"]))
	writeField(w, "Damage Resistances", damageTags(obj["resist"]))
	writeField(w, "Damage Immunities", damageTags(obj["immune"]))
	writeField(w, "Condition Immunities", stringList(obj["conditionImmune"]))
	writeField(w, "Senses", senses(obj))
	writeField(w, "Languages", stringList(obj["languages"]))
	writeField(w, "Challenge", challengeRating(obj["cr"]))
}

func renderItem(w io.Writer, obj map[string]any) {
	line := joinNonEmpty(", ", itemType(obj), itemRarity(obj))
	if attune := obj["reqAttune"]; attune != nil && attune != false {
		text := "requires attunement"
		if detail, ok := attune.(string); ok && detail != "" {
			text += " " + renderString(detail)
		}
		if line == "" {
			line = text
		} else {
			line += " (" + text + ")"
		}
	}
	if line != "" {
		fmt.Fprintln(w, line)
	}
	writeField(w, "Damage", itemDamage(obj))
	writeField(w, "Properties", itemProperties(obj))
	writeField(w, "Weapon Mastery", itemMastery(obj))
	writeField(w, "Range", stringValue(obj["range"]))
	writeField(w, "Weight", itemWeight(obj))
	writeField(w, "Value", itemValue(obj))
	writeField(w, "Armor Class", itemAC(obj))
	writeField(w, "Strength Requirement", itemStrength(obj))
	writeField(w, "Stealth", itemStealth(obj))
	writeField(w, "Bonus to AC", scalar(obj["bonusAc"]))
	writeField(w, "Bonus to Attack Rolls", scalar(obj["bonusWeapon"]))
	writeField(w, "Bonus to Damage Rolls", scalar(obj["bonusWeaponDamage"]))
	writeField(w, "Bonus to Spell Attacks", scalar(obj["bonusSpellAttack"]))
	writeField(w, "Bonus to Saving Throws", scalar(obj["bonusSavingThrow"]))
	writeField(w, "Prerequisite", itemPrerequisite(obj))
	writeField(w, "Applies To", magicVariantApplies(obj["requires"]))
}

// itemProperties expands a weapon's property codes (V versatile, F finesse,
// H heavy, 2H two-handed, L light, R reach, T thrown, A ammunition, S
// special) — 5etools ships these as bare codes with no separate rendering,
// so without this a weapon's finesse/versatile/etc traits are invisible.
func itemProperties(obj map[string]any) string {
	codes := map[string]string{"A": "ammunition", "AF": "ammunition", "F": "finesse", "H": "heavy", "L": "light", "LD": "loading", "R": "reach", "RLD": "reload", "S": "special", "T": "thrown", "2H": "two-handed", "V": "versatile"}
	values, _ := obj["property"].([]any)
	var out []string
	for _, v := range values {
		code := stringValue(v)
		if i := strings.Index(code, "|"); i >= 0 {
			code = code[:i]
		}
		name := codes[code]
		if name == "" {
			name = code
		}
		if code == "V" {
			if dmg2 := stringValue(obj["dmg2"]); dmg2 != "" {
				name = fmt.Sprintf("versatile (%s)", dmg2)
			}
		}
		out = append(out, name)
	}
	return strings.Join(out, ", ")
}

func itemMastery(obj map[string]any) string {
	values, _ := obj["mastery"].([]any)
	var out []string
	for _, v := range values {
		s := stringValue(v)
		if i := strings.Index(s, "|"); i >= 0 {
			s = s[:i]
		}
		out = append(out, s)
	}
	return strings.Join(out, ", ")
}

func itemAC(obj map[string]any) string {
	if acSpecial := stringValue(obj["acSpecial"]); acSpecial != "" {
		return renderString(acSpecial)
	}
	return armorClass(obj["ac"])
}

func itemStrength(obj map[string]any) string {
	if str := stringValue(obj["strength"]); str != "" {
		return "Str " + str
	}
	return ""
}

func itemStealth(obj map[string]any) string {
	if obj["stealth"] == true {
		return "Disadvantage on Stealth checks"
	}
	return ""
}

func itemPrerequisite(obj map[string]any) string {
	values, _ := obj["prerequisite"].([]any)
	var parts []string
	for _, v := range values {
		entry, _ := v.(map[string]any)
		for key, val := range entry {
			switch key {
			case "level":
				if lv, ok := val.(map[string]any); ok {
					parts = append(parts, fmt.Sprintf("level %v", lv["level"]))
				}
			case "race":
				if list, ok := val.([]any); ok {
					var names []string
					for _, r := range list {
						if rm, ok := r.(map[string]any); ok {
							names = append(names, stringValue(rm["name"]))
						}
					}
					parts = append(parts, strings.Join(names, " or "))
				}
			case "spellcasting", "spellcasting2020":
				parts = append(parts, "the ability to cast at least one spell")
			case "otherSummary":
				if om, ok := val.(map[string]any); ok {
					parts = append(parts, stringValue(om["entrySummary"]))
				}
			}
		}
	}
	return strings.Join(parts, ", ")
}

// renderBackground writes a background's structured mechanics — ability
// score bonuses (2024), skill/tool proficiencies, a granted feat (2024), and
// starting equipment — ahead of its generic entries (Feature, Specialty
// tables, and so on), which Render's entries walk still handles. Without
// this, a background rendered as nothing but a raw bullet dump of its
// entries, with no distinct mechanical summary the way every other
// player-facing kind gets.
func renderBackground(w io.Writer, obj map[string]any) {
	writeField(w, "Ability Scores", backgroundAbilityScores(obj["ability"]))
	writeField(w, "Skill Proficiencies", backgroundProficiencies(obj["skillProficiencies"]))
	writeField(w, "Tool Proficiencies", backgroundProficiencies(obj["toolProficiencies"]))
	writeField(w, "Languages", backgroundProficiencies(obj["languageProficiencies"]))
	writeField(w, "Feat", backgroundFeats(obj["feats"]))
	if equipment := backgroundEquipmentText(obj["startingEquipment"]); equipment != "" {
		fmt.Fprintf(w, "**Starting Equipment:** %s  \n", equipment)
	}
}

// backgroundAbilityScores renders a 2024 background's ability field — one or
// more {"choose":{"weighted":{"from":[...],"weights":[...]}}} options, each
// describing either "+2 to one, +1 to another" or "+1 to each" among a set
// of abilities a player picks from. Pre-2024 backgrounds have no ability
// field at all, so this returns "" for them.
func backgroundAbilityScores(v any) string {
	values, _ := v.([]any)
	var options []string
	for _, value := range values {
		entry, _ := value.(map[string]any)
		choose, _ := entry["choose"].(map[string]any)
		weighted, _ := choose["weighted"].(map[string]any)
		from, _ := weighted["from"].([]any)
		weightsRaw, _ := weighted["weights"].([]any)
		var names []string
		for _, f := range from {
			names = append(names, abilityFullName(stringValue(f)))
		}
		var weights []int
		for _, wt := range weightsRaw {
			weights = append(weights, integer(wt))
		}
		if len(names) == 0 || len(weights) == 0 {
			continue
		}
		allOnes := true
		for _, wt := range weights {
			if wt != 1 {
				allOnes = false
				break
			}
		}
		namesJoined := strings.Join(names, ", ")
		if allOnes && len(weights) == len(names) {
			options = append(options, fmt.Sprintf("+1 to each of %s", namesJoined))
		} else if len(weights) >= 2 {
			options = append(options, fmt.Sprintf("+%d to one and +%d to another, chosen from %s", weights[0], weights[1], namesJoined))
		}
	}
	return strings.Join(options, "; or ")
}

// backgroundProficiencies renders skillProficiencies/toolProficiencies/
// languageProficiencies — an array of maps whose keys are skill/tool/
// language names (or a choose-any key like "anyGamingSet") and whose values
// are either true (granted outright) or a pick count.
func backgroundProficiencies(v any) string {
	values, _ := v.([]any)
	var out []string
	for _, value := range values {
		obj, _ := value.(map[string]any)
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			name := backgroundProfName(k)
			switch val := obj[k].(type) {
			case bool:
				if val {
					out = append(out, name)
				}
			default:
				if n := integer(val); n > 0 {
					if n == 1 {
						out = append(out, "one "+name)
					} else {
						out = append(out, fmt.Sprintf("%d %s", n, name))
					}
				}
			}
		}
	}
	return strings.Join(out, ", ")
}

func backgroundProfName(key string) string {
	switch key {
	case "anyGamingSet":
		return "any gaming set"
	case "anyMusicalInstrument":
		return "any musical instrument"
	case "anyArtisansTool":
		return "any artisan's tools"
	case "any":
		return "any"
	}
	return title(key)
}

// backgroundFeats renders a 2024 background's granted feat: feats is
// [{"feat name|source": true}].
func backgroundFeats(v any) string {
	values, _ := v.([]any)
	var names []string
	for _, value := range values {
		obj, _ := value.(map[string]any)
		for key, granted := range obj {
			if b, ok := granted.(bool); ok && b {
				name := key
				if i := strings.Index(name, "|"); i >= 0 {
					name = name[:i]
				}
				names = append(names, title(name))
			}
		}
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// backgroundEquipmentText renders a background's startingEquipment — a
// choice between named option sets (e.g. "A"/"B", or the unlettered "_" for
// a set with no alternative), each a list of item refs, flat gp values, and
// freeform "special" gear descriptions.
func backgroundEquipmentText(v any) string {
	list, ok := v.([]any)
	if !ok || len(list) == 0 {
		return ""
	}
	set, ok := list[0].(map[string]any)
	if !ok {
		return ""
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var options []string
	for _, k := range keys {
		items, _ := set[k].([]any)
		text := backgroundEquipmentItems(items)
		if text == "" {
			continue
		}
		if k == "_" {
			options = append(options, text)
			continue
		}
		options = append(options, fmt.Sprintf("(%s) %s", strings.ToUpper(k), text))
	}
	return strings.Join(options, " or ")
}

func backgroundEquipmentItems(items []any) string {
	var parts []string
	for _, it := range items {
		switch val := it.(type) {
		case string:
			// A bare "name|source" ref (no surrounding {@item} tag) appears
			// directly in some background equipment lists.
			if rendered := renderString(val); rendered != val {
				parts = append(parts, rendered)
			} else if i := strings.Index(val, "|"); i >= 0 {
				parts = append(parts, val[:i])
			} else {
				parts = append(parts, val)
			}
		case map[string]any:
			switch {
			case val["item"] != nil:
				name := renderString("{@item " + stringValue(val["item"]) + "}")
				if display := stringValue(val["displayName"]); display != "" {
					name = display
				}
				if cv := integer(val["containsValue"]); cv > 0 {
					name += fmt.Sprintf(" (containing %s)", moneyText(cv))
				}
				parts = append(parts, name)
			case val["special"] != nil:
				parts = append(parts, stringValue(val["special"]))
			case val["equipmentType"] != nil:
				parts = append(parts, equipmentTypeName(stringValue(val["equipmentType"])))
			case val["value"] != nil:
				parts = append(parts, moneyText(integer(val["value"])))
			}
		}
	}
	return strings.Join(parts, ", ")
}

func equipmentTypeName(code string) string {
	switch code {
	case "setGaming":
		return "a gaming set"
	case "setArtisan":
		return "a set of artisan's tools"
	case "instrumentMusical":
		return "a musical instrument"
	}
	return "a " + title(code)
}

func moneyText(copper int) string {
	if copper%100 == 0 {
		return fmt.Sprintf("%d gp", copper/100)
	}
	return fmt.Sprintf("%d cp", copper)
}

// renderFeat writes a feat's mechanical summary — category, prerequisites,
// any ability score increase it grants, and saving throw proficiencies —
// ahead of its entries (Render's generic walk still handles those).
func renderFeat(w io.Writer, obj map[string]any) {
	writeField(w, "Category", featCategoryName(stringValue(obj["category"])))
	writeField(w, "Prerequisite", featPrerequisite(obj["prerequisite"]))
	writeField(w, "Ability Score Increase", featAbilityIncrease(obj["ability"]))
	writeField(w, "Saving Throw Proficiencies", profList(obj["savingThrowProficiencies"]))
	if obj["repeatable"] == true {
		fmt.Fprintln(w, "*This feat can be taken more than once.*")
	}
}

func featCategoryName(code string) string {
	switch code {
	case "G":
		return "General"
	case "O":
		return "Origin"
	case "FS:P", "FS:R", "FS:S":
		return "Fighting Style"
	case "EB":
		return "Epic Boon"
	}
	return code
}

// featPrerequisite renders a feat's prerequisite alternatives — each option
// a level/ability-score/race/spellcasting requirement, or a freeform
// otherSummary — joined as alternatives a player can meet any one of.
func featPrerequisite(v any) string {
	values, _ := v.([]any)
	var options []string
	for _, value := range values {
		entry, _ := value.(map[string]any)
		var parts []string
		if lvl := integer(entry["level"]); lvl > 0 {
			parts = append(parts, fmt.Sprintf("level %d", lvl))
		}
		if abilities, ok := entry["ability"].([]any); ok {
			for _, a := range abilities {
				am, _ := a.(map[string]any)
				for k, val := range am {
					parts = append(parts, fmt.Sprintf("%s %s or higher", abilityFullName(k), scalar(val)))
				}
			}
		}
		if races, ok := entry["race"].([]any); ok {
			var names []string
			for _, r := range races {
				rm, _ := r.(map[string]any)
				names = append(names, stringValue(rm["name"]))
			}
			if len(names) > 0 {
				parts = append(parts, strings.Join(names, " or "))
			}
		}
		if entry["spellcasting"] == true || entry["spellcasting2020"] == true {
			parts = append(parts, "the ability to cast at least one spell")
		}
		if spells, ok := entry["spell"].([]any); ok {
			var names []string
			for _, s := range spells {
				switch v := s.(type) {
				case string:
					name := strings.TrimSuffix(v, "#c")
					if i := strings.Index(name, "|"); i >= 0 {
						name = name[:i]
					}
					names = append(names, "the "+title(name)+" spell")
				case map[string]any:
					if summary := stringValue(v["entrySummary"]); summary != "" {
						names = append(names, summary)
					} else if e := stringValue(v["entry"]); e != "" {
						names = append(names, e)
					}
				}
			}
			if len(names) > 0 {
				parts = append(parts, strings.Join(names, " or "))
			}
		}
		if om, ok := entry["otherSummary"].(map[string]any); ok {
			if s := stringValue(om["entrySummary"]); s != "" {
				parts = append(parts, s)
			}
		}
		if len(parts) > 0 {
			options = append(options, strings.Join(parts, ", "))
		}
	}
	return strings.Join(options, "; or ")
}

// featAbilityIncrease renders a feat's ability field — either a fixed
// increment ([{"str":1}]) or a choice among several abilities
// ([{"choose":{"from":[...],"amount":n}}]).
func featAbilityIncrease(v any) string {
	values, _ := v.([]any)
	var out []string
	for _, value := range values {
		obj, _ := value.(map[string]any)
		if choose, ok := obj["choose"].(map[string]any); ok {
			from, _ := choose["from"].([]any)
			var names []string
			for _, f := range from {
				names = append(names, abilityFullName(stringValue(f)))
			}
			amount := integer(choose["amount"])
			if amount == 0 {
				amount = 1
			}
			if len(names) > 0 {
				out = append(out, fmt.Sprintf("+%d to one of %s", amount, strings.Join(names, ", ")))
			}
			continue
		}
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if n := integer(obj[k]); n != 0 {
				out = append(out, fmt.Sprintf("+%d %s", n, abilityFullName(k)))
			}
		}
	}
	return strings.Join(out, ", ")
}

// renderDeity writes a deity's identity fields — pantheon, alignment, title,
// domains, and symbol — which have no analogue in the generic entries walk
// since a deity entry is almost always empty or purely flavor text.
func renderDeity(w io.Writer, obj map[string]any) {
	writeField(w, "Pantheon", stringValue(obj["pantheon"]))
	writeField(w, "Alignment", monsterAlignment(obj["alignment"]))
	writeField(w, "Title", stringValue(obj["title"]))
	writeField(w, "Domains", stringList(obj["domains"]))
	writeField(w, "Symbol", stringValue(obj["symbol"]))
}

// renderVehicle writes a vehicle's core stats — type, size, terrain,
// capacity, pace, and hull AC/HP — followed by its control/movement/weapon
// components, each with its own AC/HP the way a monster's actions carry
// their own attack line. Ship-scale vehicles (the shape most DMG/GoS
// vehicles use) have no ability scores or a flat AC/HP the way a mundane
// cart or wagon does, so both shapes are handled.
func renderVehicle(w io.Writer, obj map[string]any) {
	writeField(w, "Type", vehicleTypeName(stringValue(obj["vehicleType"])))
	writeField(w, "Size", monsterSize(obj["size"]))
	writeField(w, "Terrain", stringList(obj["terrain"]))
	capacity := joinNonEmpty(", ",
		nonZeroField("Crew", integer(obj["capCrew"])),
		nonZeroField("Passengers", integer(obj["capPassenger"])),
		nonZeroField("Cargo (tons)", integer(obj["capCargo"])),
	)
	writeField(w, "Capacity", capacity)
	if pace := integer(obj["pace"]); pace > 0 {
		writeField(w, "Pace", fmt.Sprintf("%d mph", pace))
	}
	if hull, ok := obj["hull"].(map[string]any); ok {
		writeField(w, "Hull", vehiclePartLine(hull))
	} else {
		writeField(w, "Armor Class", armorClass(obj["ac"]))
		writeField(w, "Hit Points", hitPoints(obj["hp"]))
		writeField(w, "Speed", speed(obj["speed"]))
	}
	for _, section := range []struct{ key, heading string }{
		{"control", "Control"}, {"movement", "Movement"}, {"weapon", "Weapons"},
	} {
		parts, ok := obj[section.key].([]any)
		if !ok || len(parts) == 0 {
			continue
		}
		fmt.Fprintf(w, "\n## %s\n\n", section.heading)
		for _, p := range parts {
			pm, _ := p.(map[string]any)
			name := stringValue(pm["name"])
			if count := integer(pm["count"]); count > 1 {
				name = fmt.Sprintf("%s (×%d)", name, count)
			}
			fmt.Fprintf(w, "**%s.** %s\n\n", name, vehiclePartLine(pm))
			if entries, ok := pm["entries"].([]any); ok {
				renderEntries(w, entries, 0)
			}
		}
	}
}

func vehicleTypeName(code string) string {
	switch code {
	case "SHIP":
		return "Ship"
	case "INFWAR":
		return "Infernal War Machine"
	case "CREATURE":
		return "Creature-drawn vehicle"
	case "OBJECT":
		return "Object"
	}
	return title(code)
}

func vehiclePartLine(part map[string]any) string {
	var pieces []string
	if ac := integer(part["ac"]); ac > 0 {
		pieces = append(pieces, fmt.Sprintf("AC %d", ac))
	}
	if hp := integer(part["hp"]); hp > 0 {
		pieces = append(pieces, fmt.Sprintf("HP %d", hp))
	}
	if dt := integer(part["dt"]); dt > 0 {
		pieces = append(pieces, fmt.Sprintf("Damage Threshold %d", dt))
	}
	if note := stringValue(part["hpNote"]); note != "" {
		pieces = append(pieces, note)
	}
	return strings.Join(pieces, ", ")
}

func nonZeroField(label string, n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf("%s %d", label, n)
}

// renderTrapHazard writes a trap's or hazard's type label ahead of its
// entries (which carry the trigger/effect narrative as freeform prose in
// this data set, so there's no further structured field to extract).
func renderTrapHazard(w io.Writer, obj map[string]any) {
	writeField(w, "Type", trapHazardTypeName(stringValue(obj["trapHazType"])))
}

func trapHazardTypeName(code string) string {
	names := map[string]string{
		"MECH": "Mechanical trap", "MAG": "Magic trap", "SMPL": "Simple trap", "CMPX": "Complex trap",
		"WLD": "Environmental hazard", "WTH": "Weather hazard", "ENV": "Environmental hazard",
		"TRAP": "Trap", "TRP": "Trap", "HAZ": "Hazard", "GEN": "Generic hazard", "EST": "Environmental hazard",
		"HAUNT": "Haunting",
	}
	if name := names[code]; name != "" {
		return name
	}
	return code
}

// renderObject writes an object's (a siege engine, a door, a statue that can
// be attacked, and so on) mechanical summary — size, AC/HP, damage/condition
// immunities — followed by its actionEntries, which use a different shape
// (structured attack blocks) than the entries every other kind falls
// through to, so they're rendered here rather than by Render's generic walk.
func renderObject(w io.Writer, obj map[string]any) {
	writeField(w, "Size", monsterSize(obj["size"]))
	writeField(w, "Type", objectTypeName(stringValue(obj["objectType"])))
	writeField(w, "Armor Class", armorClass(obj["ac"]))
	writeField(w, "Hit Points", hitPoints(obj["hp"]))
	writeField(w, "Damage Immunities", damageTags(obj["immune"]))
	writeField(w, "Damage Resistances", damageTags(obj["resist"]))
	writeField(w, "Damage Vulnerabilities", damageTags(obj["vulnerable"]))
	writeField(w, "Condition Immunities", stringList(obj["conditionImmune"]))
	actions, ok := obj["actionEntries"].([]any)
	if !ok || len(actions) == 0 {
		return
	}
	fmt.Fprintln(w, "\n## Actions")
	for _, a := range actions {
		am, _ := a.(map[string]any)
		if name := renderString(stringValue(am["name"])); name != "" {
			fmt.Fprintf(w, "\n**%s.**\n", name)
		}
		if entries, ok := am["entries"].([]any); ok {
			renderEntries(w, entries, 0)
		}
	}
}

func objectTypeName(code string) string {
	names := map[string]string{"SW": "siege weapon", "GEN": "generic object", "U": "unrigged vehicle"}
	if name := names[code]; name != "" {
		return name
	}
	return code
}

// renderOptionalFeature writes an optional feature's (an Eldritch Invocation,
// a Fighting Style, a Metamagic option, ...) type and prerequisites ahead of
// its entries.
func renderOptionalFeature(w io.Writer, obj map[string]any) {
	writeField(w, "Feature Type", optionalFeatureTypeNames(obj["featureType"]))
	writeField(w, "Prerequisite", featPrerequisite(obj["prerequisite"]))
}

func optionalFeatureTypeNames(v any) string {
	codes := map[string]string{
		"EI": "Eldritch Invocation", "MM": "Metamagic", "MV": "Maneuver", "MV:B": "Maneuver (Battle Master)",
		"MV:C2-UA": "Maneuver", "AI": "Artificer Infusion", "FS:F": "Fighting Style (Fighter)",
		"FS:B": "Fighting Style (Bard)", "FS:P": "Fighting Style (Paladin)", "FS:R": "Fighting Style (Ranger)",
		"FS:S": "Fighting Style", "OR": "Onomancy Resonant", "RN": "Rune Knight Rune", "AS": "Arcane Shot",
		"OTH": "Other", "PB": "Pact Boon", "GENALCHM": "Alchemical Formula", "GENALTPUR": "Alternative Purpose",
	}
	values, _ := v.([]any)
	var names []string
	for _, val := range values {
		code := stringValue(val)
		if name := codes[code]; name != "" {
			names = append(names, name)
		} else {
			names = append(names, code)
		}
	}
	return strings.Join(names, ", ")
}

// renderPsionic writes a psionic talent's or discipline's type, order, focus
// (a discipline's passive benefit while concentrating on it), and its modes
// — each with the psi point cost range that activates it.
func renderPsionic(w io.Writer, obj map[string]any) {
	writeField(w, "Type", psionicTypeName(stringValue(obj["type"])))
	writeField(w, "Order", stringValue(obj["order"]))
	if focus := stringValue(obj["focus"]); focus != "" {
		fmt.Fprintf(w, "**Focus:** %s  \n", renderString(focus))
	}
	modes, ok := obj["modes"].([]any)
	if !ok || len(modes) == 0 {
		return
	}
	fmt.Fprintln(w, "\n## Modes")
	for _, m := range modes {
		mm, _ := m.(map[string]any)
		name := renderString(stringValue(mm["name"]))
		fmt.Fprintf(w, "\n**%s** (%s)  \n", name, psionicModeCost(mm))
		if entries, ok := mm["entries"].([]any); ok {
			renderEntries(w, entries, 0)
		}
	}
}

func psionicTypeName(code string) string {
	switch code {
	case "T":
		return "Talent"
	case "D":
		return "Discipline"
	}
	return code
}

func psionicModeCost(mode map[string]any) string {
	cost, ok := mode["cost"].(map[string]any)
	if !ok {
		return ""
	}
	min := integer(cost["min"])
	max := integer(cost["max"])
	text := fmt.Sprintf("%d psi", min)
	if max > min {
		text = fmt.Sprintf("%d-%d psi", min, max)
	}
	if conc, ok := mode["concentration"].(map[string]any); ok {
		text += fmt.Sprintf(", concentration up to %s %s", scalar(conc["duration"]), stringValue(conc["unit"]))
	}
	return text
}

func variantRuleTypeName(code string) string {
	names := map[string]string{"C": "Core", "O": "Optional", "V": "Variant", "VO": "Variant Optional"}
	if name := names[code]; name != "" {
		return name
	}
	return code
}

func renderRace(w io.Writer, obj map[string]any) {
	writeField(w, "Size", raceSizes(obj["size"]))
	writeField(w, "Speed", speed(obj["speed"]))
	writeField(w, "Ability Scores", raceAbilities(obj["ability"]))
}

// MergeSubrace combines a subrace's own JSON with its parent race's so a
// subrace lookup is self-sufficient. A subrace's entries are deltas against
// the race, not a full trait list: some entries explicitly overwrite a race
// trait by name (5etools' entry.data.overwrite, e.g. Drow's "Superior
// Darkvision" replacing Elf's "Darkvision"), others are pure additions
// (Sunlight Sensitivity, Drow Magic), and everything the subrace doesn't
// touch (Fey Ancestry, Trance, Keen Senses, elf weapon training) only exists
// on the race. Rendering the subrace's obj alone, as Render(kind="subrace")
// used to, silently drops that inherited third category. size/speed/ability
// fall back to the race when the subrace doesn't define its own; ability
// scores add together, since 5e grants both the race's and the subrace's
// bonus.
func MergeSubrace(subraceObj, raceObj map[string]any) map[string]any {
	if raceObj == nil {
		return subraceObj
	}
	merged := make(map[string]any, len(subraceObj)+2)
	for k, v := range subraceObj {
		merged[k] = v
	}
	if _, ok := merged["size"]; !ok {
		merged["size"] = raceObj["size"]
	}
	if _, ok := merged["speed"]; !ok {
		merged["speed"] = raceObj["speed"]
	}
	if ability := mergeRaceAbility(raceObj["ability"], subraceObj["ability"]); ability != nil {
		merged["ability"] = ability
	}
	merged["entries"] = mergeRaceEntries(asEntryList(raceObj["entries"]), asEntryList(subraceObj["entries"]))
	return merged
}

func asEntryList(v any) []any {
	entries, _ := v.([]any)
	return entries
}

func mergeRaceAbility(raceAbility, subraceAbility any) []any {
	race := firstAbilityMap(raceAbility)
	sub := firstAbilityMap(subraceAbility)
	if race == nil && sub == nil {
		return nil
	}
	combined := make(map[string]any, 6)
	for _, key := range []string{"str", "dex", "con", "int", "wis", "cha"} {
		if total := integer(race[key]) + integer(sub[key]); total != 0 {
			combined[key] = json.Number(strconv.Itoa(total))
		}
	}
	return []any{combined}
}

func firstAbilityMap(v any) map[string]any {
	values, _ := v.([]any)
	if len(values) == 0 {
		return nil
	}
	m, _ := values[0].(map[string]any)
	return m
}

// mergeRaceEntries appends the subrace's own entries onto the race's,
// dropping any race entry a subrace entry's data.overwrite names — so a
// replaced trait (e.g. Darkvision -> Superior Darkvision) appears once, in
// its subrace form, instead of both.
func mergeRaceEntries(raceEntries, subEntries []any) []any {
	overwritten := make(map[string]bool, len(subEntries))
	for _, e := range subEntries {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		data, ok := m["data"].(map[string]any)
		if !ok {
			continue
		}
		if name := stringValue(data["overwrite"]); name != "" {
			overwritten[strings.ToLower(name)] = true
		}
	}
	merged := make([]any, 0, len(raceEntries)+len(subEntries))
	for _, e := range raceEntries {
		if m, ok := e.(map[string]any); ok && overwritten[strings.ToLower(stringValue(m["name"]))] {
			continue
		}
		merged = append(merged, e)
	}
	return append(merged, subEntries...)
}

// renderClass writes a class's (or, called with a subclass's own obj and
// obj["subclassFeatures"], a subclass's) mechanical summary — hit die,
// primary ability, saving throws, starting proficiencies, hit points at
// 1st/higher level, proficiency bonus, spellcasting — followed by its
// starting equipment, its spell-slot/cantrip tables if it casts, and its
// full feature-name progression. classFeatures/startingEquipment/
// classTableGroups live outside obj["entries"] (5etools links class
// features by id rather than embedding their text, and per-level resource
// tables are their own field), so unlike every other kind, class gets
// nothing from Render's generic entries walk unless this writes it
// explicitly. The referenced features' own rules text is not embedded here
// — see ClassFeatureRefs and RenderFeatureDetails, which need a store
// lookup this package doesn't have.
func renderClass(w io.Writer, obj map[string]any) {
	writeField(w, "Hit Die", classHitDie(obj["hd"]))
	writeField(w, "Hit Points at 1st Level", classHitPointsFirst(obj))
	writeField(w, "Hit Points at Higher Levels", classHitPointsHigher(obj))
	writeField(w, "Proficiency Bonus", proficiencyBonusByLevel)
	writeField(w, "Primary Ability", abilityEitherList(obj["primaryAbility"]))
	writeField(w, "Saving Throws", abilityAbbrevList(obj["proficiency"]))
	if sp, ok := obj["startingProficiencies"].(map[string]any); ok {
		writeField(w, "Armor", profList(sp["armor"]))
		writeField(w, "Weapons", profList(sp["weapons"]))
		writeField(w, "Tools", profList(sp["tools"]))
		writeField(w, "Skills", profList(sp["skills"]))
	}
	writeField(w, "Spellcasting Ability", abilityFullNameFromAbbrev(obj["spellcastingAbility"]))
	writeField(w, "Spellcasting", castingProgressionLabel(obj["casterProgression"]))
	writeField(w, "Subclass", stringValue(obj["subclassTitle"]))
	if text := classEquipmentText(obj); text != "" {
		fmt.Fprintf(w, "\n## Starting Equipment\n\n%s\n", text)
	}
	for _, table := range classTableGroupsMarkdown(obj["classTableGroups"]) {
		fmt.Fprintf(w, "\n%s", table)
	}
	if table := classLevelTable(obj["classFeatures"]); table != "" {
		fmt.Fprintf(w, "\n## Features by Level\n\n%s\n", table)
	}
	if table := classLevelTable(obj["subclassFeatures"]); table != "" {
		fmt.Fprintf(w, "\n## Subclass Features by Level\n\n%s\n", table)
	}
}

// proficiencyBonusByLevel is the same universal table for every class (5e
// ties proficiency bonus to character level, not class), so it never
// appears in any class's own JSON.
const proficiencyBonusByLevel = "+2 (levels 1-4), +3 (5-8), +4 (9-12), +5 (13-16), +6 (17-20)"

// classHitPointsFirst and classHitPointsHigher are the SRD's standard HP
// formulas derived from the class's hit die — 5etools doesn't store these
// as text since they follow mechanically from hd alone.
func classHitPointsFirst(obj map[string]any) string {
	faces := integer(mapValue(obj["hd"])["faces"])
	if faces == 0 {
		return ""
	}
	return fmt.Sprintf("%d + your Constitution modifier", faces)
}

func classHitPointsHigher(obj map[string]any) string {
	faces := integer(mapValue(obj["hd"])["faces"])
	if faces == 0 {
		return ""
	}
	name := stringValue(obj["name"])
	if name == "" {
		name = "class"
	}
	avg := faces/2 + 1
	return fmt.Sprintf("1d%d (or %d) + your Constitution modifier per %s level after 1st", faces, avg, name)
}

func mapValue(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

// castingProgressionLabel renders casterProgression ("full", "1/2", "1/3",
// "pact") as a readable label; empty for a non-caster (the field is simply
// absent).
func castingProgressionLabel(v any) string {
	switch stringValue(v) {
	case "full":
		return "Full caster"
	case "1/2":
		return "Half caster"
	case "1/3":
		return "Third caster"
	case "pact":
		return "Pact magic"
	case "":
		return ""
	default:
		return title(stringValue(v))
	}
}

func abilityFullNameFromAbbrev(v any) string {
	if s := stringValue(v); s != "" {
		return abilityFullName(s)
	}
	return ""
}

// classTable is one classTableGroups entry (spell slots per spell level,
// cantrips known, a class-specific resource like Fighter's Weapon Mastery
// count) as structured data, so both the plain-text Render() and the
// lipgloss card can format it themselves instead of one baking in Markdown
// pipe syntax the other would have to re-parse.
type classTable struct {
	Title   string
	Headers []string
	Rows    [][]string
}

// classTableGroupsData extracts classTableGroups into classTables, adding a
// Level column since 5etools' own rows omit it. Column labels routinely
// carry a {@filter Label|...} tag (5etools' own cross-reference into its
// spell list UI); renderString strips it to the plain label.
func classTableGroupsData(v any) []classTable {
	groups, _ := v.([]any)
	out := make([]classTable, 0, len(groups))
	for _, g := range groups {
		group := mapValue(g)
		if group == nil {
			continue
		}
		rows, ok := group["rows"].([]any)
		if !ok {
			rows, ok = group["rowsSpellProgression"].([]any)
		}
		if !ok || len(rows) == 0 {
			continue
		}
		labelsRaw, _ := group["colLabels"].([]any)
		headers := make([]string, 0, len(labelsRaw)+1)
		headers = append(headers, "Level")
		for _, l := range labelsRaw {
			headers = append(headers, renderString(stringValue(l)))
		}
		tableRows := make([][]string, len(rows))
		for i, row := range rows {
			cells, _ := row.([]any)
			r := make([]string, 0, len(cells)+1)
			r = append(r, strconv.Itoa(i+1))
			for _, c := range cells {
				r = append(r, scalar(c))
			}
			tableRows[i] = r
		}
		out = append(out, classTable{Title: stringValue(group["title"]), Headers: headers, Rows: tableRows})
	}
	return out
}

// classTableGroupsMarkdown renders classTableGroupsData as Markdown pipe
// tables, for the plain-text Render() path (a script's captured stdout, or
// the chat tool-calling layer's text) — RenderCard builds an actual
// lipgloss table from the same data instead of this text.
func classTableGroupsMarkdown(v any) []string {
	tables := classTableGroupsData(v)
	out := make([]string, 0, len(tables))
	for _, t := range tables {
		var b strings.Builder
		if t.Title != "" {
			fmt.Fprintf(&b, "%s\n", t.Title)
		}
		fmt.Fprintf(&b, "| %s |\n", strings.Join(t.Headers, " | "))
		seps := make([]string, len(t.Headers))
		for i := range seps {
			seps[i] = "---"
		}
		fmt.Fprintf(&b, "| %s |\n", strings.Join(seps, " | "))
		for _, row := range t.Rows {
			fmt.Fprintf(&b, "| %s |\n", strings.Join(row, " | "))
		}
		out = append(out, b.String())
	}
	return out
}

func classEquipmentText(obj map[string]any) string {
	se, ok := obj["startingEquipment"].(map[string]any)
	if !ok {
		return ""
	}
	entries, ok := se["entries"].([]any)
	if !ok || len(entries) == 0 {
		return ""
	}
	var b strings.Builder
	renderEntries(&b, entries, 0)
	return strings.TrimRight(b.String(), "\n")
}

func classHitDie(v any) string {
	obj, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	faces := integer(obj["faces"])
	if faces == 0 {
		return ""
	}
	number := integer(obj["number"])
	if number == 0 {
		number = 1
	}
	return fmt.Sprintf("%dd%d", number, faces)
}

var abilityFullNames = map[string]string{
	"str": "Strength", "dex": "Dexterity", "con": "Constitution",
	"int": "Intelligence", "wis": "Wisdom", "cha": "Charisma",
}

func abilityFullName(abbr string) string {
	if name, ok := abilityFullNames[strings.ToLower(abbr)]; ok {
		return name
	}
	return title(abbr)
}

// abilityEitherList renders primaryAbility ([{"str":true},{"dex":true}]) as
// "Strength or Dexterity" — 5etools lists several abilities there exactly
// when a class lets a player choose which one to prioritize.
func abilityEitherList(v any) string {
	values, _ := v.([]any)
	var out []string
	for _, value := range values {
		obj, _ := value.(map[string]any)
		for _, ab := range []string{"str", "dex", "con", "int", "wis", "cha"} {
			if b, ok := obj[ab].(bool); ok && b {
				out = append(out, abilityFullName(ab))
			}
		}
	}
	return strings.Join(out, " or ")
}

// abilityAbbrevList renders a plain list of ability abbreviations
// (["str","con"], a class's saving throw proficiencies) as full names.
func abilityAbbrevList(v any) string {
	values, _ := v.([]any)
	out := make([]string, 0, len(values))
	for _, value := range values {
		if s := stringValue(value); s != "" {
			out = append(out, abilityFullName(s))
		}
	}
	return strings.Join(out, ", ")
}

// profList renders a proficiency list that mixes plain strings ("light",
// "martial") with 5etools' {"choose":{"from":[...],"count":n}} objects (a
// skill or tool pick) — the shape startingProficiencies and multiclassing
// use throughout.
func profList(v any) string {
	values, _ := v.([]any)
	var out []string
	for _, value := range values {
		switch val := value.(type) {
		case string:
			// A proficiency string is plain ("light", "martial") or carries
			// a {@item}/{@filter} tag ("{@item Thieves' Tools|XPHB}", a
			// martial-weapon subset) — renderString handles both, title()
			// alone would print the former fine but mangle the latter's raw
			// tag syntax.
			if rendered := strings.TrimSpace(renderString(val)); rendered != val {
				out = append(out, rendered)
			} else {
				out = append(out, title(val))
			}
		case map[string]any:
			choose, ok := val["choose"].(map[string]any)
			if !ok {
				continue
			}
			from, _ := choose["from"].([]any)
			count := integer(choose["count"])
			if count == 0 {
				count = 1
			}
			names := make([]string, 0, len(from))
			for _, f := range from {
				names = append(names, title(stringValue(f)))
			}
			out = append(out, fmt.Sprintf("choose %d: %s", count, strings.Join(names, ", ")))
		}
	}
	return strings.Join(out, "; ")
}

// classFeatureLevelRows groups classFeatures or subclassFeatures by level
// into (level, comma-joined feature names) rows, in level order — the whole
// level-1-through-20 progression as data, so both the plain-text table
// (classLevelTable) and the card's lipgloss table can lay it out themselves
// instead of one being built from the other's pre-formatted text. See
// ClassFeatureRefs for the parse this shares.
func classFeatureLevelRows(v any) [][2]string {
	refs := ClassFeatureRefs(v)
	byLevel := map[int][]string{}
	var levels []int
	for _, r := range refs {
		if _, ok := byLevel[r.Level]; !ok {
			levels = append(levels, r.Level)
		}
		byLevel[r.Level] = append(byLevel[r.Level], r.Name)
	}
	sort.Ints(levels)
	rows := make([][2]string, len(levels))
	for i, level := range levels {
		rows[i] = [2]string{strconv.Itoa(level), strings.Join(byLevel[level], ", ")}
	}
	return rows
}

// classLevelTable renders classFeatureLevelRows as a Markdown table, for the
// plain-text Render() path — RenderCard builds an actual lipgloss table from
// the same rows instead of this text.
func classLevelTable(v any) string {
	rows := classFeatureLevelRows(v)
	if len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("| Level | Features |\n| --- | --- |\n")
	for _, row := range rows {
		fmt.Fprintf(&b, "| %s | %s |\n", row[0], row[1])
	}
	return strings.TrimRight(b.String(), "\n")
}

// FeatureRef is one parsed classFeatures or subclassFeatures entry: enough
// to group by level (Name, Level) and enough to look up the entity that
// holds its actual rules text (LookupName, matching the "Name (Class
// Level)" / "Name (Class Subclass Level)" convention parse.disambiguateName
// builds at ingest for kind classFeature / subclassFeature respectively).
type FeatureRef struct {
	Kind           string // "classFeature" or "subclassFeature"
	Name           string
	Class          string
	ClassSource    string
	Subclass       string
	SubclassSource string
	Level          int
}

// LookupName is the entity name a FeatureRef resolves to via
// st.Lookup(ref.Kind, ref.LookupName(), ref.ClassSource-or-SubclassSource).
func (r FeatureRef) LookupName() string {
	if r.Kind == "subclassFeature" {
		return fmt.Sprintf("%s (%s %s %d)", r.Name, r.Class, r.Subclass, r.Level)
	}
	return fmt.Sprintf("%s (%s %d)", r.Name, r.Class, r.Level)
}

// Source is the source id to disambiguate LookupName with, if more than one
// entity shares it.
func (r FeatureRef) Source() string {
	if r.Kind == "subclassFeature" {
		return r.SubclassSource
	}
	return r.ClassSource
}

// ClassFeatureRefs parses a class's classFeatures or a subclass's
// subclassFeatures list — each entry either a plain
// "Name|Class|ClassSource|Level" reference (4 parts) or a
// "Name|Class|ClassSource|Subclass|SubclassSource|Level" one (6 parts, a
// subclass feature referenced from the parent class's own list), or a
// {classFeature: ref} / {subclassFeature: ref} wrapper (a feature gated
// behind choosing a subclass) — into structured refs, in declaration order.
// Level is always the last pipe segment in either shape.
func ClassFeatureRefs(v any) []FeatureRef {
	values, _ := v.([]any)
	refs := make([]FeatureRef, 0, len(values))
	for _, value := range values {
		var ref string
		switch val := value.(type) {
		case string:
			ref = val
		case map[string]any:
			if s := stringValue(val["classFeature"]); s != "" {
				ref = s
			} else {
				ref = stringValue(val["subclassFeature"])
			}
		}
		parts := strings.Split(ref, "|")
		if len(parts) < 4 || parts[0] == "" {
			continue
		}
		level, err := strconv.Atoi(parts[len(parts)-1])
		if err != nil {
			continue
		}
		fr := FeatureRef{Name: parts[0], Class: parts[1], ClassSource: parts[2], Level: level}
		if len(parts) >= 6 {
			fr.Kind = "subclassFeature"
			fr.Subclass = parts[3]
			fr.SubclassSource = parts[4]
		} else {
			fr.Kind = "classFeature"
		}
		refs = append(refs, fr)
	}
	return refs
}

// FeatureLookup resolves one classFeature/subclassFeature entity's decoded
// JSON object by kind/name/source, or ok=false when nothing matches — the
// shape a *store.Store lookup naturally satisfies without this package
// importing store directly (statblock stays a pure JSON transform, shared
// by both the CLI and the chat tool-calling layer without either pulling in
// the other's dependencies).
type FeatureLookup func(kind, name, source string) (obj map[string]any, ok bool)

// RenderFeatureDetails renders each ref's actual rules text (fetched via
// lookup, since classFeatures/subclassFeatures only carry name/level
// references, not the feature's own mechanics) in the order
// ClassFeatureRefs returned them. A lookup miss is skipped silently rather
// than erroring the whole block — a DM reading full text for nineteen of
// twenty features beats losing all of it over one bad reference.
func RenderFeatureDetails(w io.Writer, refs []FeatureRef, lookup FeatureLookup) {
	for _, r := range refs {
		obj, ok := lookupFeature(r, lookup)
		if !ok {
			continue
		}
		fmt.Fprintf(w, "\n### Level %d: %s\n\n", r.Level, r.Name)
		Render(w, r.Kind, obj)
	}
}

// lookupFeature resolves a FeatureRef against a store that only
// disambiguates a classFeature/subclassFeature's name ("Name (Class
// Level)"/"Name (Class Subclass Level)") when the plain name actually
// collided with another feature at ingest — most feature names don't, so
// the plain name is tried first and the disambiguated form is only a
// fallback for the ones that do.
func lookupFeature(r FeatureRef, lookup FeatureLookup) (map[string]any, bool) {
	if obj, ok := lookup(r.Kind, r.Name, r.Source()); ok {
		return obj, true
	}
	return lookup(r.Kind, r.LookupName(), r.Source())
}

type entrySection struct {
	key     string
	heading string
}

func entrySections(kind string) []entrySection {
	if kind == "monster" {
		return []entrySection{
			{key: "trait", heading: "Traits"},
			{key: "spellcasting", heading: "Spellcasting"},
			{key: "action", heading: "Actions"},
			{key: "bonus", heading: "Bonus Actions"},
			{key: "reaction", heading: "Reactions"},
			{key: "legendary", heading: "Legendary Actions"},
			{key: "mythic", heading: "Mythic Actions"},
			{key: "entries"},
		}
	}
	return []entrySection{{key: "entries"}, {key: "entriesHigherLevel", heading: "At Higher Levels"}}
}

func renderEntries(w io.Writer, entries []any, depth int) {
	renderEntriesOpt(w, entries, depth, nil)
}

// renderEntriesOpt is renderEntries with an optional card rendering context:
// nil renders exactly like renderEntries (Markdown, including a table
// entry's pipe syntax); a non-nil cardOpts instead renders a table entry as
// an actual bordered lipgloss table sized to the card, the same way
// renderClassTable already does for a class's level-progression tables —
// otherwise a background's or race's table (e.g. a background's "Suggested
// Characteristics" d8 table) shows up inside a card as literal Markdown pipe
// syntax instead of a real table.
func renderEntriesOpt(w io.Writer, entries []any, depth int, opts *cardOpts) {
	for _, entry := range entries {
		renderEntryOpt(w, entry, depth, opts)
	}
}

func renderEntryOpt(w io.Writer, entry any, depth int, opts *cardOpts) {
	indent := strings.Repeat("  ", depth)
	switch value := entry.(type) {
	case string:
		if opts != nil {
			fmt.Fprintln(w, lipgloss.NewStyle().Width(opts.width).Render(indent+renderString(value)))
		} else {
			fmt.Fprintf(w, "%s%s\n", indent, renderString(value))
		}
	case map[string]any:
		typ := stringValue(value["type"])
		switch typ {
		case "list":
			if items, ok := value["items"].([]any); ok {
				for _, item := range items {
					fmt.Fprintf(w, "%s- ", indent)
					renderBulletOpt(w, item, depth, opts)
				}
			}
		case "table":
			renderTableOpt(w, value, depth, opts)
		case "attack":
			text := renderStrings(value["attackEntries"])
			if label := attackTypeLabel(stringValue(value["attackType"])); label != "" {
				text = label + ": " + text
			}
			if hit := renderStrings(value["hitEntries"]); hit != "" {
				text = strings.TrimSpace(text) + " Hit: " + hit
			}
			fmt.Fprintf(w, "%s%s\n", indent, strings.TrimSpace(text))
		default:
			name := renderString(stringValue(value["name"]))
			if name != "" {
				fmt.Fprintf(w, "%s%s. ", indent, name)
			}
			if nested, ok := value["entries"].([]any); ok {
				if name != "" && len(nested) > 0 {
					renderInlineFirstOpt(w, nested, depth, opts)
				} else {
					renderEntriesOpt(w, nested, depth, opts)
				}
			} else if item := value["entry"]; item != nil {
				renderBulletOpt(w, item, depth, opts)
			} else if name != "" {
				fmt.Fprintln(w)
			}
		}
	}
}

func renderInlineFirstOpt(w io.Writer, entries []any, depth int, opts *cardOpts) {
	if text, ok := entries[0].(string); ok {
		fmt.Fprintln(w, renderString(text))
		renderEntriesOpt(w, entries[1:], depth+1, opts)
		return
	}
	fmt.Fprintln(w)
	renderEntriesOpt(w, entries, depth+1, opts)
}

func renderBulletOpt(w io.Writer, item any, depth int, opts *cardOpts) {
	wrap := func(s string) string {
		if opts == nil {
			return s
		}
		return lipgloss.NewStyle().Width(opts.width).Render(s)
	}
	switch value := item.(type) {
	case string:
		fmt.Fprintln(w, wrap(renderString(value)))
	case map[string]any:
		name := renderString(stringValue(value["name"]))
		if name != "" {
			fmt.Fprintf(w, "%s. ", name)
		}
		if entry, ok := value["entry"].(string); ok {
			fmt.Fprintln(w, wrap(renderString(entry)))
			return
		}
		fmt.Fprintln(w)
		if entries, ok := value["entries"].([]any); ok {
			renderEntriesOpt(w, entries, depth+1, opts)
		}
	default:
		fmt.Fprintln(w, scalar(value))
	}
}

func renderTableOpt(w io.Writer, table map[string]any, depth int, opts *cardOpts) {
	if opts != nil {
		renderCardTable(w, table, opts)
		return
	}
	indent := strings.Repeat("  ", depth)
	if caption := renderString(stringValue(table["caption"])); caption != "" {
		fmt.Fprintf(w, "%s%s\n", indent, caption)
	}
	writeRow := func(row []any) {
		fmt.Fprint(w, indent, "|")
		for _, cell := range row {
			fmt.Fprintf(w, " %s |", strings.ReplaceAll(renderCell(cell), "|", "\\|"))
		}
		fmt.Fprintln(w)
	}
	if labels, ok := table["colLabels"].([]any); ok {
		writeRow(labels)
		fmt.Fprint(w, indent, "|")
		for range labels {
			fmt.Fprint(w, " --- |")
		}
		fmt.Fprintln(w)
	}
	if rows, ok := table["rows"].([]any); ok {
		for _, row := range rows {
			if cells, ok := row.([]any); ok {
				writeRow(cells)
			}
		}
	}
}

// renderCardTable renders a generic (non-class) table entry as a bordered
// lipgloss table, matching renderClassTable's look, instead of the literal
// Markdown pipe syntax renderTableOpt writes for every other caller.
func renderCardTable(w io.Writer, table map[string]any, opts *cardOpts) {
	if caption := renderString(stringValue(table["caption"])); caption != "" {
		fmt.Fprintln(w, lipgloss.NewStyle().Bold(true).Render(caption))
	}
	var headers []string
	if labels, ok := table["colLabels"].([]any); ok {
		for _, l := range labels {
			headers = append(headers, renderCell(l))
		}
	}
	var rows [][]string
	if rs, ok := table["rows"].([]any); ok {
		for _, row := range rs {
			cells, ok := row.([]any)
			if !ok {
				continue
			}
			r := make([]string, len(cells))
			for i, c := range cells {
				r[i] = renderCell(c)
			}
			rows = append(rows, r)
		}
	}
	tbl := lgtable.New().
		Headers(headers...).
		Rows(rows...).
		Border(lipgloss.RoundedBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(opts.accent)).
		BorderRow(false).
		BorderColumn(true).
		StyleFunc(func(row, _ int) lipgloss.Style {
			style := lipgloss.NewStyle().Padding(0, 1)
			if row == lgtable.HeaderRow {
				return style.Bold(true)
			}
			return style
		})
	out := strings.TrimRight(tbl.Render(), "\n")
	if lipgloss.Width(out) > opts.width {
		out = strings.TrimRight(tbl.Width(opts.width).Render(), "\n")
	}
	fmt.Fprintln(w, out)
}

func writeField(w io.Writer, label, value string) {
	if value != "" {
		fmt.Fprintf(w, "**%s:** %s  \n", label, value)
	}
}

func renderString(s string) string {
	text, _ := parse.RenderString(s)
	return text
}

func stringValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func integer(v any) int {
	switch n := v.(type) {
	case json.Number:
		i, _ := strconv.Atoi(n.String())
		return i
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

func scalar(v any) string {
	switch value := v.(type) {
	case string:
		return renderString(value)
	case json.Number:
		return value.String()
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	case int:
		return strconv.Itoa(value)
	case bool:
		return strconv.FormatBool(value)
	case map[string]any:
		if text := stringValue(value["entry"]); text != "" {
			return renderString(text)
		}
	}
	return ""
}

// renderStrings joins a []any of tagged strings (an "attack" entry's
// attackEntries/hitEntries) into one space-separated, tag-expanded line.
func renderStrings(v any) string {
	values, _ := v.([]any)
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if s := stringValue(value); s != "" {
			parts = append(parts, renderString(s))
		}
	}
	return strings.Join(parts, " ")
}

func attackTypeLabel(code string) string {
	switch code {
	case "MW":
		return "Melee Weapon Attack"
	case "RW":
		return "Ranged Weapon Attack"
	case "MS":
		return "Melee Spell Attack"
	case "RS":
		return "Ranged Spell Attack"
	}
	return ""
}

func renderCell(v any) string {
	if values, ok := v.([]any); ok {
		parts := make([]string, 0, len(values))
		for _, value := range values {
			parts = append(parts, scalar(value))
		}
		return strings.Join(parts, "; ")
	}
	return scalar(v)
}

func title(s string) string {
	if s == "" {
		return ""
	}
	runes := []rune(s)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

func ordinal(n int) string {
	if n%100 >= 11 && n%100 <= 13 {
		return fmt.Sprintf("%dth", n)
	}
	suffix := "th"
	switch n % 10 {
	case 1:
		suffix = "st"
	case 2:
		suffix = "nd"
	case 3:
		suffix = "rd"
	}
	return strconv.Itoa(n) + suffix
}

func spellSchool(code string) string {
	schools := map[string]string{"A": "abjuration", "C": "conjuration", "D": "divination", "E": "enchantment", "V": "evocation", "I": "illusion", "N": "necromancy", "T": "transmutation"}
	if school := schools[code]; school != "" {
		return school
	}
	return strings.ToLower(code)
}

func spellTimes(v any) string {
	values, _ := v.([]any)
	var out []string
	for _, value := range values {
		obj, _ := value.(map[string]any)
		number := integer(obj["number"])
		unit := stringValue(obj["unit"])
		if number != 1 && unit != "" {
			unit += "s"
		}
		part := strings.TrimSpace(joinNonEmpty(" ", strconv.Itoa(number), unit))
		if condition := stringValue(obj["condition"]); condition != "" {
			part += ", " + renderString(condition)
		}
		out = append(out, part)
	}
	return strings.Join(out, " or ")
}

func spellRange(v any) string {
	obj, _ := v.(map[string]any)
	typ := stringValue(obj["type"])
	distance, _ := obj["distance"].(map[string]any)
	distanceType := stringValue(distance["type"])
	amount := integer(distance["amount"])
	if distanceType == "self" || distanceType == "touch" || distanceType == "sight" || distanceType == "unlimited" {
		return title(distanceType)
	}
	if amount > 0 {
		shape := ""
		if typ != "point" && typ != "" {
			shape = " " + typ
		}
		return fmt.Sprintf("%d %s%s", amount, distanceType, shape)
	}
	return title(typ)
}

func spellComponents(v any) string {
	obj, _ := v.(map[string]any)
	var parts []string
	if obj["v"] == true {
		parts = append(parts, "V")
	}
	if obj["s"] == true {
		parts = append(parts, "S")
	}
	if material := obj["m"]; material != nil && material != false {
		text := ""
		switch value := material.(type) {
		case string:
			text = renderString(value)
		case map[string]any:
			text = renderString(stringValue(value["text"]))
			if cost := integer(value["cost"]); cost > 0 {
				text = strings.TrimSpace(text + fmt.Sprintf(" (worth %d gp", cost))
				if value["consume"] == true {
					text += ", consumed"
				}
				text += ")"
			} else if value["consume"] == true {
				text = strings.TrimSpace(text + " (consumed)")
			}
		}
		if text != "" {
			parts = append(parts, "M ("+text+")")
		} else {
			parts = append(parts, "M")
		}
	}
	return strings.Join(parts, ", ")
}

func spellDurations(v any) string {
	values, _ := v.([]any)
	var out []string
	for _, value := range values {
		obj, _ := value.(map[string]any)
		typ := stringValue(obj["type"])
		part := title(typ)
		if duration, ok := obj["duration"].(map[string]any); ok {
			amount := integer(duration["amount"])
			unit := stringValue(duration["type"])
			if amount != 1 {
				unit += "s"
			}
			part = strings.TrimSpace(joinNonEmpty(" ", strconv.Itoa(amount), unit))
			if obj["concentration"] == true {
				part = "Concentration, up to " + part
			} else if typ == "timed" {
				part = title(part)
			}
		}
		out = append(out, part)
	}
	return strings.Join(out, " or ")
}

func monsterSize(v any) string {
	var code string
	switch value := v.(type) {
	case string:
		code = value
	case []any:
		if len(value) > 0 {
			code = stringValue(value[0])
		}
	}
	return map[string]string{"T": "Tiny", "S": "Small", "M": "Medium", "L": "Large", "H": "Huge", "G": "Gargantuan"}[code]
}

func raceSizes(v any) string {
	values, _ := v.([]any)
	var sizes []string
	for _, value := range values {
		if size := monsterSize(value); size != "" {
			sizes = append(sizes, size)
		}
	}
	return strings.Join(sizes, " or ")
}

func raceAbilities(v any) string {
	values, _ := v.([]any)
	if len(values) == 0 {
		return ""
	}
	obj, _ := values[0].(map[string]any)
	var out []string
	for _, ability := range []string{"str", "dex", "con", "int", "wis", "cha"} {
		if value := integer(obj[ability]); value != 0 {
			out = append(out, fmt.Sprintf("%s %+d", strings.ToUpper(ability), value))
		}
	}
	return strings.Join(out, ", ")
}

func monsterType(v any) string {
	switch value := v.(type) {
	case string:
		return value
	case map[string]any:
		return stringValue(value["type"])
	}
	return ""
}

func monsterAlignment(v any) string {
	values, _ := v.([]any)
	var codes []string
	for _, value := range values {
		if code := stringValue(value); code != "" {
			codes = append(codes, code)
		}
	}
	if len(codes) == 1 {
		return map[string]string{"U": "unaligned", "A": "any alignment", "N": "neutral"}[codes[0]]
	}
	key := strings.Join(codes, "")
	return map[string]string{"LG": "lawful good", "NG": "neutral good", "CG": "chaotic good", "LN": "lawful neutral", "N": "neutral", "CN": "chaotic neutral", "LE": "lawful evil", "NE": "neutral evil", "CE": "chaotic evil"}[key]
}

func armorClass(v any) string {
	values, ok := v.([]any)
	if !ok {
		return scalar(v)
	}
	var out []string
	for _, value := range values {
		switch ac := value.(type) {
		case json.Number:
			out = append(out, ac.String())
		case map[string]any:
			part := scalar(ac["ac"])
			if from, ok := ac["from"].([]any); ok && len(from) > 0 {
				part += " (" + renderCell(from) + ")"
			}
			out = append(out, part)
		}
	}
	return strings.Join(out, ", ")
}

func hitPoints(v any) string {
	obj, _ := v.(map[string]any)
	average := integer(obj["average"])
	formula := stringValue(obj["formula"])
	if average > 0 && formula != "" {
		return fmt.Sprintf("%d (%s)", average, formula)
	}
	if average > 0 {
		return strconv.Itoa(average)
	}
	return formula
}

func speed(v any) string {
	if _, ok := v.(map[string]any); !ok {
		if value := scalar(v); value != "" {
			return value + " ft."
		}
		return ""
	}
	obj, _ := v.(map[string]any)
	order := []string{"walk", "burrow", "climb", "fly", "swim"}
	var out []string
	for _, kind := range order {
		value := obj[kind]
		if value == nil {
			continue
		}
		feet, condition := speedValue(value)
		label := ""
		if kind != "walk" {
			label = kind + " "
		}
		out = append(out, label+feet+" ft."+condition)
	}
	return strings.Join(out, ", ")
}

func speedValue(value any) (feet, condition string) {
	if detail, ok := value.(map[string]any); ok {
		return scalar(detail["number"]), renderString(stringValue(detail["condition"]))
	}
	return scalar(value), ""
}

// abilityMap renders a monster's save/skill map ({"con":"+6","wis":"+6"}) as
// "Con +6, Wis +6", in ability order for saves and alphabetically for
// skills (5etools uses the map's own key order for neither).
func abilityMap(v any) string {
	obj, ok := v.(map[string]any)
	if !ok || len(obj) == 0 {
		return ""
	}
	order := []string{"str", "dex", "con", "int", "wis", "cha", "acrobatics", "animal handling",
		"arcana", "athletics", "deception", "history", "insight", "intimidation", "investigation",
		"medicine", "nature", "perception", "performance", "persuasion", "religion",
		"sleight of hand", "stealth", "survival"}
	seen := map[string]bool{}
	var out []string
	appendKey := func(key string) {
		if seen[key] {
			return
		}
		if value := stringValue(obj[key]); value != "" {
			out = append(out, title(key)+" "+value)
			seen[key] = true
		}
	}
	for _, key := range order {
		appendKey(key)
	}
	for key := range obj {
		appendKey(key)
	}
	return strings.Join(out, ", ")
}

// damageTags renders a monster's resist/immune/vulnerable list, which mixes
// plain damage-type strings with conditional groupings such as
// {"resist":["bludgeoning","piercing","slashing"],"note":"from nonmagical
// attacks"}.
func damageTags(v any) string {
	values, ok := v.([]any)
	if !ok {
		return ""
	}
	var out []string
	for _, value := range values {
		switch item := value.(type) {
		case string:
			out = append(out, item)
		case map[string]any:
			for _, key := range []string{"resist", "immune", "vulnerable"} {
				if nested, ok := item[key].([]any); ok {
					part := damageTags(nested)
					if note := stringValue(item["note"]); note != "" {
						part += " (" + note + ")"
					}
					out = append(out, part)
				}
			}
		}
	}
	return strings.Join(out, "; ")
}

func stringList(v any) string {
	values, ok := v.([]any)
	if !ok {
		return ""
	}
	var out []string
	for _, value := range values {
		if s := stringValue(value); s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, ", ")
}

func senses(obj map[string]any) string {
	parts := []string{stringList(obj["senses"])}
	if passive := integer(obj["passive"]); passive != 0 {
		parts = append(parts, fmt.Sprintf("passive Perception %d", passive))
	}
	return joinNonEmpty(", ", parts...)
}

// crXP is the standard challenge-rating-to-experience table (5e DMG/XDMG),
// used because a monster's raw JSON almost never carries its own XP value —
// 5etools computes it client-side from cr the same way.
var crXP = map[string]int{
	"0": 10, "1/8": 25, "1/4": 50, "1/2": 100,
	"1": 200, "2": 450, "3": 700, "4": 1100, "5": 1800,
	"6": 2300, "7": 2900, "8": 3900, "9": 5000, "10": 5900,
	"11": 7200, "12": 8400, "13": 10000, "14": 11500, "15": 13000,
	"16": 15000, "17": 18000, "18": 20000, "19": 22000, "20": 25000,
	"21": 33000, "22": 41000, "23": 50000, "24": 62000, "25": 75000,
	"26": 90000, "27": 105000, "28": 120000, "29": 135000, "30": 155000,
}

// XPForCR returns the standard DMG experience award for a challenge rating
// string such as "1/4" or "5", and whether that CR was recognized.
func XPForCR(cr string) (int, bool) {
	xp, ok := crXP[strings.TrimSpace(cr)]
	return xp, ok
}

func challengeRating(v any) string {
	var cr string
	switch value := v.(type) {
	case string:
		cr = value
	case json.Number:
		cr = value.String()
	case map[string]any:
		cr = stringValue(value["cr"])
	}
	if cr == "" {
		return ""
	}
	if xp, ok := XPForCR(cr); ok {
		return fmt.Sprintf("%s (%d XP)", cr, xp)
	}
	return cr
}

var itemTypeCodes = map[string]string{"A": "ammunition", "AF": "ammunition", "AT": "artisan's tools", "G": "adventuring gear", "HA": "heavy armor", "INS": "instrument", "LA": "light armor", "M": "melee weapon", "MA": "medium armor", "P": "potion", "R": "ranged weapon", "RD": "rod", "RG": "ring", "S": "shield", "SC": "scroll", "ST": "staff", "T": "tools", "W": "wand", "WD": "wand", "WOND": "wondrous item"}

// itemTypeName decodes one 5etools item type code, dropping the "|SOURCE"
// suffix a code may carry ("AF|DMG").
func itemTypeName(code string) string {
	if i := strings.Index(code, "|"); i >= 0 {
		code = code[:i]
	}
	if value := itemTypeCodes[code]; value != "" {
		return value
	}
	return strings.ToLower(code)
}

func itemType(obj map[string]any) string {
	if detail := stringValue(obj["typeAlt"]); detail != "" {
		return detail
	}
	code := stringValue(obj["type"])
	// GV ("generic variant") marks a magicvariant template rather than naming
	// a kind of item, so showing it would put a meaningless "gv" where a
	// reader expects "ammunition" or "melee weapon".
	if strings.HasPrefix(code, "GV") {
		return ""
	}
	return itemTypeName(code)
}

// magicVariantApplies names the base item types a magic variant can be
// applied to, from its requires field — the one thing a variant card needs
// that a plain item card does not, since "+1 Ammunition" is a template rather
// than an item you can pick up.
func magicVariantApplies(v any) string {
	requires, _ := v.([]any)
	var names []string
	seen := map[string]bool{}
	for _, item := range requires {
		req, _ := item.(map[string]any)
		name := itemTypeName(stringValue(req["type"]))
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return strings.Join(names, ", ")
}

// itemRarity is an item's rarity, or "" for the "none" placeholder 5etools
// gives every mundane (non-magic) item — showing that literal word in a
// card's summary line reads as if the item had a rarity called "none".
func itemRarity(obj map[string]any) string {
	rarity := stringValue(obj["rarity"])
	if rarity == "none" || rarity == "" {
		return ""
	}
	return rarity
}

func itemDamage(obj map[string]any) string {
	dmg := stringValue(obj["dmg1"])
	if dmg == "" {
		return ""
	}
	types := map[string]string{"B": "bludgeoning", "P": "piercing", "S": "slashing"}
	if typ := types[stringValue(obj["dmgType"])]; typ != "" {
		return dmg + " " + typ
	}
	return dmg
}

func itemWeight(obj map[string]any) string {
	if weight := scalar(obj["weight"]); weight != "" {
		return weight + " lb."
	}
	return ""
}

func itemValue(obj map[string]any) string {
	value := integer(obj["value"])
	if value <= 0 {
		return ""
	}
	if value%100 == 0 {
		return fmt.Sprintf("%d gp", value/100)
	}
	return fmt.Sprintf("%d cp", value)
}

func joinNonEmpty(sep string, values ...string) string {
	var out []string
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	return strings.Join(out, sep)
}

func abilityModifier(score int) int {
	if score >= 10 {
		return (score - 10) / 2
	}
	return (score - 11) / 2
}
