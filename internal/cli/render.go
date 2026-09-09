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
func writeHumanEntity(w io.Writer, e store.Entity) error {
	obj, err := statblock.Decode(e.JSON)
	if err != nil {
		return fmt.Errorf("decode %s %q: %w", e.Kind, e.Name, err)
	}
	if stylingEnabled(w) {
		_, err := io.WriteString(w, statblock.RenderCard(e.Kind, e.Name, e.Source, obj, statblockCardWidth)+"\n")
		return err
	}

	var markdown bytes.Buffer
	fmt.Fprintf(&markdown, "# %s\n\n*%s | %s*\n\n", e.Name, e.Kind, e.Source)
	statblock.Render(&markdown, e.Kind, obj)
	return renderMarkdown(w, markdown.String())
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
