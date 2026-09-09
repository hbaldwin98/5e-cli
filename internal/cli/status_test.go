package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRunStatusLoop_disabledRunsWorkWithNoOutput(t *testing.T) {
	var buf bytes.Buffer
	var gotSetPhase bool
	err := runStatusLoop(context.Background(), &buf, false, time.Millisecond, "working", func(_ context.Context, setPhase func(string)) error {
		gotSetPhase = setPhase != nil
		setPhase("phase two") // must not panic or block even though nothing reads it
		return errors.New("boom")
	})
	if err == nil || err.Error() != "boom" {
		t.Fatalf("want work's own error returned, got %v", err)
	}
	if !gotSetPhase {
		t.Fatal("setPhase should never be nil, even when disabled")
	}
	if buf.Len() != 0 {
		t.Fatalf("disabled status must never write anything: %q", buf.String())
	}
}

func TestRunStatusLoop_enabledAnimatesAndClearsOnExit(t *testing.T) {
	var buf bytes.Buffer
	started := make(chan struct{})
	err := runStatusLoop(context.Background(), &buf, true, time.Millisecond, "retrieving", func(_ context.Context, setPhase func(string)) error {
		close(started)
		time.Sleep(20 * time.Millisecond) // long enough for several ticks
		setPhase("generating")
		time.Sleep(10 * time.Millisecond)
		return nil
	})
	<-started
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "retrieving") {
		t.Fatalf("want the initial phase label rendered, got %q", out)
	}
	if !strings.Contains(out, "generating") {
		t.Fatalf("want the updated phase label rendered, got %q", out)
	}
	if !strings.Contains(out, "\x1b[2K") {
		t.Fatalf("want the line cleared between frames and on exit, got %q", out)
	}
	// The loop clears on every redraw and once more on exit; the terminal
	// should be left with no leftover spinner frame, i.e. the output ends
	// in a clear sequence.
	if !strings.HasSuffix(out, "\r\x1b[2K") {
		t.Fatalf("want the status line cleared on exit, got %q", out)
	}
}

func TestRunStatusLoop_enabledPropagatesWorkError(t *testing.T) {
	var buf bytes.Buffer
	err := runStatusLoop(context.Background(), &buf, true, time.Millisecond, "working", func(context.Context, func(string)) error {
		return errors.New("rate limited")
	})
	if err == nil || err.Error() != "rate limited" {
		t.Fatalf("got %v", err)
	}
}

func TestRunSpinner_nonTTYIsANoop(t *testing.T) {
	var buf bytes.Buffer
	stop := runSpinner(&buf, "thinking")
	stop()
	stop() // must be safe to call more than once
	if buf.Len() != 0 {
		t.Fatalf("non-TTY spinner must never write anything: %q", buf.String())
	}
}

func TestNewSpinnerLoop_animatesUntilStoppedThenClears(t *testing.T) {
	var buf bytes.Buffer
	stop := newSpinnerLoop(&buf, time.Millisecond, "thinking")
	time.Sleep(20 * time.Millisecond) // long enough for several ticks
	stop()
	stop() // must be safe to call more than once, and not race the first call

	out := buf.String()
	if !strings.Contains(out, "thinking") {
		t.Fatalf("want the label rendered, got %q", out)
	}
	if !strings.HasSuffix(out, "\r\x1b[2K") {
		t.Fatalf("want the line cleared on stop, got %q", out)
	}
}

func TestNewSpinnerLoop_stopBlocksUntilTheLastWrite(t *testing.T) {
	// stop() is documented to block until the goroutine's last write, so a
	// write the caller makes immediately after stop() returns is race-free.
	// Run under -race: any write ordering violation fails the build, not
	// this assertion.
	var buf bytes.Buffer
	stop := newSpinnerLoop(&buf, time.Millisecond, "thinking")
	time.Sleep(5 * time.Millisecond)
	stop()
	buf.WriteString("caller wrote this safely")
	if !strings.HasSuffix(buf.String(), "caller wrote this safely") {
		t.Fatalf("got %q", buf.String())
	}
}

func TestRunStatusLoop_stopsWhenContextIsCancelled(t *testing.T) {
	var buf bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()
	err := runStatusLoop(ctx, &buf, true, time.Millisecond, "working", func(ctx context.Context, _ func(string)) error {
		<-ctx.Done()
		return ctx.Err()
	})
	if err != context.Canceled {
		t.Fatalf("got %v", err)
	}
}
