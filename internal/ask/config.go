package ask

import (
	"io"
	"net/http"
	"os"
	"strings"
)

const (
	DefaultBaseURL    = "https://api.openai.com/v1"
	DefaultEmbedModel = "text-embedding-3-small"
	DefaultAskModel   = "gpt-4o-mini"
)

// Config is an OpenAI-compatible embedding and chat backend.
type Config struct {
	APIKey     string
	BaseURL    string
	EmbedModel string
	AskModel   string
	CachePath  string
	HTTPClient *http.Client
	Progress   io.Writer
}

// ConfigFromEnv reads OPENAI_* and FIVE_E_* variables. Paths are filled by the CLI.
func ConfigFromEnv() Config {
	return Config{
		APIKey:     os.Getenv("OPENAI_API_KEY"),
		BaseURL:    os.Getenv("OPENAI_BASE_URL"),
		EmbedModel: os.Getenv("FIVE_E_EMBED_MODEL"),
		AskModel:   os.Getenv("FIVE_E_ASK_MODEL"),
	}.withDefaults()
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
	if c.HTTPClient == nil {
		c.HTTPClient = http.DefaultClient
	}
	return c
}
