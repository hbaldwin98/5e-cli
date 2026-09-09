package ask

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestConverse_carriesHistoryNotesAndSources(t *testing.T) {
	st, cfg, api := harness(t)
	defer st.Close()

	res, err := Converse(context.Background(), st, cfg, Turn{
		Query: Query{Text: "what does fireball do", Limit: 3},
		History: []Message{
			{Role: "user", Content: "we are in the Sunless Citadel"},
			{Role: "assistant", Content: "Understood."},
		},
		Notes: []string{"the wizard is a tiefling named Rekt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Answer, "Fireball") {
		t.Fatalf("answer %q", res.Answer)
	}
	msgs, _ := api.lastMessages.Load().([]Message)
	if len(msgs) != 4 {
		t.Fatalf("want system, two history turns, and the question; got %d: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != "system" || msgs[1].Content != "we are in the Sunless Citadel" || msgs[2].Role != "assistant" {
		t.Fatalf("history was not sent in order: %+v", msgs)
	}
	last := msgs[len(msgs)-1]
	for _, want := range []string{"Rekt", "A bright streak flashes", "Question: what does fireball do"} {
		if !strings.Contains(last.Content, want) {
			t.Fatalf("final message is missing %q:\n%s", want, last.Content)
		}
	}
	if len(res.Citations) == 0 || res.Citations[0].Name != "Fireball" {
		t.Fatalf("citations %+v", res.Citations)
	}
}

func TestConverse_followUpRetrievesUsingEarlierQuestion(t *testing.T) {
	st, cfg, api := harness(t)
	defer st.Close()

	// "how much damage does it do" has no retrievable words of its own; only
	// the question it follows can put Fireball in front of the model.
	res, err := Converse(context.Background(), st, cfg, Turn{
		Query:   Query{Text: "how much damage does it do", Limit: 2},
		History: []Message{{Role: "user", Content: "what does fireball do"}, {Role: "assistant", Content: "It explodes."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Citations) == 0 || res.Citations[0].Name != "Fireball" {
		t.Fatalf("follow-up lost the subject: %+v", res.Citations)
	}
	if !strings.Contains(api.lastUser.Load().(string), "Question: how much damage does it do") {
		t.Fatalf("the widened text must not replace the question:\n%s", api.lastUser.Load())
	}
}

func TestConverse_keepsAdventureNamedInAnEarlierTurn(t *testing.T) {
	st, cfg, _ := harness(t)
	defer st.Close()

	res, err := Converse(context.Background(), st, cfg, Turn{
		Query:   Query{Text: "what does he want from the party", Limit: 8},
		History: []Message{{Role: "user", Content: "who is Gundren Rockseeker"}, {Role: "assistant", Content: "A dwarf."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var sawModule bool
	for _, c := range res.Citations {
		if c.Source == "LMoP" {
			sawModule = true
		}
	}
	if !sawModule {
		t.Fatalf("adventure scope did not survive the follow-up: %+v", res.Citations)
	}
}

func TestConverse_rejectsEmptyQuestion(t *testing.T) {
	st, cfg, api := harness(t)
	defer st.Close()

	if _, err := Converse(context.Background(), st, cfg, Turn{Query: Query{Text: "  "}}); err == nil {
		t.Fatal("want an error for an empty question")
	}
	if api.chatCalls.Load() != 0 {
		t.Fatalf("empty question should not reach the model, calls %d", api.chatCalls.Load())
	}
}

func TestRetrievalText_usesRecentUserTurnsOnly(t *testing.T) {
	history := []Message{
		{Role: "user", Content: "oldest"},
		{Role: "user", Content: "middle"},
		{Role: "assistant", Content: "an answer about something else"},
		{Role: "user", Content: "newest"},
	}
	got := retrievalText(history, "follow-up")
	if got != "middle\nnewest\nfollow-up" {
		t.Fatalf("got %q", got)
	}
	if strings.Contains(got, "an answer") {
		t.Fatal("an earlier answer must not steer retrieval")
	}
}

func TestTrimHistory_dropsOldestWholeTurns(t *testing.T) {
	history := []Message{
		{Role: "user", Content: strings.Repeat("a", 60)},
		{Role: "assistant", Content: strings.Repeat("b", 60)},
		{Role: "user", Content: strings.Repeat("c", 60)},
		{Role: "assistant", Content: strings.Repeat("d", 60)},
	}
	got := trimHistory(history, 130)
	if len(got) != 2 || got[0].Content[0] != 'c' || got[1].Content[0] != 'd' {
		t.Fatalf("want the most recent complete exchange, got %d: %+v", len(got), got)
	}

	// A trailing question may be retained while it has no answer yet, but an
	// answer must never survive without the question it belongs to.
	got = trimHistory(history[:3], 130)
	if len(got) != 1 || got[0].Role != "user" || got[0].Content[0] != 'c' {
		t.Fatalf("want only the unfinished latest question, got %+v", got)
	}
	got = trimHistory(history[:2], 60)
	if len(got) != 0 {
		t.Fatalf("an oversized exchange must be dropped whole, got %+v", got)
	}

	long := []Message{{Role: "user", Content: strings.Repeat("x", 500) + "tail"}}
	got = trimHistory(long, 10)
	if len(got) != 1 || got[0].Content != strings.Repeat("x", 6)+"tail" {
		t.Fatalf("an oversized turn should keep its tail, got %q", got[0].Content)
	}
	if len(trimHistory(history, 0)) != 0 {
		t.Fatal("no budget means no history")
	}
}

func TestNotesBlock_dropsOldestNotesFirst(t *testing.T) {
	notes := []string{"first note", "second note", "third note"}
	got := notesBlock(notes, 30)
	if !strings.Contains(got, "third note") || !strings.Contains(got, "second note") {
		t.Fatalf("recent notes should survive: %q", got)
	}
	if strings.Contains(got, "first note") {
		t.Fatalf("the oldest note should have been dropped: %q", got)
	}
	if utf8.RuneCountInString(got) > 30 {
		t.Fatalf("notes overshot the budget: %d runes", utf8.RuneCountInString(got))
	}
	if lines := strings.Split(strings.TrimSpace(got), "\n"); lines[0] != "- second note" {
		t.Fatalf("notes must stay in written order: %q", got)
	}
}

func TestConversePromptBody_saysWhenNothingWasRetrieved(t *testing.T) {
	body := conversePromptBody("what happened", "", nil, 100)
	if !strings.Contains(body, "Sources:\nnone") {
		t.Fatalf("an empty retrieval must be stated, not implied:\n%s", body)
	}
}
