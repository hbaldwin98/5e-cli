package statblock

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
)

// Field is one label/value line in a card's stat block body, in display
// order (AC, then HP, then Speed, and so on) — not a map, since order is
// part of what makes a stat block readable.
type Field struct{ Label, Value string }

// Ability is one ability score, pre-computed with its modifier so a card
// (or any other structured consumer) never has to re-derive it.
type Ability struct {
	Name  string
	Score int
	Mod   int
}

// Section is one heading plus its rendered body (Traits, Actions, a spell's
// own entries, and so on) — the same walk Render uses, kept as plain text so
// a card can lay it out itself instead of receiving pre-built Markdown.
type Section struct {
	Heading string
	Text    string
}

// kindAccent is the border/heading color per kind, so a monster, a spell,
// and an item read as visually distinct kinds of card at a glance rather
// than identical gray boxes. Colors are 256-palette ANSI indices, matching
// internal/cli/style.go's existing convention (e.g. its error style using
// "9"), so a card stays legible on both light and dark terminal themes.
var kindAccent = map[string]string{
	"monster":    "9",  // red
	"spell":      "13", // magenta
	"item":       "11", // yellow
	"itemBase":   "11",
	"race":       "10", // green
	"class":      "14", // cyan
	"background": "6",  // dark cyan
	"feat":       "3",  // yellow-green
	"deity":      "5",  // magenta
	"vehicle":    "4",  // blue
	"trap":       "1",  // red
	"hazard":     "1",
}

func accent(kind string) string {
	if c, ok := kindAccent[kind]; ok {
		return c
	}
	return "12" // blue, for any kind without a dedicated accent
}

// Summary is the descriptive line under a card's title: size/type/alignment
// for a monster, level and school for a spell, type/rarity/attunement for
// an item. Empty for kinds without one (e.g. race, which has no analogous
// single line).
func Summary(kind string, obj map[string]any) string {
	switch kind {
	case "spell":
		level := integer(obj["level"])
		school := spellSchool(stringValue(obj["school"]))
		if level == 0 {
			return title(school) + " cantrip"
		}
		return fmt.Sprintf("%s-level %s", ordinal(level), school)
	case "monster":
		d := joinNonEmpty(", ", monsterSize(obj["size"]), monsterType(obj["type"]))
		if a := monsterAlignment(obj["alignment"]); a != "" {
			d = joinNonEmpty(", ", d, a)
		}
		return d
	case "item", "itemBase":
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
		return line
	}
	return ""
}

