package ask

import (
	"context"
	"fmt"
	"strings"

	"github.com/hbaldwin98/5e-cli/internal/store"
)

const systemPrompt = `You answer Dungeons & Dragons 5th Edition questions using only the provided sources.
Cite claims as (kind, name, source). If the sources do not contain the answer, say so.
Do not invent rules, spells, monsters, or page numbers.`

// Result is a grounded answer plus the chunks it was built from.
type Result struct {
	Answer    string `json:"answer"`
	Citations []Hit  `json:"citations"`
}

// Ask retrieves relevant chunks and asks the chat model to answer from them.
func Ask(ctx context.Context, st *store.Store, cfg Config, q Query) (Result, error) {
	hits, err := Retrieve(ctx, st, cfg, q)
	if err != nil {
		return Result{}, err
	}
	if len(hits) == 0 {
		return Result{Answer: "No matching sources in the local index.", Citations: hits}, nil
	}
	cli := newClient(cfg.withDefaults())
	answer, err := cli.Chat(ctx, systemPrompt, userPrompt(q.Text, hits))
	if err != nil {
		return Result{}, err
	}
	return Result{Answer: strings.TrimSpace(answer), Citations: hits}, nil
}

func userPrompt(question string, hits []Hit) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Question: %s\n\nSources:\n", question)
	for i, h := range hits {
		fmt.Fprintf(&b, "%d. %s %s (%s)\n%s\n\n", i+1, h.Kind, h.Name, h.Source, h.Snippet)
	}
	return b.String()
}
