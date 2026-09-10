package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hbaldwin98/5e-cli/internal/ask"
	"github.com/hbaldwin98/5e-cli/internal/provider"
)

// applyProviderOverride layers --provider/--model onto a Config already
// built by ask.ConfigFromEnv: --provider swaps which stored credential's
// key/base-URL/models are used (instead of the active one), and --model
// pins the chat model regardless of what the provider or FIVE_E_ASK_MODEL
// says, since an explicit per-run flag is the most specific request there
// is. --model never touches the embedding model — swapping the embedding
// model would invalidate the on-disk cache, which is not something a chat
// model override should do as a side effect.
func applyProviderOverride(cfg *ask.Config, opt *options) error {
	if opt.Provider != "" {
		cred, err := loadProviderCredential(opt.Provider)
		if err != nil {
			return err
		}
		applyCredential(cfg, opt.Provider, cred)
	}
	if opt.Model != "" {
		cfg.AskModel = opt.Model
	}
	return nil
}

// effectiveProviderName reports which stored provider (if any) a session's
// credentials actually came from, so /model in chat knows where a
// persisted change belongs. An explicit --provider wins; otherwise an
// explicit OPENAI_API_KEY env var means no stored provider is in play at
// all (ConfigFromEnv never consults the store in that case); otherwise
// FIVE_E_PROVIDER or the store's active provider, if either names one that
// is actually configured.
func effectiveProviderName(opt *options) string {
	if opt.Provider != "" {
		return opt.Provider
	}
	if os.Getenv("OPENAI_API_KEY") != "" {
		return ""
	}
	path, err := provider.DefaultPath()
	if err != nil {
		return ""
	}
	store, err := provider.Load(path)
	if err != nil {
		return ""
	}
	name, _, ok := store.Resolve(os.Getenv("FIVE_E_PROVIDER"))
	if !ok {
		return ""
	}
	return name
}

// saveProviderModel persists name as the given provider's stored chat
// model, so a /model change in chat survives past the session — the whole
// point of /model persisting instead of being session-scoped like --model.
func saveProviderModel(providerName, model string) error {
	path, err := provider.DefaultPath()
	if err != nil {
		return err
	}
	store, err := provider.Load(path)
	if err != nil {
		return err
	}
	cred, ok := store.Get(providerName)
	if !ok {
		return fmt.Errorf("%s is not configured; run `5e auth login %s`", providerName, providerName)
	}
	cred.ChatModel = model
	store.Set(providerName, cred)
	return store.Save()
}

// loadProviderCredential looks up one configured provider's credential by
// name, shared by --provider and the /provider chat command.
func loadProviderCredential(name string) (provider.Credential, error) {
	path, err := provider.DefaultPath()
	if err != nil {
		return provider.Credential{}, err
	}
	store, err := provider.Load(path)
	if err != nil {
		return provider.Credential{}, err
	}
	cred, ok := store.Get(name)
	if !ok {
		return provider.Credential{}, fmt.Errorf("%s is not configured; run `5e auth login %s`", name, name)
	}
	return cred, nil
}

func configuredProviderNames() ([]string, error) {
	path, err := provider.DefaultPath()
	if err != nil {
		return nil, err
	}
	store, err := provider.Load(path)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, name := range knownProviders {
		if _, ok := store.Get(name); ok {
			names = append(names, name)
		}
	}
	return names, nil
}

func providerModels(ctx context.Context, name string) ([]string, error) {
	cred, err := loadProviderCredential(name)
	if err != nil {
		return nil, err
	}
	return provider.FetchModels(ctx, nil, name, cred)
}

// applyCredential layers a resolved credential onto cfg, which was already
// defaulted by ask.ConfigFromEnv: an empty cred.BaseURL (plain "openai")
// means "use that existing default", not "clear it", and an empty model
// leaves whatever cfg already had.
func applyCredential(cfg *ask.Config, name string, cred provider.Credential) {
	cfg.ChatProvider = name
	if cred.Type == provider.OAuthAuth {
		cfg.ChatAPIKey, cfg.ChatAccountID = cred.AccessToken, cred.AccountID
		cfg.ChatBaseURL = provider.CodexBaseURL
		if path, err := provider.DefaultPath(); err == nil {
			cfg.ChatToken = func(ctx context.Context) (string, string, error) {
				return provider.CodexAccessToken(ctx, path, nil)
			}
		}
	} else {
		cfg.APIKey, cfg.ChatAPIKey = cred.APIKey, cred.APIKey
		cfg.BaseURL, cfg.ChatBaseURL = ask.DefaultBaseURL, ask.DefaultBaseURL
		if cred.BaseURL != "" {
			cfg.BaseURL, cfg.ChatBaseURL = cred.BaseURL, cred.BaseURL
		}
		cfg.ChatAccountID, cfg.ChatToken = "", nil
	}
	if cred.ChatModel != "" {
		cfg.AskModel = cred.ChatModel
	}
	if cred.EmbedModel != "" {
		cfg.EmbedModel = cred.EmbedModel
	}
}

