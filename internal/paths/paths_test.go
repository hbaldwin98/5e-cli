package paths

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hbaldwin98/5e-cli/internal/store"
)

func TestInspect_reportsMissingSetup(t *testing.T) {
	report := Inspect(filepath.Join(t.TempDir(), "data"), filepath.Join(t.TempDir(), "index.sqlite"))
	if report.DataExists || report.IndexExists || report.Ready {
		t.Fatalf("missing setup reported as ready: %+v", report)
	}
	if report.Issue == "" {
		t.Fatalf("missing setup has no issue: %+v", report)
	}
}

func TestInspect_reportsCurrentAndStaleIndexes(t *testing.T) {
	data := t.TempDir()
	if err := os.WriteFile(filepath.Join(data, "content.json"), []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(t.TempDir(), "index.sqlite")

	initial := Inspect(data, index)
	if initial.DataFingerprint == "" {
		t.Fatalf("missing data fingerprint: %+v", initial)
	}
	if err := store.Create(index, store.Meta{SHA: initial.DataFingerprint, DataRoot: data, IngestedAt: store.Now()}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}

	current := Inspect(data, index)
	if !current.Ready || !current.Current || current.Issue != "" {
		t.Fatalf("current index: %+v", current)
	}

	if err := os.WriteFile(filepath.Join(data, "content.json"), []byte("changed content"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := Inspect(data, index)
	if stale.Ready || stale.Current || stale.Issue == "" {
		t.Fatalf("stale index: %+v", stale)
	}
}