// Fields is the ordered label/value lines Render already computes per kind,
// exposed as structured data instead of pre-formatted Markdown so a card (or
// any other consumer) can lay them out itself.
func Fields(kind string, obj map[string]any) []Field {
	var f []Field
	add := func(label, value string) {
		if value != "" {
			f = append(f, Field{label, value})
		}
	}
	switch kind {
	case "spell":
		castingTime := spellTimes(obj["time"])
		if spellIsRitual(obj) {
			castingTime += " (ritual)"
		}
		add("Casting Time", castingTime)
		add("Range", spellRange(obj["range"]))
		add("Components", spellComponents(obj["components"]))
		add("Duration", spellDurations(obj["duration"]))
		add("Classes", spellClasses(obj["classes"]))
	case "monster":
		add("Armor Class", armorClass(obj["ac"]))
		add("Hit Points", hitPoints(obj["hp"]))
		add("Speed", speed(obj["speed"]))
		add("Saving Throws", abilityMap(obj["save"]))
		add("Skills", abilityMap(obj["skill"]))
		add("Damage Vulnerabilities", damageTags(obj["vulnerable"]))
		add("Damage Resistances", damageTags(obj["resist"]))
		add("Damage Immunities", damageTags(obj["immune"]))
		add("Condition Immunities", stringList(obj["conditionImmune"]))
		add("Senses", senses(obj))
		add("Languages", stringList(obj["languages"]))
		add("Challenge", challengeRating(obj["cr"]))
	case "item", "itemBase":
		add("Damage", itemDamage(obj))
		add("Properties", itemProperties(obj))
		add("Weapon Mastery", itemMastery(obj))
		add("Range", stringValue(obj["range"]))
		add("Weight", itemWeight(obj))
		add("Value", itemValue(obj))
		add("Armor Class", itemAC(obj))
		add("Strength Requirement", itemStrength(obj))
		add("Stealth", itemStealth(obj))
		add("Bonus to AC", scalar(obj["bonusAc"]))
		add("Bonus to Attack Rolls", scalar(obj["bonusWeapon"]))
		add("Bonus to Damage Rolls", scalar(obj["bonusWeaponDamage"]))
		add("Bonus to Spell Attacks", scalar(obj["bonusSpellAttack"]))
		add("Bonus to Saving Throws", scalar(obj["bonusSavingThrow"]))
		add("Prerequisite", itemPrerequisite(obj))
	case "race":
		add("Size", raceSizes(obj["size"]))
		add("Speed", speed(obj["speed"]))
		add("Ability Scores", raceAbilities(obj["ability"]))
	case "background":
		add("Ability Scores", backgroundAbilityScores(obj["ability"]))
		add("Skill Proficiencies", backgroundProficiencies(obj["skillProficiencies"]))
		add("Tool Proficiencies", backgroundProficiencies(obj["toolProficiencies"]))
		add("Languages", backgroundProficiencies(obj["languageProficiencies"]))
		add("Feat", backgroundFeats(obj["feats"]))
		add("Starting Equipment", backgroundEquipmentText(obj["startingEquipment"]))
	case "feat":
		add("Category", featCategoryName(stringValue(obj["category"])))
		add("Prerequisite", featPrerequisite(obj["prerequisite"]))
		add("Ability Score Increase", featAbilityIncrease(obj["ability"]))
		add("Saving Throw Proficiencies", profList(obj["savingThrowProficiencies"]))
	case "deity":
		add("Pantheon", stringValue(obj["pantheon"]))
		add("Alignment", monsterAlignment(obj["alignment"]))
		add("Title", stringValue(obj["title"]))
		add("Domains", stringList(obj["domains"]))
		add("Symbol", stringValue(obj["symbol"]))
	case "vehicle":
		add("Type", vehicleTypeName(stringValue(obj["vehicleType"])))
		add("Size", monsterSize(obj["size"]))
		add("Terrain", stringList(obj["terrain"]))
		if hull, ok := obj["hull"].(map[string]any); ok {
			add("Hull", vehiclePartLine(hull))
		} else {
			add("Armor Class", armorClass(obj["ac"]))
			add("Hit Points", hitPoints(obj["hp"]))
			add("Speed", speed(obj["speed"]))
		}
	case "trap", "hazard":
		add("Type", trapHazardTypeName(stringValue(obj["trapHazType"])))
	case "class":
		add("Hit Die", classHitDie(obj["hd"]))
		add("Hit Points at 1st Level", classHitPointsFirst(obj))
		add("Hit Points at Higher Levels", classHitPointsHigher(obj))
		add("Proficiency Bonus", proficiencyBonusByLevel)
		add("Primary Ability", abilityEitherList(obj["primaryAbility"]))
		add("Saving Throws", abilityAbbrevList(obj["proficiency"]))
		if sp, ok := obj["startingProficiencies"].(map[string]any); ok {
			add("Armor", profList(sp["armor"]))
			add("Weapons", profList(sp["weapons"]))
			add("Tools", profList(sp["tools"]))
			add("Skills", profList(sp["skills"]))
		}
		add("Spellcasting Ability", abilityFullNameFromAbbrev(obj["spellcastingAbility"]))
		add("Spellcasting", castingProgressionLabel(obj["casterProgression"]))
		add("Subclass", stringValue(obj["subclassTitle"]))
	}
	return f
}

// Abilities is a monster's six ability scores with their modifiers
// pre-computed, or nil for a kind (or record) that doesn't carry them.
func Abilities(obj map[string]any) []Ability {
	names := []string{"str", "dex", "con", "int", "wis", "cha"}
	has := false
	for _, n := range names {
		has = has || obj[n] != nil
	}
	if !has {
		return nil
	}
	out := make([]Ability, len(names))
	for i, n := range names {
		score := integer(obj[n])
		out[i] = Ability{Name: strings.ToUpper(n), Score: score, Mod: abilityModifier(score)}
	}
	return out
}

// Sections is Render's entries walk (Traits, Spellcasting, Actions, and so
// on for a monster; Entries/At Higher Levels for anything else) as plain
// text instead of pre-assembled Markdown, so a card can indent and wrap each
// one itself.
func Sections(kind string, obj map[string]any) []Section {
	var out []Section
	if kind == "class" {
		if text := classEquipmentText(obj); text != "" {
			out = append(out, Section{Heading: "Starting Equipment", Text: text})
		}
		for _, table := range classTableGroupsMarkdown(obj["classTableGroups"]) {
			out = append(out, Section{Heading: "Level Progression", Text: strings.TrimRight(table, "\n")})
		}
		if table := classLevelTable(obj["classFeatures"]); table != "" {
			out = append(out, Section{Heading: "Features by Level", Text: table})
		}
		if table := classLevelTable(obj["subclassFeatures"]); table != "" {
			out = append(out, Section{Heading: "Subclass Features by Level", Text: table})
		}
		return out
	}
	for _, s := range entrySections(kind) {
		entries, ok := obj[s.key].([]any)
		if !ok || len(entries) == 0 {
			continue
		}
		var b strings.Builder
		renderEntries(&b, entries, 0)
		text := strings.TrimRight(b.String(), "\n")
		if text == "" {
			continue
		}
		heading := s.heading
		if heading == "" {
			heading = "Description"
		}
		out = append(out, Section{Heading: heading, Text: text})
	}
	return out
}

