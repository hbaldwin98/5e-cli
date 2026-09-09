package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	lgtable "charm.land/lipgloss/v2/table"
	"github.com/hbaldwin98/5e-cli/internal/search"
	"github.com/hbaldwin98/5e-cli/internal/statblock"
	"github.com/hbaldwin98/5e-cli/internal/store"
)

// statblockCardWidth is the terminal width RenderCard lays a card out to.
// Fixed rather than queried from the terminal, the same convention
// renderMarkdownToString already uses (a hardcoded 100) rather than
// depending on a terminal-size library.
const statblockCardWidth = 96

// writeHumanEntity renders one entity's stat block. On a color-capable
// terminal it's a bordered internal/statblock card — the terminal analogue
// of an actual D&D stat block. Otherwise (a script, a pipe, NO_COLOR) it
// falls back to the same Markdown Render produces for the tool-calling
// layer, since a card is built from ANSI styling that has no business in
// piped output. Both paths share internal/statblock's field extraction, so
// neither can drift from what a chat model sees through the get/encounter
// tools.
func writeHumanEntity(w io.Writer, st *store.Store, e store.Entity, full bool) error {
	obj, err := statblock.Decode(e.JSON)
	if err != nil {
		return fmt.Errorf("decode %s %q: %w", e.Kind, e.Name, err)
	}
	if stylingEnabled(w) {
		if _, err := io.WriteString(w, statblock.RenderCard(e.Kind, e.Name, e.Source, obj, statblockCardWidth)+"\n"); err != nil {
			return err
		}
	} else {
		var markdown bytes.Buffer
		fmt.Fprintf(&markdown, "# %s\n\n*%s | %s*\n\n", e.Name, e.Kind, e.Source)
		statblock.Render(&markdown, e.Kind, obj)
		if err := renderMarkdown(w, markdown.String()); err != nil {
			return err
		}
	}
	if err := writeClassFeatureDetail(w, st, e.Kind, obj, full); err != nil {
		return err
	}
	return writeRaceSubraces(w, st, e.Kind, obj)
}

// writeClassFeatureDetail appends a class's or subclass's referenced
// features' actual rules text below the base card/block, and — for a class
// — the subclasses that exist for it. classFeatures/subclassFeatures in an
// entity's own JSON carry only name/level references
// (statblock.ClassFeatureRefs); the rules text lives in separate
// classFeature/subclassFeature entities that only a store lookup can
// resolve, which is why this isn't part of statblock.Render itself.
//
// Resolving and printing every feature's full rules text is gated on full:
// a class can reference 20+ features, each several paragraphs, which
// buries the class's own mechanical summary the base card already shows.
// Without --full, get class/subclass only lists subclasses/subraces (names,
// not their full text) and points at the command to fetch a feature's or
// subclass's own full detail.
func writeClassFeatureDetail(w io.Writer, st *store.Store, kind string, obj map[string]any, full bool) error {
	if st == nil || (kind != "class" && kind != "subclass") {
		return nil
	}
	if full {
		refsKey := "classFeatures"
		if kind == "subclass" {
			refsKey = "subclassFeatures"
		}
		if refs := statblock.ClassFeatureRefs(obj[refsKey]); len(refs) > 0 {
			var b bytes.Buffer
			b.WriteString("\n## Feature Details\n")
			statblock.RenderFeatureDetails(&b, refs, classFeatureLookup(st))
			if err := renderMarkdown(w, b.String()); err != nil {
				return err
			}
		}
	}
	if kind != "class" {
		return nil
	}
	className, _ := obj["name"].(string)
	subclasses := classSubclasses(st, className)
	if len(subclasses) == 0 {
		return nil
	}
	var b bytes.Buffer
	b.WriteString("\n## Subclasses\n\n")
	for _, sc := range subclasses {
		fmt.Fprintf(&b, "- %s (%s) — `5e get subclass \"%s\" --source %s` for its full features\n", sc.Name, sc.Source, sc.Name, sc.Source)
	}
	if !full {
		b.WriteString("\nPass --full for every feature's rules text inline, or look one up by name directly (e.g. `5e get classFeature \"Second Wind\"`) — add \" (Class Level)\" only if the plain name comes back ambiguous.\n")
	}
	return renderMarkdown(w, b.String())
}

