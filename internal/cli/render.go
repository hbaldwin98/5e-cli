package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

	"charm.land/glamour/v2"
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
	var markdown bytes.Buffer
	fmt.Fprintln(&markdown, "| Kind | Name | Source | Match |")
	fmt.Fprintln(&markdown, "| --- | --- | --- | --- |")
	for _, hit := range hits {
		fmt.Fprintf(&markdown, "| %s | %s | %s | %s |\n", markdownCell(hit.Kind), markdownCell(hit.Name), markdownCell(hit.Source), markdownCell(oneLine(hit.Snippet)))
	}
	return renderMarkdown(w, markdown.String())
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