// RenderCard renders one entity as a bordered stat-block card: a title line
// (name, kind, and source), the kind's summary and fields, an ability-score
// row for a monster, and its sections — the terminal analogue of an actual
// D&D stat block, rather than a Markdown document that happens to describe
// one. width is the total card width including its border; RenderCard clamps
// it to a sane minimum so a very narrow terminal still gets something
// legible instead of a garbled layout.
func RenderCard(kind, name, source string, obj map[string]any, width int) string {
	if width < 40 {
		width = 40
	}
	accentColor := lipgloss.Color(accent(kind))
	inner := width - 4 // border (2) + horizontal padding (2)

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(accentColor)
	metaStyle := lipgloss.NewStyle().Faint(true)
	title := titleStyle.Render(name)
	meta := metaStyle.Render(joinNonEmpty(" · ", kind, source))
	titleLine := joinTitleLine(title, meta, inner)

	var body strings.Builder
	body.WriteString(titleLine)
	body.WriteString("\n")

	if summary := Summary(kind, obj); summary != "" {
		body.WriteString(lipgloss.NewStyle().Italic(true).Width(inner).Render(summary))
		body.WriteString("\n")
	}

	fields := Fields(kind, obj)
	if len(fields) > 0 {
		body.WriteString("\n")
		body.WriteString(renderFieldGrid(fields, inner, accentColor))
	}

	if abilities := Abilities(obj); len(abilities) > 0 {
		body.WriteString("\n\n")
		body.WriteString(renderAbilityRow(abilities, inner, accentColor))
	}

	for _, section := range Sections(kind, obj) {
		body.WriteString("\n\n")
		heading := lipgloss.NewStyle().Bold(true).Foreground(accentColor).Render(strings.ToUpper(section.Heading))
		body.WriteString(heading)
		body.WriteString("\n")
		body.WriteString(lipgloss.NewStyle().Width(inner).Render(section.Text))
	}

	// Style.Width sets the whole block's width, border and padding included
	// — not the content area — so this must be width (the caller's target),
	// not inner (which every piece of body was already wrapped to). Passing
	// inner here shrank the box's real content area below what body was
	// prepared for, forcing every line — including ones already exactly
	// inner runes wide — to wrap again, badly.
	card := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(accentColor).
		Padding(0, 1).
		Width(width).
		Render(strings.TrimRight(body.String(), "\n"))
	return card
}

// joinTitleLine right-aligns meta ("monster · MM") against the left-aligned
// title on one line within width, falling back to stacking them on two
// lines when they don't both fit.
func joinTitleLine(title, meta string, width int) string {
	titleW, metaW := lipgloss.Width(title), lipgloss.Width(meta)
	gap := width - titleW - metaW
	if meta == "" || gap < 1 {
		return title
	}
	return title + strings.Repeat(" ", gap) + meta
}

// renderFieldGrid lays out label/value pairs as a two-column block: labels
// bold and right-padded to the longest label (capped, so one very long
// label name doesn't blow out the whole column), values word-wrapped to
// whatever width remains.
func renderFieldGrid(fields []Field, width int, accentColor color.Color) string {
	labelWidth := 0
	for _, f := range fields {
		if n := len(f.Label); n > labelWidth {
			labelWidth = n
		}
	}
	if labelWidth > width/2 {
		labelWidth = width / 2
	}
	labelStyle := lipgloss.NewStyle().Bold(true).Foreground(accentColor).Width(labelWidth)
	valueWidth := width - labelWidth - 1
	if valueWidth < 10 {
		valueWidth = width
	}
	valueStyle := lipgloss.NewStyle().Width(valueWidth)

	lines := make([]string, len(fields))
	for i, f := range fields {
		label := labelStyle.Render(f.Label)
		value := valueStyle.Render(f.Value)
		lines[i] = lipgloss.JoinHorizontal(lipgloss.Top, label, " ", value)
	}
	return strings.Join(lines, "\n")
}

// renderAbilityRow lays out the six ability scores as a row of small boxed
// cells (name, score, modifier), the way a printed stat block's ability
// table reads, instead of one wide Markdown table row.
func renderAbilityRow(abilities []Ability, width int, accentColor color.Color) string {
	cellWidth := width / len(abilities)
	if cellWidth < 6 {
		cellWidth = 6
	}
	cell := lipgloss.NewStyle().Width(cellWidth).Align(lipgloss.Center)
	nameStyle := cell.Bold(true).Foreground(accentColor)
	scoreStyle := cell

	names := make([]string, len(abilities))
	scores := make([]string, len(abilities))
	for i, a := range abilities {
		names[i] = nameStyle.Render(a.Name)
		scores[i] = scoreStyle.Render(fmt.Sprintf("%d (%+d)", a.Score, a.Mod))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, names...) + "\n" + lipgloss.JoinHorizontal(lipgloss.Top, scores...)
}