// classFeatureLookup adapts st.Lookup to statblock.FeatureLookup so
// RenderFeatureDetails can resolve a classFeature/subclassFeature
// reference's own entity without internal/statblock importing store
// directly — it stays a pure JSON transform shared by the CLI and the chat
// tool-calling layer.
func classFeatureLookup(st *store.Store) statblock.FeatureLookup {
	return func(kind, name, source string) (map[string]any, bool) {
		ents, err := st.Lookup(kind, name, source)
		if err != nil || len(ents) == 0 {
			return nil, false
		}
		obj, err := statblock.Decode(ents[0].JSON)
		if err != nil {
			return nil, false
		}
		return obj, true
	}
}

// classSubclasses finds every subclass entity for a class by name. subclass
// rows carry no dedicated store filter for their parent class (only
// FilteredNames' generic kind/source/SRD filter), so the only way to find
// them is to check each subclass candidate's own className field.
func classSubclasses(st *store.Store, className string) []store.Entity {
	if className == "" {
		return nil
	}
	names, err := st.FilteredNames(store.NameFilter{Kind: "subclass"})
	if err != nil {
		return nil
	}
	var out []store.Entity
	for _, n := range names {
		ents, err := st.Lookup("subclass", n.Name, n.Source)
		if err != nil || len(ents) == 0 {
			continue
		}
		obj, err := statblock.Decode(ents[0].JSON)
		if err != nil {
			continue
		}
		cn, _ := obj["className"].(string)
		if strings.EqualFold(cn, className) {
			out = append(out, ents[0])
		}
	}
	return out
}

// writeRaceSubraces appends the subraces that exist for a race, the same
// way writeClassFeatureDetail lists a class's subclasses — subrace entities
// carry no dedicated store filter by parent race (only their own raceName
// field), so listing them for a base race is otherwise impossible without
// already knowing every subrace name.
func writeRaceSubraces(w io.Writer, st *store.Store, kind string, obj map[string]any) error {
	if st == nil || kind != "race" {
		return nil
	}
	raceName, _ := obj["name"].(string)
	subraces := raceSubraceEntities(st, raceName)
	if len(subraces) == 0 {
		return nil
	}
	var b bytes.Buffer
	b.WriteString("\n## Subraces\n\n")
	for _, sr := range subraces {
		fmt.Fprintf(&b, "- %s (%s) — `5e get subrace \"%s\" --source %s` for its full traits\n", sr.Name, sr.Source, sr.Name, sr.Source)
	}
	return renderMarkdown(w, b.String())
}

// raceSubraceEntities finds every subrace entity for a race by name,
// checking each subrace candidate's own raceName field the same way
// classSubclasses checks className.
func raceSubraceEntities(st *store.Store, raceName string) []store.Entity {
	if raceName == "" {
		return nil
	}
	names, err := st.FilteredNames(store.NameFilter{Kind: "subrace"})
	if err != nil {
		return nil
	}
	var out []store.Entity
	for _, n := range names {
		ents, err := st.Lookup("subrace", n.Name, n.Source)
		if err != nil || len(ents) == 0 {
			continue
		}
		obj, err := statblock.Decode(ents[0].JSON)
		if err != nil {
			continue
		}
		rn, _ := obj["raceName"].(string)
		if strings.EqualFold(rn, raceName) {
			out = append(out, ents[0])
		}
	}
	return out
}

