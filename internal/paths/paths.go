package paths

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/hbaldwin98/5e-cli/internal/ingest"
	"github.com/hbaldwin98/5e-cli/internal/store"
)

const submoduleData = "third_party/5etools-src/data"

// DefaultDataDir finds 5etools data/ from FIVE_E_DATA, cwd, or the executable.
func DefaultDataDir() string {
	if v := os.Getenv("FIVE_E_DATA"); v != "" {
		return v
	}
	var starts []string
	if wd, err := os.Getwd(); err == nil {
		starts = append(starts, wd)
	}
	if ex, err := os.Executable(); err == nil {
		starts = append(starts, filepath.Dir(ex))
	}
	for _, start := range starts {
		dir := start
		for range 10 {
			cand := filepath.Join(dir, submoduleData)
			if st, err := os.Stat(cand); err == nil && st.IsDir() {
				return cand
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return ""
}

// DefaultIndex is $XDG_CACHE_HOME/5e-cli/index.sqlite.
func DefaultIndex() (string, error) {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, "5e-cli", "index.sqlite"), nil
}

// EmbeddingsForIndex is the sidecar sqlite next to the entity index.
func EmbeddingsForIndex(index string) string {
	if v := os.Getenv("FIVE_E_EMBEDDINGS"); v != "" {
		return v
	}
	return filepath.Join(filepath.Dir(index), "embeddings.sqlite")
}

// OpenIndex opens the sqlite index and optionally refuses a stale fingerprint.
func OpenIndex(index, dataDir string) (*store.Store, error) {
	st, err := os.Stat(index)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no index at %s; run `5e ingest` first", index)
		}
		return nil, err
	}
	if st.Size() == 0 {
		return nil, fmt.Errorf("empty index at %s; run `5e ingest`", index)
	}
	s, err := store.Open(index)
	if err != nil {
		return nil, err
	}
	if dataDir == "" {
		return s, nil
	}
	if _, err := os.Stat(dataDir); err != nil {
		return s, nil
	}
	meta, err := s.Meta()
	if err != nil {
		_ = s.Close()
		return nil, err
	}
	fp, err := ingest.Fingerprint(dataDir)
	if err != nil {
		return s, nil
	}
	if fp != meta.SHA {
		_ = s.Close()
		return nil, fmt.Errorf("index is stale (data %s, index %s); run `5e ingest`", short(fp), short(meta.SHA))
	}
	return s, nil
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