// knownProviders lists every provider accepted by `5e auth login`.
var knownProviders = []string{provider.OpenAI, provider.OpenRouter, provider.Codex}

var codexOAuthConfig = provider.DefaultCodexOAuthConfig

func authCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage locally stored provider credentials",
	}
	cmd.AddCommand(authLoginCmd(), authListCmd(), authLogoutCmd(), authModelsCmd(), authSetModelCmd())
	return cmd
}

func authLoginCmd() *cobra.Command {
	var apiKey, chatModel, embedModel string
	cmd := &cobra.Command{
		Use:   "login <provider>",
		Short: "Authenticate with a model provider",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.ToLower(strings.TrimSpace(args[0]))
			if !isKnownProvider(name) {
				return fmt.Errorf("unknown provider %q (known: %s)", name, strings.Join(knownProviders, ", "))
			}
			if name == provider.Codex {
				cfg := codexOAuthConfig()
				cfg.OpenBrowser = openBrowser
				fmt.Fprintln(cmd.OutOrStdout(), "Opening a browser to sign in with ChatGPT...")
				cred, err := cfg.Login(cmd.Context())
				if err != nil {
					return err
				}
				cred.ChatModel = chatModel
				if cred.ChatModel == "" {
					cred.ChatModel = provider.DefaultCodexModel
				}
				return saveLogin(cmd, name, cred)
			}

			key := apiKey
			if key == "" {
				var err error
				key, err = promptForKey(cmd, name)
				if err != nil {
					return err
				}
			}
			if key == "" {
				return fmt.Errorf("no API key given")
			}

			return saveLogin(cmd, name, provider.Credential{
				Type:       provider.APIKeyAuth,
				APIKey:     key,
				BaseURL:    provider.DefaultBaseURL(name),
				ChatModel:  chatModel,
				EmbedModel: embedModel,
			})
		},
	}
	cmd.Flags().StringVar(&apiKey, "api-key", "", "API key (omit to be prompted)")
	cmd.Flags().StringVar(&chatModel, "chat-model", "", "preferred chat model (default: ask's built-in default)")
	cmd.Flags().StringVar(&embedModel, "embed-model", "", "preferred embedding model (default: ask's built-in default)")
	return cmd
}

func saveLogin(cmd *cobra.Command, name string, cred provider.Credential) error {
	path, err := provider.DefaultPath()
	if err != nil {
		return err
	}
	store, err := provider.Load(path)
	if err != nil {
		return err
	}
	store.Set(name, cred)
	store.Active = name
	if err := store.Save(); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), styles(cmd.OutOrStdout()).Success.Render(fmt.Sprintf("%s: credentials saved to %s", name, path)))
	return nil
}

func openBrowser(url string) error {
	var name string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		name, args = "open", []string{url}
	case "windows":
		name, args = "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		name, args = "xdg-open", []string{url}
	}
	return exec.Command(name, args...).Start()
}

func authModelsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "models <provider>",
		Short: "List models available to a configured provider",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.ToLower(strings.TrimSpace(args[0]))
			path, err := provider.DefaultPath()
			if err != nil {
				return err
			}
			store, err := provider.Load(path)
			if err != nil {
				return err
			}
			cred, ok := store.Get(name)
			if !ok {
				return fmt.Errorf("%s is not configured; run `5e auth login %s`", name, name)
			}
			models, err := provider.FetchModels(cmd.Context(), nil, name, cred)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if len(models) == 0 {
				fmt.Fprintln(w, "no models returned")
				return nil
			}
			for _, m := range models {
				fmt.Fprintln(w, m)
			}
			return nil
		},
	}
}

func authSetModelCmd() *cobra.Command {
	var chatModel, embedModel string
	cmd := &cobra.Command{
		Use:   "set-model <provider>[/<model>]",
		Short: "Change a configured provider's preferred chat/embedding model",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var selection string
			if len(args) == 0 {
				if chatModel != "" || embedModel != "" {
					return fmt.Errorf("a provider argument is required with model flags")
				}
				if !isTTY(cmd.OutOrStdout()) || !isTTYReader(cmd.InOrStdin()) {
					return fmt.Errorf("interactive model selection requires a terminal; use `5e auth set-model <provider>/<model>`")
				}
				var err error
				selection, err = pickProviderModel(cmd)
				if err != nil {
					return err
				}
			} else {
				selection = args[0]
			}
			name, directModel, _ := strings.Cut(strings.TrimSpace(selection), "/")
			name = strings.ToLower(name)
			if directModel != "" {
				if chatModel != "" {
					return fmt.Errorf("model given both in the argument and --chat-model")
				}
				chatModel = directModel
			}
			if chatModel == "" && embedModel == "" {
				return fmt.Errorf("give at least one of --chat-model or --embed-model")
			}
			path, err := provider.DefaultPath()
			if err != nil {
				return err
			}
			store, err := provider.Load(path)
			if err != nil {
				return err
			}
			cred, ok := store.Get(name)
			if !ok {
				return fmt.Errorf("%s is not configured; run `5e auth login %s`", name, name)
			}
			if chatModel != "" {
				cred.ChatModel = chatModel
			}
			if embedModel != "" {
				cred.EmbedModel = embedModel
			}
			store.Set(name, cred)
			if err := store.Save(); err != nil {
				return err
			}
			sty := styles(cmd.OutOrStdout())
			fmt.Fprintln(cmd.OutOrStdout(), sty.Success.Render(fmt.Sprintf("%s: model preferences updated", name)))
			return nil
		},
	}
	cmd.Flags().StringVar(&chatModel, "chat-model", "", "preferred chat model")
	cmd.Flags().StringVar(&embedModel, "embed-model", "", "preferred embedding model")
	return cmd
}

