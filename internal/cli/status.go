package cli

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"charm.land/bubbles/v2/spinner"
)

// runWithStatus runs work, showing a Bubbles v2 spinner with a phase label
// on an interactive TTY while it runs. On any other destination — --json
// output, a redirected file, a pipe — it calls work directly with no
// animation and no transient output at all: a script or log file must never
// see spinner frames or cursor-control sequences.
//
// setPhase, passed to work, lets a slow multi-step operation (retrieve, then
// embed, then generate) update the label the spinner shows without work
// itself knowing anything about terminals or Bubble Tea. Cancelling ctx
// (Ctrl-C) is work's responsibility to observe — the spinner stops and the
// line is cleared as soon as work returns, whatever it returns.
func runWithStatus(ctx context.Context, w io.Writer, initial string, work func(ctx context.Context, setPhase func(string)) error) error {
	return runStatusLoop(ctx, w, isTTY(w), spinner.MiniDot.FPS, initial, work)
}

// runSpinner starts a Bubbles v2 spinner with a static label on an
// interactive TTY and returns a stop function; on any other destination it
// returns a no-op immediately. Unlike runWithStatus, the spinner runs
// alongside the caller's own work rather than wrapping it — this is what
// streaming needs: a spinner while waiting for the first token, then the
// caller writes streamed deltas itself once tokens start arriving.
//
// stop blocks until the spinner goroutine has made its last write and
// exited, so a write the caller makes right after stop() returns can never
// race the spinner's own writes to the same destination. It is safe to call
// more than once; only the first call has an effect.
func runSpinner(w io.Writer, label string) (stop func()) {
	if !isTTY(w) {
		return func() {}
	}
	return newSpinnerLoop(w, spinner.MiniDot.FPS, label)
}

// newSpinnerLoop is runSpinner's animation, with the TTY gate and tick
// interval factored out so the goroutine and its clear/redraw sequencing can
// run under a test without a real tty and without waiting on real
// spinner-speed ticks.
func newSpinnerLoop(w io.Writer, tick time.Duration, label string) (stop func()) {
	frames := spinner.MiniDot.Frames
	quit := make(chan struct{})
	ack := make(chan struct{})
	go func() {
		defer close(ack)
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		frame := 0
		wrote := false
		clear := func() {
			if wrote {
				fmt.Fprint(w, "\r\x1b[2K")
			}
		}
		for {
			select {
			case <-quit:
				clear()
				return
			case <-ticker.C:
				clear()
				fmt.Fprintf(w, "%s %s", frames[frame], label)
				wrote = true
				frame = (frame + 1) % len(frames)
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(quit)
			<-ack
		})
	}
}

// runStatusLoop is runWithStatus with its TTY gate and tick interval
// injected, so the animation loop itself — not just the "is this a
// terminal" policy decision — can run under a test without a real tty and
// without waiting on real spinner-speed ticks.
func runStatusLoop(ctx context.Context, w io.Writer, enabled bool, tick time.Duration, initial string, work func(ctx context.Context, setPhase func(string)) error) error {
	if !enabled {
		return work(ctx, func(string) {})
	}

	frames := spinner.MiniDot.Frames
	phaseCh := make(chan string, 1)
	phaseCh <- initial
	done := make(chan error, 1)
	go func() {
		done <- work(ctx, func(s string) {
			select {
			case <-phaseCh:
			default:
			}
			phaseCh <- s
		})
	}()

	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	label := initial
	frame := 0
	wrote := false
	clearLine := func() {
		if wrote {
			fmt.Fprint(w, "\r\x1b[2K")
		}
	}
	defer clearLine()
	for {
		select {
		case p := <-phaseCh:
			label = p
		case <-ticker.C:
			clearLine()
			fmt.Fprintf(w, "%s %s", frames[frame], label)
			wrote = true
			frame = (frame + 1) % len(frames)
		case err := <-done:
			return err
		}
	}
}
