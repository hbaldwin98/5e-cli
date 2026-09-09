package chat

import (
	"context"
	"fmt"
	"strings"

	"github.com/hbaldwin98/5e-cli/internal/ask"
	"github.com/hbaldwin98/5e-cli/internal/store"
)

// Options are the retrieval knobs a conversation applies to every question.
// They are per-invocation, not part of the session: a saved conversation
// should not carry a --kind filter from a previous run.
type Options struct {
	Kind    string
	Sources []string
	Limit   int
	SRD     bool
}

// DefaultLimit matches `ask`, so the same question answers the same way in
// either command.
const DefaultLimit = 8

// Ask answers a question in the session's context and appends the exchange to
// its transcript. The caller persists the session; nothing here writes to
// disk, so a failed answer leaves the transcript alone.
func Ask(ctx context.Context, st *store.Store, cfg ask.Config, sess *Session, question string, opt Options) (ask.Result, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return ask.Result{}, fmt.Errorf("empty question")
	}
	if opt.Limit <= 0 {
		opt.Limit = DefaultLimit
	}
	res, err := ask.Converse(ctx, st, cfg, ask.Turn{
		Query: ask.Query{
			Text:          question,
			Kind:          opt.Kind,
			Sources:       opt.Sources,
			Limit:         opt.Limit,
			SRD:           opt.SRD,
			Adventure:     sess.Adventure,
			AdventureOnly: sess.AdventureOnly,
		},
		History: sess.History(),
		Notes:   sess.NoteTexts(),
	})
	if err != nil {
		return ask.Result{}, err
	}
	sess.AddTurn(question, res)
	return res, nil
}
