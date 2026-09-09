package cli

import (
	"io"
	"os"

	"charm.land/lipgloss/v2"
)

// styleSet is the shared Lip Gloss vocabulary for human CLI output:
// headings, muted metadata, and error/success confirmations. One small
// vocabulary applied everywhere keeps ask, chat, and session-management
// output visually consistent instead of each command inventing its own ad
// hoc formatting. It deliberately does not cover Glamour-rendered answers —
// renderMarkdown already owns that.
type styleSet struct {
	Heading lipgloss.Style
	Muted   lipgloss.Style
	Error   lipgloss.Style
	Success lipgloss.Style
}

// stylingEnabled reports whether w should receive ANSI styling: an
// interactive terminal (never a redirected/piped destination or --json
// output, which isTTY already excludes) with NO_COLOR unset. NO_COLOR is
// checked independently of terminal color-capability detection so a user
// who sets it gets plain output even on a color-capable terminal.
func stylingEnabled(w io.Writer) bool {
	return isTTY(w) && os.Getenv("NO_COLOR") == ""
}

// styles builds the vocabulary for w. When styling is disabled, every field
// is lipgloss's zero-value Style, whose Render is the identity function, so
// callers can use it unconditionally without an enabled/disabled branch of
// their own — a script's captured output or --json is never touched, and a
// caller without color support never sees raw escape codes.
func styles(w io.Writer) styleSet {
	return newStyles(stylingEnabled(w))
}

func newStyles(enabled bool) styleSet {
	if !enabled {
		return styleSet{}
	}
	return styleSet{
		Heading: lipgloss.NewStyle().Bold(true),
		Muted:   lipgloss.NewStyle().Faint(true),
		Error:   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9")),
		Success: lipgloss.NewStyle().Foreground(lipgloss.Color("10")),
	}
}
