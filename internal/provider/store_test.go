package provider

import (
	"path/filepath"
	"testing"
)

func TestStore_saveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")

	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Set(OpenAI, Credential{APIKey: "sk-test"})
	s.Active = OpenAI
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cred, ok := again.Get(OpenAI)
	if !ok || cred.APIKey != "sk-test" {
		t.Fatalf("want the saved credential back, got %+v ok=%v", cred, ok)
	}
	if again.Active != OpenAI {
		t.Fatalf("want active=%q, got %q", OpenAI, again.Active)
	}
}

func TestStore_loadMissingFileReturnsEmptyStore(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Providers) != 0 {
		t.Fatalf("want an empty store, got %+v", s.Providers)
	}
}

func TestStore_resolveFallsBackToActive(t *testing.T) {
	s := &Store{Providers: map[string]Credential{
		OpenRouter: {APIKey: "or-key"},
	}, Active: OpenRouter}

	name, cred, ok := s.Resolve("")
	if !ok || name != OpenRouter || cred.APIKey != "or-key" {
		t.Fatalf("want the active provider resolved, got name=%q cred=%+v ok=%v", name, cred, ok)
	}

	if _, _, ok := s.Resolve("openai"); ok {
		t.Fatal("an unconfigured explicit provider must not resolve")
	}
}

func TestStore_removeClearsCredential(t *testing.T) {
	s := &Store{Providers: map[string]Credential{OpenAI: {APIKey: "k"}}}
	s.Remove(OpenAI)
	if _, ok := s.Get(OpenAI); ok {
		t.Fatal("want the credential gone after Remove")
	}
}

func TestDefaultBaseURL(t *testing.T) {
	if got := DefaultBaseURL(OpenRouter); got == "" {
		t.Fatal("want a default OpenRouter base URL")
	}
	if got := DefaultBaseURL(OpenAI); got != "" {
		t.Fatalf("want no override for plain openai, got %q", got)
	}
}
