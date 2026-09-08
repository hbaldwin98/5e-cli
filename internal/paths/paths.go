package paths

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/hbaldwin98/5e-cli/internal/ingest"
	"github.com/hbaldwin98/5e-cli/internal/store"
)

const submoduleData = "third_party/5etools-src/data"

// Inspection describes whether the local data and derived index are ready.
type Inspection struct {
	DataPath         string `json:"dataPath"`
	DataExists       bool   `json:"dataExists"`
	DataFingerprint  string `json:"dataFingerprint,omitempty"`
	IndexPath        string `json:"indexPath"`
	IndexExists      bool   `json:"indexExists"`
	IndexFingerprint string `json:"indexFingerprint,omitempty"`
	Current          bool   `json:"current"`
	Ready            bool   `json:"ready"`
	Issue            string `json:"issue,omitempty"`
}

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

// Inspect reports setup problems without requiring the caller to parse an
// OpenIndex error. Missing data or an index is a normal diagnostic result.
func Inspect(dataDir, index string) Inspection {
	r := Inspection{DataPath: dataDir, IndexPath: index}

	dataInfo, dataErr := os.Stat(dataDir)
	if dataErr == nil && dataInfo.IsDir() {
		r.DataExists = true
		if fp, err := ingest.Fingerprint(dataDir); err == nil {
			r.DataFingerprint = fp
		} else {
			r.Issue = fmt.Sprintf("could not fingerprint data: %v", err)
		}
	}

	indexInfo, indexErr := os.Stat(index)
	if indexErr == nil {
		r.IndexExists = true
		if indexInfo.Size() == 0 {
			r.Issue = fmt.Sprintf("empty index at %s; run `5e ingest`", index)
		} else if st, err := store.Open(index); err != nil {
			r.Issue = fmt.Sprintf("could not open index: %v", err)
		} else {
			meta, err := st.Meta()
			_ = st.Close()
			if err != nil {
				r.Issue = fmt.Sprintf("could not read index metadata: %v", err)
			} else {
				r.IndexFingerprint = meta.SHA
				r.Current = r.DataExists && r.DataFingerprint != "" && r.DataFingerprint == meta.SHA
			}
		}
	}

	if !r.DataExists {
		if dataDir == "" {
			r.Issue = "data directory not set; use --data or FIVE_E_DATA, or run `make data`"
		} else {
			r.Issue = fmt.Sprintf("data directory %s not found; use --data or FIVE_E_DATA, or run `make data`", dataDir)
		}
	} else if !r.IndexExists {
		r.Issue = fmt.Sprintf("no index at %s; run `5e ingest`", index)
	} else if r.Issue == "" && !r.Current {
		r.Issue = fmt.Sprintf("index is stale; run `5e ingest` (data %s, index %s)", short(r.DataFingerprint), short(r.IndexFingerprint))
	}
	r.Ready = r.DataExists && r.IndexExists && r.Current && r.Issue == ""
	return r
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
