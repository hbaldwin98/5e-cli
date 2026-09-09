package ask

import (
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultBaseURL    = "https://api.openai.com/v1"
	DefaultEmbedModel = "text-embedding-3-small"
	DefaultAskModel   = "gpt-4o-mini"

	// DefaultTimeout bounds one embedding or chat request. http.DefaultClient
	// has no timeout, so a stalled connection would hang the CLI forever.
	DefaultTimeout = 120 * time.Second

	// DefaultEmbedMaxTokens is the per-input limit of text-embedding-3-*.
	// Other OpenAI-compatible backends cap lower — many local servers stop at
	// 512 — so FIVE_E_EMBED_MAX_TOKENS overrides it.
	DefaultEmbedMaxTokens = 8192
)

// Config is an OpenAI-compatible embedding and chat backend.
type Config struct {
	APIKey         string
	BaseURL        string
	EmbedModel     string
	AskModel       string
	EmbedMaxTokens int
	CachePath      string
	HTTPClient     *http.Client
	Progress       io.Writer
}

// ConfigFromEnv reads OPENAI_* and FIVE_E_* variables. Paths are filled by the CLI.
func ConfigFromEnv() Config {
	return Config{
		APIKey:         os.Getenv("OPENAI_API_KEY"),
		BaseURL:        os.Getenv("OPENAI_BASE_URL"),
		EmbedModel:     os.Getenv("FIVE_E_EMBED_MODEL"),
		AskModel:       os.Getenv("FIVE_E_ASK_MODEL"),
		EmbedMaxTokens: envInt("FIVE_E_EMBED_MAX_TOKENS"),
	}.withDefaults()
}

// envInt reads a positive integer setting, ignoring unset or unparseable
// values so a typo falls back to the default rather than failing every ask.
func envInt(name string) int {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

func (c Config) withDefaults() Config {
	if c.BaseURL == "" {
		c.BaseURL = DefaultBaseURL
	}
	c.BaseURL = strings.TrimRight(c.BaseURL, "/")
	if c.EmbedModel == "" {
		c.EmbedModel = DefaultEmbedModel
	}
	if c.AskModel == "" {
		c.AskModel = DefaultAskModel
	}
	if c.EmbedMaxTokens <= 0 {
		c.EmbedMaxTokens = DefaultEmbedMaxTokens
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: DefaultTimeout}
	}
	return c
}
