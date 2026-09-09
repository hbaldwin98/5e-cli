package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/hbaldwin98/5e-cli/internal/ask"
)

func TestEmbedProgressRenderer_nilOnNonTTY(t *testing.T) {
	var buf bytes.Buffer
	if r := embedProgressRenderer(&buf); r != nil {
		t.Fatal("a non-TTY destination must get no renderer, so the caller falls back to the plain-line Progress writer")
	}
}

func TestNewEmbedProgressRenderer_redrawsAndFinalizes(t *testing.T) {
	var buf bytes.Buffer
	render := newEmbedProgressRenderer(&buf)

	render(ask.EmbedProgress{Done: 10, Total: 100, Phase: "embedding"})
	first := buf.String()
	if !strings.Contains(first, "embedding") || !strings.Contains(first, "10/100") {
		t.Fatalf("want the phase and counts rendered, got %q", first)
	}
	if strings.Contains(first, "\r\x1b[2K") {
		t.Fatalf("the first frame has nothing to clear yet, got %q", first)
	}

	render(ask.EmbedProgress{Done: 50, Total: 100, Phase: "embedding"})
	second := buf.String()
	if !strings.Contains(second, "\r\x1b[2K") {
		t.Fatalf("a later frame should clear the previous one first, got %q", second)
	}
	if !strings.Contains(second, "50/100") {
		t.Fatalf("want the updated counts rendered, got %q", second)
	}
	if strings.HasSuffix(second, "\n") {
		t.Fatalf("an in-progress frame must not finalize with a newline, got %q", second)
	}

	render(ask.EmbedProgress{Done: 100, Total: 100, Phase: "embedding"})
	final := buf.String()
	if !strings.HasSuffix(final, "100/100\n") {
		t.Fatalf("the completing frame should finalize with a trailing newline, got %q", final)
	}

	// A fresh build afterward starts a new line instead of overwriting the
	// finished one from before.
	render(ask.EmbedProgress{Done: 1, Total: 10, Phase: "embedding"})
	afterDone := strings.TrimPrefix(buf.String(), final)
	if strings.HasPrefix(afterDone, "\r\x1b[2K") {
		t.Fatalf("a new build should not clear the previous build's finished line, got %q", afterDone)
	}
}
