package chat

import (
	"crypto/sha256"
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
	id, err := sessionID(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.dir, id+sessionExt), nil
}

// legacyPath is the pre-identity-hash filename. Load uses it once so existing
// sessions remain available after the filename format changes.
func (s *Store) legacyPath(name string) (string, error) {
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
	sourcePath := path
	legacy := false
	if os.IsNotExist(err) {
		legacyPath, pathErr := s.legacyPath(name)
		if pathErr != nil {
			return nil, pathErr
		}
		if legacyPath == path {
			return newSession(name), nil
		}
		raw, err = os.ReadFile(legacyPath)
		if err == nil {
			sourcePath = legacyPath
			legacy = true
		}
	}
	if os.IsNotExist(err) {
		return newSession(name), nil
	}
	if err != nil {
		return nil, err
	}
	var sess Session
	if err := json.Unmarshal(raw, &sess); err != nil {
		return nil, fmt.Errorf("%s: %w", sourcePath, err)
	}
	if strings.TrimSpace(sess.Name) == "" {
		sess.Name = strings.TrimSpace(name)
	}
	if !sameSessionName(sess.Name, name) {
		// A legacy slug may belong to another name that used to collide with
		// this one. Leave it untouched and let Save create the hashed path.
		if legacy {
			return newSession(name), nil
		}
		return nil, fmt.Errorf("session file %s belongs to %q, not %q", path, sess.Name, strings.TrimSpace(name))
	}
	if legacy {
		if err := os.Rename(sourcePath, path); err != nil {
			return nil, fmt.Errorf("migrate session %s: %w", sourcePath, err)
		}
	}
	return &sess, nil
}

func newSession(name string) *Session {
	name = strings.TrimSpace(name)
	return &Session{Name: name, Created: now(), Updated: now()}
}

func sessionID(name string) (string, error) {
	slugged, err := slug(name)
	if err != nil {
		return "", err
	}
	identity := strings.ToLower(strings.TrimSpace(name))
	digest := sha256.Sum256([]byte(identity))
	return fmt.Sprintf("%s-%x", slugged, digest), nil
}

func sameSessionName(a, b string) bool {
	return strings.ToLower(strings.TrimSpace(a)) == strings.ToLower(strings.TrimSpace(b))
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

// exists reports whether a session file is actually on disk, as opposed to
// Load's habit of returning a new empty session for a name that isn't there.
func (s *Store) exists(name string) (bool, error) {
	path, err := s.Path(name)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Rename moves a session to a new name, keeping its transcript and notes.
// The target name must not already be a session, so a rename can never
// silently discard another conversation; delete it first if that is really
// what's wanted.
func (s *Store) Rename(oldName, newName string) (*Session, error) {
	if strings.TrimSpace(newName) == "" {
		return nil, fmt.Errorf("new session name cannot be empty")
	}
	had, err := s.exists(oldName)
	if err != nil {
		return nil, err
	}
	if !had {
		return nil, fmt.Errorf("no session named %q", strings.TrimSpace(oldName))
	}
	if sameSessionName(oldName, newName) {
		return nil, fmt.Errorf("%q is already the session's name", strings.TrimSpace(newName))
	}
	taken, err := s.exists(newName)
	if err != nil {
		return nil, err
	}
	if taken {
		return nil, fmt.Errorf("a session named %q already exists", strings.TrimSpace(newName))
	}
	sess, err := s.Load(oldName)
	if err != nil {
		return nil, err
	}
	sess.Name = strings.TrimSpace(newName)
	if err := s.Save(sess); err != nil {
		return nil, err
	}
	if err := s.Delete(oldName); err != nil {
		return nil, err
	}
	return sess, nil
}

// Export marshals a session the same way Save persists it, so the output of
// `chat export` is a valid `chat import` input and a valid on-disk session
// file, not a bespoke format to keep in sync with the real one.
func (s *Store) Export(name string) ([]byte, error) {
	had, err := s.exists(name)
	if err != nil {
		return nil, err
	}
	if !had {
		return nil, fmt.Errorf("no session named %q", strings.TrimSpace(name))
	}
	sess, err := s.Load(name)
	if err != nil {
		return nil, err
	}
	raw, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// Import decodes an exported session and saves it into this store. name
// overrides the session's own recorded name when non-empty, so an export can
// be re-imported under a different name without editing the file. Import
// refuses to overwrite an existing session unless force is set, the same
// collision protection Rename gives a renamed session.
func (s *Store) Import(raw []byte, name string, force bool) (*Session, error) {
	var sess Session
	if err := json.Unmarshal(raw, &sess); err != nil {
		return nil, fmt.Errorf("not a chat session: %w", err)
	}
	if strings.TrimSpace(name) != "" {
		sess.Name = strings.TrimSpace(name)
	}
	if strings.TrimSpace(sess.Name) == "" {
		return nil, fmt.Errorf("imported session has no name; pass one explicitly")
	}
	if !force {
		taken, err := s.exists(sess.Name)
		if err != nil {
			return nil, err
		}
		if taken {
			return nil, fmt.Errorf("a session named %q already exists; use --force to overwrite it", sess.Name)
		}
	}
	if err := s.Save(&sess); err != nil {
		return nil, err
	}
	return &sess, nil
}
