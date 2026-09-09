package cli

import (
	"bufio"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hbaldwin98/5e-cli/internal/provider"
)

// knownProviders lists the API-key providers `5e auth login` accepts today.
// OpenAI Codex's OAuth flow will add a distinct login path (no API key
// prompt) alongside these rather than joining this list.
var knownProviders = []string{provider.OpenAI, provider.OpenRouter}

func authCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage locally stored provider credentials",
	}
	cmd.AddCommand(authLoginCmd(), authListCmd(), authLogoutCmd())
	return cmd
}

func authLoginCmd() *cobra.Command {
	var apiKey string
	cmd := &cobra.Command{
		Use:   "login <provider>",
		Short: "Store an API key for a provider (openai, openrouter)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := strings.ToLower(strings.TrimSpace(args[0]))
			if !isKnownProvider(name) {
				return fmt.Errorf("unknown provider %q (known: %s)", name, strings.Join(knownProviders, ", "))
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

			path, err := provider.DefaultPath()
			if err != nil {
				return err
			}
			store, err := provider.Load(path)
			if err != nil {
				return err
			}
			store.Set(name, provider.Credential{
				APIKey:  key,
				BaseURL: provider.DefaultBaseURL(name),
			})
			store.Active = name
			if err := store.Save(); err != nil {
				return err
			}

			sty := styles(cmd.OutOrStdout())
			fmt.Fprintln(cmd.OutOrStdout(), sty.Success.Render(fmt.Sprintf("%s: credentials saved to %s", name, path)))
			return nil
		},
	}
	cmd.Flags().StringVar(&apiKey, "api-key", "", "API key (omit to be prompted)")
	return cmd
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
				line := fmt.Sprintf("%s %s\t%s", marker, name, masked)
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
