package cli

import (
	"fmt"
	"io"
	"sync"

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

// spinnerYieldingToProgress runs a "thinking" spinner on w that steps aside
// while onProgress draws an embedding build's bar. The spinner and the bar
// each redraw the current terminal line, so running both at once makes the
// line flash between them; instead the spinner is stopped on the first
// progress update and restarted once the build completes, to cover the
// wait for the first token that follows. It returns the stop func (same
// contract as runSpinner's) and the OnProgress to install in place of
// onProgress, which is nil when onProgress is (no bar to yield to).
func spinnerYieldingToProgress(w io.Writer, label string, onProgress func(ask.EmbedProgress)) (stop func(), progress func(ask.EmbedProgress)) {
	return yieldingSpinner(func() func() { return runSpinner(w, label) }, onProgress)
}

// yieldingSpinner is spinnerYieldingToProgress with the spinner's start
// injected, so the stop/restart sequencing can run under a test.
func yieldingSpinner(start func() (stop func()), onProgress func(ask.EmbedProgress)) (stop func(), progress func(ask.EmbedProgress)) {
	var mu sync.Mutex
	cur := start()
	stopped := false
	stop = func() {
		mu.Lock()
		defer mu.Unlock()
		stopped = true
		cur()
	}
	if onProgress == nil {
		return stop, nil
	}
	progress = func(p ask.EmbedProgress) {
		mu.Lock()
		defer mu.Unlock()
		cur()
		cur = func() {}
		onProgress(p)
		if !stopped && p.Total > 0 && p.Done >= p.Total {
			cur = start()
		}
	}
	return stop, progress
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
