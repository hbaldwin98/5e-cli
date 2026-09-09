package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestStylingEnabled_falseForANonTTYWriter(t *testing.T) {
	var buf bytes.Buffer
	if stylingEnabled(&buf) {
		t.Fatal("a bytes.Buffer is never a terminal, styling should be disabled")
	}
}

func TestStylingEnabled_respectsNoColor(t *testing.T) {
	// isTTY already returns false for a non-*os.File writer, so this only
	// exercises that NO_COLOR is consulted at all, not that it overrides a
	// genuine terminal (that needs a real *os.File connected to a tty, which
	// a unit test can't fake); the *os.File-and-NO_COLOR interaction is
	// covered by construction, not by a test that can observe a real tty.
	t.Setenv("NO_COLOR", "1")
	var buf bytes.Buffer
	if stylingEnabled(&buf) {
		t.Fatal("styling should stay disabled with NO_COLOR set")
	}
}

func TestNewStyles_disabledIsIdentity(t *testing.T) {
	s := newStyles(false)
	for name, got := range map[string]string{
		"Heading": s.Heading.Render("x"),
		"Muted":   s.Muted.Render("x"),
		"Error":   s.Error.Render("x"),
		"Success": s.Success.Render("x"),
	} {
		if got != "x" {
			t.Fatalf("%s: disabled style should render as plain text, got %q", name, got)
		}
	}
}

func TestNewStyles_enabledAddsANSICodes(t *testing.T) {
	s := newStyles(true)
	for name, got := range map[string]string{
		"Heading": s.Heading.Render("x"),
		"Muted":   s.Muted.Render("x"),
		"Error":   s.Error.Render("x"),
		"Success": s.Success.Render("x"),
	} {
		if got == "x" || !strings.Contains(got, "x") {
			t.Fatalf("%s: enabled style should wrap the text in escape codes, got %q", name, got)
		}
	}
}

func TestWriteAskHits_stylingDisabledKeepsPlainOutput(t *testing.T) {
	// Every existing writeAskHits/writeChatTurn test already runs through a
	// bytes.Buffer, which is never a TTY, so styling is already disabled for
	// all of them; this test names that invariant explicitly, so a future
	// change that makes styles() TTY-detection depend on something else
	// (breaking that implicit coverage) fails loudly here instead of only
	// showing up as raw ANSI codes leaking into a script's output.
	var buf bytes.Buffer
	if err := writeAskHits(&buf, nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "\x1b[") {
		t.Fatalf("non-TTY output must never contain ANSI escapes: %q", buf.String())
	}
}
