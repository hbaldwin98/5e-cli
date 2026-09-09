package chat

import (
	"context"
	"fmt"
	"strings"

	"github.com/hbaldwin98/5e-cli/internal/ask"
	"github.com/hbaldwin98/5e-cli/internal/edition"
	"github.com/hbaldwin98/5e-cli/internal/store"
)

// Options are the retrieval knobs a conversation applies to every question.
// They are per-invocation, not part of the session: a saved conversation
// should not carry a --kind filter from a previous run.
type Options struct {
	Kind    string
	Sources []string
	Limit   int
	Edition edition.Pref
	SRD     bool
}

// DefaultLimit matches `ask`, so the same question answers the same way in
// either command.
const DefaultLimit = 8

// Ask answers a question in the session's context and appends the exchange to
// its transcript. The caller persists the session; nothing here writes to
// disk, so a failed answer leaves the transcript alone.
func Ask(ctx context.Context, st *store.Store, cfg ask.Config, sess *Session, question string, opt Options) (ask.Result, error) {
	return askWith(ctx, st, cfg, sess, question, opt, nil)
}

// AskStream is Ask, but calls onDelta with each token as the model generates
// it, so a TTY caller can render the answer as it arrives.
func AskStream(ctx context.Context, st *store.Store, cfg ask.Config, sess *Session, question string, opt Options, onDelta func(string) error) (ask.Result, error) {
	if onDelta == nil {
		return ask.Result{}, fmt.Errorf("AskStream requires onDelta")
	}
	return askWith(ctx, st, cfg, sess, question, opt, onDelta)
}

func askWith(ctx context.Context, st *store.Store, cfg ask.Config, sess *Session, question string, opt Options, onDelta func(string) error) (ask.Result, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return ask.Result{}, fmt.Errorf("empty question")
	}
	if opt.Limit <= 0 {
		opt.Limit = DefaultLimit
	}
	turn := ask.Turn{
		Query: ask.Query{
			Text:          question,
			Kind:          opt.Kind,
			Sources:       opt.Sources,
			Limit:         opt.Limit,
			Edition:       opt.Edition,
			SRD:           opt.SRD,
			Adventure:     sess.Adventure,
			AdventureOnly: sess.AdventureOnly,
		},
		History: sess.History(),
		Notes:   sess.NoteTexts(),
	}
	var res ask.Result
	var err error
	if onDelta != nil {
		res, err = ask.ConverseStream(ctx, st, cfg, turn, onDelta)
	} else {
		res, err = ask.Converse(ctx, st, cfg, turn)
	}
	if err != nil {
		return ask.Result{}, err
	}
	sess.AddTurn(question, res)
	return res, nil
}
