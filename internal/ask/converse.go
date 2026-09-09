package ask

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/hbaldwin98/5e-cli/internal/store"
)

const conversePrompt = `You answer Dungeons & Dragons 5th Edition questions in an ongoing conversation.
Answer the latest question using only the provided sources and campaign notes.
Cite claims from the sources as (kind, name, source). If the sources do not contain the answer, say so.
Campaign notes are the user's own record of their table. Treat them as true, prefer them over the
rules when they conflict, and do not cite them as sources.
Earlier turns are context, not sources; do not treat your own earlier answers as evidence.
Do not invent rules, spells, monsters, or page numbers.`

// Message is one conversation turn as the chat model sees it.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Turn is one request in an ongoing conversation: the new question, the turns
// before it (oldest first), and the notes the user keeps in context.
type Turn struct {
	Query   Query
	History []Message
	Notes   []string
}

// retrievalLookback is how many earlier user turns widen the embedding query.
// A follow-up is often unintelligible alone ("how much damage does it do?"),
// so the question it follows is embedded with it.
const retrievalLookback = 2

// Converse answers Turn.Query with the conversation and the notes in context.
// Retrieval runs on every turn, so each answer is grounded in the corpus
// rather than in what the model said earlier.
func Converse(ctx context.Context, st *store.Store, cfg Config, t Turn) (Result, error) {
	cfg = cfg.withDefaults()
	if strings.TrimSpace(t.Query.Text) == "" {
		return Result{}, fmt.Errorf("empty query")
	}
	scoped := t.Query
	// The widened text also feeds adventure detection, so a module named once
	// stays in scope for the follow-ups that only say "he" or "there".
	scoped.Text = retrievalText(t.History, t.Query.Text)
	ranked, err := retrieveChunks(ctx, st, cfg, scoped)
	if err != nil {
		return Result{}, err
	}

	total := promptRunes(cfg.AskMaxTokens)
	notes := notesBlock(t.Notes, total/5)
	history := trimHistory(t.History, total/3)
	spent := utf8.RuneCountInString(notes)
	for _, m := range history {
		spent += utf8.RuneCountInString(m.Content)
	}

	msgs := make([]Message, 0, len(history)+2)
	msgs = append(msgs, Message{Role: "system", Content: conversePrompt})
	msgs = append(msgs, history...)
	msgs = append(msgs, Message{
		Role:    "user",
		Content: conversePromptBody(t.Query.Text, notes, ranked, max(total-spent, 0)),
	})

	answer, err := newClient(cfg).ChatMessages(ctx, msgs)
	if err != nil {
		return Result{}, err
	}
	answer = strings.TrimSpace(answer)
	return Result{Answer: answer, Citations: validatedCitations(answer, ranked)}, nil
}

// conversePromptBody is the final user message: the notes, the sources this
// turn retrieved, and the question. Notes and sources are repeated every turn
// rather than left in the history, so a stale set never outlives its turn.
func conversePromptBody(question, notes string, ranked []scoredChunk, budget int) string {
	var b strings.Builder
	if notes != "" {
		fmt.Fprintf(&b, "Campaign notes:\n%s\n", notes)
	}
	b.WriteString("Sources:\n")
	if len(ranked) == 0 {
		b.WriteString("none\n")
	} else {
		texts := make([]string, len(ranked))
		for i, r := range ranked {
			texts[i] = r.Text
		}
		shares := shareBudget(texts, budget)
		for i, r := range ranked {
			body := clipText(r.Text, shares[i])
			fmt.Fprintf(&b, "%d. %s %s (%s)\n%s\n\n", i+1, r.Kind, r.Name, r.Source, strings.TrimSpace(body))
		}
	}
	fmt.Fprintf(&b, "\nQuestion: %s\n", question)
	return b.String()
}

// retrievalText widens a follow-up with the questions it follows so the
// embedding has something to match. Only user turns are used: an earlier
// answer would pull retrieval toward what the model already said.
func retrievalText(history []Message, question string) string {
	var prior []string
	for i := len(history) - 1; i >= 0 && len(prior) < retrievalLookback; i-- {
		if history[i].Role != "user" {
			continue
		}
		if text := strings.TrimSpace(history[i].Content); text != "" {
			prior = append(prior, text)
		}
	}
	parts := make([]string, 0, len(prior)+1)
	for i := len(prior) - 1; i >= 0; i-- {
		parts = append(parts, prior[i])
	}
	return strings.Join(append(parts, question), "\n")
}

// notesBlock renders the notes as a list within a rune budget, dropping the
// oldest first: a note written this session is likelier to bear on the
// question than one from the first session.
func notesBlock(notes []string, budget int) string {
	kept := make([]string, 0, len(notes))
	spent := 0
	for i := len(notes) - 1; i >= 0; i-- {
		line := "- " + strings.TrimSpace(notes[i]) + "\n"
		n := utf8.RuneCountInString(line)
		if spent+n > budget {
			break
		}
		spent += n
		kept = append(kept, line)
	}
	var b strings.Builder
	for i := len(kept) - 1; i >= 0; i-- {
		b.WriteString(kept[i])
	}
	return b.String()
}

// trimHistory keeps the most recent complete exchanges that fit the budget.
// A trailing user message without an answer is safe to keep on its own, but an
// assistant message is never retained without its corresponding question. A
// standalone user message longer than the budget is clipped to its tail.
func trimHistory(history []Message, budget int) []Message {
	if budget <= 0 || len(history) == 0 {
		return nil
	}

	// A trailing user message can represent an unfinished question supplied by
	// a caller. Keep it before older exchanges, but never do the equivalent for
	// an assistant message: that would expose an answer without its question.
	end := len(history)
	kept := make([]Message, 0, len(history))
	remaining := budget
	if history[end-1].Role == "user" {
		m := history[end-1]
		n := utf8.RuneCountInString(m.Content)
		if n > remaining {
			m.Content = clipTail(m.Content, remaining)
		} else {
			remaining -= n
		}
		if m.Content != "" {
			kept = append(kept, m)
		}
		end--
	}

	// Find complete user/assistant exchanges first. This also makes malformed
	// history harmless: unrelated messages are ignored rather than paired by
	// position and accidentally presented as conversation context.
	type exchange struct {
		user      Message
		assistant Message
	}
	exchanges := make([]exchange, 0, end/2)
	for i := 0; i+1 < end; {
		if history[i].Role == "user" && history[i+1].Role == "assistant" {
			exchanges = append(exchanges, exchange{user: history[i], assistant: history[i+1]})
			i += 2
			continue
		}
		i++
	}

	selected := make([]exchange, 0, len(exchanges))
	for i := len(exchanges) - 1; i >= 0; i-- {
		pair := exchanges[i]
		n := utf8.RuneCountInString(pair.user.Content) + utf8.RuneCountInString(pair.assistant.Content)
		if n > remaining {
			break
		}
		remaining -= n
		selected = append(selected, pair)
	}

	// The selected exchanges were collected newest-first. Add them before the
	// optional trailing user so the returned history remains chronological.
	out := make([]Message, 0, len(selected)*2+len(kept))
	for i := len(selected) - 1; i >= 0; i-- {
		out = append(out, selected[i].user, selected[i].assistant)
	}
	out = append(out, kept...)
	return out
}

func clipTail(s string, window int) string {
	if window <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= window {
		return s
	}
	return string(r[len(r)-window:])
}
