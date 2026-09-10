package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hbaldwin98/5e-cli/internal/ask"
	"github.com/hbaldwin98/5e-cli/internal/provider"
)

// withIsolatedConfigDir points os.UserConfigDir (via XDG_CONFIG_HOME) at a
// scratch directory so these tests never touch the real machine's stored
// credentials.
// providerCredentialForTest reads one provider's stored credential straight
// off disk, for asserting a slash command actually persisted a change.
func providerCredentialForTest(t *testing.T, name string) (provider.Credential, bool) {
	t.Helper()
	path, err := provider.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	store, err := provider.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return store.Get(name)
}

func withIsolatedConfigDir(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

func runAuth(t *testing.T, stdin string, args ...string) string {
	t.Helper()
	cmd := authCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("5e auth %v: %v (output: %s)", args, err, out.String())
	}
	return out.String()
}

func TestAuthLogin_storesAnApiKeyAndMarksItActive(t *testing.T) {
	withIsolatedConfigDir(t)
	out := runAuth(t, "", "login", "openai", "--api-key", "sk-abcdef1234")
	if !strings.Contains(out, "credentials saved") {
		t.Fatalf("want a confirmation, got %q", out)
	}

	list := runAuth(t, "", "list")
	if !strings.Contains(list, "openai") || !strings.Contains(list, "sk-a") {
		t.Fatalf("want the masked key listed, got %q", list)
	}
}

func TestAuthLogin_promptsWhenNoFlagGiven(t *testing.T) {
	withIsolatedConfigDir(t)
	runAuth(t, "or-key-0123456789\n", "login", "openrouter")

	list := runAuth(t, "", "list")
	if !strings.Contains(list, "openrouter") {
		t.Fatalf("want openrouter listed, got %q", list)
	}
}