// configuredProviderModel treats the prefix before the first slash as a
// provider only when that provider is configured. This keeps model IDs such
// as "openai/gpt-4.1" valid when "openai" is not a configured provider.
func configuredProviderModel(value, fallbackProvider string) (string, string, error) {
	value = strings.TrimSpace(value)
	prefix, model, found := strings.Cut(value, "/")
	if !found {
		return fallbackProvider, value, nil
	}
	path, err := provider.DefaultPath()
	if err != nil {
		return "", "", err
	}
	store, err := provider.Load(path)
	if err != nil {
		return "", "", err
	}
	name := strings.ToLower(strings.TrimSpace(prefix))
	if _, ok := store.Get(name); ok {
		return name, model, nil
	}
	return fallbackProvider, value, nil
}

func applyChatModel(cfg *ask.Config, providerName *string, value string) error {
	name, model, err := configuredProviderModel(value, *providerName)
	if err != nil {
		return err
	}
	if model == "" {
		return fmt.Errorf("model cannot be empty")
	}
	if name == "" {
		cfg.AskModel = model
		return fmt.Errorf("model %s applied for this session, but there is no active stored provider to save it to", model)
	}
	if name != *providerName {
		cred, err := loadProviderCredential(name)
		if err != nil {
			return err
		}
		applyCredential(cfg, name, cred)
	}
	if err := saveProviderModel(name, model); err != nil {
		return err
	}
	cfg.AskModel = model
	*providerName = name
	return nil
}

// promptForKey reads a key from stdin without echoing it to the transcript
// (the terminal itself still echoes; --api-key or piping the key in avoids
// that when it matters).
func promptForKey(cmd *cobra.Command, name string) (string, error) {
	fmt.Fprintf(cmd.OutOrStdout(), "%s API key: ", name)
	reader := bufio.NewReader(cmd.InOrStdin())
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("read API key: %w", err)
	}
	return strings.TrimSpace(line), nil
}

func authListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured providers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := provider.DefaultPath()
			if err != nil {
				return err
			}
			store, err := provider.Load(path)
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if len(store.Providers) == 0 {
				fmt.Fprintln(w, "no providers configured; run `5e auth login <provider>`")
				return nil
			}
			sty := styles(w)
			for _, name := range knownProviders {
				cred, ok := store.Providers[name]
				if !ok {
					continue
				}
				marker := " "
				if name == store.Active {
					marker = "*"
				}
				masked := maskKey(cred.APIKey)
				if cred.Type == provider.OAuthAuth {
					masked = "oauth"
					if cred.AccountID != "" {
						masked += " account=" + cred.AccountID
					}
				}
				line := fmt.Sprintf("%s %s\t%s", marker, name, masked)
				if cred.ChatModel != "" {
					line += fmt.Sprintf("\tchat=%s", cred.ChatModel)
				}
				if cred.EmbedModel != "" {
					line += fmt.Sprintf("\tembed=%s", cred.EmbedModel)
				}
				if name == store.Active {
					line = sty.Success.Render(line)
				}
				fmt.Fprintln(w, line)
			}
			return nil
		},
	}
}

func authLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout <provider>",
		Short: "Remove a stored provider credential",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.ToLower(strings.TrimSpace(args[0]))
			path, err := provider.DefaultPath()
			if err != nil {
				return err
			}
			store, err := provider.Load(path)
			if err != nil {
				return err
			}
			if _, ok := store.Providers[name]; !ok {
				return fmt.Errorf("%s is not configured", name)
			}
			store.Remove(name)
			if store.Active == name {
				store.Active = ""
			}
			if err := store.Save(); err != nil {
				return err
			}
			sty := styles(cmd.OutOrStdout())
			fmt.Fprintln(cmd.OutOrStdout(), sty.Success.Render(name+": credentials removed"))
			return nil
		},
	}
}

func isKnownProvider(name string) bool {
	for _, p := range knownProviders {
		if p == name {
			return true
		}
	}
	return false
}

// maskKey shows just enough of a key to recognize it without exposing it in
// full on screen, in scrollback, or in a screen-shared terminal.
func maskKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "…" + key[len(key)-4:]
}
