// Package provider stores locally-authenticated backend credentials (API
// keys today; OAuth-based providers like OpenAI Codex are meant to slot in
// here later) so the CLI is not limited to reading OPENAI_API_KEY from the
// environment.
package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Well-known provider names. A Codex OAuth provider will join this list once
// its login flow lands; it needs the same Credential shape plus a refresh
// token, which the schema below already leaves room for via extra fields on
// Credential if/when that's added.
const (
	OpenAI     = "openai"
	OpenRouter = "openrouter"

	openRouterBaseURL = "https://openrouter.ai/api/v1"
)

// Credential is one provider's locally stored auth. APIKey covers the
// simple case (OpenAI, OpenRouter); BaseURL lets a provider override the
// default endpoint (OpenRouter's, or a self-hosted OpenAI-compatible
// gateway).
type Credential struct {
	APIKey  string `json:"api_key,omitempty"`
	BaseURL string `json:"base_url,omitempty"`
}

// Store is the on-disk set of configured providers, keyed by provider name.
type Store struct {
	// Active is the provider name a plain login without an explicit
	// "--use" should be read from when the caller doesn't pin one via
	// FIVE_E_PROVIDER. Empty means "none configured yet".
	Active    string                `json:"active,omitempty"`
	Providers map[string]Credential `json:"providers"`
	path      string
}

// DefaultPath returns the path to the local credential file:
// $XDG_CONFIG_HOME/5e/auth.json, falling back to os.UserConfigDir.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve config dir: %w", err)
	}
	return filepath.Join(dir, "5e", "auth.json"), nil
}

// Load reads the credential store at path, returning an empty Store if the
// file does not exist yet.
func Load(path string) (*Store, error) {
	s := &Store{Providers: map[string]Credential{}, path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) == 0 {
		return s, nil
	}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if s.Providers == nil {
		s.Providers = map[string]Credential{}
	}
	s.path = path
	return s, nil
}

// Save writes the store back to disk with 0600 permissions, since it holds
// API keys.
func (s *Store) Save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}

// Set stores (or replaces) a provider's credential.
func (s *Store) Set(name string, cred Credential) {
	if s.Providers == nil {
		s.Providers = map[string]Credential{}
	}
	s.Providers[name] = cred
}

// Remove deletes a provider's credential, if present.
func (s *Store) Remove(name string) {
	delete(s.Providers, name)
}

// Get returns the provider's credential and whether it is configured.
func (s *Store) Get(name string) (Credential, bool) {
	c, ok := s.Providers[name]
	return c, ok
}

// Resolve picks the credential to use: the explicit name if given and
// configured, else the store's Active provider, else nothing (ok=false),
// which callers should treat as "fall back to OPENAI_API_KEY".
func (s *Store) Resolve(name string) (providerName string, cred Credential, ok bool) {
	if name == "" {
		name = s.Active
	}
	if name == "" {
		return "", Credential{}, false
	}
	cred, ok = s.Providers[name]
	return name, cred, ok
}

// DefaultBaseURL returns the well-known base URL for a provider name, or ""
// for providers (like plain "openai") that use the backend's own default.
func DefaultBaseURL(name string) string {
	if name == OpenRouter {
		return openRouterBaseURL
	}
	return ""
}
