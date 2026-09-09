package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode"

	"charm.land/glamour/v2"
	"github.com/hbaldwin98/5e-cli/internal/parse"
	"github.com/hbaldwin98/5e-cli/internal/search"
	"github.com/hbaldwin98/5e-cli/internal/store"
)

func writeHumanEntity(w io.Writer, e store.Entity) error {
	dec := json.NewDecoder(bytes.NewReader(e.JSON))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return fmt.Errorf("decode %s %q: %w", e.Kind, e.Name, err)
	}

	var markdown bytes.Buffer
	fmt.Fprintf(&markdown, "# %s\n\n*%s | %s*\n\n", e.Name, e.Kind, e.Source)
	switch e.Kind {
	case "spell":
		renderSpell(&markdown, obj)
	case "monster":
		renderMonster(&markdown, obj)
	case "item", "itemBase":
		renderItem(&markdown, obj)
	case "race":
		renderRace(&markdown, obj)
	}

	sections := entrySections(e.Kind)
	for _, section := range sections {
		if entries, ok := obj[section.key].([]any); ok && len(entries) > 0 {
			if section.heading != "" {
				fmt.Fprintf(&markdown, "\n## %s\n\n", section.heading)
			}
			renderEntries(&markdown, entries, 0)
		}
	}
	return renderMarkdown(w, markdown.String())
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
	alignment := monsterAlignment(obj["alignment"])
	if alignment != "" {
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
}

func renderRace(w io.Writer, obj map[string]any) {
	writeField(w, "Size", raceSizes(obj["size"]))
	writeField(w, "Speed", speed(obj["speed"]))
	writeField(w, "Ability Scores", raceAbilities(obj["ability"]))
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

func writeSearchResults(w io.Writer, hits []search.Hit) error {
	var markdown bytes.Buffer
	fmt.Fprintln(&markdown, "| Kind | Name | Source | Match |")
	fmt.Fprintln(&markdown, "| --- | --- | --- | --- |")
	for _, hit := range hits {
		fmt.Fprintf(&markdown, "| %s | %s | %s | %s |\n", markdownCell(hit.Kind), markdownCell(hit.Name), markdownCell(hit.Source), markdownCell(oneLine(hit.Snippet)))
	}
	return renderMarkdown(w, markdown.String())
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

func joinNonEmpty(sep string, values ...string) string {
	var out []string
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	return strings.Join(out, sep)
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func markdownCell(s string) string {
	return strings.ReplaceAll(oneLine(s), "|", "\\|")
}

// isTTY reports whether w is an interactive terminal: a real *os.File
// connected to a character device, not redirected to a file or pipe, and not
// a "dumb" terminal that can't render ANSI. Streaming output, spinners, and
// Glamour rendering are all gated on this — none of them belong in --json
// output, a script's captured stdout, or a CI log.
func isTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && isTTYFile(f)
}

// isTTYReader is isTTY for an input source: whether r is an interactive
// terminal's stdin, as opposed to scripted or piped input. The chat
// workspace (#44) needs this in addition to isTTY(stdout) — a TUI reading
// its "keystrokes" from a script's piped stdin would hang or misbehave, so
// both ends of the terminal must be interactive before it activates.
func isTTYReader(r io.Reader) bool {
	f, ok := r.(*os.File)
	return ok && isTTYFile(f)
}

func isTTYFile(f *os.File) bool {
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func renderMarkdown(w io.Writer, markdown string) error {
	if !isTTY(w) {
		_, err := io.WriteString(w, markdown)
		return err
	}
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStylePath("dark"),
		glamour.WithWordWrap(100),
	)
	if err != nil {
		return err
	}
	out, err := renderer.Render(markdown)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, out)
	return err
}

func abilityModifier(score int) int {
	if score >= 10 {
		return (score - 10) / 2
	}
	return (score - 11) / 2
}
