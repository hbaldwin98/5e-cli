package ask

import (
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hbaldwin98/5e-cli/internal/provider"
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

	// DefaultAnswerMaxTokens caps how many tokens the chat model may generate
	// for one answer. The cap exists so a model that would otherwise ramble
	// (or a backend that defaults to "as long as the context allows") has a
	// predictable cost and latency ceiling, but a synthesized answer that
	// pulls together several sources (e.g. "flesh out this quest") routinely
	// runs well past a couple of citations' worth of text, so the default
	// leaves generous headroom rather than cutting a real answer off
	// mid-sentence. FIVE_E_ANSWER_MAX_TOKENS overrides it either way.
	DefaultAnswerMaxTokens = 4096
)

// Config is an OpenAI-compatible embedding and chat backend.
type Config struct {
	APIKey          string
	BaseURL         string
	EmbedModel      string
	AskModel        string
	EmbedMaxTokens  int
	AskMaxTokens    int
	AnswerMaxTokens int
	// MinScore is the relevance gate: chunks scoring below it are rejected as
	// irrelevant rather than returned. Zero means "use DefaultMinScore"; a
	// negative value disables the gate, since cosine similarity never falls
	// below -1.
	MinScore   float64
	CachePath  string
	HTTPClient *http.Client
	// Progress receives one concise line per embedding batch ("embedding
	// 40/120") when the cache needs to be built or rebuilt. Ignored when
	// OnProgress is set.
	Progress io.Writer
	// OnProgress, if set, receives structured build progress instead of
	// Progress getting a written line — a caller that wants to render its
	// own view (a determinate progress bar, a phase label) rather than
	// consume preformatted text uses this.
	OnProgress func(EmbedProgress)
	// Tools and ToolExecutor let Converse/ConverseStream call live lookups
	// (exact CR, a dice roll, a table roll) mid-conversation instead of
	// answering only from retrieved text. Both must be set for tool-calling
	// to activate; see BuildTools for the built-in set. Ask/AskStream never
	// use these — tool-calling is a chat feature.
	Tools        []Tool
	ToolExecutor ToolExecutor
}

// hasTools reports whether tool-calling is wired up for this Config. Both
// Tools and ToolExecutor are required: a tool definition with nothing to run
// it is as useless as an executor the model was never told about.
func (c Config) hasTools() bool {
	return len(c.Tools) > 0 && c.ToolExecutor != nil
}

// HasTools is hasTools, exported for a caller outside this package (the CLI)
// that needs to pick a rendering strategy: an answer produced through the
// tool loop is delivered as one finished flush rather than true
// token-by-token streaming (see runToolLoop), so it should be treated —
// and Markdown-rendered — like a non-streaming answer, not like a stream of
// raw deltas.
func (c Config) HasTools() bool {
	return c.hasTools()
}

// EmbedProgress is one update on building the embedding cache: how many of
// how many chunks have been embedded so far, and what phase that count
// belongs to. Phase exists because a future step (say, writing the built
// vectors to disk) is a distinct, separately-reportable phase from
// embedding itself, even though today embedding is the only one.
type EmbedProgress struct {
	Done, Total int
	Phase       string
}

// ConfigFromEnv resolves credentials with explicit env vars taking
// precedence over the local provider store (set via `5e auth login`): an
// explicitly set OPENAI_API_KEY always wins, since a caller who set it
// clearly wants that key used regardless of what's stored (this also keeps
// tests and CI, which set OPENAI_API_KEY/OPENAI_BASE_URL to point at a fake
// server, from being silently overridden by whatever is logged in on the
// host). Only when OPENAI_API_KEY is unset does the stored provider fill
// in: FIVE_E_PROVIDER pins which one, otherwise the store's active provider
// (the most recently logged-in one) is used. Non-credential tuning stays
// env-var-only (FIVE_E_* variables). Paths are filled in by the CLI.
func ConfigFromEnv() Config {
	apiKey := os.Getenv("OPENAI_API_KEY")
	baseURL := os.Getenv("OPENAI_BASE_URL")
	var chatModel, embedModel string

	if apiKey == "" {
		if path, err := provider.DefaultPath(); err == nil {
			if store, err := provider.Load(path); err == nil {
				if _, cred, ok := store.Resolve(os.Getenv("FIVE_E_PROVIDER")); ok {
					apiKey = cred.APIKey
					if baseURL == "" {
						baseURL = cred.BaseURL
					}
					chatModel = cred.ChatModel
					embedModel = cred.EmbedModel
				}
			}
		}
	}

	if v := os.Getenv("FIVE_E_ASK_MODEL"); v != "" {
		chatModel = v
	}
	if v := os.Getenv("FIVE_E_EMBED_MODEL"); v != "" {
		embedModel = v
	}

	return Config{
		APIKey:          apiKey,
		BaseURL:         baseURL,
		EmbedModel:      embedModel,
		AskModel:        chatModel,
		EmbedMaxTokens:  envInt("FIVE_E_EMBED_MAX_TOKENS"),
		AskMaxTokens:    envInt("FIVE_E_ASK_MAX_TOKENS"),
		AnswerMaxTokens: envInt("FIVE_E_ANSWER_MAX_TOKENS"),
		MinScore:        envFloat("FIVE_E_MIN_SCORE"),
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
	if c.AnswerMaxTokens <= 0 {
		c.AnswerMaxTokens = DefaultAnswerMaxTokens
	}
	if c.MinScore == 0 {
		c.MinScore = DefaultMinScore
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: DefaultTimeout}
	}
	return c
}
