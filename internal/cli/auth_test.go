package cli

import (
	"bytes"
	"strings"
	"testing"
)

// withIsolatedConfigDir points os.UserConfigDir (via XDG_CONFIG_HOME) at a
// scratch directory so these tests never touch the real machine's stored
// credentials.
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
