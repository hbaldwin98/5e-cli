package chat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Store is a directory of chat sessions, one JSON file each. A file per
// session keeps the transcript readable and greppable outside this tool, and
// keeps a corrupt session from taking the others with it.
type Store struct {
	dir string
}

const sessionExt = ".json"

// OpenStore prepares dir for session files.
func OpenStore(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("no chat directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

// Dir is the directory holding the sessions.
func (s *Store) Dir() string { return s.dir }

// Path is where a named session is stored.
func (s *Store) Path(name string) (string, error) {
	id, err := slug(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.dir, id+sessionExt), nil
}

// Load reads a session, returning a new empty one under that name when the
// file does not exist yet. Naming a session is how you create it.
func (s *Store) Load(name string) (*Session, error) {
	path, err := s.Path(name)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Session{Name: strings.TrimSpace(name), Created: now(), Updated: now()}, nil
	}
	if err != nil {
		return nil, err
	}
	var sess Session
	if err := json.Unmarshal(raw, &sess); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if strings.TrimSpace(sess.Name) == "" {
		sess.Name = strings.TrimSpace(name)
	}
	return &sess, nil
}

// Save writes the session atomically, so an interrupted write cannot leave a
// half-written transcript where the conversation used to be.
func (s *Store) Save(sess *Session) error {
	path, err := s.Path(sess.Name)
	if err != nil {
		return err
	}
	if sess.Created == "" {
		sess.Created = now()
	}
	sess.Updated = now()
	raw, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(s.dir, "session-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(name)
	}()
	if _, err := tmp.Write(raw); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// List reports the stored sessions, most recently updated first.
func (s *Store) List() ([]Summary, error) {
	entries, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Summary
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != sessionExt {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var sess Session
		if err := json.Unmarshal(raw, &sess); err != nil {
			// One unreadable file must not hide the rest of the sessions.
			continue
		}
		id := strings.TrimSuffix(e.Name(), sessionExt)
		if strings.TrimSpace(sess.Name) == "" {
			sess.Name = id
		}
		out = append(out, sess.summary(id))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Updated != out[j].Updated {
			return out[i].Updated > out[j].Updated
		}
		return out[i].Slug < out[j].Slug
	})
	return out, nil
}

// Delete removes a session. A session that is not there is not an error, so
// deleting twice does not fail a script.
func (s *Store) Delete(name string) error {
	path, err := s.Path(name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
