// Package statblock renders one entity's raw 5etools JSON as a readable
// Markdown stat block. It is shared by the CLI's human output and by the
// tool-calling layer in internal/ask: a chat model gets the same clean,
// complete block a person reading the terminal does, instead of parsing the
// raw tagged JSON itself.
package statblock

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/hbaldwin98/5e-cli/internal/parse"
)

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
	case "item", "itemBase":
		renderItem(w, obj)
	case "race":
		renderRace(w, obj)
	case "class":
		renderClass(w, obj)
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
	writeField(w, "Casting Time", spellTimes(obj["time"]))
	writeField(w, "Range", spellRange(obj["range"]))
	writeField(w, "Components", spellComponents(obj["components"]))
	writeField(w, "Duration", spellDurations(obj["duration"]))
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
	line := joinNonEmpty(", ", itemType(obj), stringValue(obj["rarity"]))
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
	writeField(w, "Weight", itemWeight(obj))
	writeField(w, "Value", itemValue(obj))
	writeField(w, "Armor Class", armorClass(obj["ac"]))
}

func renderRace(w io.Writer, obj map[string]any) {
	writeField(w, "Size", raceSizes(obj["size"]))
	writeField(w, "Speed", speed(obj["speed"]))
	writeField(w, "Ability Scores", raceAbilities(obj["ability"]))
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

// classTableGroupsMarkdown renders classTableGroups (a class's per-level
// resource tables — spell slots per spell level, cantrips known, a
// class-specific resource like Fighter's Weapon Mastery count) as Markdown
// tables, prefixed with a Level column since 5etools' own rows omit it.
// Column labels routinely carry a {@filter Label|...} tag (5etools' own
// cross-reference into its spell list UI); renderString strips it to the
// plain label.
func classTableGroupsMarkdown(v any) []string {
	groups, _ := v.([]any)
	out := make([]string, 0, len(groups))
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
		labels, _ := group["colLabels"].([]any)
		table := map[string]any{
			"caption":   stringValue(group["title"]),
			"colLabels": append([]any{"Level"}, labels...),
		}
		leveled := make([]any, len(rows))
		for i, row := range rows {
			cells, _ := row.([]any)
			leveled[i] = append([]any{strconv.Itoa(i + 1)}, cells...)
		}
		table["rows"] = leveled
		var b strings.Builder
		renderTable(&b, table, 0)
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

// classLevelTable groups classFeatures or subclassFeatures by level ("Level
// 3: Fighter Subclass, Ability Score Improvement") so the whole
// level-1-through-20 progression is visible at once instead of only the raw
// reference strings 5etools stores. See ClassFeatureRefs for the parse this
// shares.
func classLevelTable(v any) string {
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
	var b strings.Builder
	for _, level := range levels {
		fmt.Fprintf(&b, "Level %d: %s\n", level, strings.Join(byLevel[level], ", "))
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
		obj, ok := lookup(r.Kind, r.LookupName(), r.Source())
		if !ok {
			continue
		}
		fmt.Fprintf(w, "\n### Level %d: %s\n\n", r.Level, r.Name)
		Render(w, r.Kind, obj)
	}
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
	for _, entry := range entries {
		renderEntry(w, entry, depth)
	}
}

func renderEntry(w io.Writer, entry any, depth int) {
	indent := strings.Repeat("  ", depth)
	switch value := entry.(type) {
	case string:
		fmt.Fprintf(w, "%s%s\n", indent, renderString(value))
	case map[string]any:
		typ := stringValue(value["type"])
		switch typ {
		case "list":
			if items, ok := value["items"].([]any); ok {
				for _, item := range items {
					fmt.Fprintf(w, "%s- ", indent)
					renderBullet(w, item, depth)
				}
			}
		case "table":
			renderTable(w, value, depth)
		default:
			name := renderString(stringValue(value["name"]))
			if name != "" {
				fmt.Fprintf(w, "%s%s. ", indent, name)
			}
			if nested, ok := value["entries"].([]any); ok {
				if name != "" && len(nested) > 0 {
					renderInlineFirst(w, nested, depth)
				} else {
					renderEntries(w, nested, depth)
				}
			} else if item := value["entry"]; item != nil {
				renderBullet(w, item, depth)
			} else if name != "" {
				fmt.Fprintln(w)
			}
		}
	}
}

func renderInlineFirst(w io.Writer, entries []any, depth int) {
	if text, ok := entries[0].(string); ok {
		fmt.Fprintln(w, renderString(text))
		renderEntries(w, entries[1:], depth+1)
		return
	}
	fmt.Fprintln(w)
	renderEntries(w, entries, depth+1)
}

func renderBullet(w io.Writer, item any, depth int) {
	switch value := item.(type) {
	case string:
		fmt.Fprintln(w, renderString(value))
	case map[string]any:
		name := renderString(stringValue(value["name"]))
		if name != "" {
			fmt.Fprintf(w, "%s. ", name)
		}
		if entry, ok := value["entry"].(string); ok {
			fmt.Fprintln(w, renderString(entry))
			return
		}
		fmt.Fprintln(w)
		if entries, ok := value["entries"].([]any); ok {
			renderEntries(w, entries, depth+1)
		}
	default:
		fmt.Fprintln(w, scalar(value))
	}
}

func renderTable(w io.Writer, table map[string]any, depth int) {
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

func itemType(obj map[string]any) string {
	if detail := stringValue(obj["typeAlt"]); detail != "" {
		return detail
	}
	codes := map[string]string{"A": "ammunition", "AF": "ammunition", "AT": "artisan's tools", "G": "adventuring gear", "HA": "heavy armor", "INS": "instrument", "LA": "light armor", "M": "melee weapon", "MA": "medium armor", "P": "potion", "R": "ranged weapon", "RD": "rod", "RG": "ring", "S": "shield", "SC": "scroll", "ST": "staff", "T": "tools", "W": "wand", "WD": "wand", "WOND": "wondrous item"}
	code := stringValue(obj["type"])
	if value := codes[code]; value != "" {
		return value
	}
	return strings.ToLower(code)
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