func writeSearchResults(w io.Writer, hits []search.Hit) error {
	cols := []tableColumn{
		{Header: "Kind", Width: 12},
		{Header: "Name", Width: 28},
		{Header: "Source", Width: 8},
		{Header: "Match", Width: 42},
	}
	rows := make([][]string, len(hits))
	for i, hit := range hits {
		rows[i] = []string{hit.Kind, hit.Name, hit.Source, hit.Snippet}
	}
	return writeTable(w, cols, rows)
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// tableColumn describes one column of a fixed-width table. Width is a rune
// count, not a byte count, so multi-byte content still lines up.
type tableColumn struct {
	Header string
	Width  int
	// Right right-aligns the column (numeric columns: a roll, a score).
	Right bool
}

// tableBorderColor is the muted, low-contrast border every rendered table
// shares, so a table reads as one consistent visual family across every
// command instead of each one inventing its own look.
var tableBorderColor = lipgloss.Color("240")

// writeTable renders headers and rows as a table — bordered lipgloss on a
// styled terminal, a plain fixed-width table otherwise (a pipe, a script,
// NO_COLOR) — and writes it to w. Every table-shaped command in this package
// goes through this one helper (directly or via renderTable) so a column
// such as Source is exactly as wide in every table it appears in, on every
// run, instead of a rendering library auto-sizing it from whatever that
// particular call's content happened to be — which is why the same
// command's tables used to visibly resize between runs.
func writeTable(w io.Writer, cols []tableColumn, rows [][]string) error {
	_, err := io.WriteString(w, renderTable(cols, rows, stylingEnabled(w))+"\n")
	return err
}

// renderTable is writeTable without deciding how (or whether) styling
// applies — for a caller (the chat TUI, a rolled-table report) that already
// knows its own styling policy, the way renderRandomTable and
// renderEncounterResults do for the rest of their output.
func renderTable(cols []tableColumn, rows [][]string, styled bool) string {
	headers := make([]string, len(cols))
	for i, c := range cols {
		headers[i] = padCell(c.Header, c.Width, c.Right)
	}
	body := make([][]string, len(rows))
	for i, row := range rows {
		cells := make([]string, len(cols))
		for j, c := range cols {
			var v string
			if j < len(row) {
				v = row[j]
			}
			cells[j] = padCell(v, c.Width, c.Right)
		}
		body[i] = cells
	}
	if !styled {
		var b strings.Builder
		b.WriteString(strings.Join(headers, "  "))
		for _, row := range body {
			b.WriteString("\n")
			b.WriteString(strings.Join(row, "  "))
		}
		return b.String()
	}
	t := lgtable.New().
		Headers(headers...).
		Rows(body...).
		Border(lipgloss.RoundedBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(tableBorderColor)).
		BorderRow(false).
		BorderColumn(true).
		StyleFunc(func(row, _ int) lipgloss.Style {
			style := lipgloss.NewStyle().Padding(0, 1)
			if row == lgtable.HeaderRow {
				return style.Bold(true)
			}
			return style
		})
	return strings.TrimRight(t.Render(), "\n")
}

// padCell collapses s to one line and pads or truncates it to exactly width
// runes, left- or right-aligned, so every row's column is exactly as wide as
// its declared Width — a value longer than its column is truncated with an
// ellipsis rather than being allowed to widen the column past its fixed
// size, which is what kept the same table's width jumping between runs.
func padCell(s string, width int, right bool) string {
	r := []rune(oneLine(s))
	if width <= 0 {
		return string(r)
	}
	if len(r) > width {
		if width == 1 {
			r = r[:1]
		} else {
			r = append(r[:width-1], '…')
		}
	}
	pad := strings.Repeat(" ", width-len(r))
	if right {
		return pad + string(r)
	}
	return string(r) + pad
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
	out, err := renderMarkdownToString(markdown, 100)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, out)
	return err
}

// ttyAnswerRenderer returns a function applying renderMarkdown's own
// TTY policy — glamour-rendered on a real terminal, passed through
// unchanged otherwise — to a string instead of writing straight to w. It
// exists for a caller assembling a larger formatted block itself (like
// writeChatSession, which interleaves each turn's answer with its question
// and citations) rather than handing the whole thing to renderMarkdown at
// once.
func ttyAnswerRenderer(w io.Writer) func(string) string {
	if !isTTY(w) {
		return func(s string) string { return s }
	}
	return func(s string) string {
		rendered, err := renderMarkdownToString(s, 100)
		if err != nil {
			return s
		}
		return rendered
	}
}

// renderMarkdownToString renders markdown to ANSI-styled text at width,
// unconditionally — unlike renderMarkdown, it has no writer to run isTTY
// against, so the caller (the chat TUI, which is only ever running on a
// real terminal in the first place) decides whether rendering makes sense.
func renderMarkdownToString(markdown string, width int) (string, error) {
	if width <= 0 {
		width = 100
	}
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStylePath("dark"),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return "", err
	}
	return renderer.Render(markdown)
}
