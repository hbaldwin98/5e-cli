package cli

import (
	"testing"

	"github.com/hbaldwin98/5e-cli/internal/ask"
)

func TestYieldingSpinner_stepsAsideForTheBarAndResumesAfterTheBuild(t *testing.T) {
	var events []string
	start := func() func() {
		events = append(events, "spin")
		return func() { events = append(events, "halt") }
	}
	stop, progress := yieldingSpinner(start, func(p ask.EmbedProgress) {
		events = append(events, "bar")
	})
	progress(ask.EmbedProgress{Done: 0, Total: 2, Phase: "embedding"})
	progress(ask.EmbedProgress{Done: 1, Total: 2, Phase: "embedding"})
	progress(ask.EmbedProgress{Done: 2, Total: 2, Phase: "embedding"})
	stop()

	want := []string{"spin", "halt", "bar", "bar", "bar", "spin", "halt"}
	if len(events) != len(want) {
		t.Fatalf("got %v, want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("got %v, want %v", events, want)
		}
	}
}

func TestYieldingSpinner_nilProgressLeavesOnProgressUnset(t *testing.T) {
	stop, progress := yieldingSpinner(func() func() { return func() {} }, nil)
	if progress != nil {
		t.Fatal("want a nil OnProgress so Config.Progress's plain lines still apply")
	}
	stop()
}