func TestAuthLogin_rejectsUnknownProvider(t *testing.T) {
	withIsolatedConfigDir(t)
	cmd := authCmd()
	cmd.SetArgs([]string{"login", "anthropic", "--api-key", "x"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err == nil {
		t.Fatal("want an error for an unknown provider")
	}
}

func TestAuthLogout_removesTheCredential(t *testing.T) {
	withIsolatedConfigDir(t)
	runAuth(t, "", "login", "openai", "--api-key", "sk-abcdef1234")
	runAuth(t, "", "logout", "openai")

	list := runAuth(t, "", "list")
	if strings.Contains(list, "openai") {
		t.Fatalf("want openai gone after logout, got %q", list)
	}
}

func TestAuthList_withNothingConfigured(t *testing.T) {
	withIsolatedConfigDir(t)
	out := runAuth(t, "", "list")
	if !strings.Contains(out, "no providers configured") {
		t.Fatalf("want the empty-state message, got %q", out)
	}
}

func TestAuthLogin_storesPreferredModels(t *testing.T) {
	withIsolatedConfigDir(t)
	runAuth(t, "", "login", "openai", "--api-key", "sk-abcdef1234", "--chat-model", "gpt-x", "--embed-model", "embed-x")
	list := runAuth(t, "", "list")
	if !strings.Contains(list, "chat=gpt-x") || !strings.Contains(list, "embed=embed-x") {
		t.Fatalf("want the stored model preferences listed, got %q", list)
	}
}

func TestAuthSetModel_updatesAnExistingProvider(t *testing.T) {
	withIsolatedConfigDir(t)
	runAuth(t, "", "login", "openai", "--api-key", "sk-abcdef1234")
	runAuth(t, "", "set-model", "openai", "--chat-model", "gpt-y")

	list := runAuth(t, "", "list")
	if !strings.Contains(list, "chat=gpt-y") {
		t.Fatalf("want the updated chat model listed, got %q", list)
	}
}

func TestAuthSetModel_acceptsQualifiedModel(t *testing.T) {
	withIsolatedConfigDir(t)
	runAuth(t, "", "login", "openrouter", "--api-key", "or-key-0123456789")
	runAuth(t, "", "set-model", "openrouter/vendor/model")
	cred, _ := providerCredentialForTest(t, "openrouter")
	if cred.ChatModel != "vendor/model" {
		t.Fatalf("chat model = %q", cred.ChatModel)
	}
}

func TestAuthSetModel_withoutTerminalExplainsDirectSyntax(t *testing.T) {
	withIsolatedConfigDir(t)
	cmd := authCmd()
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"set-model"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "<provider>/<model>") {
		t.Fatalf("error = %v", err)
	}
}

func TestApplyCredential_codexOnlyChangesChatTransport(t *testing.T) {
	withIsolatedConfigDir(t)
	cfg := ask.Config{APIKey: "embedding-key", BaseURL: "https://embeddings.example", EmbedModel: "embed-model"}
	applyCredential(&cfg, provider.Codex, provider.Credential{Type: provider.OAuthAuth, AccessToken: "oauth-token", AccountID: "account-1", ChatModel: "codex-model"})
	if cfg.APIKey != "embedding-key" || cfg.BaseURL != "https://embeddings.example" || cfg.EmbedModel != "embed-model" {
		t.Fatalf("embedding config changed: %#v", cfg)
	}
	if cfg.ChatProvider != provider.Codex || cfg.ChatAPIKey != "oauth-token" || cfg.ChatBaseURL != provider.CodexBaseURL {
		t.Fatalf("chat config = %#v", cfg)
	}
}

func TestAuthSetModel_errorsForAnUnconfiguredProvider(t *testing.T) {
	withIsolatedConfigDir(t)
	cmd := authCmd()
	cmd.SetArgs([]string{"set-model", "openai", "--chat-model", "gpt-y"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err == nil {
		t.Fatal("want an error for a provider that was never logged in")
	}
}

func TestAuthModels_listsModelsFromTheRealEndpoint(t *testing.T) {
	withIsolatedConfigDir(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"id": "model-a"}, {"id": "model-b"}},
		})
	}))
	defer srv.Close()

	cmd := authCmd()
	cmd.SetArgs([]string{"login", "openai", "--api-key", "sk-abcdef1234"})
	var discard bytes.Buffer
	cmd.SetOut(&discard)
	cmd.SetErr(&discard)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	// login always fills in BaseURL via provider.DefaultBaseURL, which is ""
	// for plain "openai" (it uses the ask package's default); point it at
	// the fake server instead via a direct store edit.
	path, err := provider.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	store, err := provider.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	cred, _ := store.Get("openai")
	cred.BaseURL = srv.URL
	store.Set("openai", cred)
	if err := store.Save(); err != nil {
		t.Fatal(err)
	}

	out := runAuth(t, "", "models", "openai")
	if !strings.Contains(out, "model-a") || !strings.Contains(out, "model-b") {
		t.Fatalf("want the fetched models listed, got %q", out)
	}
}

func TestApplyProviderOverride_pinsAProviderAndModel(t *testing.T) {
	withIsolatedConfigDir(t)
	runAuth(t, "", "login", "openrouter", "--api-key", "or-key-0123456789", "--chat-model", "or-model")

	cfg := ask.ConfigFromEnv()
	opt := &options{Provider: "openrouter"}
	if err := applyProviderOverride(&cfg, opt); err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != "or-key-0123456789" {
		t.Fatalf("want the openrouter key applied, got %q", cfg.APIKey)
	}
	if cfg.AskModel != "or-model" {
		t.Fatalf("want the provider's stored chat model applied, got %q", cfg.AskModel)
	}

	opt.Model = "explicit-override"
	if err := applyProviderOverride(&cfg, opt); err != nil {
		t.Fatal(err)
	}
	if cfg.AskModel != "explicit-override" {
		t.Fatalf("want --model to win over the provider's stored model, got %q", cfg.AskModel)
	}
}

func TestApplyProviderOverride_errorsForAnUnconfiguredProvider(t *testing.T) {
	withIsolatedConfigDir(t)
	cfg := ask.ConfigFromEnv()
	if err := applyProviderOverride(&cfg, &options{Provider: "openai"}); err == nil {
		t.Fatal("want an error for a --provider that was never logged in")
	}
}
