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

	// DefaultAskMaxTokens budgets the source text an answer is grounded in.
	// It is well inside gpt-4o-mini's context; FIVE_E_ASK_MAX_TOKENS lowers it
	// for a smaller chat model or raises it for a larger one.
	DefaultAskMaxTokens = 12000

	// DefaultMinScore is the cosine similarity a chunk needs to ground an
	// answer. Query and corpus vectors are both L2-normalized, so the score
	// from dot is already cosine similarity in [-1, 1]. text-embedding-3-small
	// puts an unrelated query and passage around 0.0-0.1; a genuinely relevant
	// passage is well above that. FIVE_E_MIN_SCORE overrides it for a
	// differently calibrated embedding model.
	DefaultMinScore = 0.15
)

// Config is an OpenAI-compatible embedding and chat backend.
type Config struct {
	APIKey         string
	BaseURL        string
	EmbedModel     string
	AskModel       string
	EmbedMaxTokens int
	AskMaxTokens   int
	// MinScore is the relevance gate: chunks scoring below it are rejected as
	// irrelevant rather than returned. Zero means "use DefaultMinScore"; a
	// negative value disables the gate, since cosine similarity never falls
	// below -1.
	MinScore   float64
	CachePath  string
	HTTPClient *http.Client
	Progress   io.Writer
}

// ConfigFromEnv reads OPENAI_* and FIVE_E_* variables. Paths are filled by the CLI.
func ConfigFromEnv() Config {
	return Config{
		APIKey:         os.Getenv("OPENAI_API_KEY"),
		BaseURL:        os.Getenv("OPENAI_BASE_URL"),
		EmbedModel:     os.Getenv("FIVE_E_EMBED_MODEL"),
		AskModel:       os.Getenv("FIVE_E_ASK_MODEL"),
		EmbedMaxTokens: envInt("FIVE_E_EMBED_MAX_TOKENS"),
		AskMaxTokens:   envInt("FIVE_E_ASK_MAX_TOKENS"),
		MinScore:       envFloat("FIVE_E_MIN_SCORE"),
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

// envFloat reads a numeric setting, ignoring an unset or unparseable value so
// a typo falls back to the default rather than failing every ask. A negative
// value is a valid override: it disables the minimum-score gate, since no
// cosine similarity falls below -1.
func envFloat(name string) float64 {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 0
	}
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0
	}
	return f
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
	if c.AskMaxTokens <= 0 {
		c.AskMaxTokens = DefaultAskMaxTokens
	}
	if c.MinScore == 0 {
		c.MinScore = DefaultMinScore
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: DefaultTimeout}
	}
	return c
}
