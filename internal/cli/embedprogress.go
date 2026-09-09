package cli

import (
	"fmt"
	"io"

	"charm.land/bubbles/v2/progress"
	"github.com/hbaldwin98/5e-cli/internal/ask"
)

// embedProgressRenderer builds an ask.Config.OnProgress implementation that
// draws a determinate Bubbles v2 progress bar on w, redrawing the same
// terminal line as the count advances, and returns nil on any destination
// that is not an interactive terminal. A caller sets Config.OnProgress only
// when this returns non-nil, so Config.Progress's existing plain
// "embedding 40/120" line keeps working unchanged for a redirected file, a
// pipe, or --json's stderr — the same destinations that never see an ask/chat
// spinner either. It is driven manually off ask.EmbedProgress calls rather
// than a full tea.Program, the same pattern runSpinner uses.
func embedProgressRenderer(w io.Writer) func(ask.EmbedProgress) {
	if !isTTY(w) {
		return nil
	}
	return newEmbedProgressRenderer(w)
}

// newEmbedProgressRenderer is embedProgressRenderer's rendering logic with
// the TTY gate factored out, so the redraw/finalize sequencing can run under
// a test without a real tty.
func newEmbedProgressRenderer(w io.Writer) func(ask.EmbedProgress) {
	bar := progress.New(progress.WithWidth(30))
	wrote := false
	return func(p ask.EmbedProgress) {
		if wrote {
			fmt.Fprint(w, "\r\x1b[2K")
		}
		var pct float64
		if p.Total > 0 {
			pct = float64(p.Done) / float64(p.Total)
		}
		fmt.Fprintf(w, "%s %s  %d/%d", p.Phase, bar.ViewAs(pct), p.Done, p.Total)
		wrote = true
		// The build only ever calls back on this one phase today, so
		// Done >= Total is the only completion signal available; finalize
		// the line (a trailing newline, and the next call — a rebuild in a
		// later command, say — starts a fresh line rather than overwriting
		// this finished one) rather than leaving a bar that looks live but
		// will never move again.
		if p.Total > 0 && p.Done >= p.Total {
			fmt.Fprintln(w)
			wrote = false
		}
	}
}
